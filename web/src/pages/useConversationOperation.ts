import { useEffect, useMemo, useSyncExternalStore } from "react";
import { newSteerRequestID } from "@/lib/runtimeOwner/ids";
import { conversationBusy, initialConversationState, transitionConversation,
  runtimeTurnPhase, steeringPhase, type ConversationEvent, type ConversationOperation } from "@/lib/runtimeOwner/conversationState";
import type { RuntimeOwnerView } from "@/lib/runtimeOwner/types";

function createConversationOperation(ownerKey: string) {
  let state = initialConversationState(ownerKey);
  let active = true;
  const listeners = new Set<() => void>();
  const dispatch = (event: ConversationEvent) => {
    if (!active) return;
    const next = transitionConversation(state, event);
    if (next === state) return;
    state = next;
    for (const listener of listeners) listener();
  };
  return {
    getSnapshot: () => state,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
    activate: () => { active = true; },
    deactivate: () => { active = false; },
    begin: (operation: ConversationOperation) => {
      const id = newSteerRequestID();
      dispatch({ type: "begin", ownerKey, id, operation });
      return state.request?.id === id ? id : null;
    },
    isCurrent: (id: string) => active && state.request?.id === id && conversationBusy(state),
    accepted: (id: string) => dispatch({ type: "accepted", ownerKey, id }),
    refreshed: (id: string) => dispatch({ type: "refreshed", ownerKey, id }),
    rejected: (id: string, error: string) => dispatch({ type: "rejected", ownerKey, id, error }),
    observe: (owner: RuntimeOwnerView | null, refreshError?: string | null) => dispatch({
      type: "observed", ownerKey,
      runtime: { turn: runtimeTurnPhase(owner ?? {}, refreshError), steering: steeringPhase(owner?.runtimeControls?.native_steer_state) },
    }),
  };
}

export function useConversationOperation(ownerKey: string, owner: RuntimeOwnerView | null, refreshError?: string | null) {
  const operation = useMemo(() => createConversationOperation(ownerKey), [ownerKey]);
  const state = useSyncExternalStore(operation.subscribe, operation.getSnapshot);
  useEffect(() => {
    operation.activate();
    return operation.deactivate;
  }, [operation]);
  useEffect(() => { operation.observe(owner, refreshError); }, [operation, owner, refreshError]);
  return { operation, state };
}
