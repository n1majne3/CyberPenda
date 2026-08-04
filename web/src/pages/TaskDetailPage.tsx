import { useEffect, useState, useRef, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { useParams, useNavigate, useSearchParams } from "react-router-dom";
import { Square, Send, Terminal, Activity, GitBranch, MessageSquare, Play, ChevronRight, Wrench, User, ArrowDown, ArrowUp, CheckCircle2, Trash2, CircleX, KeyRound, ListPlus, Loader2, Maximize2, Minimize2, Flag, RefreshCcw, TriangleAlert, Archive, ArchiveRestore, Pencil, Paperclip } from "lucide-react";
import { apiDelete, apiGet, apiPatch, apiPost, apiPostForm, type BlackboardConclusionMode, type BlackboardConclusionView, type ModelProvider, type ProviderPermissionRequest, type RuntimeActivity, type RuntimeControls, type RuntimePlugin, type RuntimeProfile, type Session, type SessionContinuation, type Task, type TaskContinuation, type TaskTimeline, type TaskTimelineItem, type TaskTranscript, type TaskTranscriptEntry } from "@/lib/api";
import { Button, Badge, Input, Select, Textarea } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { ErrorState, LoadingState, PageContainer } from "@/components/shared";
import { AgentTranscriptView } from "@/components/task-transcript/AgentTranscriptView";
import { AttachmentFileRow } from "@/components/AttachmentPicker";
import { collapsedTranscriptTitle, toolCallFields } from "./taskDetailView";
import { displayReasoningEffort, REASONING_EFFORT_VALUES, selectableModelProviders } from "./runtimeProfileForm";
import { modelsForProvider } from "./taskLaunchForm";
import { formatDateTime } from "@/lib/format";

const ACTIVE = new Set(["running", "paused"]);
const DELETABLE = new Set(["completed", "failed", "stopped", "interrupted"]);

function newSteerRequestID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `steer-${Math.random().toString(36).slice(2)}-${performance.now().toString(36)}`;
}

function newBlackboardRetryID() {
  return `blackboard-retry-${newSteerRequestID()}`;
}

type RuntimeOwnerKind = "task" | "session";

type RuntimeOwnerCapabilities = {
  projectChrome: boolean;
  rename: boolean;
  archive: boolean;
  restore: boolean;
  delete: boolean;
  finish: boolean;
  resumeWithoutMessage: boolean;
  attachments: boolean;
};

type RuntimeOwnerView = {
  kind: RuntimeOwnerKind;
  id: string;
  title: string;
  status: string;
  lifecycle?: Session["lifecycle"];
  runner: string;
  runtimeProfileID: string;
  blackboardConclusionMode?: BlackboardConclusionMode;
  blackboardConclusion?: BlackboardConclusionView;
  runtimeControls?: RuntimeControls;
  runtimeActivity?: RuntimeActivity;
  activeContinuation?: RuntimeOwnerContinuation;
  latestContinuation?: RuntimeOwnerContinuation;
  createdAt: string;
  updatedAt: string;
  capabilities: RuntimeOwnerCapabilities;
};

type RuntimeOwnerContinuation = {
  id: string;
  number: number;
  runtimeProfileID: string;
  runtimeProvider: string;
  runner: string;
  status: string;
  nativeSessionID?: string;
};

export function TaskDetailPage() {
  return <RuntimeOwnerDetailPage ownerKind="task" />;
}

export function RuntimeOwnerDetailPage({ ownerKind }: { ownerKind: RuntimeOwnerKind }) {
  const { projectId, taskId, sessionId } = useParams<{ projectId: string; taskId: string; sessionId: string }>();
  const isSession = ownerKind === "session";
  const ownerID = isSession ? sessionId : taskId;
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [owner, setOwner] = useState<RuntimeOwnerView | null>(null);
  const [timeline, setTimeline] = useState<TaskTimelineItem[]>([]);
  const [transcript, setTranscript] = useState<TaskTranscriptEntry[]>([]);
  const [activeView, setActiveView] = useState<"conversation" | "timeline">(
    () => searchParams.get("view") === "timeline" ? "timeline" : "conversation",
  );
  const [autoFollow, setAutoFollow] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [steering, setSteering] = useState("");
  const [continuationModelProvider, setContinuationModelProvider] = useState("");
  const [continuationModelOverride, setContinuationModelOverride] = useState("");
  const [continuationReasoningEffort, setContinuationReasoningEffort] = useState("high");
  const [turnSelectionSeeded, setTurnSelectionSeeded] = useState(false);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [runtimePlugins, setRuntimePlugins] = useState<RuntimePlugin[]>([]);
  const [permissionBusy, setPermissionBusy] = useState("");
  const [retryingConclusion, setRetryingConclusion] = useState(false);
  const [editingTitle, setEditingTitle] = useState(false);
  const [titleDraft, setTitleDraft] = useState("");
  const [attachments, setAttachments] = useState<File[]>([]);
  const [confirmAction, setConfirmAction] = useState<"stop" | "finish" | "delete" | null>(null);
  const attachmentInput = useRef<HTMLInputElement>(null);
  const conversationViewport = useRef<HTMLDivElement>(null);
  const conversationEnd = useRef<HTMLDivElement>(null);
  const autoFollowRef = useRef(true);
  // Monotonic generation so slower in-flight polls cannot overwrite newer data.
  const loadGeneration = useRef(0);
  const abortRef = useRef<AbortController | null>(null);

  const base = isSession ? `/api/sessions/${sessionId}` : `/api/projects/${projectId}/tasks/${taskId}`;

  async function loadAll() {
    if (!ownerID || (!isSession && !projectId)) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const generation = ++loadGeneration.current;
    try {
      const loaded = isSession
        ? await loadSessionWorkspace(base, controller.signal)
        : await loadTaskWorkspace(base, controller.signal);
      if (generation !== loadGeneration.current || controller.signal.aborted) {
        return;
      }
      setOwner(loaded.owner);
      if (loaded.owner.kind === "session") setTitleDraft(loaded.owner.title);
      setTimeline(loaded.timeline);
      setTranscript(loaded.transcript);
      setError(null);
    } catch (e) {
      if (controller.signal.aborted || generation !== loadGeneration.current) {
        return;
      }
      setError((e as Error).message);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    // Initial load on mount/owner change. loadAll() is reused by the poll loop
    // and event handlers.
    setTurnSelectionSeeded(false);
    loadAll();
    Promise.all([
      apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles").then((d) => setProfiles(d.profiles ?? [])),
      apiGet<{ providers: ModelProvider[] }>("/api/model-providers").then((d) => setModelProviders(d.providers ?? [])),
      apiGet<{ plugins: RuntimePlugin[] }>("/api/runtime-plugins").then((d) => setRuntimePlugins(d.plugins ?? [])),
    ]).catch(() => {});
    return () => {
      abortRef.current?.abort();
    };
  }, [isSession, ownerID, projectId]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  // Seed the composer once from the preceding Runtime Turn Selection. Later
  // submits retain the operator's choice instead of resetting.
  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    if (turnSelectionSeeded || !owner) return;
    const preceding = owner.runtimeControls?.turn_selection;
    if (!preceding) {
      setTurnSelectionSeeded(true);
      return;
    }
    setContinuationModelProvider(preceding.model_provider_id?.trim() ?? "");
    setContinuationModelOverride(preceding.model?.trim() ?? "");
    setContinuationReasoningEffort(displayReasoningEffort(preceding.reasoning_effort));
    setTurnSelectionSeeded(true);
  }, [owner, turnSelectionSeeded]);
  /* eslint-enable react-hooks/set-state-in-effect */

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    if (!owner || activeView !== "conversation") return;

    // Reset auto-follow when the runtime owner changes. This is an intentional
    // synchronous reset, not a cascading render.
    autoFollowRef.current = true;
    setAutoFollow(true);
    const container = conversationViewport.current;
    if (!container) return;

    function updateAutoFollow() {
      const pinned = isNearScrollBottom(container!);
      autoFollowRef.current = pinned;
      setAutoFollow((current) => current === pinned ? current : pinned);
    }

    container.addEventListener("scroll", updateAutoFollow, { passive: true });
    return () => {
      container.removeEventListener("scroll", updateAutoFollow);
    };
  }, [owner?.id, activeView]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  // Poll events while the runtime owner is active. Depends on status only so the
  // interval is not reset every render; loadAll/owner are intentionally omitted.
  /* eslint-disable react-hooks/exhaustive-deps */
  useEffect(() => {
    if (!owner || !ACTIVE.has(owner.status)) return;
    const id = setInterval(loadAll, 1000);
    return () => clearInterval(id);
  }, [owner?.status]);
  /* eslint-enable react-hooks/exhaustive-deps */

  useEffect(() => {
    if (activeView === "conversation" && autoFollowRef.current) {
      conversationEnd.current?.scrollIntoView?.({ behavior: prefersReducedMotion() ? "auto" : "smooth", block: "end" });
    }
  }, [activeView, timeline, transcript]);

  const currentProfileRuntimeProvider = profiles.find((profile) => profile.id === owner?.runtimeProfileID)?.provider;
  const currentRuntimeProvider =
    owner?.runtimeControls?.runtime_provider ??
    owner?.activeContinuation?.runtimeProvider ??
    owner?.latestContinuation?.runtimeProvider ??
    currentProfileRuntimeProvider;
  const runtimePlugin = runtimePlugins.find((plugin) => plugin.id === currentRuntimeProvider);
  const continuationModelProviders = selectableModelProviders(
    modelProviders,
    runtimePlugin,
    continuationModelProvider,
  );
  const selectedContinuationModelProvider =
    continuationModelProviders.find((provider) => provider.id === continuationModelProvider) ??
    modelProviders.find((provider) => provider.id === continuationModelProvider);
  // Compute inline instead of useMemo: the compiler cannot preserve memoization
  // over selectedContinuationModelProvider (it may be mutated later), and the
  // derivation is a cheap filter.
  const continuationModelOptions = modelsForProvider(selectedContinuationModelProvider);

  // Keep continuationModelOverride valid for the selected provider by adjusting
  // state during render (not in an effect), comparing against the previous
  // provider/option set so we only reset when the selection actually changes.
  const overrideKey = `${continuationModelProvider}:${continuationModelOptions.join(",")}`;
  const [lastOverrideKey, setLastOverrideKey] = useState("");
  if (lastOverrideKey !== overrideKey) {
    setLastOverrideKey(overrideKey);
    if (!continuationModelProvider) {
      if (continuationModelOverride) setContinuationModelOverride("");
    } else if (!continuationModelOverride || !continuationModelOptions.includes(continuationModelOverride)) {
      setContinuationModelOverride(continuationModelOptions[0] ?? "");
    }
  }

  function scrollToLatest() {
    const container = conversationViewport.current;
    if (!container) return;
    autoFollowRef.current = true;
    setAutoFollow(true);
    container.scrollTo({ top: container.scrollHeight, behavior: prefersReducedMotion() ? "auto" : "smooth" });
  }

  function scrollToTop() {
    const container = conversationViewport.current;
    if (!container) return;
    autoFollowRef.current = false;
    setAutoFollow(false);
    container.scrollTo({ top: 0, behavior: prefersReducedMotion() ? "auto" : "smooth" });
  }

  async function stop() {
    if (!owner) return;
    try {
      await apiPost(`${base}/stop`, {});
      setActionError(null);
      loadAll();
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function finishTask() {
    if (!owner?.capabilities.finish) return;
    try {
      await apiPost(`${base}/finish`, {});
      setActionError(null);
      loadAll();
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function retryBlackboardConclusion() {
    if (retryingConclusion) return;
    setRetryingConclusion(true);
    try {
      const retried = owner?.kind === "session"
        ? sessionAsRuntimeOwner(await apiPost<Session>(`${base}/blackboard-conclusion/retry`, {}, {
          headers: { "Idempotency-Key": newBlackboardRetryID() },
        }))
        : taskAsRuntimeOwner(await apiPost<Task>(`${base}/blackboard-conclusion/retry`, {}, {
          headers: { "Idempotency-Key": newBlackboardRetryID() },
        }));
      setOwner(retried);
      setActionError(null);
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setRetryingConclusion(false);
    }
  }

  async function deleteTask() {
    if (!owner?.capabilities.delete) return;
    try {
      await apiDelete(base);
      navigate(owner.kind === "session" ? "/sessions" : `/projects/${projectId}/tasks`);
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function resumeNative() {
    if (!owner?.capabilities.resumeWithoutMessage) return;
    try {
      await apiPost(`${base}/resume`, continuationModelPayload());
      setActionError(null);
      loadAll();
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
      if (owner.kind === "session") {
        await postSessionRuntimeMessage(
          `${base}/steer/queue`,
          directive,
          continuationModelPayload(),
          attachments,
        );
        setAttachments([]);
        if (attachmentInput.current) attachmentInput.current.value = "";
      } else {
        await apiPost(`${base}/steer/queue`, { directive, ...continuationModelPayload() });
      }
      setSteering("");
      setActionError(null);
      await loadAll();
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
        running: ACTIVE.has(owner.status),
        nativeSteerAvailable: owner.runtimeControls?.native_steer_available ?? false,
        interruptSteerAvailable: owner.runtimeControls?.interrupt_steer_available ?? false,
        queueSteerAvailable: owner.runtimeControls?.queue_steer_available ?? true,
        resumeAvailable: owner.runtimeControls?.resume_available ?? !ACTIVE.has(owner.status),
        nativeResumeAvailable: owner.runtimeControls?.native_resume_available ?? false,
        precedingProviderID: owner.runtimeControls?.turn_selection?.model_provider_id?.trim() ?? "",
        selectedProviderID: continuationModelProvider.trim(),
        runtimeProvider: currentRuntimeProvider ?? "",
        nativeSteerMode: owner.runtimeControls?.native_steer_mode,
      }, requestID);
      await action.run(message, modelPayload, {
        session: (path, payload, files) => postSessionRuntimeMessage(path, payload.text, payload.selection, files),
        task: (path, payload) => apiPost(path, payload),
        attachments,
        base,
        ownerTitle: owner.title,
      });
      setSteering("");
      if (owner.kind === "session") {
        setAttachments([]);
        if (attachmentInput.current) attachmentInput.current.value = "";
      }
      setActionError(null);
      await loadAll();
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
      await apiPost(`${base}/permissions/${encodeURIComponent(permissionRequestID)}/respond`, {
        request_id: `permission-${newSteerRequestID()}`,
        decision,
      });
      setActionError(null);
      loadAll();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setPermissionBusy("");
    }
  }

  async function saveSessionTitle() {
    if (owner?.kind !== "session" || !titleDraft.trim()) return;
    try {
      await apiPatch<Session>(base, { title: titleDraft.trim() });
      setEditingTitle(false);
      setActionError(null);
      await loadAll();
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function changeSessionLifecycle(action: "archive" | "restore") {
    if (owner?.kind !== "session") return;
    try {
      await apiPost<Session>(`${base}/${action}`, {});
      setActionError(null);
      await loadAll();
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  function continuationModelPayload() {
    const providerID = continuationModelProvider.trim();
    const model = continuationModelOverride.trim();
    const effort = displayReasoningEffort(continuationReasoningEffort);
    // Every Runtime Turn sends the complete resolved selection. `model` is the
    // canonical field; model_override is kept as a compatibility alias.
    const payload: {
      model_provider_id?: string;
      model?: string;
      model_override?: string;
      reasoning_effort: string;
    } = {
      reasoning_effort: effort,
    };
    if (providerID) payload.model_provider_id = providerID;
    if (model) {
      payload.model = model;
      payload.model_override = model;
    }
    return payload;
  }

  function selectView(view: "conversation" | "timeline") {
    setActiveView(view);
    const next = new URLSearchParams(searchParams);
    next.set("view", view);
    setSearchParams(next, { replace: true });
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

  function selectContinuationModelProvider(providerID: string) {
    setContinuationModelProvider(providerID);
    const provider = continuationModelProviders.find((item) => item.id === providerID) ??
      modelProviders.find((item) => item.id === providerID);
    setContinuationModelOverride(modelsForProvider(provider)[0] ?? "");
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
  const controls = owner.runtimeControls;
  const nativeResumeAvailable = controls?.native_resume_available ?? Boolean(currentContinuation?.nativeSessionID);
  const resumeAvailable = !ACTIVE.has(owner.status) || controls?.resume_available === true;
  const queueSteerAvailable = controls?.queue_steer_available ?? true;
  const interruptSteerAvailable = controls?.interrupt_steer_available ?? nativeResumeAvailable;
  const nativeSteerAvailable = controls?.native_steer_available ?? false;
  const nativeSteerMode = controls?.native_steer_mode;
  const providerPermissions = controls?.provider_permissions ?? [];
  const running = ACTIVE.has(owner.status);
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
  const sendActionLabel = providerSwitchRequested
    ? providerSwitchAvailable ? "Switch provider and resume" : "Provider switch unavailable"
    : conversationSendLabel(sendMode, nativeSteerMode);
  const focusMode = searchParams.get("focus") === "1";

  return (
    <RuntimeOwnerShell
      projectChrome={owner.capabilities.projectChrome}
      hideChrome={focusMode}
      data-testid="task-detail-shell"
      className={focusMode ? "h-[calc(100dvh-3.5rem)] max-w-none p-0 md:h-dvh lg:p-0" : "flex min-h-full flex-col"}
      bodyClassName={focusMode ? "flex h-full min-h-0 flex-col" : "flex min-h-[32rem] flex-1 flex-col pb-0 lg:min-h-0"}
    >
      <div data-testid="task-session-header" className="flex h-12 shrink-0 items-center gap-2 border-b border-border px-2 sm:px-3">
        <StatusBadge status={owner.status} />
        <RuntimeActivityBadge activity={owner.runtimeActivity} />
        <BlackboardConclusionBadge owner={owner} />
        {owner.capabilities.rename && editingTitle ? (
          <div className="flex min-w-0 flex-1 items-center gap-1.5">
            <Input
              aria-label="Session title"
              value={titleDraft}
              onChange={(event) => setTitleDraft(event.target.value)}
              className="h-8 min-w-0"
              autoFocus
            />
            <Button size="sm" onClick={() => void saveSessionTitle()} disabled={!titleDraft.trim()}>Save</Button>
            <Button size="sm" variant="ghost" onClick={() => setEditingTitle(false)}>Cancel</Button>
          </div>
        ) : (
          <h1 className="min-w-0 flex-1 truncate text-sm font-medium" title={owner.title}>{owner.title}</h1>
        )}
        {currentContinuation && (
          <div
            data-testid="continuation-summary"
            title={`continuation #${currentContinuation.number} · runtime: ${currentContinuation.runtimeProvider} · runner: ${owner.runner} · status: ${currentContinuation.status}`}
            className="hidden min-w-0 flex-1 items-center gap-1 overflow-hidden whitespace-nowrap font-mono text-xs text-muted-foreground lg:flex"
          >
            <span>continuation #{currentContinuation.number}</span>
            <span aria-hidden="true">·</span>
            <span>runtime: {currentContinuation.runtimeProvider}</span>
            <span aria-hidden="true">·</span>
            <span>runner: {owner.runner}</span>
            <span className="hidden xl:inline" aria-hidden="true">·</span>
            <span className="hidden xl:inline">continuation status: {currentContinuation.status}</span>
            {(controls?.native_session_captured || currentContinuation.nativeSessionID) && (
              <span className="hidden 2xl:inline">native session: captured</span>
            )}
            {controls?.same_runtime_provider_only && <span className="hidden 2xl:inline">same runtime only</span>}
          </div>
        )}
        <div className="flex shrink-0 items-center gap-1">
          {owner.capabilities.rename && !editingTitle && (
            <Button size="icon" variant="ghost" onClick={() => setEditingTitle(true)} aria-label={`Rename ${owner.title}`} title="Rename Session" className="h-8 w-8">
              <Pencil className="h-4 w-4" />
            </Button>
          )}
          {owner.capabilities.archive && (
            <Button size="icon" variant="ghost" onClick={() => void changeSessionLifecycle("archive")} aria-label="Archive" title="Archive Session" className="h-8 w-8">
              <Archive className="h-4 w-4" />
            </Button>
          )}
          {owner.capabilities.restore && (
            <Button size="icon" variant="ghost" onClick={() => void changeSessionLifecycle("restore")} aria-label="Restore" title="Restore Session" className="h-8 w-8">
              <ArchiveRestore className="h-4 w-4" />
            </Button>
          )}
          {running && finishAvailable && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setConfirmAction("finish")}
              aria-label="Finish task"
              title="Finish Task: close Runtime and mark completed"
              data-testid="finish-task"
            >
              <Flag className="h-4 w-4" /> <span className="hidden sm:inline">Finish</span>
            </Button>
          )}
          {owner.capabilities.resumeWithoutMessage && !running && (
            <Button size="sm" variant="ghost" onClick={resumeNative} disabled={!resumeAvailable} aria-label="Resume" title={nativeResumeAvailable ? "Resume native session" : "Start a fresh continuation from the current Task state"}>
              <Play className="h-4 w-4" /> <span className="hidden sm:inline">Resume</span>
            </Button>
          )}
          {owner.capabilities.delete && (
            <Button size="icon" variant="ghost" onClick={() => setConfirmAction("delete")} aria-label={`Delete ${owner.kind}`} title={`Delete ${owner.kind === "session" ? "Session" : "Task"}`} className="h-8 w-8 text-destructive hover:text-destructive">
              <Trash2 className="h-4 w-4" />
            </Button>
          )}
          <Button
            size="icon"
            variant="ghost"
            onClick={() => selectFocus(!focusMode)}
            aria-label={focusMode ? "Exit focus view" : "Enter focus view"}
            title={focusMode ? "Exit focus view" : "Enter focus view"}
            className="h-10 w-10"
          >
            {focusMode ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
          </Button>
        </div>
      </div>

      {owner.kind === "session" && !focusMode && (
        <div role="note" className="shrink-0 border-b border-info/20 bg-info/5 px-3 py-2 text-xs text-muted-foreground">
          Non-Project Mode · no Project Scope; Runtime execution is unrestricted by Project Scope.
        </div>
      )}

      {owner.blackboardConclusion?.state === "action_required" && (
        <BlackboardConclusionRecovery
          errorCode={owner.blackboardConclusion.error_code}
          retryAvailable={owner.blackboardConclusion.retry_available === true}
          nextEligibleAt={owner.blackboardConclusion.next_eligible_at}
          retrying={retryingConclusion}
          onRetry={() => void retryBlackboardConclusion()}
        />
      )}

      <div className="flex h-10 shrink-0 items-center gap-1 border-b border-border px-2 sm:px-3">
        <button
          type="button"
          className={tabClass(activeView === "conversation")}
          aria-pressed={activeView === "conversation"}
          onClick={() => selectView("conversation")}
        >
          <MessageSquare className="h-4 w-4" /> Conversation
        </button>
        <button
          type="button"
          className={tabClass(activeView === "timeline")}
          aria-pressed={activeView === "timeline"}
          onClick={() => selectView("timeline")}
        >
          <Activity className="h-4 w-4" /> Timeline
        </button>
        <div className="ml-auto">
          {activeView === "conversation" && <FloatingScrollControls autoFollow={autoFollow} onTop={scrollToTop} onBottom={scrollToLatest} />}
        </div>
      </div>

      <div
        data-testid="task-workspace"
        className={`flex min-h-[28rem] min-w-0 flex-1 flex-col overflow-visible bg-card/30 md:overflow-hidden lg:min-h-0 ${focusMode ? "border-0" : "rounded-b-lg border-x border-b border-border"}`}
      >
        {activeView === "timeline" ? (
          <div className="min-h-0 flex-1 overflow-y-auto p-2 pb-44 sm:p-3 md:pb-5">
            <AgentTranscriptView
              owner={owner}
              items={timeline}
              profileName={profiles.find((p) => p.id === owner.runtimeProfileID)?.name}
              isLive={ACTIVE.has(owner.status)}
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
            ref={conversationViewport}
            data-testid="conversation-workspace"
            className="min-h-0 flex-1 overflow-y-auto overscroll-contain bg-background px-3 py-5 pb-44 sm:px-6 md:pb-5"
          >
            <div className="mx-auto max-w-3xl">
              <TranscriptList entries={transcript} endRef={conversationEnd} />
              {providerPermissions.length > 0 && (
                <ProviderPermissionRequests
                  permissions={providerPermissions}
                  permissionBusy={permissionBusy}
                  onRespond={respondToPermission}
                />
              )}
            </div>
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
          running={running}
          queueAvailable={queueActionAvailable}
          queueDisabled={owner.kind === "session" && providerSwitchRequested}
          providerSwitchRequested={providerSwitchRequested}
          providerSwitchAvailable={providerSwitchAvailable}
          sendMode={sendMode}
          sendActionLabel={sendActionLabel}
          actionError={actionError}
          continuationModelProviders={continuationModelProviders}
          continuationModelProvider={continuationModelProvider}
          continuationModelOverride={continuationModelOverride}
          continuationModelOptions={continuationModelOptions}
          continuationReasoningEffort={continuationReasoningEffort}
          onSelectProvider={selectContinuationModelProvider}
          onSelectModel={setContinuationModelOverride}
          onSelectReasoningEffort={setContinuationReasoningEffort}
          attachments={owner.capabilities.attachments ? attachments : undefined}
          attachmentInputRef={owner.capabilities.attachments ? attachmentInput : undefined}
          onAttachmentsChange={owner.capabilities.attachments ? setAttachments : undefined}
        />
      </div>

      <ConfirmDialog
        open={confirmAction === "stop"}
        title={owner ? `Stop ${owner.kind} ${owner.title}?` : "Stop?"}
        description={owner ? `Stopping ${owner.kind} ${owner.title} closes its Runtime without deleting its work.` : undefined}
        confirmLabel="Stop"
        destructive
        onConfirm={() => { setConfirmAction(null); void stop(); }}
        onCancel={() => setConfirmAction(null)}
      />
      <ConfirmDialog
        open={confirmAction === "finish"}
        title={owner ? `Finish task ${owner.title}?` : "Finish task?"}
        description="This marks the Task completed after closing the Runtime."
        confirmLabel="Finish"
        onConfirm={() => { setConfirmAction(null); void finishTask(); }}
        onCancel={() => setConfirmAction(null)}
      />
      <ConfirmDialog
        open={confirmAction === "delete"}
        title={owner?.kind === "session" ? `Delete archived session ${owner.title}?` : `Delete task ${owner.title}?`}
        description={owner?.kind === "session" ? "This removes its Session Workdir and Events." : "This removes the Task and its Workdir."}
        confirmLabel="Delete"
        destructive
        onConfirm={() => { setConfirmAction(null); void deleteTask(); }}
        onCancel={() => setConfirmAction(null)}
      />
    </RuntimeOwnerShell>
  );
}

type RuntimeOwnerShellProps = {
  projectChrome: boolean;
  children: ReactNode;
  hideChrome?: boolean;
  className?: string;
  bodyClassName?: string;
  "data-testid"?: string;
};

function RuntimeOwnerShell({ projectChrome, children, bodyClassName, ...props }: RuntimeOwnerShellProps) {
  if (projectChrome) {
    return <ProjectPageShell bodyClassName={bodyClassName} {...props}>{children}</ProjectPageShell>;
  }
  return (
    <PageContainer
      data-testid={props["data-testid"]}
      className={`mx-auto w-full max-w-6xl ${props.className ?? ""}`}
    >
      <div className={bodyClassName}>{children}</div>
    </PageContainer>
  );
}

type RuntimeWorkspaceLoad = {
  owner: RuntimeOwnerView;
  timeline: TaskTimelineItem[];
  transcript: TaskTranscriptEntry[];
};

async function loadTaskWorkspace(base: string, signal: AbortSignal): Promise<RuntimeWorkspaceLoad> {
  const [task, timeline, transcript] = await Promise.all([
    apiGet<Task>(base, { signal }),
    apiGet<TaskTimeline>(`${base}/timeline`, { signal }),
    apiGet<TaskTranscript>(`${base}/transcript`, { signal }),
  ]);
  return {
    owner: taskAsRuntimeOwner(task),
    timeline: timeline.items ?? [],
    transcript: transcript.entries ?? [],
  };
}

async function loadSessionWorkspace(base: string, signal: AbortSignal): Promise<RuntimeWorkspaceLoad> {
  const [session, timeline, transcript] = await Promise.all([
    apiGet<Session>(base, { signal }),
    apiGet<TaskTimeline>(`${base}/timeline`, { signal }),
    apiGet<TaskTranscript>(`${base}/transcript`, { signal }),
  ]);
  return {
    owner: sessionAsRuntimeOwner(session),
    // Session timelines and transcripts are built by the same daemon pipeline
    // as task ones, so the rendered shapes are identical.
    timeline: timeline.items ?? [],
    transcript: transcript.entries ?? [],
  };
}

function taskAsRuntimeOwner(task: Task): RuntimeOwnerView {
  return {
    kind: "task",
    id: task.id,
    title: task.goal,
    status: task.status,
    runner: task.runner,
    runtimeProfileID: task.runtime_profile_id,
    blackboardConclusionMode: task.run_controls.blackboard_conclusion_mode,
    blackboardConclusion: task.blackboard_conclusion,
    runtimeControls: task.runtime_controls,
    runtimeActivity: task.runtime_activity,
    activeContinuation: task.active_continuation ? taskContinuationAsRuntimeOwner(task.active_continuation) : undefined,
    latestContinuation: task.latest_continuation ? taskContinuationAsRuntimeOwner(task.latest_continuation) : undefined,
    createdAt: task.created_at,
    updatedAt: task.updated_at,
    capabilities: {
      projectChrome: true,
      rename: false,
      archive: false,
      restore: false,
      delete: DELETABLE.has(task.status),
      finish: true,
      resumeWithoutMessage: true,
      attachments: false,
    },
  };
}

function taskContinuationAsRuntimeOwner(continuation: TaskContinuation): RuntimeOwnerContinuation {
  return {
    id: continuation.id,
    number: continuation.number,
    runtimeProfileID: continuation.runtime_profile_id,
    runtimeProvider: continuation.runtime_provider,
    runner: continuation.runner,
    status: continuation.status,
    nativeSessionID: continuation.native_session_id,
  };
}

function sessionAsRuntimeOwner(session: Session): RuntimeOwnerView {
  const active = session.active_continuation ? sessionContinuationAsRuntimeOwner(session.active_continuation) : undefined;
  const latest = session.latest_continuation ? sessionContinuationAsRuntimeOwner(session.latest_continuation) : active;
  const continuation = active ?? latest;
  const status = session.lifecycle === "archived" ? "archived" : active?.status ?? latest?.status ?? "stopped";
  const controls = session.runtime_controls;
  return {
    kind: "session",
    id: session.id,
    title: session.title,
    status,
    lifecycle: session.lifecycle,
    runner: continuation?.runner ?? "host",
    runtimeProfileID: continuation?.runtimeProfileID ?? "",
    blackboardConclusionMode: session.run_controls?.blackboard_conclusion_mode,
    blackboardConclusion: session.blackboard_conclusion,
    runtimeActivity: session.runtime_activity,
    activeContinuation: active,
    latestContinuation: latest,
    runtimeControls: controls ? {
      native_resume_available: controls.native_resume_available,
      resume_available: session.lifecycle === "open",
      native_steer_available: controls.native_steer_available,
      native_steer_mode: controls.native_steer_mode,
      queue_steer_available: controls.queue_steer_available,
      interrupt_steer_available: controls.interrupt_steer_available,
      native_session_captured: controls.native_session_captured,
      same_runtime_provider_only: true,
      runtime_provider: controls.runtime_provider,
      turn_selection: controls.turn_selection,
      provider_permissions: controls.provider_permissions,
      recovery_state: controls.recovery_state,
      recovery_reason: controls.recovery_reason,
      finish_available: false,
    } : {
      native_resume_available: false,
      resume_available: session.lifecycle === "open",
      queue_steer_available: true,
      interrupt_steer_available: false,
      native_session_captured: false,
      same_runtime_provider_only: true,
      finish_available: false,
    },
    createdAt: session.created_at,
    updatedAt: session.updated_at,
    capabilities: {
      projectChrome: false,
      rename: true,
      archive: session.lifecycle === "open",
      restore: session.lifecycle === "archived",
      delete: session.lifecycle === "archived",
      finish: false,
      resumeWithoutMessage: false,
      attachments: true,
    },
  };
}

function sessionContinuationAsRuntimeOwner(continuation: SessionContinuation): RuntimeOwnerContinuation {
  return {
    id: continuation.id,
    number: continuation.number,
    runtimeProfileID: continuation.runtime_profile_id,
    runtimeProvider: continuation.runtime_provider,
    runner: continuation.runner,
    status: continuation.status,
    nativeSessionID: continuation.native_session_id,
  };
}

async function postSessionRuntimeMessage(
  path: string,
  message: string,
  selection: { model_provider_id?: string; model?: string; model_override?: string; reasoning_effort: string },
  attachments: File[],
) {
  if (attachments.length === 0) {
    return apiPost<Session>(path, { message, ...selection });
  }
  const form = new FormData();
  form.append("payload", JSON.stringify({ message, ...selection }));
  attachments.forEach((attachment) => form.append("attachments", attachment, attachment.name));
  return apiPostForm<Session>(path, form);
}

function tabClass(active: boolean) {
  return [
    "inline-flex items-center gap-1.5 rounded-t-md border-b-2 px-3 py-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    active ? "border-signal text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
  ].join(" ");
}

function isNearScrollBottom(container: HTMLElement, threshold = 160) {
  return container.scrollHeight - (container.scrollTop + container.clientHeight) <= threshold;
}

type ConversationSendMode = "native" | "interrupt" | "queue" | "resume" | "unavailable";

/** Pi native cross-provider only when target is in the fixed projected set. */
function canPiNativeCrossProvider(input: {
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

// conversationQueueUnavailable reports whether queueing a Session steer is
// blocked by a pending model-provider switch that only the restart path can
// honor. Tasks allow the switch through the queue→stop→resume pipeline.
function conversationQueueUnavailable(owner: RuntimeOwnerView, selectedProviderID: string): boolean {
  return (
    owner.kind === "session" &&
    ACTIVE.has(owner.status) &&
    selectedProviderID !== "" &&
    selectedProviderID !== (owner.runtimeControls?.turn_selection?.model_provider_id?.trim() ?? "")
  );
}

type ConversationActionContext = {
  session: (path: string, payload: { text: string; selection: ConversationSelection }, files: File[]) => Promise<unknown>;
  task: (path: string, payload: Record<string, unknown>) => Promise<unknown>;
  attachments: File[];
  base: string;
  ownerTitle: string;
};

type ConversationSelection = {
  model_provider_id?: string;
  model?: string;
  model_override?: string;
  reasoning_effort: string;
};

// resolveConversationAction decides, from owner-independent runtime controls,
// which transport a new operator message takes. Both owner kinds share the
// decision so steer/queue/restart rules cannot drift between Task and Session.
function resolveConversationAction(
  owner: RuntimeOwnerView,
  state: {
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
  },
  requestID: string,
): { run: (message: string, selection: ConversationSelection, context: ConversationActionContext) => Promise<void> } {
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
        run: async (message, selection, ctx) => {
          await apiPost(`${ctx.base}/stop`, {});
          await ctx.session(`${ctx.base}/messages`, { text: message, selection }, ctx.attachments);
        },
      };
    }
    if (state.running && (state.nativeSteerAvailable || state.interruptSteerAvailable)) {
      return {
        run: async (message, selection, ctx) => {
          await ctx.session(`${ctx.base}/steer`, { text: message, selection }, ctx.attachments);
        },
      };
    }
    if (state.running && state.queueSteerAvailable) {
      return {
        run: async (message, selection, ctx) => {
          await ctx.session(`${ctx.base}/steer/queue`, { text: message, selection }, ctx.attachments);
        },
      };
    }
    return {
      run: async (message, selection, ctx) => {
        await ctx.session(`${ctx.base}/messages`, { text: message, selection }, ctx.attachments);
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
      run: async (message, selection, ctx) => {
        await ctx.task(`${ctx.base}/steer/queue`, { directive: message, ...selection });
        await ctx.task(`${ctx.base}/stop`, {});
        await ctx.task(`${ctx.base}/resume`, {});
      },
    };
  }
  if (state.running && (state.nativeSteerAvailable || state.interruptSteerAvailable)) {
    return {
      run: async (message, selection, ctx) => {
        await ctx.task(`${ctx.base}/steer`, {
          ...(state.nativeSteerAvailable ? { request_id: requestID, message } : { directive: message }),
          ...selection,
        });
      },
    };
  }
  if (state.running && state.queueSteerAvailable) {
    return {
      run: async (message, selection, ctx) => {
        await ctx.task(`${ctx.base}/steer/queue`, { directive: message, ...selection });
      },
    };
  }
  if (!state.running && state.queueSteerAvailable && state.resumeAvailable) {
    return {
      // Queue first so a failed resume retains the operator's message for the
      // next successful Continuation instead of silently dropping it.
      run: async (message, selection, ctx) => {
        await ctx.task(`${ctx.base}/steer/queue`, { directive: message, ...selection });
        await ctx.task(`${ctx.base}/resume`, {});
      },
    };
  }
  throw new Error("Task conversation is unavailable for this runtime state");
}

function resolveConversationSendMode(input: {
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

function conversationSendLabel(mode: ConversationSendMode, nativeSteerMode?: string): string {
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

function conversationModeText(mode: ConversationSendMode): string {
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

function FloatingScrollControls({
  autoFollow,
  onTop,
  onBottom,
}: {
  autoFollow: boolean;
  onTop: () => void;
  onBottom: () => void;
}) {
  const latestLabel = autoFollow
    ? "Scroll to latest (auto-follow on)"
    : "Scroll to latest (auto-follow off)";

  return (
    <div className="flex items-center gap-1">
      <Button size="sm" variant="outline" className="h-9 w-9 p-0 shadow-md" onClick={onTop} aria-label="Scroll to top" title="Top">
        <ArrowUp className="h-4 w-4" />
      </Button>
      <Button
        size="sm"
        variant={autoFollow ? "secondary" : "outline"}
        className="relative h-9 w-9 p-0 shadow-md"
        onClick={onBottom}
        aria-label={latestLabel}
        title={latestLabel}
      >
        <ArrowDown className="h-4 w-4" />
        {autoFollow && (
          <CheckCircle2 className="absolute right-0.5 top-0.5 h-3 w-3 text-signal" aria-hidden="true" />
        )}
      </Button>
    </div>
  );
}

function ProviderPermissionRequests({
  permissions,
  permissionBusy,
  onRespond,
}: {
  permissions: ProviderPermissionRequest[];
  permissionBusy: string;
  onRespond: (permissionRequestID: string, decision: "allow" | "deny") => void;
}) {
  return (
    <section className="mt-5 rounded-lg border border-warning/25 bg-warning/5 p-3" aria-label="Provider permission requests">
      <div className="space-y-2">
        <div className="flex items-center gap-2 text-xs font-medium text-warning-foreground">
          <KeyRound className="h-3.5 w-3.5" /> Provider permission
        </div>
        {permissions.map((permission) => (
          <div key={permission.permission_request_id} className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-warning/20 bg-background/70 px-3 py-2">
            <div className="min-w-0">
              <div className="text-sm">Permission request</div>
              <code className="block max-w-full truncate text-xs text-muted-foreground">{permission.permission_request_id}</code>
            </div>
            <div className="flex gap-2">
              <Button
                size="sm"
                onClick={() => onRespond(permission.permission_request_id, "allow")}
                disabled={permissionBusy !== ""}
                aria-label={`Allow provider permission ${permission.permission_request_id}`}
              >
                <CheckCircle2 className="h-4 w-4" /> Allow
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => onRespond(permission.permission_request_id, "deny")}
                disabled={permissionBusy !== ""}
                aria-label={`Deny provider permission ${permission.permission_request_id}`}
              >
                <CircleX className="h-4 w-4" /> Deny
              </Button>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

function RuntimeOwnerComposer({
  ownerLabel,
  value,
  onChange,
  onKeyDown,
  onSend,
  onQueue,
  onStop,
  onFinish,
  finishAvailable,
  sending,
  running,
  queueAvailable,
  queueDisabled,
  providerSwitchRequested,
  providerSwitchAvailable,
  sendMode,
  sendActionLabel,
  actionError,
  continuationModelProviders,
  continuationModelProvider,
  continuationModelOverride,
  continuationModelOptions,
  continuationReasoningEffort,
  onSelectProvider,
  onSelectModel,
  onSelectReasoningEffort,
  attachments,
  attachmentInputRef,
  onAttachmentsChange,
}: {
  ownerLabel: "Task" | "Session";
  value: string;
  onChange: (value: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  onSend: () => void;
  onQueue: () => void;
  onStop: () => void;
  onFinish: () => void;
  finishAvailable: boolean;
  sending: boolean;
  running: boolean;
  queueAvailable: boolean;
  queueDisabled: boolean;
  providerSwitchRequested: boolean;
  providerSwitchAvailable: boolean;
  sendMode: ConversationSendMode;
  sendActionLabel: string;
  actionError: string | null;
  continuationModelProviders: ModelProvider[];
  continuationModelProvider: string;
  continuationModelOverride: string;
  continuationModelOptions: string[];
  continuationReasoningEffort: string;
  onSelectProvider: (providerID: string) => void;
  onSelectModel: (model: string) => void;
  onSelectReasoningEffort: (effort: string) => void;
  attachments?: File[];
  attachmentInputRef?: RefObject<HTMLInputElement | null>;
  onAttachmentsChange?: (files: File[]) => void;
}) {
  return (
    <div data-testid="task-composer" className="fixed inset-x-0 bottom-0 z-30 shrink-0 border-t border-border bg-background/95 px-3 py-2 shadow-[0_-8px_24px] shadow-black/15 backdrop-blur-sm sm:px-4 md:static md:z-10 md:shadow-none">
      <div className="mx-auto max-w-3xl space-y-2">
        {actionError && <p role="alert" className="text-xs text-destructive">{actionError}</p>}
        <div className="overflow-hidden rounded-lg border border-border bg-card shadow-sm focus-within:border-ring">
          <Textarea
            aria-label={`${ownerLabel} message`}
            name="task_message"
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Focus on admin.example.com next…"
            rows={2}
            autoComplete="off"
            className="max-h-40 min-h-[60px] resize-y rounded-none border-0 bg-transparent px-3 py-2.5 shadow-none focus-visible:border-transparent focus-visible:ring-0"
          />
          <div className="flex flex-wrap items-center gap-1.5 border-t border-border px-2 py-1.5">
            <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
              {onAttachmentsChange && (
                <label className="inline-flex h-7 cursor-pointer items-center gap-1 rounded-md bg-muted/60 px-2 text-xs text-muted-foreground transition-colors hover:text-foreground">
                  <Paperclip className="h-3.5 w-3.5" />
                  <span>{attachments?.length ? `${attachments.length} attached` : "Attach"}</span>
                  <input
                    ref={attachmentInputRef}
                    type="file"
                    multiple
                    className="sr-only"
                    aria-label="Attach files to Session message"
                    onChange={(event) => onAttachmentsChange(Array.from(event.target.files ?? []))}
                  />
                </label>
              )}
              <Select
                size="sm"
                className="h-7 min-w-0 w-auto max-w-full border-0 bg-muted/60 px-2 text-xs shadow-none sm:max-w-[13rem]"
                name="continuation_model_provider"
                value={continuationModelProvider}
                onChange={(event) => onSelectProvider(event.target.value)}
                aria-label="Continuation model provider"
              >
                <option value="">Select model provider</option>
                {continuationModelProviders.map((provider) => (
                  <option key={provider.id} value={provider.id}>{provider.name}</option>
                ))}
              </Select>
              <Select
                size="sm"
                className="h-7 min-w-0 w-auto max-w-full border-0 bg-muted/60 px-2 text-xs shadow-none sm:max-w-[13rem]"
                name="continuation_model"
                value={continuationModelOverride}
                onChange={(event) => onSelectModel(event.target.value)}
                aria-label="Continuation model"
                disabled={!continuationModelProvider || continuationModelOptions.length === 0}
              >
                {continuationModelOptions.length === 0 ? (
                  <option value="">Default model</option>
                ) : continuationModelOptions.map((model) => (
                  <option key={model} value={model}>{model}</option>
                ))}
              </Select>
              <Select
                size="sm"
                className="h-7 min-w-0 w-auto max-w-full border-0 bg-muted/60 px-2 text-xs shadow-none sm:max-w-[9rem]"
                name="continuation_reasoning_effort"
                value={displayReasoningEffort(continuationReasoningEffort)}
                onChange={(event) => onSelectReasoningEffort(event.target.value)}
                aria-label="Continuation reasoning effort"
              >
                {REASONING_EFFORT_VALUES.map((effort) => (
                  <option key={effort} value={effort}>{effort}</option>
                ))}
              </Select>
              <Badge variant={sendMode === "unavailable" ? "warning" : "outline"} size="sm">
                {providerSwitchRequested ? "switch provider" : conversationModeText(sendMode)}
              </Badge>
            </div>
            <div className="ml-auto flex shrink-0 items-center gap-1">
              {running && queueAvailable && sendMode !== "queue" && (
                <Button
                  size="icon-xl"
                  variant="ghost"
                  onClick={onQueue}
                  disabled={!value.trim() || sending || queueDisabled}
                  aria-label="Queue message"
                  title={queueDisabled ? "Queue cannot switch a Session model provider" : "Queue message"}
                >
                  <ListPlus className="h-4 w-4" />
                </Button>
              )}
              {running && finishAvailable && (
                <Button
                  size="icon-xl"
                  variant="ghost"
                  onClick={onFinish}
                  disabled={sending}
                  aria-label={`Finish ${ownerLabel.toLowerCase()}`}
                  title="Finish Task: close Runtime and mark completed"
                  data-testid="finish-task-composer"
                >
                  <Flag className="h-4 w-4" />
                </Button>
              )}
              {running && (
                <Button size="icon-xl" variant="destructive" onClick={onStop} disabled={sending} aria-label={`Stop ${ownerLabel.toLowerCase()}`} title={`Stop ${ownerLabel}`}>
                  <Square className="h-4 w-4" />
                </Button>
              )}
              <Button
                size="icon-xl"
                onClick={onSend}
                disabled={!value.trim() || sending || sendMode === "unavailable" || (providerSwitchRequested && !providerSwitchAvailable)}
                aria-label={sendActionLabel}
                title={sendActionLabel}
              >
                {sending ? <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" /> : sendMode === "resume" ? <Play className="h-4 w-4" /> : <Send className="h-4 w-4" />}
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  const variant =
    status === "completed" ? "success" :
    status === "running" ? "primary" :
    status === "failed" ? "destructive" :
    status === "stopped" ? "warning" :
    status === "interrupted" ? "warning" : "outline";
  return <Badge variant={variant} className="shrink-0 whitespace-nowrap">{status}</Badge>;
}

function RuntimeActivityBadge({ activity }: { activity?: RuntimeActivity }) {
  if (!activity?.liveness) return null;
  const liveness = activity.liveness;
  const turn = activity.turn_activity;
  const label =
    liveness === "live" && turn
      ? `runtime ${liveness} · ${turn}`
      : `runtime ${liveness}`;
  const variant =
    liveness === "live" ? "primary" :
    liveness === "offline" ? "outline" :
    liveness === "orphaned" ? "warning" :
    liveness === "unknown" ? "warning" : "outline";
  return (
    <Badge
      variant={variant}
      data-testid="runtime-activity"
      title={activity.warning || label}
      className="shrink-0 whitespace-nowrap"
    >
      {label}
    </Badge>
  );
}

function BlackboardConclusionBadge({ owner }: { owner: RuntimeOwnerView }) {
  const mode = owner.blackboardConclusion?.mode ?? owner.blackboardConclusionMode ?? "interactive";
  const state = owner.blackboardConclusion?.state ?? "clean";
  const sourceTurn = owner.blackboardConclusion?.source_turn_id;
  const appliedRevision = owner.blackboardConclusion?.applied_revision;
  const stateLabel = state === "action_required" ? "action required" : state;
  const label = `Blackboard · ${mode} · ${stateLabel}${appliedRevision !== undefined ? ` · applied revision ${appliedRevision}` : ""}`;
  const details = [label];
  if (sourceTurn) details.push(`source Turn ${sourceTurn}`);
  if (appliedRevision !== undefined) details.push(`applied revision ${appliedRevision}`);
  return (
    <Badge
      variant={state === "action_required" ? "destructive" : state === "pending" || state === "concluding" ? "warning" : "outline"}
      data-testid="blackboard-conclusion-state"
      title={details.join(" · ")}
      className="min-w-0 shrink"
    >
      <span className="truncate whitespace-nowrap">{label}</span>
    </Badge>
  );
}

const blackboardConclusionErrorCopy: Record<string, string> = {
  semantic_conclusion_invalid_result: "The runtime returned an invalid Blackboard conclusion.",
  semantic_conclusion_repair_exhausted: "The runtime could not produce a valid Blackboard conclusion after one repair attempt.",
  conclude_tool_use_forbidden: "The runtime attempted to use a tool while concluding Blackboard state.",
};

function BlackboardConclusionRecovery({
  errorCode,
  retryAvailable,
  nextEligibleAt,
  retrying,
  onRetry,
}: {
  errorCode?: string;
  retryAvailable: boolean;
  nextEligibleAt?: string;
  retrying: boolean;
  onRetry: () => void;
}) {
  const code = errorCode?.trim() || "conclusion_action_required";
  const message = blackboardConclusionErrorCopy[code] ?? "Blackboard conclusion requires operator attention.";
  return (
    <div
      role="alert"
      aria-label="Blackboard conclusion requires attention"
      className="flex shrink-0 flex-col gap-2 border-b border-destructive/30 bg-destructive/5 px-3 py-2 text-sm sm:flex-row sm:items-center"
    >
      <TriangleAlert className="h-4 w-4 shrink-0 text-destructive" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="text-foreground">{message}</p>
        <p className="break-all font-mono text-xs text-muted-foreground">{code}</p>
      </div>
      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={onRetry}
        disabled={retrying || !retryAvailable}
        aria-label="Retry Blackboard conclusion"
        title={!retryAvailable && nextEligibleAt ? `Retry available after ${formatDateTime(nextEligibleAt)}` : "Retry Blackboard conclusion"}
      >
        <RefreshCcw className={`h-4 w-4 ${retrying ? "animate-spin motion-reduce:animate-none" : ""}`} />
        Retry
      </Button>
    </div>
  );
}

function TranscriptList({ entries, endRef }: { entries: TaskTranscriptEntry[]; endRef: RefObject<HTMLDivElement | null> }) {
  return (
    <div>
      {entries.map((entry, index) => {
        // A user message starts a new turn and gets breathing room above it.
        // Everything inside an agent turn — assistant messages, tool calls,
        // tool results — stays tight so the agent's reasoning reads as one block.
        const spacing = index === 0 ? "" : isUserTurnStart(entry) ? "mt-4" : "mt-1";
        return (
          <div
            key={entry.id}
            data-testid="transcript-row"
            className={`[contain-intrinsic-size:72px] [content-visibility:auto] ${spacing}`}
          >
            <TranscriptRow entry={entry} />
          </div>
        );
      })}
      {entries.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-2 py-12 text-center text-sm text-muted-foreground">
          <MessageSquare className="size-5 opacity-50" aria-hidden="true" />
          <p>No transcript yet. Send a message below to start.</p>
        </div>
      )}
      <div ref={endRef} />
    </div>
  );
}

// isUserTurnStart identifies a row that begins a new operator turn. Only these
// get the roomier spacing; agent reasoning (assistant text, tool calls, tool
// results) stays tight so a single agent turn reads as one cohesive block.
function isUserTurnStart(entry: TaskTranscriptEntry): boolean {
  if (entry.kind === "continuation" || entry.kind === "tool_call" || entry.kind === "tool_result") {
    return false;
  }
  return entry.role === "user";
}

function TranscriptRow({ entry }: { entry: TaskTranscriptEntry }) {
  if (entry.kind === "continuation") {
    return (
      <div className="flex items-center justify-center gap-2 py-1 text-xs text-muted-foreground">
        <span className="h-px flex-1 bg-border" />
        <GitBranch className="h-3.5 w-3.5 shrink-0" />
        <span className="min-w-0 break-words text-center">#{entry.seq} {entry.text}</span>
        <span className="h-px flex-1 bg-border" />
      </div>
    );
  }

  if (entry.kind === "attachment") {
    const filename = typeof entry.details?.filename === "string" ? entry.details.filename : "attachment";
    const size = typeof entry.details?.size === "number" ? entry.details.size : undefined;
    return <AttachmentFileRow name={filename} size={size} prefix="Attached" />;
  }

  if (isCollapsedTranscriptEntry(entry)) {
    return <CollapsedTranscriptRow entry={entry} />;
  }

  const isUser = entry.role === "user";
  const isAssistant = entry.role === "assistant";
  const Icon = isUser ? User : MessageSquare;
  const roleLabel = isUser ? "You" : isAssistant ? "Agent" : entry.role;
  return (
    <div className={`flex gap-3 text-sm ${isUser ? "justify-end pl-8 sm:pl-16" : "justify-start pr-2 sm:pr-8"}`}>
      {!isUser && !isAssistant && (
        <span className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border bg-muted/60 text-muted-foreground">
          <Icon className="h-4 w-4" />
        </span>
      )}
      <div
        data-testid="transcript-message-bubble"
        className={`min-w-0 max-w-[88%] rounded-lg px-3 py-2 sm:px-3.5 ${isUser ? "bg-primary/10 dark:bg-primary/15" : "bg-transparent px-0"}`}
      >
        {!isAssistant && (
          <div className="mb-1 text-[11px] font-medium text-muted-foreground">
            {roleLabel}
            {entry.created_at && (
              <span className="font-normal text-muted-foreground/70"> · {formatDateTime(entry.created_at)}</span>
            )}
          </div>
        )}
        <div className="whitespace-pre-wrap break-words leading-6 text-foreground">{entry.text}</div>
      </div>
      {isUser && (
        <span className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-foreground/70 dark:bg-primary/15">
          <Icon className="h-4 w-4" />
        </span>
      )}
    </div>
  );
}

function CollapsedTranscriptRow({ entry }: { entry: TaskTranscriptEntry }) {
  const isError = entry.kind === "tool_result" && (entry.details as { is_error?: boolean } | undefined)?.is_error === true;
  const Icon =
    entry.kind === "runtime_output"
      ? Terminal
      : entry.kind === "tool_result"
        ? isError
          ? CircleX
          : CheckCircle2
        : Wrench;
  return (
    <details data-testid="transcript-tool-row" className="group border-b border-border/50 last:border-b-0">
      <summary className="-mx-1 flex min-h-9 cursor-pointer list-none items-center gap-2 rounded-sm px-1 py-1.5 text-sm transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
        <Icon className={`h-4 w-4 shrink-0 ${isError ? "text-destructive" : "text-muted-foreground"}`} />
        <span className="min-w-0 flex-1 truncate text-[13px] text-muted-foreground transition-colors group-hover:text-foreground group-open:text-foreground">{collapsedTranscriptTitle(entry)}</span>
        {entry.created_at && <span className="ml-auto shrink-0 text-[11px] tabular-nums text-muted-foreground/70">{formatDateTime(entry.created_at)}</span>}
      </summary>
      <div className="ml-[1.625rem] border-l border-border/60 pb-3 pl-4 pr-2 pt-2">
        {entry.kind === "tool_call" ? (
          <ToolCallDetails entry={entry} />
        ) : (
          <pre className="overflow-x-auto whitespace-pre-wrap break-words text-xs leading-5 text-foreground/80">{collapsedBody(entry)}</pre>
        )}
      </div>
    </details>
  );
}

function ToolCallDetails({ entry }: { entry: TaskTranscriptEntry }) {
  const fields = toolCallFields(entry);
  if (fields.length === 0) {
    return <p className="text-xs text-muted-foreground">No arguments.</p>;
  }
  return (
    <dl className="space-y-1">
      {fields.map((field) => (
        <div
          key={field.label}
          className={field.block ? "space-y-0.5" : "flex flex-wrap items-baseline gap-x-2 gap-y-0.5"}
        >
          <dt className="shrink-0 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/80">{field.label}</dt>
          {field.block ? (
            <dd>
              <pre className="overflow-x-auto whitespace-pre-wrap break-words py-0.5 font-mono text-xs leading-5 text-foreground/85">{field.value}</pre>
            </dd>
          ) : (
            <dd className="min-w-0 break-words font-mono text-xs text-foreground/90">{field.value}</dd>
          )}
        </div>
      ))}
    </dl>
  );
}

function isCollapsedTranscriptEntry(entry: TaskTranscriptEntry) {
  return entry.kind === "tool_call" || entry.kind === "tool_result" || entry.kind === "runtime_output";
}

function collapsedBody(entry: TaskTranscriptEntry) {
  if (entry.kind === "tool_result") return entry.text || "(empty)";

  const parts: string[] = [];
  if (entry.text) parts.push(entry.text);
  if (entry.tool_call_id) parts.push(`tool_call_id: ${entry.tool_call_id}`);
  if (entry.tool_name) parts.push(`tool_name: ${entry.tool_name}`);
  if (entry.details && Object.keys(entry.details).length > 0) {
    parts.push(JSON.stringify(entry.details, null, 2));
  }
  return parts.join("\n\n") || "(empty)";
}

function prefersReducedMotion(): boolean {
  return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
}
