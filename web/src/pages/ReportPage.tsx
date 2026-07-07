import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { ClipboardList, Download } from "lucide-react";
import { apiGet, apiPost, type Task } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { BackLink, PageContainer } from "@/components/shared";
import { Button, Card, Label, Select } from "@/components/ui";

export function ReportPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [taskId, setTaskId] = useState("");
  const [markdown, setMarkdown] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [generating, setGenerating] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const d = await apiGet<{ tasks: Task[] }>(`/api/projects/${projectId}/tasks`);
        setTasks(d.tasks ?? []);
        if (d.tasks && d.tasks.length > 0) setTaskId(d.tasks[0].id);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, [projectId]);

  async function generate() {
    setGenerating(true);
    try {
      const out = await apiPost<{ markdown: string }>(`/api/projects/${projectId}/report`, {
        task_id: taskId || undefined,
      });
      setMarkdown(out.markdown);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setGenerating(false);
    }
  }

  function download() {
    if (!markdown) return;
    const blob = new Blob([markdown], { type: "text/markdown" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "report.md";
    a.click();
    URL.revokeObjectURL(url);
  }

  return (
    <PageContainer className="max-w-3xl">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>
      <ProjectNav />
      <h2 className="text-xl font-semibold mb-6">Generate report</h2>

      <Card className="mb-4 space-y-3">
        <div>
          <Label htmlFor="report-task">Task (for runner and scope context)</Label>
          <Select
            id="report-task"
            name="task_id"
            className="mt-1"
            value={taskId}
            onChange={(e) => setTaskId(e.target.value)}
          >
            {tasks.map((t) => (
              <option key={t.id} value={t.id}>{t.goal.slice(0, 60)} ({t.runner})</option>
            ))}
          </Select>
        </div>
        <div className="flex gap-2">
          <Button size="sm" onClick={generate} disabled={generating}>
            <ClipboardList className="h-4 w-4 mr-1" /> {generating ? "Generating…" : "Generate"}
          </Button>
          {markdown && (
            <Button size="sm" variant="outline" onClick={download}>
              <Download className="h-4 w-4 mr-1" /> Download .md
            </Button>
          )}
        </div>
      </Card>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      {markdown && (
        <Card>
          <pre className="text-xs whitespace-pre-wrap font-mono overflow-x-auto">{markdown}</pre>
        </Card>
      )}
    </PageContainer>
  );
}
