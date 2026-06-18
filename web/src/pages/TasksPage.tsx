import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, ListChecks, Plus } from "lucide-react";
import { apiGet, type Task } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { Badge, Button, Card } from "@/components/ui";

const STATUS_VARIANT: Record<string, "primary" | "success" | "warning" | "destructive" | "outline"> = {
  running: "primary",
  completed: "success",
  pending: "outline",
  paused: "warning",
  failed: "destructive",
  stopped: "outline",
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
    <div className="p-8 max-w-4xl">
      <Link to="/" className="inline-flex items-center text-sm text-muted-foreground hover:text-foreground mb-4">
        <ArrowLeft className="h-4 w-4 mr-1" /> All projects
      </Link>
      <ProjectNav />

      <div className="flex items-center justify-between mb-4">
        <h2 className="text-xl font-semibold flex items-center gap-2">
          <ListChecks className="h-5 w-5" /> Tasks
        </h2>
        <Link to={`${base}/tasks/new`}>
          <Button size="sm">
            <Plus className="h-4 w-4 mr-1" /> Launch task
          </Button>
        </Link>
      </div>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      {tasks.length === 0 && !error && (
        <p className="text-sm text-muted-foreground">No tasks yet. Launch one to start testing.</p>
      )}

      <div className="space-y-2">
        {tasks.map((task) => (
          <Link key={task.id} to={`${base}/tasks/${task.id}`}>
            <Card className="hover:bg-accent/50 transition-colors">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                  <p className="font-medium truncate">{task.goal || "(no goal)"}</p>
                  <p className="text-xs text-muted-foreground mt-1">
                    {new Date(task.created_at).toLocaleString()} · runner {task.runner}
                  </p>
                </div>
                <div className="flex gap-1 shrink-0">
                  {task.run_controls?.yolo && <Badge variant="warning">YOLO</Badge>}
                  <Badge variant={STATUS_VARIANT[task.status] ?? "outline"}>{task.status}</Badge>
                </div>
              </div>
            </Card>
          </Link>
        ))}
      </div>
    </div>
  );
}