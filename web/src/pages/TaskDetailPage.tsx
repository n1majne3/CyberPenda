import { useEffect, useState, useRef, type RefObject } from "react";
import { useParams, Link } from "react-router-dom";
import { Square, Send, Terminal, Activity, GitBranch, MessageSquare, Play, FileText, Shield, ChevronRight, Wrench, User, Bot, ArrowDown, ArrowUp } from "lucide-react";
import { apiGet, apiPost, type Task, type TaskTimeline, type TaskTimelineItem, type TaskTranscript, type TaskTranscriptEntry } from "@/lib/api";
import { Button, Card, Input, Badge, Select } from "@/components/ui";
import { BackLink, PageContainer } from "@/components/shared";
import { AgentTranscriptView } from "@/components/task-transcript/AgentTranscriptView";
import { collapsedTranscriptTitle } from "./taskDetailView";

const ACTIVE = new Set(["running", "paused"]);

export function TaskDetailPage() {
  const { projectId, taskId } = useParams<{ projectId: string; taskId: string }>();
  const [task, setTask] = useState<Task | null>(null);
  const [timeline, setTimeline] = useState<TaskTimelineItem[]>([]);
  const [transcript, setTranscript] = useState<TaskTranscriptEntry[]>([]);
  const [activeView, setActiveView] = useState<"conversation" | "timeline">("timeline");
  const [autoFollow, setAutoFollow] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [steering, setSteering] = useState("");
  const [steerProfile, setSteerProfile] = useState("");
  const [profiles, setProfiles] = useState<{ id: string; name: string }[]>([]);
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
    apiGet<{ profiles: { id: string; name: string }[] }>("/api/runtime-profiles").then((d) => setProfiles(d.profiles ?? [])).catch(() => {});
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

  function scrollToLatest() {
    const container = findScrollContainer(pageRef.current);
    autoFollowRef.current = true;
    setAutoFollow(true);
    container.scrollTo({ top: container.scrollHeight, behavior: "smooth" });
  }

  function scrollToTop() {
    const container = findScrollContainer(pageRef.current);
    autoFollowRef.current = false;
    setAutoFollow(false);
    container.scrollTo({ top: 0, behavior: "smooth" });
  }

  async function stop() {
    try {
      await apiPost(`${base}/stop`, {});
      loadAll();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function resume() {
    try {
      await apiPost(`${base}/resume`, {});
      loadAll();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function steer() {
    try {
      await apiPost(`${base}/steer`, { directive: steering, runtime_profile_id: steerProfile || undefined });
      setSteering("");
      loadAll();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  if (error) return <PageContainer className="text-destructive">{error}</PageContainer>;
  if (!task) return <PageContainer className="text-muted-foreground">Loading…</PageContainer>;
  const currentContinuation = task.active_continuation ?? task.latest_continuation;

  return (
    <PageContainer ref={pageRef} className="max-w-4xl">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>

      <div className="mb-2 flex items-center justify-between">
        <h2 className="text-xl font-semibold tracking-tight">{task.goal}</h2>
        <div className="flex gap-2">
          {!ACTIVE.has(task.status) && (
            <Button size="sm" variant="outline" onClick={resume}>
              <Play className="h-4 w-4" /> Resume
            </Button>
          )}
          {ACTIVE.has(task.status) && (
            <Button size="sm" variant="destructive" onClick={stop}>
              <Square className="h-4 w-4" /> Stop
            </Button>
          )}
        </div>
      </div>
      <div className="mb-6 flex gap-2">
        <StatusBadge status={task.status} />
        <Badge variant={task.runner === "host" ? "destructive" : "outline"}>
          runner: {task.runner}
        </Badge>
        {task.run_controls.yolo && <Badge variant="warning">YOLO</Badge>}
      </div>
      {currentContinuation && (
        <div className="mb-6 flex gap-2">
          <Badge variant="outline">continuation #{currentContinuation.number}</Badge>
          <Badge variant="outline">runtime: {currentContinuation.runtime_provider}</Badge>
          <Badge variant="outline">continuation status: {currentContinuation.status}</Badge>
        </div>
      )}

      <div className="mb-6 flex flex-wrap gap-2 text-sm">
        <Link to={`/projects/${projectId}/facts`} className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground">
          <FileText className="h-4 w-4" /> Facts
        </Link>
        <Link to={`/projects/${projectId}/findings`} className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground">
          <Shield className="h-4 w-4" /> Findings
        </Link>
        <Link to={`/projects/${projectId}/evidence`} className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground">
          <Terminal className="h-4 w-4" /> Evidence
        </Link>
        <Link to={`/projects/${projectId}/report`} className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground">
          <FileText className="h-4 w-4" /> Report
        </Link>
      </div>

      {/* Steering */}
      <Card className="mb-6 space-y-2">
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <GitBranch className="h-4 w-4" /> Steering (applies to next continuation)
        </div>
        <Input value={steering} onChange={(e) => setSteering(e.target.value)} placeholder="Focus on admin.example.com next" />
        <div className="flex gap-2 items-center">
          <Select
            className="max-w-xs"
            value={steerProfile}
            onChange={(e) => setSteerProfile(e.target.value)}
          >
            <option value="">Keep current profile</option>
            {profiles.map((p) => (
              <option key={p.id} value={p.id}>Switch to {p.name}</option>
            ))}
          </Select>
          <Button size="sm" onClick={steer} disabled={!steering.trim()}>
            <Send className="h-4 w-4 mr-1" /> Steer
          </Button>
        </div>
      </Card>

      <div className="flex items-center gap-1 border-b border-border mb-3">
        <button className={tabClass(activeView === "timeline")} onClick={() => setActiveView("timeline")}>
          <Activity className="h-4 w-4" /> Timeline
        </button>
        <button className={tabClass(activeView === "conversation")} onClick={() => setActiveView("conversation")}>
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
    </PageContainer>
  );
}

function tabClass(active: boolean) {
  return [
    "inline-flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm",
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
  return (
    <div className="fixed bottom-5 right-5 z-30 flex flex-col gap-2">
      <Button size="sm" variant="outline" className="h-9 w-9 p-0 shadow-md" onClick={onTop} aria-label="Scroll to top" title="Top">
        <ArrowUp className="h-4 w-4" />
      </Button>
      <Button size="sm" variant={autoFollow ? "secondary" : "outline"} className="h-9 w-9 p-0 shadow-md" onClick={onBottom} aria-label="Scroll to bottom" title="Bottom">
        <ArrowDown className="h-4 w-4" />
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
        <TranscriptRow key={entry.id} entry={entry} />
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
      <div className={`min-w-0 max-w-[85%] rounded-2xl border px-4 py-3 shadow-sm ${isUser ? "border-primary/20 bg-primary/10" : "border-border bg-card"}`}>
        <div className="mb-1 text-xs text-muted-foreground">
          #{entry.seq} {entry.role}{entry.created_at && ` · ${new Date(entry.created_at).toLocaleString()}`}
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
        {entry.created_at && <span className="text-xs text-muted-foreground ml-auto shrink-0">{new Date(entry.created_at).toLocaleString()}</span>}
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
