import { useLayoutEffect } from "react";
import { render } from "@testing-library/react";
import { expect, it } from "vitest";
import type { ConversationState } from "@/lib/runtimeOwner/conversationState";
import type { RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { useConversationOperation } from "./useConversationOperation";

it("applies Runtime observations before consumers commit", () => {
  const committed: Array<Pick<ConversationState, "ownerKey" | "runtime">> = [];
  function Probe({ ownerKey, owner, pollError }: {
    ownerKey: string;
    owner: RuntimeOwnerView;
    pollError?: string;
  }) {
    const { state } = useConversationOperation(ownerKey, owner, pollError);
    useLayoutEffect(() => {
      committed.push({ ownerKey: state.ownerKey, runtime: state.runtime });
    });
    return null;
  }
  const busy = {
    runtimeActivity: { liveness: "live", turn_activity: "busy" },
    runtimeControls: { native_steer_state: "pending" },
  } as RuntimeOwnerView;
  const applied: RuntimeOwnerView = { ...busy, runtimeControls: { native_steer_state: "applied" } };
  const idle: RuntimeOwnerView = { ...applied, runtimeActivity: { liveness: "live", turn_activity: "idle" } };
  const offline = { runtimeActivity: { liveness: "offline" } } as RuntimeOwnerView;
  const { rerender } = render(<Probe ownerKey="session:one" owner={busy} />);
  rerender(<Probe ownerKey="session:one" owner={applied} />);
  rerender(<Probe ownerKey="session:one" owner={idle} />);
  rerender(<Probe ownerKey="session:one" owner={idle} pollError="connection lost" />);
  rerender(<Probe ownerKey="session:one" owner={applied} />);
  rerender(<Probe ownerKey="session:two" owner={offline} />);

  expect(committed).toEqual([
    { ownerKey: "session:one", runtime: { turn: "busy", steering: "pending" } },
    { ownerKey: "session:one", runtime: { turn: "busy", steering: "applied" } },
    { ownerKey: "session:one", runtime: { turn: "idle", steering: "applied" } },
    { ownerKey: "session:one", runtime: { turn: "reconnecting", steering: "applied" } },
    { ownerKey: "session:one", runtime: { turn: "busy", steering: "applied" } },
    { ownerKey: "session:two", runtime: { turn: "offline", steering: "idle" } },
  ]);
});
