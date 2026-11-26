import type { Dispatch, KeyboardEvent, SetStateAction } from "react";
import type { NavigateFunction } from "react-router-dom";
import type { RuntimeOwnerAdapter } from "@/lib/runtimeOwner/adapter";
import { conversationQueueUnavailable, resolveConversationAction } from "@/lib/runtimeOwner/conversationKernel";
import { ACTIVE_OWNER_STATUSES } from "@/lib/runtimeOwner/status";
import type { RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { conversationBusy, type ConversationOperation } from "@/lib/runtimeOwner/conversationState";
import type { useContinuationSelection } from "./useContinuationSelection";
import { deriveConversationControls } from "./runtimeOwnerConversationControls";
import { useConversationOperation } from "./useConversationOperation";

export function useRuntimeOwnerActions({
  owner,
  ownerAdapter,
  pollError,
  loadAll,
  setActionError,
  navigate,
  projectId,
  continuationModelPayload,
  steering,
  setSteering,
  continuationModelProvider,
  attachments,
  setAttachments,
  forceReplaceNext,
  setForceReplaceNext,
  currentRuntimeProvider,
  setPermissionBusy,
  titleDraft,
  setEditingTitle,
}: {
  owner: RuntimeOwnerView | null;
  ownerAdapter: RuntimeOwnerAdapter;
  pollError?: string | null;
  loadAll: (mode: "initial" | "poll") => Promise<void>;
  setActionError: Dispatch<SetStateAction<string | null>>;
  navigate: NavigateFunction;
  projectId: string | undefined;
  continuationModelPayload: ReturnType<typeof useContinuationSelection>["continuationModelPayload"];
  steering: string;
  setSteering: Dispatch<SetStateAction<string>>;
  continuationModelProvider: string;
  attachments: File[];
  setAttachments: Dispatch<SetStateAction<File[]>>;
  forceReplaceNext: boolean;
  setForceReplaceNext: Dispatch<SetStateAction<boolean>>;
  currentRuntimeProvider: string | undefined;
  setPermissionBusy: Dispatch<SetStateAction<string>>;
  titleDraft: string | null;
  setEditingTitle: Dispatch<SetStateAction<boolean>>;
}) {
  const { operation, state: conversationState } = useConversationOperation(ownerAdapter.base, owner, pollError);
  const sending = conversationBusy(conversationState);

  async function runOperation(
    kind: ConversationOperation,
    run: (isCurrent: () => boolean, requestID: string) => Promise<unknown>,
    accepted?: () => void,
  ) {
    const requestID = operation.begin(kind);
    if (!requestID) return;
    const isCurrent = () => operation.isCurrent(requestID);
    setActionError(null);
    try {
      const request = run(isCurrent, requestID);
      if (kind !== "stop") operation.track(request);
      await request;
      if (operation.isActive()) accepted?.();
      if (!isCurrent()) return;
      operation.accepted(requestID);
      await loadAll("poll");
      if (isCurrent()) operation.refreshed(requestID);
    } catch (e) {
      if (!isCurrent()) return;
      const error = e instanceof Error ? e.message : String(e);
      operation.rejected(requestID, error);
      setActionError(error);
    }
  }

  function clearSubmittedDraft() {
    setSteering((current) => current === steering ? "" : current);
    if (!owner?.capabilities.attachments) return;
    setAttachments((current) => current.filter((file) => !attachments.includes(file)));
  }

  function conversationControls() {
    if (!owner) return null;
    const selection = continuationModelPayload();
    return deriveConversationControls({
      owner, currentContinuation: owner.activeContinuation ?? owner.latestContinuation,
      currentRuntimeProvider, continuationModelProvider,
      continuationModelOverride: selection.model ?? "",
      continuationReasoningEffort: selection.reasoning_effort, forceReplaceNext,
    });
  }

  async function stop() {
    if (!owner) return;
    const pending = operation.pending();
    await runOperation("stop", async () => {
      if (pending.length) {
        await Promise.allSettled([ownerAdapter.stop(), ...pending]);
      }
      await ownerAdapter.stop();
    });
  }

  async function finishTask() {
    if (!owner?.capabilities.finish) return;
    await runOperation("finish", () => ownerAdapter.finish());
  }

  async function deleteTask() {
    if (!owner?.capabilities.delete) return;
    try {
      await ownerAdapter.remove();
      navigate(owner.kind === "session" ? "/sessions" : `/projects/${projectId}/tasks`);
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function resumeNative() {
    if (!owner?.capabilities.resumeWithoutMessage) return;
    await runOperation("resume", () => ownerAdapter.resume(continuationModelPayload()));
  }

  async function queueSteer() {
    const directive = steering.trim();
    const controls = conversationControls();
    if (!directive || !owner || !controls?.queueActionAvailable || controls.sendMode === "unavailable") return;
    if (conversationQueueUnavailable(owner, continuationModelProvider.trim())) {
      setActionError("Queue cannot switch a Session model provider; use Switch provider and resume.");
      return;
    }
    await runOperation("queue", () => ownerAdapter.queueSteer(
      directive, continuationModelPayload(), owner.capabilities.attachments ? attachments : [],
    ), clearSubmittedDraft);
  }

  async function sendConversationMessage() {
    const message = steering.trim();
    const controls = conversationControls();
    if (!message || !owner || !controls || controls.sendMode === "unavailable"
      || (controls.providerSwitchRequested && !controls.providerSwitchAvailable)) return;
    await runOperation("send", async (isCurrent, requestID) => {
      const modelPayload = continuationModelPayload();
      const action = resolveConversationAction(owner, {
        running: ACTIVE_OWNER_STATUSES.has(owner.status),
        nativeSteerAvailable: owner.runtimeControls?.native_steer_available ?? false,
        interruptSteerAvailable: owner.runtimeControls?.interrupt_steer_available ?? false,
        queueSteerAvailable: owner.runtimeControls?.queue_steer_available ?? true,
        resumeAvailable: owner.runtimeControls?.resume_available ?? !ACTIVE_OWNER_STATUSES.has(owner.status),
        nativeResumeAvailable: owner.runtimeControls?.native_resume_available ?? false,
        precedingProviderID: owner.runtimeControls?.turn_selection?.model_provider_id?.trim() ?? "",
        selectedProviderID: continuationModelProvider.trim(),
        runtimeProvider: currentRuntimeProvider ?? "",
        nativeSteerMode: owner.runtimeControls?.native_steer_mode,
        forceReplace: forceReplaceNext,
      }, requestID);
      await action.run(message, modelPayload, ownerAdapter, owner.capabilities.attachments ? attachments : [], isCurrent, () => {
        if (!operation.isActive()) return;
        clearSubmittedDraft();
        setForceReplaceNext(false);
      });
    });
  }

  function handleComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    void sendConversationMessage();
  }

  async function respondToPermission(permissionRequestID: string, decision: "allow" | "deny") {
    setPermissionBusy(permissionRequestID);
    try {
      await ownerAdapter.respondPermission(permissionRequestID, decision);
      setActionError(null);
      loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setPermissionBusy("");
    }
  }

  async function saveSessionTitle() {
    if (owner?.kind !== "session" || !titleDraft?.trim() || titleDraft.trim() === owner.title.trim()) return;
    try {
      await ownerAdapter.rename(titleDraft.trim());
      setEditingTitle(false);
      setActionError(null);
      await loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function changeSessionLifecycle(action: "archive" | "restore") {
    if (owner?.kind !== "session") return;
    try {
      await ownerAdapter.changeLifecycle(action);
      setActionError(null);
      await loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  return {
    sending,
    conversationState,
    stop,
    finishTask,
    deleteTask,
    resumeNative,
    queueSteer,
    sendConversationMessage,
    handleComposerKeyDown,
    respondToPermission,
    saveSessionTitle,
    changeSessionLifecycle,
  };
}
