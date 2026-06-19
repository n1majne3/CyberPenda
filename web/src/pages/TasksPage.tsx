import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ListChecks, Plus } from "lucide-react";
import { apiGet, type Task } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { Badge, Button, Card } from "@/components/ui";
import { BackLink, PageContainer } from "@/components/shared";

const STATUS_VARIANT: Record<string, "primary" | "success" | "warning" | "destructive" | "outline"> = {
  running: "primary",
  completed: "success",
  pending: "outline",
  paused: "warning",
  failed: "destructive",
  stopped: "outline",
  interrupted: "warning",
};

export function TasksPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId) return;
    apiGet<{ tasks: Task[] }>(`/api/projects/${projectId}/tasks`)
      .then((d) => {
        setTasks(d.tasks ?? []);
        setError(null);
      })
      .catch((e) => setError((e as Error).message));
  }, [projectId]);

  const base = `/projects/${projectId}`;

  return (
    <PageContainer className="max-w-4xl">
      <BackLink to="/">All projects</BackLink>
      <ProjectNav />

      <div className="mb-4 flex items-center justify-between">
        <h2 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
          <ListChecks className="h-5 w-5 text-primary" /> Tasks
        </h2>
        <Link to={`${base}/tasks/new`}>
          <Button size="sm">
            <Plus className="h-4 w-4" /> Launch task
          </Button>
        </Link>
      </div>

      {error && <p className="mb-4 text-sm text-destructive">{error}</p>}

      {tasks.length === 0 && !error && (
        <Card className="items-center justify-center !py-12 text-center">
          <p className="text-sm font-medium">No tasks yet</p>
          <p className="text-sm text-muted-foreground">Launch one to start testing.</p>
        </Card>
      )}

      <div className="space-y-2">
        {tasks.map((task) => (
          <Link key={task.id} to={`${base}/tasks/${task.id}`} className="group">
            <Card className="transition-all hover:bg-accent/40 hover:ring-foreground/20">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium group-hover:text-foreground">{task.goal || "(no goal)"}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {new Date(task.created_at).toLocaleString()} · runner {task.runner}
                  </p>
                </div>
                <div className="flex shrink-0 gap-1">
                  {task.run_controls?.yolo && <Badge variant="warning">YOLO</Badge>}
                  <Badge variant={STATUS_VARIANT[task.status] ?? "outline"}>{task.status}</Badge>
                </div>
              </div>
            </Card>
          </Link>
        ))}
      </div>
    </PageContainer>
  );
}