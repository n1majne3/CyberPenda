import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ClipboardList, Download } from "lucide-react";
import {
  formatBlackboardV2Error,
  readPentestReport,
  readPentestReportMarkdown,
  recordHref,
  type PentestReportProjection,
  type ReportFactDTO,
  type ReportFindingDTO,
} from "@/lib/blackboardv2";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Button, Card, CardHeader, CardTitle } from "@/components/ui";
import { ErrorState, LoadingState, SectionHeading } from "@/components/shared";

const SEVERITY_VARIANT: Record<string, "destructive" | "warning" | "info" | "outline"> = {
  critical: "destructive",
  high: "destructive",
  medium: "warning",
  low: "info",
  info: "info",
};

function severityVariant(severity: string): "destructive" | "warning" | "info" | "outline" {
  return SEVERITY_VARIANT[severity?.toLowerCase()] ?? "outline";
}

/**
 * Deterministic Pentest report surface over GET /api/v2/.../reports/pentest.
 * Structured v2 JSON supplies Blackboard Keys for detail/history navigation;
 * markdown remains the downloadable deliverable.
 */
export function ReportPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const [report, setReport] = useState<PentestReportProjection | null>(null);
  const [markdown, setMarkdown] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const [json, md] = await Promise.all([
          readPentestReport(projectId),
          readPentestReportMarkdown(projectId),
        ]);
        if (cancelled) return;
        setReport(json);
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
          Rendered from current Blackboard v2 conclusions: confirmed Findings and Facts
          are listed separately from unconfirmed Findings and tentative Facts. Each key
          links to current detail/history.
        </p>
        <div className="flex flex-wrap gap-2">
          {markdown && (
            <Button size="sm" variant="outline" onClick={download}>
              <Download className="mr-1 h-4 w-4" /> Download .md
            </Button>
          )}
        </div>
      </Card>

      {loading && <LoadingState label="Generating report" minHeight="min-h-20" />}
      {error && <ErrorState error={error} title="Couldn't generate report" />}

      {report && (
        <Card as="section" className="space-y-4">
          <CardHeader>
            <CardTitle>{report.project.name} — structured report</CardTitle>
          </CardHeader>
          {report.project.description && (
            <p className="text-sm text-muted-foreground">{report.project.description}</p>
          )}
          <FindingSection
            projectId={projectId}
            title="Confirmed Findings"
            items={report.confirmed_findings}
          />
          <FindingSection
            projectId={projectId}
            title="Unconfirmed Findings"
            items={report.unconfirmed_findings}
            muted
          />
          <FactSection
            projectId={projectId}
            title="Confirmed Facts"
            items={report.confirmed_facts}
          />
          <FactSection
            projectId={projectId}
            title="Tentative Facts"
            items={report.tentative_facts}
            muted
          />
        </Card>
      )}

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
            <p className="px-3 py-2">No report generated yet.</p>
          </Card>
        )
      )}
    </ProjectPageShell>
  );
}

function FindingSection({
  projectId,
  title,
  items,
  muted = false,
}: {
  projectId: string;
  title: string;
  items: ReportFindingDTO[];
  muted?: boolean;
}) {
  return (
    <section className="space-y-2">
      <SectionHeading count={items.length} muted={muted}>{title}</SectionHeading>
      {items.length === 0 ? (
        <p className="text-sm text-muted-foreground">No records.</p>
      ) : (
        <ul className="divide-y divide-border border-y border-border" role="list">
          {items.map((finding) => (
            <li key={finding.key} className="space-y-1 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <Link
                  to={recordHref(projectId, finding.key)}
                  className="text-sm font-medium text-foreground underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {finding.title}
                </Link>
                <div className="flex flex-wrap gap-1">
                  {finding.severity && <Badge variant={severityVariant(finding.severity)}>{finding.severity}</Badge>}
                  <Badge variant="outline">{finding.status}</Badge>
                </div>
              </div>
              <p className="font-mono text-xs text-muted-foreground">{finding.key}</p>
              {finding.supporting_facts.length > 0 && (
                <ul className="mt-1 space-y-0.5 text-xs text-muted-foreground">
                  {finding.supporting_facts.map((fact) => (
                    <li key={fact.key}>
                      Supporting Fact:{" "}
                      <Link
                        to={recordHref(projectId, fact.key)}
                        className="underline-offset-2 hover:underline"
                      >
                        {fact.summary}
                      </Link>{" "}
                      <span className="font-mono">({fact.key})</span>
                    </li>
                  ))}
                </ul>
              )}
              {finding.contradictions.length > 0 && (
                <ul className="mt-1 space-y-0.5 text-xs text-muted-foreground">
                  {finding.contradictions.map((fact) => (
                    <li key={fact.key}>
                      Contradicting Fact:{" "}
                      <Link
                        to={recordHref(projectId, fact.key)}
                        className="underline-offset-2 hover:underline"
                      >
                        {fact.summary}
                      </Link>{" "}
                      <span className="font-mono">({fact.key})</span>
                    </li>
                  ))}
                </ul>
              )}
              {finding.evidence.length > 0 && (
                <ul className="mt-1 space-y-0.5 text-xs text-muted-foreground">
                  {finding.evidence.map((item) => (
                    <li key={item.key}>
                      Evidence:{" "}
                      <Link
                        to={recordHref(projectId, item.key)}
                        className="font-mono underline-offset-2 hover:underline"
                      >
                        {item.key}
                      </Link>{" "}
                      — {item.summary}
                    </li>
                  ))}
                </ul>
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
      <SectionHeading count={items.length} muted={muted}>{title}</SectionHeading>
      {items.length === 0 ? (
        <p className="text-sm text-muted-foreground">No records.</p>
      ) : (
        <ul className="divide-y divide-border border-y border-border" role="list">
          {items.map((fact) => (
            <li key={fact.key} className="space-y-1 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <Link
                  to={recordHref(projectId, fact.key)}
                  className="text-sm font-medium text-foreground underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {fact.summary}
                </Link>
                <div className="flex flex-wrap gap-1">
                  <Badge variant="outline">{fact.category}</Badge>
                  <Badge variant="outline">{fact.confidence}</Badge>
                </div>
              </div>
              <p className="font-mono text-xs text-muted-foreground">{fact.key}</p>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
