import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Flag, Download } from "lucide-react";
import { readCTFSolution } from "@/lib/blackboard";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, Card, CardHeader, CardTitle } from "@/components/ui";

/**
 * CTF Solution deliverable over GET .../reports/ctf-solution.
 * Only mounted for CTF Projects in navigation; the route itself is bookmarkable.
 */
export function SolutionPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const [markdown, setMarkdown] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const envelope = await readCTFSolution(projectId, { format: "markdown" });
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
    a.download = "solution.md";
    a.click();
    URL.revokeObjectURL(url);
  }

  return (
    <ProjectPageShell title="Solution" bodyClassName="space-y-4">
        <Card as="section" className="space-y-3">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Flag className="h-4 w-4" aria-hidden="true" />
              CTF Solution
            </CardTitle>
          </CardHeader>
          <p className="text-sm text-muted-foreground">
            Deterministic CTFSolutionV1 for this challenge Project.
          </p>
          {markdown && (
            <Button size="sm" variant="outline" onClick={download}>
              <Download className="mr-1 h-4 w-4" /> Download .md
            </Button>
          )}
        </Card>

        {loading && (
          <Card role="status" className="min-h-20 items-center justify-center text-sm text-muted-foreground">
            Loading solution
          </Card>
        )}
        {error && <p className="p-4 text-sm text-destructive">{error}</p>}
        {markdown && (
          <Card as="section" className="overflow-hidden">
            <CardHeader>
              <CardTitle>Solution preview</CardTitle>
            </CardHeader>
            <pre className="max-h-[32rem] overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/30 p-3 font-mono text-xs">
              {markdown}
            </pre>
          </Card>
        )}
    </ProjectPageShell>
  );
}
