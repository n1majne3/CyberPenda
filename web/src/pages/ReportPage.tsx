import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { ClipboardList, Download } from "lucide-react";
import { apiGet, apiPost, type Task } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, Card, CardHeader, CardTitle, Label, Select } from "@/components/ui";

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
    <ProjectPageShell title="Generate report" bodyClassName="space-y-6">
      <Card as="section" className="space-y-3">
        <CardHeader>
          <CardTitle>Report source</CardTitle>
        </CardHeader>
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
        <div className="flex flex-wrap gap-2">
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

      {error && <p className="text-sm text-destructive">{error}</p>}

      {markdown ? (
        <Card as="section" className="overflow-hidden">
          <CardHeader>
            <CardTitle>Report preview</CardTitle>
          </CardHeader>
          <pre className="max-h-[32rem] overflow-auto rounded-md border border-border bg-muted/30 p-3 text-xs font-mono whitespace-pre-wrap">{markdown}</pre>
        </Card>
      ) : (
        <Card as="section" variant="flat" className="border-dashed bg-muted/30 text-sm text-muted-foreground">
          No report generated yet.
        </Card>
      )}
    </ProjectPageShell>
  );
}
