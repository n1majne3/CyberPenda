import type { RuntimeOwnerView, ConversationSendMode } from "@/lib/runtimeOwner/types";
import { canPiNativeCrossProvider, conversationSendLabel, resolveConversationSendMode } from "@/lib/runtimeOwner/conversationKernel";
import { ACTIVE_OWNER_STATUSES } from "@/lib/runtimeOwner/status";

export function deriveConversationControls({
  owner,
  currentContinuation,
  currentRuntimeProvider,
  continuationModelProvider,
  continuationModelOverride,
  continuationReasoningEffort,
  forceReplaceNext,
}: {
  owner: RuntimeOwnerView;
  currentContinuation: RuntimeOwnerView["activeContinuation"];
  currentRuntimeProvider: string | undefined;
  continuationModelProvider: string;
  continuationModelOverride: string;
  continuationReasoningEffort: string;
  forceReplaceNext: boolean;
}) {
  const controls = owner.runtimeControls;
  const nativeResumeAvailable = controls?.native_resume_available ?? Boolean(currentContinuation?.nativeSessionID);
  const resumeAvailable = !ACTIVE_OWNER_STATUSES.has(owner.status) || controls?.resume_available === true;
  const queueSteerAvailable = controls?.queue_steer_available ?? true;
  const interruptSteerAvailable = controls?.interrupt_steer_available ?? nativeResumeAvailable;
  const nativeSteerAvailable = controls?.native_steer_available ?? false;
  const nativeSteerMode = controls?.native_steer_mode;
  const providerPermissions = controls?.provider_permissions ?? [];
  const running = ACTIVE_OWNER_STATUSES.has(owner.status);
  // Finish gate trusts server RuntimeControls only (live+idle already applied).
  const finishAvailable = controls?.finish_available === true;
  const sendMode: ConversationSendMode = owner.kind === "session" && owner.lifecycle !== "open"
    ? "unavailable"
    : resolveConversationSendMode({
      running,
      nativeSteerAvailable,
      interruptSteerAvailable,
      queueSteerAvailable,
      resumeAvailable,
    });
  const precedingProviderID = controls?.turn_selection?.model_provider_id?.trim() ?? "";
  const selectedProviderID = continuationModelProvider.trim();
  // Pi native cross-provider only with explicit projected set membership.
  // Codex/Claude and legacy Pi (missing ids) restart; empty preceding is a switch.
  const piNativeCrossProvider = owner.kind === "task" && canPiNativeCrossProvider({
    runtimeProvider: currentRuntimeProvider ?? "",
    nativeSteerAvailable: nativeSteerAvailable || interruptSteerAvailable,
    projectedModelProviderIDs: controls?.projected_model_provider_ids,
    targetProviderID: selectedProviderID,
  });
  const providerSwitchRequested =
    running &&
    selectedProviderID !== "" &&
    selectedProviderID !== precedingProviderID &&
    !piNativeCrossProvider;
  const providerSwitchAvailable = owner.kind === "session" || queueSteerAvailable;
  const queueActionAvailable = queueSteerAvailable;
  const currentTurnModel = controls?.turn_selection?.model?.trim() ?? "";
  const currentTurnEffort = controls?.turn_selection?.reasoning_effort?.trim() ?? "";
  const selectedTurnModel = continuationModelOverride.trim();
  const selectedTurnEffort = continuationReasoningEffort.trim();
  const turnSelectionChanged = !providerSwitchRequested && currentRuntimeProvider === "codex" && (
    (selectedTurnModel !== "" && selectedTurnModel !== currentTurnModel) ||
    (selectedTurnEffort !== "" && selectedTurnEffort !== currentTurnEffort)
  );
  const sendActionLabel = forceReplaceNext
    ? "Interrupt and replace"
    : providerSwitchRequested
      ? providerSwitchAvailable ? "Switch provider and resume" : "Provider switch unavailable"
      : conversationSendLabel(sendMode, nativeSteerMode, turnSelectionChanged);
  const steerActionRequired = controls?.native_steer_state === "action_required";
  const replacementRecoveryAvailable = steerActionRequired && interruptSteerAvailable && [
    "active_turn_not_steerable", "target_turn_changed", "target_turn_completed",
  ].includes(controls?.native_steer_error_code ?? "");

  return {
    controls,
    nativeResumeAvailable,
    resumeAvailable,
    providerPermissions,
    running,
    finishAvailable,
    sendMode,
    providerSwitchRequested,
    providerSwitchAvailable,
    queueActionAvailable,
    sendActionLabel,
    replacementRecoveryAvailable,
  };
}
