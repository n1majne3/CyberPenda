import { useEffect, useState, useRef } from "react";
import { useParams, Link } from "react-router-dom";
import { ArrowLeft, Square, Send, Terminal, Activity, GitBranch, MessageSquare } from "lucide-react";
import { apiGet, apiPost, type Task, type TaskEvent } from "@/lib/api";
import { Button, Card, Input, Badge } from "@/components/ui";

const ACTIVE = new Set(["running", "paused"]);

export function TaskDetailPage() {
  const { projectId, taskId } = useParams<{ projectId: string; taskId: string }>();
  const [task, setTask] = useState<Task | null>(null);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [steering, setSteering] = useState("");
  const [steerProfile, setSteerProfile] = useState("");
  const [profiles, setProfiles] = useState<{ id: string; name: string }[]>([]);
  const timelineEnd = useRef<HTMLDivElement>(null);

  const base = `/api/projects/${projectId}/tasks/${taskId}`;

  async function loadAll() {
    if (!projectId || !taskId) return;
    try {
      const [t, ev] = await Promise.all([
        apiGet<Task>(`${base}`),
        apiGet<{ events: TaskEvent[] }>(`${base}/events`),
      ]);
      setTask(t);
      setEvents(ev.events ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    loadAll();
    apiGet<{ profiles: { id: string; name: string }[] }>("/api/runtime-profiles").then((d) => setProfiles(d.profiles ?? [])).catch(() => {});
  }, [projectId, taskId]);

  // Poll events while the task is active.
  useEffect(() => {
    if (!task || !ACTIVE.has(task.status)) return;
    const id = setInterval(loadAll, 2000);
    return () => clearInterval(id);
  }, [task?.status]);

  useEffect(() => {
    timelineEnd.current?.scrollIntoView({ behavior: "smooth" });
  }, [events]);

  async function stop() {
    try {
      await apiPost(`${base}/stop`, {});
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

  if (error) return <div className="p-8 text-destructive">{error}</div>;
  if (!task) return <div className="p-8 text-muted-foreground">Loading…</div>;

  return (
    <div className="p-8 max-w-3xl">
      <Link to={`/projects/${projectId}`} className="inline-flex items-center text-sm text-muted-foreground hover:text-foreground mb-4">
        <ArrowLeft className="h-4 w-4 mr-1" /> Back to dashboard
      </Link>

      <div className="flex items-center justify-between mb-2">
        <h2 className="text-xl font-semibold">{task.goal}</h2>
        {ACTIVE.has(task.status) && (
          <Button size="sm" variant="destructive" onClick={stop}>
            <Square className="h-4 w-4 mr-1" /> Stop
          </Button>
        )}
      </div>
      <div className="flex gap-2 mb-6">
        <StatusBadge status={task.status} />
        <Badge variant={task.runner === "host" ? "destructive" : "outline"}>
          runner: {task.runner}
        </Badge>
        {task.run_controls.yolo && <Badge variant="warning">YOLO</Badge>}
      </div>

      {/* Steering */}
      <Card className="mb-6 space-y-2">
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <GitBranch className="h-4 w-4" /> Steering (applies to next continuation)
        </div>
        <Input value={steering} onChange={(e) => setSteering(e.target.value)} placeholder="Focus on admin.example.com next" />
        <div className="flex gap-2 items-center">
          <select
            className="flex h-9 rounded-md border border-input bg-background px-3 text-sm"
            value={steerProfile}
            onChange={(e) => setSteerProfile(e.target.value)}
          >
            <option value="">Keep current profile</option>
            {profiles.map((p) => (
              <option key={p.id} value={p.id}>Switch to {p.name}</option>
            ))}
          </select>
          <Button size="sm" onClick={steer} disabled={!steering.trim()}>
            <Send className="h-4 w-4 mr-1" /> Steer
          </Button>
        </div>
      </Card>

      {/* Timeline — structured events, not raw output (prd.md:205). */}
      <h3 className="text-sm font-medium text-muted-foreground mb-2 flex items-center gap-1">
        <Activity className="h-4 w-4" /> Timeline
      </h3>
      <div className="space-y-2">
        {events.map((ev) => (
          <EventRow key={ev.id} ev={ev} />
        ))}
        {events.length === 0 && <p className="text-sm text-muted-foreground">No events yet.</p>}
        <div ref={timelineEnd} />
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  const variant =
    status === "completed" ? "success" :
    status === "running" ? "primary" :
    status === "failed" ? "destructive" :
    status === "stopped" ? "warning" : "outline";
  return <Badge variant={variant}>{status}</Badge>;
}

function EventRow({ ev }: { ev: TaskEvent }) {
  const icon =
    ev.kind === "lifecycle" ? <Activity className="h-3.5 w-3.5" /> :
    ev.kind === "steering" ? <GitBranch className="h-3.5 w-3.5" /> :
    ev.kind === "conversation" ? <MessageSquare className="h-3.5 w-3.5" /> :
    <Terminal className="h-3.5 w-3.5" />;
  const text = (ev.payload.text as string) ?? (ev.payload.phase as string) ?? JSON.stringify(ev.payload);
  return (
    <div className="flex gap-2 text-sm">
      <span className="text-muted-foreground shrink-0 mt-0.5">{icon}</span>
      <div className="flex-1 min-w-0">
        <span className="text-xs text-muted-foreground mr-2">#{ev.seq} {ev.kind}</span>
        <span className="whitespace-pre-wrap break-words">{text}</span>
      </div>
    </div>
  );
}
