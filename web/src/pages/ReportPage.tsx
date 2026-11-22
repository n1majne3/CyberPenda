import { ProjectReportProtocol } from "./FGSReport";
import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Download } from "lucide-react";
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
import { downloadMarkdown } from "@/lib/download";

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
export function ReportPage() { return <ProjectReportProtocol legacy={<LegacyReportPage />} />; }

function LegacyReportPage() {
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
    downloadMarkdown(markdown, "report.md");
  }

  return (
    <ProjectPageShell
      title="Report"
      description="Confirmed Findings and Facts are separate from unconfirmed Findings and tentative Facts."
      actions={markdown && <Button size="sm" variant="outline" onClick={download}><Download className="h-4 w-4" /> Download .md</Button>}
      bodyClassName="space-y-6"
    >
      {loading && <LoadingState label="Generating report" minHeight="min-h-20" />}
      {error && <ErrorState error={error} title="Couldn't generate report" />}

      {markdown ? (
        <Card as="section" className="mx-auto max-w-4xl gap-6 p-6 sm:p-10">
          <CardHeader>
            <CardTitle>Report preview</CardTitle>
          </CardHeader>
          <pre className="whitespace-pre-wrap break-words font-sans text-sm leading-7">
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
      {report && (
        <details className="rounded-lg border border-border">
          <summary className="cursor-pointer px-5 py-4 text-sm font-medium">Supporting records</summary>
          <Card as="section" className="space-y-6 rounded-none border-0 border-t border-border">
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
        </details>
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
