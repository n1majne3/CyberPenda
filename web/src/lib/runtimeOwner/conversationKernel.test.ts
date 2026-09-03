import { describe, expect, it } from "vitest";
import {
  canPiNativeCrossProvider,
  conversationQueueUnavailable,
  conversationSendLabel,
  newSteerRequestID,
  resolveConversationAction,
  resolveConversationSendMode,
} from "./conversationKernel";
import { fakeRuntimeOwnerAdapter, recordedCalls } from "./adapter";
import type { RuntimeOwnerView } from "./types";

function ownerView(overrides: Partial<RuntimeOwnerView> = {}): RuntimeOwnerView {
  return {
    kind: "task",
    id: "task-1",
    title: "goal",
    status: "running",
    runner: "host",
    runtimeProfileID: "profile-1",
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
    capabilities: {
      projectChrome: true, rename: false, archive: false, restore: false,
      delete: true, finish: true, resumeWithoutMessage: true, attachments: false,
    },
    ...overrides,
  };
}

const selection = { model_provider_id: "provider-a", model: "model-a", reasoning_effort: "high" };

function kernelState(overrides: Record<string, unknown> = {}) {
  return {
    running: true,
    nativeSteerAvailable: false,
    interruptSteerAvailable: false,
    queueSteerAvailable: true,
    resumeAvailable: false,
    nativeResumeAvailable: false,
    precedingProviderID: "provider-a",
    selectedProviderID: "provider-a",
    runtimeProvider: "codex",
    ...overrides,
  };
}

describe("resolveConversationAction task", () => {
  it("native steers with a request id while running", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "task" });
    const action = resolveConversationAction(ownerView(), kernelState({ nativeSteerAvailable: true }), "req-1");
    await action.run("inspect", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([{ op: "steer", text: "inspect", native: true, requestID: "req-1" }]);
  });

  it("queues while running without native steer", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "task" });
    const action = resolveConversationAction(ownerView(), kernelState(), "req-2");
    await action.run("inspect", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([{ op: "queueSteer", text: "inspect" }]);
  });

  it("queues then resumes when idle so a failed resume keeps the message", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "task" });
    const action = resolveConversationAction(ownerView({ status: "completed" }), kernelState({
      running: false, resumeAvailable: true,
    }), "req-3");
    await action.run("continue", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([{ op: "queueSteer", text: "continue" }, { op: "resume" }]);
  });

  it("switches provider through queue, stop, and resume", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "task" });
    const action = resolveConversationAction(ownerView(), kernelState({
      selectedProviderID: "provider-b",
    }), "req-4");
    await action.run("switch", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([
      { op: "queueSteer", text: "switch" },
      { op: "stop" },
      { op: "resume" },
    ]);
  });

  it("interrupt steers with a directive when only interrupt is available", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "task" });
    const action = resolveConversationAction(ownerView(), kernelState({
      interruptSteerAvailable: true,
    }), "req-5");
    await action.run("interrupt", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([{ op: "steer", text: "interrupt", native: false, requestID: "req-5" }]);
  });

  it("fails closed when no task transport can run", () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "task" });
    expect(() => resolveConversationAction(ownerView(), kernelState({
      queueSteerAvailable: false, running: false, resumeAvailable: false,
    }), "req-6")).toThrow(/unavailable/i);
    expect(recordedCalls(adapter)).toEqual([]);
  });
});

describe("resolveConversationAction session", () => {
  const sessionView = () => ownerView({
    kind: "session",
    status: "running",
    lifecycle: "open",
    capabilities: {
      projectChrome: false, rename: true, archive: true, restore: false,
      delete: false, finish: false, resumeWithoutMessage: false, attachments: true,
    },
  });

  it("steers natively while running", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "session" });
    const action = resolveConversationAction(sessionView(), kernelState({ nativeSteerAvailable: true }), "req-7");
    await action.run("hello", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([{ op: "steer", text: "hello", native: true, requestID: "req-7" }]);
  });

  it("queues while running without native steer", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "session" });
    const action = resolveConversationAction(sessionView(), kernelState(), "req-8");
    await action.run("hello", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([{ op: "queueSteer", text: "hello" }]);
  });

  it("sends a fresh message when idle", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "session" });
    const action = resolveConversationAction(sessionView({ status: "stopped" }), kernelState({ running: false }), "req-9");
    await action.run("hello", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([{ op: "sendMessage", text: "hello" }]);
  });

  it("switches provider by stopping, then sending on the fresh continuation", async () => {
    const adapter = fakeRuntimeOwnerAdapter({ kind: "session" });
    const action = resolveConversationAction(sessionView(), kernelState({
      selectedProviderID: "provider-b",
    }), "req-10");
    await action.run("switch", selection, adapter, []);
    expect(recordedCalls(adapter)).toEqual([{ op: "stop" }, { op: "sendMessage", text: "switch" }]);
  });
});

describe("conversation send mode", () => {
  it("prefers native, then interrupt, then queue while running", () => {
    expect(resolveConversationSendMode({
      running: true, nativeSteerAvailable: true, interruptSteerAvailable: true, queueSteerAvailable: true, resumeAvailable: true,
    })).toBe("native");
    expect(resolveConversationSendMode({
      running: true, nativeSteerAvailable: false, interruptSteerAvailable: true, queueSteerAvailable: true, resumeAvailable: true,
    })).toBe("interrupt");
    expect(resolveConversationSendMode({
      running: true, nativeSteerAvailable: false, interruptSteerAvailable: false, queueSteerAvailable: true, resumeAvailable: true,
    })).toBe("queue");
    expect(resolveConversationSendMode({
      running: true, nativeSteerAvailable: false, interruptSteerAvailable: false, queueSteerAvailable: false, resumeAvailable: true,
    })).toBe("unavailable");
  });

  it("requires queue and resume when idle", () => {
    expect(resolveConversationSendMode({
      running: false, nativeSteerAvailable: false, interruptSteerAvailable: false, queueSteerAvailable: true, resumeAvailable: true,
    })).toBe("resume");
    expect(resolveConversationSendMode({
      running: false, nativeSteerAvailable: false, interruptSteerAvailable: false, queueSteerAvailable: true, resumeAvailable: false,
    })).toBe("unavailable");
  });

  it("labels the real provider operation for the composer", () => {
    expect(conversationSendLabel("native")).toBe("Send");
    expect(conversationSendLabel("native", "send_turn")).toBe("Send");
    expect(conversationSendLabel("native", "in_turn_steer")).toBe("Steer current turn");
    expect(conversationSendLabel("native", "in_turn_steer", true)).toBe("Interrupt and send");
    expect(conversationSendLabel("native", "interrupt_then_replace")).toBe("Interrupt and send");
    expect(conversationSendLabel("interrupt")).toBe("Interrupt and send");
    expect(conversationSendLabel("queue")).toBe("Queue message");
    expect(conversationSendLabel("resume")).toBe("Resume and send");
    expect(conversationSendLabel("unavailable")).toBe("Send unavailable");
  });
});

describe("provider switch gates", () => {
  it("allows Pi native cross-provider only inside the projected set", () => {
    const input = {
      runtimeProvider: "pi",
      nativeSteerAvailable: true,
      projectedModelProviderIDs: ["provider-a", "provider-b"],
      targetProviderID: "provider-b",
    };
    expect(canPiNativeCrossProvider(input)).toBe(true);
    expect(canPiNativeCrossProvider({ ...input, targetProviderID: "provider-c" })).toBe(false);
    expect(canPiNativeCrossProvider({ ...input, projectedModelProviderIDs: null })).toBe(false);
    expect(canPiNativeCrossProvider({ ...input, projectedModelProviderIDs: [] })).toBe(false);
    expect(canPiNativeCrossProvider({ ...input, runtimeProvider: "codex" })).toBe(false);
    expect(canPiNativeCrossProvider({ ...input, nativeSteerAvailable: false })).toBe(false);
  });

  it("blocks Session queueing across a provider switch, never Task queueing", () => {
    const switching: Parameters<typeof conversationQueueUnavailable>[1] = "provider-b";
    expect(conversationQueueUnavailable(ownerView(), switching)).toBe(false);
    expect(conversationQueueUnavailable(ownerView({
      kind: "session",
      runtimeControls: {
        turn_selection: { model_provider_id: "provider-a" },
      } as RuntimeOwnerView["runtimeControls"],
    }), switching)).toBe(true);
    expect(conversationQueueUnavailable(ownerView({
      kind: "session",
      status: "stopped",
      runtimeControls: {
        turn_selection: { model_provider_id: "provider-a" },
      } as RuntimeOwnerView["runtimeControls"],
    }), switching)).toBe(false);
  });
});

describe("request ids", () => {
  it("generates unique steer ids", () => {
    const a = newSteerRequestID();
    const b = newSteerRequestID();
    expect(a).not.toBe(b);
  });
});
