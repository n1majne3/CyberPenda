import type { RuntimeOwnerAdapter } from "./adapter";
import type { ConversationSelection, ConversationSendMode, RuntimeOwnerView } from "./types";

export { newBlackboardRetryID, newSteerRequestID } from "./ids";

/**
 * The conversation decision kernel: from owner-neutral runtime controls it
 * decides which adapter verb sequence one operator message takes. Both owner
 * kinds share the decision so steer/queue/restart rules cannot drift between
 * Task and Session. Pure and unit-testable through a fake adapter.
 */
export type ConversationKernelState = {
  running: boolean;
  nativeSteerAvailable: boolean;
  interruptSteerAvailable: boolean;
  queueSteerAvailable: boolean;
  resumeAvailable: boolean;
  nativeResumeAvailable: boolean;
  precedingProviderID: string;
  selectedProviderID: string;
  runtimeProvider: string;
  nativeSteerMode?: string;
};

export type ConversationAction = {
  run: (message: string, selection: ConversationSelection, adapter: RuntimeOwnerAdapter, attachments: File[]) => Promise<void>;
};

export function resolveConversationAction(
  owner: RuntimeOwnerView,
  state: ConversationKernelState,
  requestID: string,
): ConversationAction {
  const switchingProvider =
    state.running &&
    state.selectedProviderID !== "" &&
    state.selectedProviderID !== state.precedingProviderID &&
    !canPiNativeCrossProvider({
      runtimeProvider: state.runtimeProvider,
      nativeSteerAvailable: state.nativeSteerAvailable || state.interruptSteerAvailable,
      projectedModelProviderIDs: owner.runtimeControls?.projected_model_provider_ids,
      targetProviderID: state.selectedProviderID,
    });

  if (owner.kind === "session") {
    // Sessions cannot switch a live provider session; the restart path is
    // stop → message on the fresh continuation.
    if (switchingProvider) {
      return {
        run: async (message, selection, adapter) => {
          await adapter.stop();
          await adapter.sendMessage(message, selection, []);
        },
      };
    }
    if (state.running && (state.nativeSteerAvailable || state.interruptSteerAvailable)) {
      return {
        run: async (message, selection, adapter, attachments) => {
          await adapter.steer({ text: message, selection, requestID, native: true, attachments });
        },
      };
    }
    if (state.running && state.queueSteerAvailable) {
      return {
        run: async (message, selection, adapter, attachments) => {
          await adapter.queueSteer(message, selection, attachments);
        },
      };
    }
    return {
      run: async (message, selection, adapter, attachments) => {
        await adapter.sendMessage(message, selection, attachments);
      },
    };
  }

  // Task branch.
  if (switchingProvider) {
    if (!state.queueSteerAvailable) {
      throw new Error("Model provider switching is unavailable for this Task");
    }
    // A live provider session cannot change its endpoint or credentials.
    // Persist the message/config first, then restart the Continuation so a
    // failed stop or resume never drops the operator's request.
    return {
      run: async (message, selection, adapter) => {
        await adapter.queueSteer(message, selection, []);
        await adapter.stop();
        await adapter.resume(selection);
      },
    };
  }
  if (state.running && (state.nativeSteerAvailable || state.interruptSteerAvailable)) {
    return {
      run: async (message, selection, adapter) => {
        await adapter.steer({
          text: message,
          selection,
          requestID,
          native: state.nativeSteerAvailable,
        });
      },
    };
  }
  if (state.running && state.queueSteerAvailable) {
    return {
      run: async (message, selection, adapter) => {
        await adapter.queueSteer(message, selection, []);
      },
    };
  }
  if (!state.running && state.queueSteerAvailable && state.resumeAvailable) {
    return {
      run: async (message, selection, adapter) => {
        // Queue first so a failed resume retains the operator's message for the
        // next successful Continuation instead of silently dropping it.
        await adapter.queueSteer(message, selection, []);
        await adapter.resume(selection);
      },
    };
  }
  throw new Error("Task conversation is unavailable for this runtime state");
}

/** Reports which composer send mode the runtime controls admit. */
export function resolveConversationSendMode(input: {
  running: boolean;
  nativeSteerAvailable: boolean;
  interruptSteerAvailable: boolean;
  queueSteerAvailable: boolean;
  resumeAvailable: boolean;
}): ConversationSendMode {
  if (input.running) {
    if (input.nativeSteerAvailable) return "native";
    if (input.interruptSteerAvailable) return "interrupt";
    if (input.queueSteerAvailable) return "queue";
    return "unavailable";
  }
  return input.queueSteerAvailable && input.resumeAvailable ? "resume" : "unavailable";
}

export function conversationSendLabel(mode: ConversationSendMode, nativeSteerMode?: string): string {
  switch (mode) {
    case "native":
      return nativeSteerMode === "in_turn_steer" ? "Send message" : "Native interrupt & send";
    case "interrupt":
      return "Interrupt and resume";
    case "queue":
      return "Queue message";
    case "resume":
      return "Resume and send";
    default:
      return "Send unavailable";
  }
}

export function conversationModeText(mode: ConversationSendMode): string {
  switch (mode) {
    case "native":
      return "direct native";
    case "interrupt":
      return "interrupt then replace";
    case "queue":
      return "queued";
    case "resume":
      return "resume";
    default:
      return "unavailable";
  }
}

/** Reports whether queueing a Session steer is blocked by a pending
 * model-provider switch that only the restart path can honor. Tasks allow the
 * switch through the queue→stop→resume pipeline. */
export function conversationQueueUnavailable(owner: RuntimeOwnerView, selectedProviderID: string): boolean {
  return (
    owner.kind === "session" &&
    (owner.status === "running" || owner.status === "paused") &&
    selectedProviderID !== "" &&
    selectedProviderID !== (owner.runtimeControls?.turn_selection?.model_provider_id?.trim() ?? "")
  );
}

/** Pi native cross-provider only when target is in the fixed projected set. */
export function canPiNativeCrossProvider(input: {
  runtimeProvider: string;
  nativeSteerAvailable: boolean;
  projectedModelProviderIDs?: string[] | null;
  targetProviderID: string;
}): boolean {
  if (input.runtimeProvider !== "pi" || !input.nativeSteerAvailable) {
    return false;
  }
  const target = input.targetProviderID.trim();
  if (!target) {
    return false;
  }
  const projected = (input.projectedModelProviderIDs ?? [])
    .map((id) => id.trim())
    .filter(Boolean);
  // Fail closed: missing/empty projected set requires Config Projection restart.
  if (projected.length === 0) {
    return false;
  }
  return projected.includes(target);
}

/** steerPendingState reports whether an accepted native steer is still waiting
 * for the Runtime to apply or reject it (#194). */
export function steerPendingState(state: string | undefined): boolean {
  return (
    state === "pending" ||
    state === "requested" ||
    state === "acknowledged" ||
    state === "settled" ||
    state === "started"
  );
}
