import { useEffect, useState, useRef, type KeyboardEvent, type RefObject } from "react";
import { useParams, useNavigate, useSearchParams } from "react-router-dom";
import { Square, Send, Terminal, Activity, GitBranch, MessageSquare, Play, ChevronRight, Wrench, User, Bot, ArrowDown, ArrowUp, CheckCircle2, Trash2, CircleX, KeyRound, ListPlus, Loader2 } from "lucide-react";
import { apiDelete, apiGet, apiPost, type ModelProvider, type ProviderPermissionRequest, type RuntimePlugin, type RuntimeProfile, type Task, type TaskTimeline, type TaskTimelineItem, type TaskTranscript, type TaskTranscriptEntry } from "@/lib/api";
import { Button, Badge, Select, Textarea } from "@/components/ui";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { AgentTranscriptView } from "@/components/task-transcript/AgentTranscriptView";
import { collapsedTranscriptTitle } from "./taskDetailView";
import { selectableModelProviders } from "./runtimeProfileForm";
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
    loadAll();
    Promise.all([
      apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles").then((d) => setProfiles(d.profiles ?? [])),
      apiGet<{ providers: ModelProvider[] }>("/api/model-providers").then((d) => setModelProviders(d.providers ?? [])),
      apiGet<{ plugins: RuntimePlugin[] }>("/api/runtime-plugins").then((d) => setRuntimePlugins(d.plugins ?? [])),
    ]).catch(() => {});
  }, [projectId, taskId]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

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
      resetContinuationModelSelection();
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
      resetContinuationModelSelection();
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

      if (runningNow && (nativeNow || interruptNow)) {
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
      resetContinuationModelSelection();
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
    if (!providerID) return {};
    const payload: { model_provider_id: string; model_override?: string } = {
      model_provider_id: providerID,
    };
    const modelOverride = continuationModelOverride.trim();
    if (modelOverride) payload.model_override = modelOverride;
    return payload;
  }

  function resetContinuationModelSelection() {
    setContinuationModelProvider("");
    setContinuationModelOverride("");
  }

  function selectView(view: "conversation" | "timeline") {
    setActiveView(view);
    const next = new URLSearchParams(searchParams);
    next.set("view", view);
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
  const resumeAvailable = controls?.resume_available ?? !ACTIVE.has(task.status);
  const queueSteerAvailable = controls?.queue_steer_available ?? true;
  const interruptSteerAvailable = controls?.interrupt_steer_available ?? nativeResumeAvailable;
  const nativeSteerAvailable = controls?.native_steer_available ?? false;
  const nativeSteerMode = controls?.native_steer_mode;
  const nativeSteerState = controls?.native_steer_state;
  const providerPermissions = controls?.provider_permissions ?? [];
  const running = ACTIVE.has(task.status);
  const sendMode = resolveConversationSendMode({
    running,
    nativeSteerAvailable,
    interruptSteerAvailable,
    queueSteerAvailable,
    resumeAvailable,
  });
  const sendActionLabel = conversationSendLabel(sendMode, nativeSteerMode);

  return (
    <ProjectPageShell
      className="flex min-h-full flex-col"
      title={
        <h2
          className="line-clamp-2 min-w-0 break-words text-lg font-semibold leading-6 tracking-tight"
          title={task.goal}
        >
          {task.goal}
        </h2>
      }
      bodyClassName="flex min-h-[32rem] flex-1 flex-col gap-2 pb-0 lg:min-h-0"
    >
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border pb-2">
        <StatusBadge status={task.status} />
        <Badge variant={task.runner === "host" ? "destructive" : "outline"}>
          runner: {task.runner}
        </Badge>
        {currentContinuation && (
          <>
          <Badge variant="outline">continuation #{currentContinuation.number}</Badge>
          <Badge variant="outline">runtime: {currentContinuation.runtime_provider}</Badge>
          <Badge variant="outline" className="hidden sm:inline-flex">continuation status: {currentContinuation.status}</Badge>
          {(controls?.native_session_captured || currentContinuation.native_session_id) && <Badge variant="outline" className="hidden xl:inline-flex">native session: captured</Badge>}
          {controls?.same_runtime_provider_only && <Badge variant="outline" className="hidden xl:inline-flex">same runtime only</Badge>}
          {nativeSteerMode && <Badge variant="outline">steer: {nativeSteerMode === "in_turn_steer" ? "direct native" : "interrupt then replace"}</Badge>}
          {nativeSteerState && nativeSteerState !== "idle" && <Badge variant={nativeSteerState === "failed" ? "destructive" : "outline"}>steer: {nativeSteerState}</Badge>}
          {controls?.recovery_state && <Badge variant={controls.recovery_state === "failed_closed" ? "warning" : "outline"}>session recovery: {controls.recovery_state.replaceAll("_", " ")}</Badge>}
          </>
        )}
        <div className="ml-auto flex shrink-0 items-center gap-2">
          {!running && (
            <Button size="sm" variant="outline" onClick={resumeNative} disabled={!resumeAvailable} title={nativeResumeAvailable ? "Resume native session" : "Start a fresh continuation from the current Task state"}>
              <Play className="h-4 w-4" /> Resume
            </Button>
          )}
          {DELETABLE.has(task.status) && (
            <Button size="sm" variant="destructive" onClick={deleteTask}>
              <Trash2 className="h-4 w-4" /> Delete
            </Button>
          )}
        </div>
      </div>

      <div className="flex shrink-0 items-center gap-1 border-b border-border">
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
        className="flex min-h-[28rem] min-w-0 flex-1 flex-col overflow-visible rounded-lg border border-border bg-card/30 md:overflow-hidden lg:min-h-0"
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
          sendMode={sendMode}
          sendActionLabel={sendActionLabel}
          actionError={actionError}
          continuationModelProviders={continuationModelProviders}
          continuationModelProvider={continuationModelProvider}
          continuationModelOverride={continuationModelOverride}
          continuationModelOptions={continuationModelOptions}
          onSelectProvider={selectContinuationModelProvider}
          onSelectModel={setContinuationModelOverride}
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
  sendMode,
  sendActionLabel,
  actionError,
  continuationModelProviders,
  continuationModelProvider,
  continuationModelOverride,
  continuationModelOptions,
  onSelectProvider,
  onSelectModel,
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
  sendMode: ConversationSendMode;
  sendActionLabel: string;
  actionError: string | null;
  continuationModelProviders: ModelProvider[];
  continuationModelProvider: string;
  continuationModelOverride: string;
  continuationModelOptions: string[];
  onSelectProvider: (providerID: string) => void;
  onSelectModel: (model: string) => void;
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
                <option value="">Keep current model provider</option>
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
              <Badge variant={sendMode === "unavailable" ? "warning" : "outline"} size="sm">
                {conversationModeText(sendMode)}
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
                disabled={!value.trim() || sending || sendMode === "unavailable"}
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

  const visibleRuntimeMessage = projectVisibleRuntimeMessage(entry);
  if (visibleRuntimeMessage) {
    return <TranscriptRow entry={visibleRuntimeMessage} />;
  }

  if (isCollapsedTranscriptEntry(entry)) {
    return <CollapsedTranscriptRow entry={entry} />;
  }

  const isUser = entry.role === "user";
  const Icon = isUser ? User : entry.role === "assistant" ? Bot : MessageSquare;
  return (
    <div className={`flex gap-3 text-sm ${isUser ? "justify-end pl-8 sm:pl-16" : "justify-start pr-2 sm:pr-8"}`}>
      {!isUser && (
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

function projectVisibleRuntimeMessage(entry: TaskTranscriptEntry): TaskTranscriptEntry | null {
  if (entry.kind !== "runtime_output" || !entry.text) return null;
  try {
    const record = JSON.parse(entry.text) as {
      type?: unknown;
      message?: { content?: unknown };
    };
    if (record.type !== "assistant" || !Array.isArray(record.message?.content)) return null;
    const text = record.message.content
      .filter((block): block is { type: "text"; text: string } => (
        typeof block === "object" && block !== null &&
        (block as { type?: unknown }).type === "text" &&
        typeof (block as { text?: unknown }).text === "string"
      ))
      .map((block) => block.text.trim())
      .filter(Boolean)
      .join("\n\n");
    if (!text) return null;
    return { ...entry, kind: "message", role: "assistant", text, stream: undefined, status: undefined };
  } catch {
    return null;
  }
}

function CollapsedTranscriptRow({ entry }: { entry: TaskTranscriptEntry }) {
  const Icon = entry.kind === "runtime_output" ? Terminal : Wrench;
  return (
    <details className="group rounded-md border border-border bg-card/60">
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-sm [&::-webkit-details-marker]:hidden">
        <ChevronRight className="h-3.5 w-3.5 text-muted-foreground transition-transform group-open:rotate-90" />
        <Icon className="h-4 w-4 text-muted-foreground" />
        <span className="text-xs text-muted-foreground shrink-0">#{entry.seq}</span>
        <span className="truncate">{collapsedTranscriptTitle(entry)}</span>
        {entry.created_at && <span className="text-xs text-muted-foreground ml-auto shrink-0">{formatDateTime(entry.created_at)}</span>}
      </summary>
      <div className="border-t border-border px-3 py-2">
        <pre className="overflow-x-auto whitespace-pre-wrap break-words text-xs leading-5 text-muted-foreground">{collapsedBody(entry)}</pre>
      </div>
    </details>
  );
}

function isCollapsedTranscriptEntry(entry: TaskTranscriptEntry) {
  return entry.kind === "tool_call" || entry.kind === "tool_result" || entry.kind === "runtime_output";
}

function collapsedBody(entry: TaskTranscriptEntry) {
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
