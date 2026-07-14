import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { ClipboardList, Download } from "lucide-react";
import { readPentestReport } from "@/lib/blackboard";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, Card, CardHeader, CardTitle } from "@/components/ui";

/**
 * Deterministic Pentest report surface over GET .../reports/pentest.
 * Keeps the legacy /report bookmark; does not POST the frozen-table fallback.
 */
export function ReportPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const [markdown, setMarkdown] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const envelope = await readPentestReport(projectId, {
          format: "markdown",
          scope_context: "current",
        });
        if (cancelled) return;
        setMarkdown(envelope.result.markdown);
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

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
    <ProjectPageShell title="Generate report" bodyClassName="space-y-4">
        <Card as="section" className="space-y-3">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <ClipboardList className="h-4 w-4" aria-hidden="true" />
              Deterministic Pentest report
            </CardTitle>
          </CardHeader>
          <p className="text-sm text-muted-foreground">
            Rendered from the current graph revision through PentestReportV1.
          </p>
          <div className="flex flex-wrap gap-2">
            {markdown && (
              <Button size="sm" variant="outline" onClick={download}>
                <Download className="mr-1 h-4 w-4" /> Download .md
              </Button>
            )}
          </div>
        </Card>

        {loading && (
          <Card role="status" className="min-h-20 items-center justify-center text-sm text-muted-foreground">
            Generating report
          </Card>
        )}
        {error && <p className="p-4 text-sm text-destructive">{error}</p>}

        {markdown ? (
          <Card as="section" className="overflow-hidden">
            <CardHeader>
              <CardTitle>Report preview</CardTitle>
            </CardHeader>
            <pre className="max-h-[32rem] overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/30 p-3 font-mono text-xs">
              {markdown}
            </pre>
          </Card>
        ) : (
          !loading &&
          !error && (
            <Card as="section" variant="flat" className="border-dashed bg-muted/30 text-sm text-muted-foreground">
              No report generated yet.
            </Card>
          )
        )}
    </ProjectPageShell>
  );
}
