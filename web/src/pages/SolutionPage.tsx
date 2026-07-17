import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Flag, Download } from "lucide-react";
import {
  formatBlackboardV2Error,
  readCTFSolution,
  readCTFSolutionMarkdown,
  recordHref,
  type CTFSolutionProjection,
  type ReportFactDTO,
  type ReportSolutionDTO,
} from "@/lib/blackboardv2";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Button, Card, CardHeader, CardTitle } from "@/components/ui";

/**
 * CTF Solution deliverable over GET /api/v2/.../reports/ctf-solution.
 * Solved state comes only from current verified flag Solutions. Structured v2
 * JSON supplies Blackboard Keys for detail/history navigation.
 */
export function SolutionPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const [solution, setSolution] = useState<CTFSolutionProjection | null>(null);
  const [markdown, setMarkdown] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const [json, md] = await Promise.all([
          readCTFSolution(projectId),
          readCTFSolutionMarkdown(projectId),
        ]);
        if (cancelled) return;
        setSolution(json);
        setMarkdown(md.markdown);
        setError(null);
      } catch (e) {
        if (!cancelled) setError(formatBlackboardV2Error(e));
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
          Deterministic CTF solution from current verified flag Solutions. Solved
          reverses when no verified flags remain. Keys link to current detail/history.
        </p>
        {markdown && (
          <Button size="sm" variant="outline" onClick={download}>
            <Download className="mr-1 h-4 w-4" /> Download .md
          </Button>
        )}
      </Card>

      {loading && (
        <Card
          role="status"
          className="min-h-20 items-center justify-center text-sm text-muted-foreground"
        >
          Loading solution
        </Card>
      )}
      {error && <p className="p-4 text-sm text-destructive">{error}</p>}

      {solution && (
        <Card as="section" className="space-y-4">
          <CardHeader>
            <CardTitle>
              {solution.project.name} — {solution.solved ? "Solved: yes" : "Solved: no"}
            </CardTitle>
          </CardHeader>
          <SolutionSection
            projectId={projectId}
            title="Verified Flags"
            items={solution.verified_flags}
          />
          <SolutionSection
            projectId={projectId}
            title="Candidate Flags"
            items={solution.candidate_flags}
            muted
          />
          <SolutionSection projectId={projectId} title="Answers" items={solution.answers} />
          <SolutionSection
            projectId={projectId}
            title="Procedures"
            items={solution.procedures}
          />
          <FactSection
            projectId={projectId}
            title="Confirmed Facts"
            items={solution.confirmed_facts}
          />
          <FactSection
            projectId={projectId}
            title="Tentative Facts"
            items={solution.tentative_facts}
            muted
          />
          <section className="space-y-2">
            <h3 className="text-sm font-medium tracking-tight">
              Evidence ({solution.evidence.length})
            </h3>
            {solution.evidence.length === 0 ? (
              <p className="text-sm text-muted-foreground">_No records._</p>
            ) : (
              <ul className="divide-y divide-slate-300 border-y border-slate-300" role="list">
                {solution.evidence.map((item) => (
                  <li key={item.key} className="p-3">
                    <Link
                      to={recordHref(projectId, item.key)}
                      className="text-sm font-medium underline-offset-2 hover:underline"
                    >
                      {item.summary}
                    </Link>
                    <p className="font-mono text-xs text-slate-500">{item.key}</p>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </Card>
      )}

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

function SolutionSection({
  projectId,
  title,
  items,
  muted = false,
}: {
  projectId: string;
  title: string;
  items: ReportSolutionDTO[];
  muted?: boolean;
}) {
  return (
    <section className="space-y-2">
      <h3
        className={`text-sm font-medium tracking-tight ${muted ? "text-muted-foreground" : ""}`}
      >
        {title} ({items.length})
      </h3>
      {items.length === 0 ? (
        <p className="text-sm text-muted-foreground">_No records._</p>
      ) : (
        <ul className="divide-y divide-slate-300 border-y border-slate-300" role="list">
          {items.map((item) => (
            <li key={item.key} className="space-y-1 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <Link
                  to={recordHref(projectId, item.key)}
                  className="text-sm font-medium text-slate-950 underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {item.summary}
                </Link>
                <div className="flex flex-wrap gap-1">
                  <Badge variant="outline">{item.status}</Badge>
                  <Badge variant="outline">{item.kind}</Badge>
                </div>
              </div>
              <p className="font-mono text-xs text-slate-500">{item.key}</p>
              {item.value && (
                <p className="font-mono text-xs text-muted-foreground">{item.value}</p>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function FactSection({
  projectId,
  title,
  items,
  muted = false,
}: {
  projectId: string;
  title: string;
  items: ReportFactDTO[];
  muted?: boolean;
}) {
  return (
    <section className="space-y-2">
      <h3
        className={`text-sm font-medium tracking-tight ${muted ? "text-muted-foreground" : ""}`}
      >
        {title} ({items.length})
      </h3>
      {items.length === 0 ? (
        <p className="text-sm text-muted-foreground">_No records._</p>
      ) : (
        <ul className="divide-y divide-slate-300 border-y border-slate-300" role="list">
          {items.map((fact) => (
            <li key={fact.key} className="space-y-1 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <Link
                  to={recordHref(projectId, fact.key)}
                  className="text-sm font-medium text-slate-950 underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {fact.summary}
                </Link>
                <div className="flex flex-wrap gap-1">
                  <Badge variant="outline">{fact.category}</Badge>
                  <Badge variant="outline">{fact.confidence}</Badge>
                </div>
              </div>
              <p className="font-mono text-xs text-slate-500">{fact.key}</p>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
