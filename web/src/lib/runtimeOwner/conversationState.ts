import type { RuntimeOwnerView } from "./types";

export type ConversationOperation = "send" | "queue" | "stop" | "resume" | "finish";
export type ConversationPhase = "ready" | "submitting" | "stopping" | "refreshing" | "failed";
export type RuntimeTurnPhase = "busy" | "idle" | "offline" | "unknown" | "reconnecting";
export type SteeringPhase = "idle" | "pending" | "applied" | "failed" | "action_required";
type RuntimeConversation = { turn: RuntimeTurnPhase; steering: SteeringPhase };

type PendingConversation = {
  phase: "submitting" | "stopping" | "refreshing";
  request: { id: string; operation: ConversationOperation };
  error?: never;
};

export type ConversationState = { ownerKey: string; runtime: RuntimeConversation } & (
  | { phase: "ready"; request?: never; error?: never }
  | PendingConversation
  | { phase: "failed"; request?: never; error: string }
);

export type ConversationEvent =
  | { type: "owner_changed"; ownerKey: string }
  | { type: "observed"; ownerKey: string; runtime: RuntimeConversation }
  | { type: "begin"; ownerKey: string; id: string; operation: ConversationOperation }
  | { type: "accepted"; ownerKey: string; id: string }
  | { type: "refreshed"; ownerKey: string; id: string }
  | { type: "rejected"; ownerKey: string; id: string; error: string };

export function initialConversationState(ownerKey: string): ConversationState {
  return { ownerKey, phase: "ready", runtime: { turn: "unknown", steering: "idle" } };
}

export function conversationBusy(state: ConversationState): state is ConversationState & PendingConversation {
  return state.phase === "submitting" || state.phase === "stopping" || state.phase === "refreshing";
}

export function transitionConversation(state: ConversationState, event: ConversationEvent): ConversationState {
  if (event.type === "owner_changed") {
    return event.ownerKey === state.ownerKey ? state : initialConversationState(event.ownerKey);
  }
  if (event.ownerKey !== state.ownerKey) return state;
  if (event.type === "observed") {
    return event.runtime.turn === state.runtime.turn && event.runtime.steering === state.runtime.steering
      ? state : { ...state, runtime: event.runtime };
  }
  if (event.type === "begin") {
    if (conversationBusy(state) && (event.operation !== "stop" || state.request?.operation === "stop")) return state;
    return {
      ownerKey: state.ownerKey,
      runtime: state.runtime,
      phase: event.operation === "stop" ? "stopping" : "submitting",
      request: { id: event.id, operation: event.operation },
    };
  }
  if (event.id !== state.request?.id || !conversationBusy(state)) return state;
  switch (event.type) {
    case "accepted":
      return state.phase === "refreshing" ? state : { ...state, phase: "refreshing" };
    case "refreshed":
      return state.phase === "refreshing" ? { ...initialConversationState(state.ownerKey), runtime: state.runtime } : state;
    case "rejected":
      return { ownerKey: state.ownerKey, runtime: state.runtime, phase: "failed", error: event.error };
  }
}

export function steeringPhase(state?: string): SteeringPhase {
  if (state === "applied" || state === "failed" || state === "action_required") return state;
  return ["pending", "requested", "acknowledged", "settled", "started"].includes(state ?? "") ? "pending" : "idle";
}

export function runtimeTurnPhase(owner: Pick<RuntimeOwnerView, "runtimeActivity">, refreshError?: string | null): RuntimeTurnPhase {
  if (refreshError) return "reconnecting";
  const activity = owner.runtimeActivity;
  if (activity?.liveness === "offline" || activity?.liveness === "orphaned") return "offline";
  if (activity?.liveness !== "live") return "unknown";
  if (activity.turn_activity === "busy") return "busy";
  if (activity.turn_activity === "idle") return "idle";
  return "unknown";
}

export function conversationStatus(state: ConversationState, turn: RuntimeTurnPhase): string {
  switch (state.phase) {
    case "stopping": return "Stopping Runtime…";
    case "submitting":
      if (state.request?.operation === "queue") return "Queueing message…";
      if (state.request?.operation === "resume") return "Resuming Runtime…";
      if (state.request?.operation === "finish") return "Finishing Task…";
      return "Sending message…";
    case "refreshing": return "Refreshing conversation…";
    default:
      switch (turn) {
        case "busy": return "Working";
        case "idle": return "Ready for your next message";
        case "offline": return "Runtime offline";
        case "reconnecting": return "Connection interrupted · retrying";
        case "unknown": return "Runtime status unknown";
      }
  }
}
