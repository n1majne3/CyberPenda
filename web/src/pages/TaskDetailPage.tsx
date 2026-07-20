import { useEffect, useState, useRef, type KeyboardEvent, type RefObject } from "react";
import { useParams, useNavigate, useSearchParams } from "react-router-dom";
import { Square, Send, Terminal, Activity, GitBranch, MessageSquare, Play, ChevronRight, Wrench, User, ArrowDown, ArrowUp, CheckCircle2, Trash2, CircleX, KeyRound, ListPlus, Loader2, Maximize2, Minimize2 } from "lucide-react";
import { apiDelete, apiGet, apiPost, type ModelProvider, type ProviderPermissionRequest, type RuntimePlugin, type RuntimeProfile, type Task, type TaskTimeline, type TaskTimelineItem, type TaskTranscript, type TaskTranscriptEntry } from "@/lib/api";
import { Button, Badge, Select, Textarea } from "@/components/ui";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { AgentTranscriptView } from "@/components/task-transcript/AgentTranscriptView";
import { collapsedTranscriptTitle } from "./taskDetailView";
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

export function TaskDetailPage() {
  const { projectId, taskId } = useParams<{ projectId: string; taskId: string }>();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [task, setTask] = useState<Task | null>(null);
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
  const conversationViewport = useRef<HTMLDivElement>(null);
  const conversationEnd = useRef<HTMLDivElement>(null);
  const autoFollowRef = useRef(true);

  const base = `/api/projects/${projectId}/tasks/${taskId}`;

  async function loadAll() {
    if (!projectId || !taskId) return;
    try {
      const [t, tl, tr] = await Promise.all([
        apiGet<Task>(`${base}`),
        apiGet<TaskTimeline>(`${base}/timeline`),
        apiGet<TaskTranscript>(`${base}/transcript`),
      ]);
      setTask(t);
      setTimeline(tl.items ?? []);
      setTranscript(tr.entries ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    // Initial load on mount/task change. loadAll() is reused by the poll loop
    // and event handlers.
    setTurnSelectionSeeded(false);
    loadAll();
    Promise.all([
      apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles").then((d) => setProfiles(d.profiles ?? [])),
      apiGet<{ providers: ModelProvider[] }>("/api/model-providers").then((d) => setModelProviders(d.providers ?? [])),
      apiGet<{ plugins: RuntimePlugin[] }>("/api/runtime-plugins").then((d) => setRuntimePlugins(d.plugins ?? [])),
    ]).catch(() => {});
  }, [projectId, taskId]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  // Seed the composer once from the preceding Runtime Turn Selection. Later
  // submits retain the operator's choice instead of resetting.
  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    if (turnSelectionSeeded || !task) return;
    const preceding = task.runtime_controls?.turn_selection;
    if (!preceding) {
      setTurnSelectionSeeded(true);
      return;
    }
    setContinuationModelProvider(preceding.model_provider_id?.trim() ?? "");
    setContinuationModelOverride(preceding.model?.trim() ?? "");
    setContinuationReasoningEffort(displayReasoningEffort(preceding.reasoning_effort));
    setTurnSelectionSeeded(true);
  }, [task, turnSelectionSeeded]);
  /* eslint-enable react-hooks/set-state-in-effect */

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    if (!task || activeView !== "conversation") return;

    // Reset auto-follow when the task changes. This is an intentional
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
  }, [task?.id, activeView]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  // Poll events while the task is active. Depends on status only so the
  // interval is not reset every render; loadAll/task are intentionally omitted.
  /* eslint-disable react-hooks/exhaustive-deps */
  useEffect(() => {
    if (!task || !ACTIVE.has(task.status)) return;
    const id = setInterval(loadAll, 1000);
    return () => clearInterval(id);
  }, [task?.status]);
  /* eslint-enable react-hooks/exhaustive-deps */

  useEffect(() => {
    if (activeView === "conversation" && autoFollowRef.current) {
      conversationEnd.current?.scrollIntoView({ behavior: prefersReducedMotion() ? "auto" : "smooth", block: "end" });
    }
  }, [activeView, timeline, transcript]);

  const currentProfileRuntimeProvider = profiles.find((profile) => profile.id === task?.runtime_profile_id)?.provider;
  const currentRuntimeProvider =
    task?.runtime_controls?.runtime_provider ??
    task?.active_continuation?.runtime_provider ??
    task?.latest_continuation?.runtime_provider ??
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
    if (!task || !window.confirm(`Stop task ${task.goal}?`)) return;
    try {
      await apiPost(`${base}/stop`, {});
      setActionError(null);
      loadAll();
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function deleteTask() {
    if (!task || !window.confirm(`Delete task ${task.goal}?`)) return;
    try {
      await apiDelete(base);
      navigate(`/projects/${projectId}/tasks`);
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function resumeNative() {
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
    if (!directive || sending) return;
    setSending(true);
    try {
      await apiPost(`${base}/steer/queue`, {
        directive,
        ...continuationModelPayload(),
      });
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
    if (!message || sending || !task) return;
    setSending(true);
    const requestID = newSteerRequestID();
    try {
      const runningNow = ACTIVE.has(task.status);
      const currentControls = task.runtime_controls;
      const nativeNow = currentControls?.native_steer_available ?? false;
      const interruptNow = currentControls?.interrupt_steer_available ?? false;
      const queueNow = currentControls?.queue_steer_available ?? true;
      const resumeNow = currentControls?.resume_available ?? !runningNow;
      const modelPayload = continuationModelPayload();
      const precedingProvider = currentControls?.turn_selection?.model_provider_id?.trim() ?? "";
      const selectedProvider = continuationModelProvider.trim();
      const runtimeProvider =
        currentControls?.runtime_provider ??
        task.active_continuation?.runtime_provider ??
        task.latest_continuation?.runtime_provider ??
        "";
      // Pi native cross-provider only when Runtime Config explicitly lists the
      // target in projected_model_provider_ids (fixed-at-launch set). Missing or
      // empty metadata fails closed → queue/reproject/restart (no 409 surprise).
      const piNativeCrossProvider = canPiNativeCrossProvider({
        runtimeProvider,
        nativeSteerAvailable: nativeNow || interruptNow,
        projectedModelProviderIDs: currentControls?.projected_model_provider_ids,
        targetProviderID: selectedProvider,
      });
      // Model Provider introduction or change requires Config Projection + restart
      // when not covered by Pi's projected set. An empty preceding provider with a
      // newly selected provider is a switch (do not send native and get 409).
      const switchingModelProvider =
        runningNow &&
        selectedProvider !== "" &&
        selectedProvider !== precedingProvider &&
        !piNativeCrossProvider;

      if (switchingModelProvider) {
        if (!queueNow) throw new Error("Model provider switching is unavailable for this Task");
        // A live provider session cannot change its endpoint or credentials.
        // Persist the message/config first, then restart the Continuation so a
        // failed stop or resume never drops the operator's request.
        await apiPost(`${base}/steer/queue`, { directive: message, ...modelPayload });
        await apiPost(`${base}/stop`, {});
        await apiPost(`${base}/resume`, {});
      } else if (runningNow && (nativeNow || interruptNow)) {
        await apiPost(`${base}/steer`, {
          ...(nativeNow ? { request_id: requestID, message } : { directive: message }),
          ...modelPayload,
        });
      } else if (runningNow && queueNow) {
        await apiPost(`${base}/steer/queue`, { directive: message, ...modelPayload });
      } else if (!runningNow && queueNow && resumeNow) {
        // Queue first so a failed resume retains the operator's message for the
        // next successful Continuation instead of silently dropping it.
        await apiPost(`${base}/steer/queue`, { directive: message, ...modelPayload });
        await apiPost(`${base}/resume`, {});
      } else {
        throw new Error("Task conversation is unavailable for this runtime state");
      }
      setSteering("");
      // Retain Runtime Turn Selection for the next turn.
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
      <ProjectPageShell>
        <p className="text-destructive">{error}</p>
      </ProjectPageShell>
    );
  }
  if (!task) {
    return (
      <ProjectPageShell>
        <p className="text-muted-foreground">Loading…</p>
      </ProjectPageShell>
    );
  }
  const currentContinuation = task.active_continuation ?? task.latest_continuation;
  const controls = task.runtime_controls;
  const nativeResumeAvailable = controls?.native_resume_available ?? Boolean(currentContinuation?.native_session_id);
  const resumeAvailable = !ACTIVE.has(task.status) || controls?.resume_available === true;
  const queueSteerAvailable = controls?.queue_steer_available ?? true;
  const interruptSteerAvailable = controls?.interrupt_steer_available ?? nativeResumeAvailable;
  const nativeSteerAvailable = controls?.native_steer_available ?? false;
  const nativeSteerMode = controls?.native_steer_mode;
  const providerPermissions = controls?.provider_permissions ?? [];
  const running = ACTIVE.has(task.status);
  const sendMode = resolveConversationSendMode({
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
  const piNativeCrossProvider = canPiNativeCrossProvider({
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
  const sendActionLabel = providerSwitchRequested
    ? queueSteerAvailable ? "Switch provider and resume" : "Provider switch unavailable"
    : conversationSendLabel(sendMode, nativeSteerMode);
  const focusMode = searchParams.get("focus") === "1";

  return (
    <ProjectPageShell
      hideChrome={focusMode}
      data-testid="task-detail-shell"
      className={focusMode ? "h-[calc(100dvh-3.5rem)] max-w-none p-0 md:h-dvh lg:p-0" : "flex min-h-full flex-col"}
      bodyClassName={focusMode ? "flex h-full min-h-0 flex-col" : "flex min-h-[32rem] flex-1 flex-col pb-0 lg:min-h-0"}
    >
      <div data-testid="task-session-header" className="flex h-12 shrink-0 items-center gap-2 border-b border-border px-2 sm:px-3">
        <StatusBadge status={task.status} />
        <h1 className="min-w-0 flex-1 truncate text-sm font-medium" title={task.goal}>{task.goal}</h1>
        {currentContinuation && (
          <div className="hidden shrink-0 items-center gap-1 text-xs text-muted-foreground md:flex">
            <span>continuation #{currentContinuation.number}</span>
            <span aria-hidden="true">·</span>
            <span>runtime: {currentContinuation.runtime_provider}</span>
            <span aria-hidden="true">·</span>
            <span>runner: {task.runner}</span>
            <span className="hidden xl:inline" aria-hidden="true">·</span>
            <span className="hidden xl:inline">continuation status: {currentContinuation.status}</span>
            {(controls?.native_session_captured || currentContinuation.native_session_id) && (
              <span className="hidden 2xl:inline">native session: captured</span>
            )}
            {controls?.same_runtime_provider_only && <span className="hidden 2xl:inline">same runtime only</span>}
          </div>
        )}
        <div className="flex shrink-0 items-center gap-1">
          {!running && (
            <Button size="sm" variant="ghost" onClick={resumeNative} disabled={!resumeAvailable} title={nativeResumeAvailable ? "Resume native session" : "Start a fresh continuation from the current Task state"}>
              <Play className="h-4 w-4" /> <span className="hidden sm:inline">Resume</span>
            </Button>
          )}
          {DELETABLE.has(task.status) && (
            <Button size="icon" variant="ghost" onClick={deleteTask} aria-label="Delete task" title="Delete task" className="h-8 w-8 text-destructive hover:text-destructive">
              <Trash2 className="h-4 w-4" />
            </Button>
          )}
          <Button
            size="icon"
            variant="ghost"
            onClick={() => selectFocus(!focusMode)}
            aria-label={focusMode ? "Exit focus view" : "Enter focus view"}
            title={focusMode ? "Exit focus view" : "Enter focus view"}
            className="h-8 w-8"
          >
            {focusMode ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
          </Button>
        </div>
      </div>

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
              task={task}
              items={timeline}
              profileName={profiles.find((p) => p.id === task.runtime_profile_id)?.name}
              isLive={ACTIVE.has(task.status)}
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

        <TaskComposer
          value={steering}
          onChange={setSteering}
          onKeyDown={handleComposerKeyDown}
          onSend={() => void sendConversationMessage()}
          onQueue={() => void queueSteer()}
          onStop={stop}
          sending={sending}
          running={running}
          queueAvailable={queueSteerAvailable}
          providerSwitchRequested={providerSwitchRequested}
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
        />
      </div>
    </ProjectPageShell>
  );
}

function tabClass(active: boolean) {
  return [
    "inline-flex items-center gap-1.5 rounded-t-md border-b-2 px-3 py-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    active ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
  ].join(" ");
}

function isNearScrollBottom(container: HTMLElement, threshold = 160) {
  return container.scrollHeight - (container.scrollTop + container.clientHeight) <= threshold;
}

type ConversationSendMode = "native" | "interrupt" | "queue" | "resume" | "unavailable";

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
          <CheckCircle2 className="absolute right-0.5 top-0.5 h-3 w-3 text-primary" aria-hidden="true" />
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

function TaskComposer({
  value,
  onChange,
  onKeyDown,
  onSend,
  onQueue,
  onStop,
  sending,
  running,
  queueAvailable,
  providerSwitchRequested,
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
}: {
  value: string;
  onChange: (value: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  onSend: () => void;
  onQueue: () => void;
  onStop: () => void;
  sending: boolean;
  running: boolean;
  queueAvailable: boolean;
  providerSwitchRequested: boolean;
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
}) {
  return (
    <div data-testid="task-composer" className="fixed inset-x-0 bottom-0 z-30 shrink-0 border-t border-border bg-background/95 px-3 py-2 shadow-[0_-8px_24px_rgba(0,0,0,0.12)] backdrop-blur-sm sm:px-4 md:static md:z-10 md:shadow-none">
      <div className="mx-auto max-w-3xl space-y-2">
        {actionError && <p role="alert" className="text-xs text-destructive">{actionError}</p>}
        <div className="overflow-hidden rounded-lg border border-border bg-card shadow-sm focus-within:border-ring">
          <Textarea
            aria-label="Task message"
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
                  size="icon-lg"
                  variant="ghost"
                  onClick={onQueue}
                  disabled={!value.trim() || sending}
                  aria-label="Queue message"
                  title="Queue message"
                >
                  <ListPlus className="h-4 w-4" />
                </Button>
              )}
              {running && (
                <Button size="icon-lg" variant="destructive" onClick={onStop} disabled={sending} aria-label="Stop task" title="Stop task">
                  <Square className="h-4 w-4" />
                </Button>
              )}
              <Button
                size="icon-lg"
                onClick={onSend}
                disabled={!value.trim() || sending || sendMode === "unavailable" || (providerSwitchRequested && !queueAvailable)}
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
  return <Badge variant={variant}>{status}</Badge>;
}

function TranscriptList({ entries, endRef }: { entries: TaskTranscriptEntry[]; endRef: RefObject<HTMLDivElement | null> }) {
  return (
    <div className="space-y-5">
      {entries.map((entry) => (
        <div
          key={entry.id}
          data-testid="transcript-row"
          className="[contain-intrinsic-size:72px] [content-visibility:auto]"
        >
          <TranscriptRow entry={entry} />
        </div>
      ))}
      {entries.length === 0 && <p className="text-sm text-muted-foreground">No transcript yet.</p>}
      <div ref={endRef} />
    </div>
  );
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

  const projectedRuntimeEntries = projectRuntimeOutput(entry);
  if (projectedRuntimeEntries) {
    return (
      <div className="space-y-1">
        {projectedRuntimeEntries.map((projectedEntry) => (
          <TranscriptRow key={projectedEntry.id} entry={projectedEntry} />
        ))}
      </div>
    );
  }

  if (isCollapsedTranscriptEntry(entry)) {
    return <CollapsedTranscriptRow entry={entry} />;
  }

  const isUser = entry.role === "user";
  const isAssistant = entry.role === "assistant";
  const Icon = isUser ? User : MessageSquare;
  return (
    <div className={`flex gap-3 text-sm ${isUser ? "justify-end pl-8 sm:pl-16" : "justify-start pr-2 sm:pr-8"}`}>
      {!isUser && !isAssistant && (
        <span className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border bg-muted/60 text-muted-foreground">
          <Icon className="h-4 w-4" />
        </span>
      )}
      <div
        data-testid="transcript-message-bubble"
        className={`min-w-0 max-w-[88%] rounded-lg px-3 py-2.5 sm:px-4 sm:py-3 ${isUser ? "border border-info/20 bg-info/10 shadow-sm" : "border border-transparent bg-transparent px-0 shadow-none"}`}
      >
        <div className="mb-1 text-[11px] text-muted-foreground">
          #{entry.seq} {entry.role}{entry.created_at && ` · ${formatDateTime(entry.created_at)}`}
        </div>
        <div className="whitespace-pre-wrap break-words leading-6 text-foreground">{entry.text}</div>
      </div>
      {isUser && (
        <span className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-info/20 bg-info/10 text-info">
          <Icon className="h-4 w-4" />
        </span>
      )}
    </div>
  );
}

function projectRuntimeOutput(entry: TaskTranscriptEntry): TaskTranscriptEntry[] | null {
  if (entry.kind !== "runtime_output" || !entry.text) return null;
  try {
    const record = JSON.parse(entry.text) as {
      type?: unknown;
      message?: { content?: unknown };
    };
    if ((record.type !== "assistant" && record.type !== "user") || !Array.isArray(record.message?.content)) return null;

    const projected = record.message.content.flatMap((rawBlock, index): TaskTranscriptEntry[] => {
      if (typeof rawBlock !== "object" || rawBlock === null) return [];
      const block = rawBlock as Record<string, unknown>;
      const id = `${entry.id}-${index}`;

      if (block.type === "text" && typeof block.text === "string" && block.text.trim()) {
        return [{
          ...entry,
          id,
          kind: "message",
          role: record.type === "assistant" ? "assistant" : "user",
          text: block.text.trim(),
          stream: undefined,
          status: undefined,
        }];
      }

      if (record.type === "assistant" && block.type === "tool_use") {
        const toolCallID = typeof block.id === "string" ? block.id : undefined;
        const toolName = typeof block.name === "string" ? block.name : undefined;
        if (!toolCallID && !toolName) return [];
        return [{
          ...entry,
          id,
          kind: "tool_call",
          role: "assistant",
          text: undefined,
          tool_call_id: toolCallID,
          tool_name: toolName,
          details: { input: block.input ?? {} },
          stream: undefined,
          status: undefined,
        }];
      }

      if (record.type === "user" && block.type === "tool_result") {
        const toolCallID = typeof block.tool_use_id === "string" ? block.tool_use_id : undefined;
        if (!toolCallID) return [];
        return [{
          ...entry,
          id,
          kind: "tool_result",
          role: "tool",
          text: runtimeContentText(block.content),
          tool_call_id: toolCallID,
          details: typeof block.is_error === "boolean" ? { is_error: block.is_error } : undefined,
          stream: undefined,
          status: undefined,
        }];
      }

      return [];
    });

    return projected.length > 0 ? projected : null;
  } catch {
    return null;
  }
}

function runtimeContentText(content: unknown): string | undefined {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return undefined;
  const text = content
    .flatMap((part) => (
      typeof part === "object" && part !== null &&
      (part as { type?: unknown }).type === "text" &&
      typeof (part as { text?: unknown }).text === "string"
        ? [(part as { text: string }).text]
        : []
    ))
    .join("\n");
  return text || undefined;
}

function CollapsedTranscriptRow({ entry }: { entry: TaskTranscriptEntry }) {
  const Icon = entry.kind === "runtime_output" ? Terminal : Wrench;
  return (
    <details data-testid="transcript-tool-row" className="group border-b border-border/50 last:border-b-0">
      <summary className="-mx-1 flex min-h-9 cursor-pointer list-none items-center gap-2 rounded-sm px-1 py-1.5 text-sm transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
        <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">#{entry.seq}</span>
        <span className="min-w-0 flex-1 truncate text-[13px] text-muted-foreground transition-colors group-hover:text-foreground group-open:text-foreground">{collapsedTranscriptTitle(entry)}</span>
        {entry.created_at && <span className="ml-auto shrink-0 text-[11px] tabular-nums text-muted-foreground">{formatDateTime(entry.created_at)}</span>}
      </summary>
      <div className="ml-[1.625rem] border-l border-border/60 pb-3 pl-4 pr-2 pt-1">
        <pre className="overflow-x-auto whitespace-pre-wrap break-words text-xs leading-5 text-foreground/80">{collapsedBody(entry)}</pre>
      </div>
    </details>
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
