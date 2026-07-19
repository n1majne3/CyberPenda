import { useEffect, useState, useRef, type RefObject } from "react";
import { useParams, Link, useNavigate, useSearchParams } from "react-router-dom";
import { Square, Send, Terminal, Activity, GitBranch, MessageSquare, Play, Shield, FileText, ChevronRight, Wrench, User, Bot, ArrowDown, ArrowUp, CheckCircle2, Trash2, CircleX, KeyRound } from "lucide-react";
import { apiDelete, apiGet, apiPost, type ModelProvider, type RuntimePlugin, type RuntimeProfile, type Task, type TaskTimeline, type TaskTimelineItem, type TaskTranscript, type TaskTranscriptEntry } from "@/lib/api";
import { Button, Card, Input, Badge, Select } from "@/components/ui";
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
    () => searchParams.get("view") === "conversation" ? "conversation" : "timeline",
  );
  const [autoFollow, setAutoFollow] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [steering, setSteering] = useState("");
  const [continuationModelProvider, setContinuationModelProvider] = useState("");
  const [continuationModelOverride, setContinuationModelOverride] = useState("");
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [runtimePlugins, setRuntimePlugins] = useState<RuntimePlugin[]>([]);
  const [permissionBusy, setPermissionBusy] = useState("");
  const pageRef = useRef<HTMLDivElement>(null);
  const timelineEnd = useRef<HTMLDivElement>(null);
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
    if (!task) return;

    // Reset auto-follow when the task changes. This is an intentional
    // synchronous reset, not a cascading render.
    autoFollowRef.current = true;
    setAutoFollow(true);
    const container = findScrollContainer(pageRef.current);

    function updateAutoFollow() {
      const pinned = isNearScrollBottom(container);
      autoFollowRef.current = pinned;
      setAutoFollow((current) => current === pinned ? current : pinned);
    }

    container.addEventListener("scroll", updateAutoFollow, { passive: true });
    window.addEventListener("resize", updateAutoFollow);
    return () => {
      container.removeEventListener("scroll", updateAutoFollow);
      window.removeEventListener("resize", updateAutoFollow);
    };
  }, [task?.id]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  // Poll events while the task is active. Depends on status only so the
  // interval is not reset every render; loadAll/task are intentionally omitted.
  /* eslint-disable react-hooks/exhaustive-deps */
  useEffect(() => {
    if (!task || !ACTIVE.has(task.status)) return;
    const id = setInterval(loadAll, 2000);
    return () => clearInterval(id);
  }, [task?.status]);
  /* eslint-enable react-hooks/exhaustive-deps */

  useEffect(() => {
    if (activeView === "conversation" && autoFollowRef.current) {
      timelineEnd.current?.scrollIntoView({ behavior: "smooth" });
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
    const container = findScrollContainer(pageRef.current);
    autoFollowRef.current = true;
    setAutoFollow(true);
    container.scrollTo({ top: container.scrollHeight, behavior: prefersReducedMotion() ? "auto" : "smooth" });
  }

  function scrollToTop() {
    const container = findScrollContainer(pageRef.current);
    autoFollowRef.current = false;
    setAutoFollow(false);
    container.scrollTo({ top: 0, behavior: prefersReducedMotion() ? "auto" : "smooth" });
  }

  async function stop() {
    if (!task || !window.confirm(`Stop task ${task.goal}?`)) return;
    try {
      await apiPost(`${base}/stop`, {});
      loadAll();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function deleteTask() {
    if (!task || !window.confirm(`Delete task ${task.goal}?`)) return;
    try {
      await apiDelete(base);
      navigate(`/projects/${projectId}/tasks`);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function resumeNative() {
    try {
      await apiPost(`${base}/resume`, continuationModelPayload());
      resetContinuationModelSelection();
      loadAll();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function queueSteer() {
    try {
      await apiPost(`${base}/steer/queue`, {
        directive: steering,
        ...continuationModelPayload(),
      });
      setSteering("");
      resetContinuationModelSelection();
      loadAll();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function interruptSteer() {
    const requestID = newSteerRequestID();
    try {
      await apiPost(`${base}/steer`, {
        ...(task?.runtime_controls?.native_steer_available
          ? { request_id: requestID, message: steering }
          : { directive: steering }),
        ...continuationModelPayload(),
      });
      setSteering("");
      resetContinuationModelSelection();
      loadAll();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function respondToPermission(permissionRequestID: string, decision: "allow" | "deny") {
    setPermissionBusy(permissionRequestID);
    try {
      await apiPost(`${base}/permissions/${encodeURIComponent(permissionRequestID)}/respond`, {
        request_id: `permission-${newSteerRequestID()}`,
        decision,
      });
      loadAll();
    } catch (e) {
      setError((e as Error).message);
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
  const nativeResumeReason = controls?.native_resume_reason ?? "Native resume unavailable";
  const interruptSteerReason = controls?.interrupt_steer_reason ?? nativeResumeReason;
  const nativeSteerAvailable = controls?.native_steer_available ?? false;
  const nativeSteerMode = controls?.native_steer_mode;
  const nativeSteerState = controls?.native_steer_state;
  const providerPermissions = controls?.provider_permissions ?? [];
  const steerAvailable = nativeSteerAvailable || interruptSteerAvailable;
  const running = ACTIVE.has(task.status);

  return (
    <ProjectPageShell
      ref={pageRef}
      title={<h2 className="min-w-0 break-words text-xl font-semibold tracking-tight">{task.goal}</h2>}
      actions={
        <div className="flex flex-wrap gap-2 sm:justify-end">
          {!running && (
            <Button size="sm" variant="outline" onClick={resumeNative} disabled={!resumeAvailable} title={nativeResumeAvailable ? "Resume native session" : "Start a fresh continuation from the current Task state"}>
              <Play className="h-4 w-4" /> Resume
            </Button>
          )}
          {running && (
            <Button size="sm" variant="destructive" onClick={stop}>
              <Square className="h-4 w-4" /> Stop
            </Button>
          )}
          {DELETABLE.has(task.status) && (
            <Button size="sm" variant="destructive" onClick={deleteTask}>
              <Trash2 className="h-4 w-4" /> Delete
            </Button>
          )}
        </div>
      }
      bodyClassName="space-y-6"
    >
      <div className="flex flex-wrap gap-2">
        <StatusBadge status={task.status} />
        <Badge variant={task.runner === "host" ? "destructive" : "outline"}>
          runner: {task.runner}
        </Badge>
      </div>
      {currentContinuation && (
        <div className="flex flex-wrap gap-2">
          <Badge variant="outline">continuation #{currentContinuation.number}</Badge>
          <Badge variant="outline">runtime: {currentContinuation.runtime_provider}</Badge>
          <Badge variant="outline">continuation status: {currentContinuation.status}</Badge>
          {(controls?.native_session_captured || currentContinuation.native_session_id) && <Badge variant="outline">native session: captured</Badge>}
          {controls?.same_runtime_provider_only && <Badge variant="outline">same runtime only</Badge>}
          {nativeSteerMode && <Badge variant="outline">steer: {nativeSteerMode === "in_turn_steer" ? "direct native" : "interrupt then replace"}</Badge>}
          {nativeSteerState && nativeSteerState !== "idle" && <Badge variant={nativeSteerState === "failed" ? "destructive" : "outline"}>steer: {nativeSteerState}</Badge>}
          {controls?.recovery_state && <Badge variant={controls.recovery_state === "failed_closed" ? "warning" : "outline"}>session recovery: {controls.recovery_state.replaceAll("_", " ")}</Badge>}
        </div>
      )}

      {providerPermissions.length > 0 && (
        <Card className="space-y-3" aria-label="Provider permission requests">
          <div className="flex items-center gap-2 text-sm font-medium">
            <KeyRound className="h-4 w-4" /> Provider permission
          </div>
          {providerPermissions.map((permission) => (
            <div key={permission.permission_request_id} className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-3">
              <div className="min-w-0">
                <div className="text-sm">Permission request</div>
                <code className="block max-w-full truncate text-xs text-muted-foreground">{permission.permission_request_id}</code>
              </div>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  onClick={() => respondToPermission(permission.permission_request_id, "allow")}
                  disabled={permissionBusy !== ""}
                  aria-label={`Allow provider permission ${permission.permission_request_id}`}
                >
                  <CheckCircle2 className="h-4 w-4" /> Allow
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => respondToPermission(permission.permission_request_id, "deny")}
                  disabled={permissionBusy !== ""}
                  aria-label={`Deny provider permission ${permission.permission_request_id}`}
                >
                  <CircleX className="h-4 w-4" /> Deny
                </Button>
              </div>
            </div>
          ))}
        </Card>
      )}

      <div className="mb-6 flex flex-wrap gap-2 text-sm">
        <Link to={`/projects/${projectId}/facts`} className="inline-flex items-center gap-1 rounded-md text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
          <FileText className="h-4 w-4" /> Facts
        </Link>
        <Link to={`/projects/${projectId}/findings`} className="inline-flex items-center gap-1 rounded-md text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
          <Shield className="h-4 w-4" /> Findings
        </Link>
        <Link to={`/projects/${projectId}/evidence`} className="inline-flex items-center gap-1 rounded-md text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
          <Terminal className="h-4 w-4" /> Evidence
        </Link>
        <Link to={`/projects/${projectId}/report`} className="inline-flex items-center gap-1 rounded-md text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
          <FileText className="h-4 w-4" /> Report
        </Link>
      </div>

      {/* Steering */}
      <Card className="mb-6 space-y-2">
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <GitBranch className="h-4 w-4" /> Task controls
        </div>
        <Input
          aria-label="Steering directive"
          name="steering_directive"
          value={steering}
          onChange={(e) => setSteering(e.target.value)}
          placeholder="Focus on admin.example.com next…"
          autoComplete="off"
        />
        <div className="flex flex-wrap gap-2 items-center">
          <Select
            className="min-w-0 flex-1 sm:max-w-xs"
            name="continuation_model_provider"
            value={continuationModelProvider}
            onChange={(e) => selectContinuationModelProvider(e.target.value)}
            aria-label="Continuation model provider"
          >
            <option value="">Keep current model provider</option>
            {continuationModelProviders.map((provider) => (
              <option key={provider.id} value={provider.id}>{provider.name}</option>
            ))}
          </Select>
          <Select
            className="min-w-0 flex-1 sm:max-w-xs"
            name="continuation_model"
            value={continuationModelOverride}
            onChange={(e) => setContinuationModelOverride(e.target.value)}
            aria-label="Continuation model"
            disabled={!continuationModelProvider || continuationModelOptions.length === 0}
          >
            {continuationModelOptions.length === 0 ? (
              <option value="">Default model</option>
            ) : continuationModelOptions.map((model) => (
              <option key={model} value={model}>{model}</option>
            ))}
          </Select>
          <Button size="sm" variant="outline" onClick={queueSteer} disabled={!steering.trim() || !queueSteerAvailable}>
            <Send className="h-4 w-4 mr-1" /> Queue steer
          </Button>
          {running && (
            <Button size="sm" onClick={interruptSteer} disabled={!steering.trim() || !steerAvailable} title={nativeSteerAvailable ? "Send through the provider-native session" : interruptSteerAvailable ? "Interrupt and resume native session" : controls?.native_steer_reason ?? interruptSteerReason}>
              <GitBranch className="h-4 w-4 mr-1" /> {nativeSteerAvailable && nativeSteerMode === "in_turn_steer" ? "Steer natively" : nativeSteerAvailable ? "Native interrupt & send" : "Recovery interrupt & resume"}
            </Button>
          )}
        </div>
      </Card>

      <div className="flex items-center gap-1 border-b border-border mb-3">
        <button
          type="button"
          className={tabClass(activeView === "timeline")}
          aria-pressed={activeView === "timeline"}
          onClick={() => selectView("timeline")}
        >
          <Activity className="h-4 w-4" /> Timeline
        </button>
        <button
          type="button"
          className={tabClass(activeView === "conversation")}
          aria-pressed={activeView === "conversation"}
          onClick={() => selectView("conversation")}
        >
          <MessageSquare className="h-4 w-4" /> Conversation
        </button>
      </div>

      {activeView === "timeline" ? (
        <div>
          <AgentTranscriptView
            task={task}
            items={timeline}
            profileName={profiles.find((p) => p.id === task.runtime_profile_id)?.name}
            isLive={ACTIVE.has(task.status)}
          />
          <div ref={timelineEnd} />
        </div>
      ) : (
        <TranscriptList entries={transcript} endRef={timelineEnd} />
      )}

      <FloatingScrollControls autoFollow={autoFollow} onTop={scrollToTop} onBottom={scrollToLatest} />
    </ProjectPageShell>
  );
}

function tabClass(active: boolean) {
  return [
    "inline-flex items-center gap-1.5 rounded-t-md border-b-2 px-3 py-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    active ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
  ].join(" ");
}

function findScrollContainer(element: HTMLElement | null): HTMLElement {
  let current = element?.parentElement ?? null;
  while (current) {
    const overflowY = window.getComputedStyle(current).overflowY;
    if (overflowY === "auto" || overflowY === "scroll" || overflowY === "overlay") {
      return current;
    }
    current = current.parentElement;
  }
  return document.documentElement;
}

function isNearScrollBottom(container: HTMLElement, threshold = 160) {
  return container.scrollHeight - (container.scrollTop + container.clientHeight) <= threshold;
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
    <div className="fixed bottom-5 right-5 z-30 flex flex-col gap-2">
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
    <div className="space-y-4">
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
        <span className="shrink-0">#{entry.seq} {entry.text}</span>
        <span className="h-px flex-1 bg-border" />
      </div>
    );
  }

  if (isCollapsedTranscriptEntry(entry)) {
    return <CollapsedTranscriptRow entry={entry} />;
  }

  const isUser = entry.role === "user";
  const Icon = isUser ? User : entry.role === "assistant" ? Bot : MessageSquare;
  return (
    <div className={`flex gap-2 text-sm ${isUser ? "justify-end" : "justify-start"}`}>
      {!isUser && (
        <span className="text-muted-foreground shrink-0 mt-2">
          <Icon className="h-4 w-4" />
        </span>
      )}
      <div
        data-testid="transcript-message-bubble"
        className={`min-w-0 max-w-[85%] rounded-lg border px-4 py-3 shadow-sm ${isUser ? "border-primary/20 bg-primary/10" : "border-border bg-card"}`}
      >
        <div className="mb-1 text-xs text-muted-foreground">
          #{entry.seq} {entry.role}{entry.created_at && ` · ${formatDateTime(entry.created_at)}`}
        </div>
        <div className="whitespace-pre-wrap break-words leading-6">{entry.text}</div>
      </div>
      {isUser && (
        <span className="text-muted-foreground shrink-0 mt-2">
          <Icon className="h-4 w-4" />
        </span>
      )}
    </div>
  );
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
