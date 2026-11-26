import { describe, expect, it } from "vitest";
import { conversationBusy, initialConversationState, runtimeTurnPhase, steeringPhase, transitionConversation,
  type ConversationEvent, type ConversationOperation } from "./conversationState";
import type { RuntimeOwnerView } from "./types";

const operations: ConversationOperation[] = ["send", "queue", "stop", "resume", "finish"];
const key = "session:one";
const begin = (operation: ConversationOperation, id = "a"): ConversationEvent => ({ type: "begin", ownerKey: key, id, operation });
const receipt = (type: "accepted" | "refreshed", id = "a"): ConversationEvent => ({ type, ownerKey: key, id });

describe("conversation transition contract", () => {
  it.each(operations)("admits %s, then requires acceptance before a completed refresh", (operation) => {
    const pending = transitionConversation(initialConversationState(key), begin(operation));
    expect(conversationBusy(pending)).toBe(true);
    expect(transitionConversation(pending, receipt("refreshed"))).toBe(pending);
    const refreshing = transitionConversation(pending, receipt("accepted"));
    expect(refreshing.phase).toBe("refreshing");
    expect(transitionConversation(refreshing, receipt("refreshed"))).toEqual(initialConversationState(key));
  });

  it.each(operations.flatMap((first) => operations.map((second) => [first, second] as const)))(
    "%s → %s only allows Stop to preempt", (first, second) => {
      const pending = transitionConversation(initialConversationState(key), begin(first));
      const next = transitionConversation(pending, begin(second, "b"));
      if (second === "stop" && first !== "stop") {
        expect(next.request).toEqual({ id: "b", operation: "stop" });
        expect(next.phase).toBe("stopping");
      } else {
        expect(next).toBe(pending);
      }
    },
  );

  it("does not let a delayed send acknowledgement undo Stop or a route change", () => {
    let state = transitionConversation(initialConversationState(key), begin("send"));
    state = transitionConversation(state, begin("stop", "b"));
    expect(transitionConversation(state, receipt("accepted"))).toBe(state);
    state = transitionConversation(state, { type: "owner_changed", ownerKey: "session:two" });
    expect(transitionConversation(state, receipt("accepted", "b"))).toBe(state);
    expect(state.phase).toBe("ready");
  });

  it("permits retry after a failed request without replaying it automatically", () => {
    const pending = transitionConversation(initialConversationState(key), begin("send"));
    const failed = transitionConversation(pending, { type: "rejected", ownerKey: key, id: "a", error: "offline" });
    expect(failed.phase).toBe("failed");
    expect(conversationBusy(failed)).toBe(false);
    expect(transitionConversation(failed, receipt("accepted"))).toBe(failed);
    expect(transitionConversation(failed, begin("send", "b")).request?.id).toBe("b");
  });

  it("does not equate a steer acknowledgement with a completed provider Turn", () => {
    const state = transitionConversation(initialConversationState(key), {
      type: "observed", ownerKey: key, runtime: { turn: "busy", steering: steeringPhase("applied") },
    });
    expect(state.runtime).toEqual({ turn: "busy", steering: "delivered" });
    let pending = transitionConversation(state, begin("send"));
    pending = transitionConversation(pending, receipt("accepted"));
    pending = transitionConversation(pending, receipt("refreshed"));
    expect(pending.runtime.turn).toBe("busy");
    expect(pending.phase).toBe("ready");
  });

  it("retains Turn state across reconnect without inventing completion or replaying a send", () => {
    let state = transitionConversation(initialConversationState(key), {
      type: "observed", ownerKey: key, runtime: { turn: "busy", steering: "pending" },
    });
    state = transitionConversation(state, {
      type: "observed", ownerKey: key, runtime: { turn: "reconnecting", steering: "pending" },
    });
    expect(state.request).toBeUndefined();
    state = transitionConversation(state, {
      type: "observed", ownerKey: key, runtime: { turn: "idle", steering: "delivered" },
    });
    expect(state.runtime.turn).toBe("idle");
    expect(state.phase).toBe("ready");
    expect(transitionConversation(state, {
      type: "observed", ownerKey: "session:old", runtime: { turn: "busy", steering: "pending" },
    })).toBe(state);
  });

  it("fuzzes duplicate, delayed, reordered, lost and reconnected receipts", () => {
    let seed = 82731;
    const random = (limit: number) => {
      seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
      return seed % limit;
    };
    for (let run = 0; run < 1000; run++) {
      let state = initialConversationState(key);
      const sent = begin("send", `send-${run}`);
      state = transitionConversation(state, sent);
      const stop = begin("stop", `stop-${run}`);
      state = transitionConversation(state, stop);
      const faults: ConversationEvent[] = [
        sent, sent, receipt("refreshed", `send-${run}`), receipt("accepted", `send-${run}`),
        receipt("refreshed", `stop-${run}`),
        { type: "rejected", ownerKey: "session:old", id: `stop-${run}`, error: "old error" },
      ];
      for (let index = 0; index < 20; index++) {
        state = transitionConversation(state, faults[random(faults.length)]!);
        expect(state.request).toEqual({ id: `stop-${run}`, operation: "stop" });
        expect(state.phase).toBe("stopping");
      }
      state = transitionConversation(state, receipt("accepted", `stop-${run}`));
      state = transitionConversation(state, receipt("refreshed", `stop-${run}`));
      expect(state).toEqual(initialConversationState(key));
    }
  });
});

it("uses authoritative Turn activity, not durable owner status or elapsed time", () => {
  for (const status of ["running", "completed", "stopped", "failed"]) {
    const snapshot = { status, runtimeActivity: { liveness: "live", turn_activity: "idle" } } as RuntimeOwnerView;
    expect(runtimeTurnPhase(snapshot)).toBe("idle");
    expect(runtimeTurnPhase(snapshot, "network offline")).toBe("reconnecting");
    expect(runtimeTurnPhase({ ...snapshot, runtimeActivity: { liveness: "live", turn_activity: "busy" } })).toBe("busy");
    expect(runtimeTurnPhase({ ...snapshot, runtimeActivity: { liveness: "offline" } })).toBe("offline");
    expect(runtimeTurnPhase({ ...snapshot, runtimeActivity: undefined })).toBe("unknown");
  }
});
