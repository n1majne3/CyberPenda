import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { AlertTriangle, CheckCircle2, Circle, ListChecks, Loader2, PauseCircle, Plus, Square } from "lucide-react";
import { apiGet, type Task } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Button, Card } from "@/components/ui";
import { formatDateTime } from "@/lib/format";

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

export function TasksPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [error, setError] = useState<string | null>(null);

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
      }
    }

    loadTasks();
    // Poll while any task may still have live Runtime Activity.
    const id = setInterval(loadTasks, 2000);
    return () => {
      cancelled = true;
      controller?.abort();
      clearInterval(id);
    };
  }, [projectId]);

  const base = `/projects/${projectId}`;

  return (
    <ProjectPageShell
      title={
        <h2 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
          <ListChecks className="h-5 w-5 text-primary" /> Tasks
        </h2>
      }
      actions={
        <Link to={`${base}/tasks/new`}>
          <Button size="sm">
            <Plus className="h-4 w-4" /> Launch task
          </Button>
        </Link>
      }
      bodyClassName="space-y-4"
    >
      {error && <p className="text-sm text-destructive">{error}</p>}

      {tasks.length === 0 && !error && (
        <Card className="items-center justify-center !py-12 text-center">
          <p className="text-sm font-medium">No tasks yet</p>
          <p className="text-sm text-muted-foreground">Launch one to start testing.</p>
        </Card>
      )}

      <div className="space-y-2">
        {sortTasksForDisplay(tasks).map((task) => (
          <Link
            key={task.id}
            to={`${base}/tasks/${task.id}`}
            className="group block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
          >
            <Card className="transition-[background-color,box-shadow] hover:bg-accent/40 hover:ring-foreground/20">
              <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                <div className="min-w-0 flex-1">
                  <p className="break-words font-medium leading-snug group-hover:text-foreground">
                    {task.goal || "(no goal)"}
                  </p>
                  <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                    <span>{formatDateTime(task.created_at)}</span>
                    <span>runner {task.runner}</span>
                  </div>
                </div>
                <div className="flex shrink-0 flex-wrap gap-1 sm:justify-end">
                  <TaskStatusBadge status={task.status} />
                  <RuntimeActivityBadge activity={task.runtime_activity} />
                </div>
              </div>
            </Card>
          </Link>
        ))}
      </div>
    </ProjectPageShell>
  );
}

function TaskStatusBadge({ status }: { status: string }) {
  const meta = STATUS_META[status] ?? { variant: "outline" as const, icon: Circle };
  const Icon = meta.icon;
  return (
    <Badge variant={meta.variant}>
      <Icon className={`h-3 w-3 ${status === "running" ? "animate-spin motion-reduce:animate-none" : ""}`} />
      {status}
    </Badge>
  );
}

function RuntimeActivityBadge({ activity }: { activity?: Task["runtime_activity"] }) {
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
    liveness === "orphaned" || liveness === "unknown" ? "warning" : "outline";
  return (
    <Badge variant={variant} data-testid="runtime-activity" title={activity.warning || label}>
      {label}
    </Badge>
  );
}

function sortTasksForDisplay(tasks: Task[]): Task[] {
  return [...tasks].sort((a, b) => {
    const runningDelta = Number(b.status === "running") - Number(a.status === "running");
    if (runningDelta !== 0) return runningDelta;
    return Date.parse(b.created_at) - Date.parse(a.created_at);
  });
}
