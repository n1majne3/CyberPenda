import { useEffect, useState } from "react";
import { useParams, useNavigate, useSearchParams } from "react-router-dom";
import { ArrowDown, ArrowUp, Loader2 } from "lucide-react";
import { apiGet, type FinishReadiness, type ModelProvider, type RuntimePlugin, type RuntimeProfile } from "@/lib/api";
import { Button } from "@/components/ui";
import { ErrorState, LoadingState } from "@/components/shared";
import { AgentTranscriptView } from "@/components/task-transcript/AgentTranscriptView";
import { useDocumentVisibility } from "@/lib/useDocumentVisibility";
import { taskRuntimeOwnerAdapter, sessionRuntimeOwnerAdapter, type RuntimeOwnerAdapter } from "@/lib/runtimeOwner/adapter";
import { ACTIVE_OWNER_STATUSES } from "@/lib/runtimeOwner/status";
import { FGSBoard } from "./FGSPage";
import { emptyHistory, type RuntimeOwnerKind, type RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { runtimeOwnerBlackboardMode, liveReasoningEntryID } from "./runtimeOwnerView";
import { TranscriptList } from "./RuntimeOwnerTranscript";
import { ProviderPermissionRequests, RuntimeOwnerComposer } from "./RuntimeOwnerComposer";
import { RuntimeOwnerConfirmDialogs, RuntimeOwnerHeader, RuntimeOwnerNotices } from "./RuntimeOwnerHeader";
import { deriveConversationControls } from "./runtimeOwnerConversationControls";
import { useRuntimeOwnerActions } from "./useRuntimeOwnerActions";
import { useOwnerHistoryState, useRuntimeOwnerHistory } from "./useRuntimeOwnerHistory";
import { useConversationFollow } from "./useConversationFollow";
import { useContinuationSelection } from "./useContinuationSelection";
import { RuntimeOwnerViewTabs } from "./RuntimeOwnerViewTabs";
import { RuntimeOwnerShell } from "./RuntimeOwnerChrome";

export function TaskDetailPage() {
  return <RuntimeOwnerDetailPage ownerKind="task" />;
}

export function RuntimeOwnerDetailPage({ ownerKind }: { ownerKind: RuntimeOwnerKind }) {
  const { projectId, taskId, sessionId } = useParams<{ projectId: string; taskId: string; sessionId: string }>();
  const isSession = ownerKind === "session";
  const ownerID = isSession ? sessionId : taskId;
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const isVisible = useDocumentVisibility();
  const [owner, setOwner] = useState<RuntimeOwnerView | null>(null);
  const [finishReadiness, setFinishReadiness] = useState<FinishReadiness | null>(null);
  const [requestedView, setActiveView] = useState<"conversation" | "timeline" | "blackboard">(
    () => searchParams.get("view") === "blackboard" ? "blackboard" : searchParams.get("view") === "timeline" ? "timeline" : "conversation",
  );
  const hasSessionBlackboard = isSession && owner !== null && owner.id === ownerID && owner.blackboardProtocol === "fgs" && runtimeOwnerBlackboardMode(owner) !== "disabled";
  const activeView = requestedView === "blackboard" && !hasSessionBlackboard ? "conversation" : requestedView;
  const { historyRef, history, commitHistory } = useOwnerHistoryState();
  const {
    autoFollow,
    autoFollowRef,
    conversationEnd,
    cancelAutoFollowSettlement,
    resetConversationFollow,
    transcriptDisplayRows,
    transcriptWindow,
    bindTranscriptViewport,
    bindTranscriptRows,
    scrollToLatest,
    scrollToTop,
  } = useConversationFollow({ owner, activeView, history, historyRef, commitHistory });
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [steering, setSteering] = useState("");
  const [forceReplaceNext, setForceReplaceNext] = useState(false);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [runtimePlugins, setRuntimePlugins] = useState<RuntimePlugin[]>([]);
  const {
    continuationModelProvider,
    continuationModelOverride,
    setContinuationModelOverride,
    continuationReasoningEffort,
    setContinuationReasoningEffort,
    setTurnSelectionSeeded,
    currentRuntimeProvider,
    continuationModelProviders,
    continuationModelOptions,
    continuationModelPayload,
    selectContinuationModelProvider,
  } = useContinuationSelection({ owner, profiles, modelProviders, runtimePlugins });
  const [permissionBusy, setPermissionBusy] = useState("");
  const [editingTitle, setEditingTitle] = useState(false);
  const [titleDraft, setTitleDraft] = useState("");
  const [attachments, setAttachments] = useState<File[]>([]);
  const [confirmAction, setConfirmAction] = useState<"stop" | "finish" | "delete" | null>(null);
  const base = isSession ? `/api/sessions/${sessionId}` : `/api/projects/${projectId}/tasks/${taskId}`;
  // The behavioral owner adapter owns every endpoint and payload shape; the
  // workspace keeps only rendering and orchestration.
  const ownerAdapter: RuntimeOwnerAdapter = isSession ? sessionRuntimeOwnerAdapter(base) : taskRuntimeOwnerAdapter(base);

  const {
    abortRef,
    pollError,
    timelineViewport,
    resetTimelineTail,
    loadAll,
    loadOlderConversation,
    loadOlderTimeline,
    handleTimelineSortDirection,
    handleTimelineAtTail,
    jumpToTimelineLatest,
    scrollTimelineToTop,
    scrollTimelineToBottom,
  } = useRuntimeOwnerHistory({
    ownerID,
    isSession,
    projectId,
    activeView,
    ownerAdapter,
    autoFollowRef,
    historyRef,
    commitHistory,
    setOwner,
    setFinishReadiness,
    setTitleDraft,
    setError,
    setActionError,
  });

  /* eslint-disable react-hooks/exhaustive-deps */
  useEffect(() => {
    // Initial load on mount/owner change. A route change aborts old requests
    // and atomically replaces owner-local history with an empty record before
    // any merge, so Timeline and Transcript content from the prior owner can
    // never appear on the new route (#202).
    setTurnSelectionSeeded(false);
    resetConversationFollow();
    resetTimelineTail();
    commitHistory(emptyHistory());
    loadAll("initial");
    Promise.all([
      apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles").then((d) => setProfiles(d.profiles ?? [])),
      apiGet<{ providers: ModelProvider[] }>("/api/model-providers").then((d) => setModelProviders(d.providers ?? [])),
      apiGet<{ plugins: RuntimePlugin[] }>("/api/runtime-plugins").then((d) => setRuntimePlugins(d.plugins ?? [])),
    ]).catch(() => {});
    return () => {
      abortRef.current?.abort();
      cancelAutoFollowSettlement();
    };
  }, [isSession, ownerID, projectId]);
  /* eslint-enable react-hooks/exhaustive-deps */

  // Poll events while the runtime owner is active and the tab is visible.
  // Depends on status/visibility only so the interval is not reset every render;
  // loadAll/owner are intentionally omitted.
  /* eslint-disable react-hooks/exhaustive-deps */
  useEffect(() => {
    if (!isVisible || !owner || !ACTIVE_OWNER_STATUSES.has(owner.status)) return;
    const id = setInterval(() => void loadAll("poll"), 1000);
    return () => clearInterval(id);
  }, [owner?.status, isVisible]);
  /* eslint-enable react-hooks/exhaustive-deps */


  const {
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
  } = useRuntimeOwnerActions({
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
  });

  function selectView(view: "conversation" | "timeline" | "blackboard") {
    setActiveView(view);
    const next = new URLSearchParams(searchParams);
    next.set("view", view);
    setSearchParams(next, { replace: true });
    // Unseen counts belong to the visible view only; switching resets them.
    if (historyRef.current.timelineUnseen > 0 || historyRef.current.transcriptUnseen > 0) {
      commitHistory({ ...historyRef.current, timelineUnseen: 0, transcriptUnseen: 0 });
    }
  }

  function selectFocus(focused: boolean) {
    const next = new URLSearchParams(searchParams);
    if (focused) {
      next.set("focus", "1");
    } else {
      next.delete("focus");
    }
    setSearchParams(next, { replace: true });
  }

  if (error) {
    return (
      <RuntimeOwnerShell projectChrome={!isSession}>
        <ErrorState error={error} title="Couldn't load this conversation" className="m-6 max-w-2xl" />
      </RuntimeOwnerShell>
    );
  }
  if (!owner) {
    return (
      <RuntimeOwnerShell projectChrome={!isSession}>
        <LoadingState label="Loading conversation" className="m-6 max-w-2xl" />
      </RuntimeOwnerShell>
    );
  }
  const currentContinuation = owner.activeContinuation ?? owner.latestContinuation;
  const {
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
  } = deriveConversationControls({
    owner,
    currentContinuation,
    currentRuntimeProvider,
    continuationModelProvider,
    continuationModelOverride,
    continuationReasoningEffort,
    forceReplaceNext,
  });
  const focusMode = searchParams.get("focus") === "1";
  const runtimeDisplayName = profiles.find((profile) => profile.id === owner.runtimeProfileID)?.name
    ?? currentContinuation?.runtimeProvider
    ?? "Runtime";

  return (
    <RuntimeOwnerShell
      projectChrome={owner.capabilities.projectChrome}
      hideChrome={focusMode}
      data-testid="task-detail-shell"
      className={focusMode ? "h-full max-w-none p-0 lg:p-0" : "flex h-full flex-col lg:min-h-0 lg:overflow-hidden"}
      contentClassName={focusMode ? undefined : "w-full"}
      bodyClassName={focusMode ? "flex h-full min-h-0 flex-col" : "flex min-h-[32rem] flex-1 flex-col pb-0 lg:min-h-0"}
    >
      <RuntimeOwnerHeader
        owner={owner}
        projectId={projectId}
        currentContinuation={currentContinuation}
        controls={controls}
        editingTitle={editingTitle}
        setEditingTitle={setEditingTitle}
        titleDraft={titleDraft}
        setTitleDraft={setTitleDraft}
        saveSessionTitle={saveSessionTitle}
        changeSessionLifecycle={changeSessionLifecycle}
        running={running}
        finishAvailable={finishAvailable}
        setConfirmAction={setConfirmAction}
        resumeNative={resumeNative}
        resumeAvailable={resumeAvailable}
        nativeResumeAvailable={nativeResumeAvailable}
        focusMode={focusMode}
        selectFocus={selectFocus}
        navigate={navigate}
      />

      <RuntimeOwnerNotices
        owner={owner}
        focusMode={focusMode}
        finishReadiness={finishReadiness}
        pollError={pollError}
        onRetryPoll={() => void loadAll("poll")}
      />

      <RuntimeOwnerViewTabs
        activeView={activeView}
        hasSessionBlackboard={hasSessionBlackboard}
        selectView={selectView}
        autoFollow={autoFollow}
        scrollToTop={scrollToTop}
        scrollToLatest={scrollToLatest}
        scrollTimelineToTop={scrollTimelineToTop}
        scrollTimelineToBottom={scrollTimelineToBottom}
      />

      <div
        data-testid="task-workspace"
        className="flex min-h-[28rem] min-w-0 flex-1 flex-col overflow-visible bg-transparent md:overflow-hidden lg:min-h-0"
      >
        {activeView === "blackboard" ? (
          <div className="min-h-0 flex-1 overflow-auto p-3 pb-44 sm:p-5 md:pb-5">
            <FGSBoard key={owner.id} scope="sessions" id={owner.id} />
          </div>
        ) : activeView === "timeline" ? (
          <div className="min-h-0 flex-1 overflow-hidden p-2 pb-44 sm:p-3 md:pb-5">
            <AgentTranscriptView
              owner={owner}
              items={history.timeline}
              profileName={profiles.find((p) => p.id === owner.runtimeProfileID)?.name}
              isLive={ACTIVE_OWNER_STATUSES.has(owner.status)}
              scrollRef={timelineViewport}
              onAtTailChange={handleTimelineAtTail}
              onSortDirectionChange={handleTimelineSortDirection}
              unseenCount={history.timelineUnseen}
              onShowLatest={jumpToTimelineLatest}
              footer={
                history.timelineHasOlder ? (
                  <div className="flex justify-center p-3">
                    <Button
                      variant="outline"
                      size="sm"
                      data-testid="load-older-timeline"
                      onClick={() => void loadOlderTimeline()}
                      disabled={history.loadingOlder === "timeline"}
                    >
                      {history.loadingOlder === "timeline" ? (
                        <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" />
                      ) : (
                        <ArrowUp className="h-3.5 w-3.5" />
                      )}
                      Load older events
                    </Button>
                  </div>
                ) : undefined
              }
            />
            {providerPermissions.length > 0 && (
              <ProviderPermissionRequests
                permissions={providerPermissions}
                permissionBusy={permissionBusy}
                onRespond={respondToPermission}
              />
            )}
          </div>
        ) : (
          <div
            ref={bindTranscriptViewport}
            data-testid="conversation-workspace"
            className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain bg-transparent px-0 pt-6 pb-44 md:pb-5 [overflow-anchor:none]"
          >
            <div className="mx-auto max-w-[980px]">
              {history.transcriptHasOlder && (
                <div className="mb-2 flex justify-center">
                  <Button
                    variant="outline"
                    size="sm"
                    data-testid="load-older-transcript"
                    onClick={() => void loadOlderConversation()}
                    disabled={history.loadingOlder === "transcript"}
                  >
                    {history.loadingOlder === "transcript" ? (
                      <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" />
                    ) : (
                      <ArrowUp className="h-3.5 w-3.5" />
                    )}
                    Load older messages
                  </Button>
                </div>
              )}
              {transcriptWindow.spacerBefore > 0 && (
                <div aria-hidden="true" data-testid="transcript-spacer-before" style={{ height: transcriptWindow.spacerBefore }} />
              )}
              <TranscriptList
                rows={transcriptDisplayRows.slice(transcriptWindow.startIndex, transcriptWindow.endIndex)}
                rowsRef={bindTranscriptRows}
                endRef={conversationEnd}
                liveReasoningID={liveReasoningEntryID(
                  history.transcript,
                  owner.runtimeActivity?.liveness === "live" && owner.runtimeActivity.turn_activity === "busy",
                )}
                runtimeLabel={runtimeDisplayName}
              />
              {transcriptWindow.spacerAfter > 0 && (
                <div aria-hidden="true" data-testid="transcript-spacer-after" style={{ height: transcriptWindow.spacerAfter }} />
              )}
              {providerPermissions.length > 0 && (
                <ProviderPermissionRequests
                  permissions={providerPermissions}
                  permissionBusy={permissionBusy}
                  onRespond={respondToPermission}
                />
              )}
            </div>
            {history.transcriptUnseen > 0 && (
              <button
                type="button"
                data-testid="unseen-transcript-indicator"
                onClick={scrollToLatest}
                className="absolute bottom-28 left-1/2 z-10 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-border bg-background/95 px-3 py-1.5 text-xs font-medium text-foreground shadow-md transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <ArrowDown className="h-3.5 w-3.5" />
                {history.transcriptUnseen} new {history.transcriptUnseen === 1 ? "message" : "messages"}
              </button>
            )}
          </div>
        )}

        <RuntimeOwnerComposer
          ownerLabel={owner.kind === "session" ? "Session" : "Task"}
          value={steering}
          onChange={setSteering}
          onKeyDown={handleComposerKeyDown}
          onSend={() => void sendConversationMessage()}
          onQueue={() => void queueSteer()}
          onStop={() => setConfirmAction("stop")}
          onFinish={() => setConfirmAction("finish")}
          finishAvailable={finishAvailable}
          sending={sending}
          conversationState={conversationState}
          running={running}
          queueAvailable={queueActionAvailable}
          queueDisabled={owner.kind === "session" && providerSwitchRequested}
          providerSwitchRequested={providerSwitchRequested}
          providerSwitchAvailable={providerSwitchAvailable}
          sendMode={sendMode}
          sendActionLabel={sendActionLabel}
          actionError={actionError}
          steerError={controls?.native_steer_error}
          replacementRecoveryAvailable={replacementRecoveryAvailable}
          replacementSelected={forceReplaceNext}
          onUseReplacement={() => setForceReplaceNext(true)}
          continuationModelProviders={continuationModelProviders}
          continuationModelProvider={continuationModelProvider}
          continuationModelOverride={continuationModelOverride}
          continuationModelOptions={continuationModelOptions}
          continuationReasoningEffort={continuationReasoningEffort}
          onSelectProvider={selectContinuationModelProvider}
          onSelectModel={setContinuationModelOverride}
          onSelectReasoningEffort={setContinuationReasoningEffort}
          attachments={owner.capabilities.attachments ? attachments : undefined}
          onAttachmentsChange={owner.capabilities.attachments ? setAttachments : undefined}
        />
      </div>

      <RuntimeOwnerConfirmDialogs
        owner={owner}
        confirmAction={confirmAction}
        setConfirmAction={setConfirmAction}
        stop={stop}
        finishTask={finishTask}
        deleteTask={deleteTask}
      />
    </RuntimeOwnerShell>
  );
}
