import type { Dispatch, KeyboardEvent, RefObject, SetStateAction } from "react";
import type { NavigateFunction } from "react-router-dom";
import type { RuntimeOwnerAdapter } from "@/lib/runtimeOwner/adapter";
import { conversationQueueUnavailable, newSteerRequestID, resolveConversationAction } from "@/lib/runtimeOwner/conversationKernel";
import { ACTIVE_OWNER_STATUSES } from "@/lib/runtimeOwner/status";
import type { RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import type { useContinuationSelection } from "./useContinuationSelection";

export function useRuntimeOwnerActions({
  owner,
  ownerAdapter,
  loadAll,
  setActionError,
  navigate,
  projectId,
  continuationModelPayload,
  steering,
  setSteering,
  sending,
  setSending,
  continuationModelProvider,
  attachments,
  setAttachments,
  attachmentInput,
  forceReplaceNext,
  setForceReplaceNext,
  currentRuntimeProvider,
  setPermissionBusy,
  titleDraft,
  setEditingTitle,
}: {
  owner: RuntimeOwnerView | null;
  ownerAdapter: RuntimeOwnerAdapter;
  loadAll: (mode: "initial" | "poll") => Promise<void>;
  setActionError: Dispatch<SetStateAction<string | null>>;
  navigate: NavigateFunction;
  projectId: string | undefined;
  continuationModelPayload: ReturnType<typeof useContinuationSelection>["continuationModelPayload"];
  steering: string;
  setSteering: Dispatch<SetStateAction<string>>;
  sending: boolean;
  setSending: Dispatch<SetStateAction<boolean>>;
  continuationModelProvider: string;
  attachments: File[];
  setAttachments: Dispatch<SetStateAction<File[]>>;
  attachmentInput: RefObject<HTMLInputElement | null>;
  forceReplaceNext: boolean;
  setForceReplaceNext: Dispatch<SetStateAction<boolean>>;
  currentRuntimeProvider: string | undefined;
  setPermissionBusy: Dispatch<SetStateAction<string>>;
  titleDraft: string;
  setEditingTitle: Dispatch<SetStateAction<boolean>>;
}) {
  async function stop() {
    if (!owner) return;
    try {
      await ownerAdapter.stop();
      setActionError(null);
      loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function finishTask() {
    if (!owner?.capabilities.finish) return;
    try {
      await ownerAdapter.finish();
      setActionError(null);
      loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
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
    try {
      await ownerAdapter.resume(continuationModelPayload());
      setActionError(null);
      loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function queueSteer() {
    const directive = steering.trim();
    if (!directive || sending || !owner) return;
    if (conversationQueueUnavailable(owner, continuationModelProvider.trim())) {
      setActionError("Queue cannot switch a Session model provider; use Switch provider and resume.");
      return;
    }
    setSending(true);
    try {
      await ownerAdapter.queueSteer(directive, continuationModelPayload(), owner.capabilities.attachments ? attachments : []);
      if (owner.capabilities.attachments) {
        setAttachments([]);
        if (attachmentInput.current) attachmentInput.current.value = "";
      }
      setSteering("");
      setActionError(null);
      await loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setSending(false);
    }
  }

  async function sendConversationMessage() {
    const message = steering.trim();
    if (!message || sending || !owner) return;
    setSending(true);
    const requestID = newSteerRequestID();
    try {
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
      await action.run(message, modelPayload, ownerAdapter, owner.capabilities.attachments ? attachments : []);
      setSteering("");
      setForceReplaceNext(false);
      if (owner.capabilities.attachments) {
        setAttachments([]);
        if (attachmentInput.current) attachmentInput.current.value = "";
      }
      setActionError(null);
      await loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setSending(false);
    }
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
    if (owner?.kind !== "session" || !titleDraft.trim()) return;
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
