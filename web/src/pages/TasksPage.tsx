import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { AlertTriangle, CheckCircle2, Circle, ListChecks, Loader2, PauseCircle, Plus, Rocket, Search, Square } from "lucide-react";
import { apiGet, type Task } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, Input } from "@/components/ui";
import { ErrorState, LoadingState, RichEmptyState } from "@/components/shared";
import { formatDateTime } from "@/lib/format";
import { useDocumentVisibility } from "@/lib/useDocumentVisibility";

const STATUS_META: Record<
  string,
  {
    variant: "primary" | "success" | "warning" | "destructive" | "outline";
    icon: typeof Circle;
  }
> = {
  running: { variant: "primary", icon: Loader2 },
  completed: { variant: "success", icon: CheckCircle2 },
  pending: { variant: "outline", icon: Circle },
  paused: { variant: "warning", icon: PauseCircle },
  failed: { variant: "destructive", icon: AlertTriangle },
  stopped: { variant: "outline", icon: Square },
  interrupted: { variant: "warning", icon: AlertTriangle },
};

const ACTIVE_STATUS = new Set(["running", "paused"]);

function isTaskActive(task: Task) {
  return ACTIVE_STATUS.has(task.status) || task.runtime_activity?.liveness === "live";
}

export function TasksPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [filter, setFilter] = useState("All");
  const [query, setQuery] = useState("");
  const isVisible = useDocumentVisibility();
  const hasActive = tasks.some(isTaskActive);

  const loadTasksRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    let generation = 0;
    let controller: AbortController | null = null;

    async function loadTasks() {
      controller?.abort();
      controller = new AbortController();
      const current = ++generation;
      try {
        const d = await apiGet<{ tasks: Task[] }>(`/api/projects/${projectId}/tasks`, {
          signal: controller.signal,
        });
        if (cancelled || current !== generation) return;
        setTasks(d.tasks ?? []);
        setError(null);
      } catch (e) {
        if (cancelled || current !== generation || controller?.signal.aborted) return;
        setError((e as Error).message);
      } finally {
        if (!cancelled && current === generation) setLoading(false);
      }
    }

    loadTasksRef.current = loadTasks;
    void loadTasks();
    return () => {
      cancelled = true;
      controller?.abort();
      loadTasksRef.current = null;
    };
  }, [projectId]);

  // Gated poll: fast (2s) while work is active, slow (30s) once idle, and off
  // entirely while the tab is hidden. Returning to a visible tab eagerly
  // refreshes before resuming the cadence; the interval restarts when the active
  // flag flips so the period tracks the latest task data.
  useEffect(() => {
    if (!projectId || !isVisible) return;
    void loadTasksRef.current?.();
    const id = setInterval(() => void loadTasksRef.current?.(), hasActive ? 2000 : 30000);
    return () => clearInterval(id);
  }, [projectId, isVisible, hasActive]);

  const base = `/projects/${projectId}`;
  const visibleTasks = sortTasksForDisplay(tasks).filter((task) => {
    const matchesState = filter === "All" || (filter === "Active" ? task.status === "pending" || isTaskActive(task) : filter === "Needs input" ? ["paused", "failed", "interrupted"].includes(task.status) : task.status === "completed" && !isTaskActive(task));
    return matchesState && task.goal.toLowerCase().includes(query.trim().toLowerCase());
  });

  return (
    <ProjectPageShell
      title={
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
          <ListChecks className="h-5 w-5 text-signal" /> Tasks
        </h1>
      }
      description="Give each Task one clear goal."
      actions={
        <Link to={`${base}/tasks/new`}>
          <Button size="sm">
            <Plus className="h-3.5 w-3.5" /> New task
          </Button>
        </Link>
      }
      bodyClassName="space-y-4"
    >
      {error && !loading && <ErrorState error={error} title="Couldn't load tasks" />}

      {loading && tasks.length === 0 && !error && <LoadingState label="Loading tasks" />}

      {tasks.length === 0 && !error && !loading && (
        <RichEmptyState
          icon={<Rocket className="h-6 w-6" aria-hidden="true" />}
          title="No tasks yet"
          description="Launch one to start testing."
          action={
            <Link to={`${base}/tasks/new`}>
              <Button size="sm">
                <Plus className="h-4 w-4" /> Launch task
              </Button>
            </Link>
          }
        />
      )}

      {tasks.length > 0 && (
        <div className="mb-[15px] flex flex-wrap items-center justify-between gap-3">
          <label className="flex min-w-0 max-w-80 flex-1 items-center gap-2"><Search className="size-3.5 shrink-0 text-muted-foreground" /><Input type="search" aria-label="Search tasks" placeholder="Search tasks…" value={query} onChange={(event) => setQuery(event.target.value)} className="max-w-64 bg-card" /></label>
          <div className="flex shrink-0 gap-0.5 rounded-md border border-border bg-muted/50 p-0.5" role="group" aria-label="Task state">
            {["All", "Active", "Needs input", "Completed"].map((state) => (
              <button key={state} type="button" className={`rounded px-2.5 py-1.5 text-[11px] transition-colors ${filter === state ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"}`} aria-pressed={filter === state} onClick={() => setFilter(state)}>{state}</button>
            ))}
          </div>
        </div>
      )}
      {tasks.length > 0 && visibleTasks.length === 0 && <p role="status" className="py-8 text-center text-sm text-muted-foreground">No tasks match your filters.</p>}

      {tasks.length > 0 && (
        <div className="divide-y divide-border">
          {visibleTasks.map((task) => {
            const meta = STATUS_META[task.status] ?? { variant: "outline" as const, icon: Circle };
            const StatusIcon = meta.icon;
            return (
              <Link
                key={task.id}
                to={`${base}/tasks/${task.id}`}
                className="group block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-signal/40 focus-visible:ring-offset-2 focus-visible:ring-offset-background"
              >
                <div className="py-[17px] transition-colors group-hover:bg-muted/30">
                  <div className="flex items-center justify-between gap-[13px]">
                    <div className="flex min-w-0 flex-1 items-center gap-[13px]">
                      <span className={`flex size-8 shrink-0 items-center justify-center rounded-[7px] border ${task.status === "running" ? "border-transparent bg-signal/5 text-signal" : "border-border bg-card text-muted-foreground"}`}>
                        <StatusIcon className={`h-4 w-4 ${task.status === "running" ? "animate-spin motion-reduce:animate-none" : ""}`} aria-hidden="true" />
                      </span>
                      <div className="min-w-0 flex-1">
                        <p title={task.goal || "(no goal)"} className="truncate text-xs font-medium leading-snug group-hover:text-signal">
                          {task.goal || "(no goal)"}
                        </p>
                        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-muted-foreground">
                          <span className="tabular-nums">{formatDateTime(task.created_at)}</span>
                          <span aria-hidden="true" className="text-muted-foreground/40">
                            ·
                          </span>
                          <span>runner {task.runner}</span>
                          <RuntimeActivityBadge activity={task.runtime_activity} />
                        </div>
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      <TaskStatusBadge status={task.status} />
                    </div>
                  </div>
                </div>
              </Link>
            );
          })}
        </div>
      )}
    </ProjectPageShell>
  );
}

function TaskStatusBadge({ status }: { status: string }) {
  const color = status === "running" ? "bg-signal" : ["failed", "interrupted", "paused"].includes(status) ? "bg-warning" : "bg-muted-foreground/50";
  return <span className="inline-flex items-center gap-1.5 text-[11px] capitalize text-muted-foreground"><span className={`size-1.5 rounded-full ${color}`} />{status}</span>;
}

function RuntimeActivityBadge({ activity }: { activity?: Task["runtime_activity"] }) {
  if (!activity?.liveness) return null;
  const liveness = activity.liveness;
  const label = liveness === "live" && activity.turn_activity ? `runtime ${liveness} · ${activity.turn_activity}` : `runtime ${liveness}`;
  const color = liveness === "live" ? "text-signal" : ["orphaned", "unknown"].includes(liveness) ? "text-warning" : "text-muted-foreground";
  return <span className={color} data-testid="runtime-activity" title={activity.warning || label}>{label}</span>;
}

function sortTasksForDisplay(tasks: Task[]): Task[] {
  return [...tasks].sort((a, b) => {
    const runningDelta = Number(b.status === "running") - Number(a.status === "running");
    if (runningDelta !== 0) return runningDelta;
    return Date.parse(b.created_at) - Date.parse(a.created_at);
  });
}
