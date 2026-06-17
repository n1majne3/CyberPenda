import { useEffect, useState } from "react";
import { useParams, Link } from "react-router-dom";
import { ArrowLeft, AlertTriangle, FlaskConical, History, ChevronDown, ChevronRight } from "lucide-react";
import { apiGet, type Finding, type FindingVersion } from "@/lib/api";
import { Card, CardTitle, CardHeader, Badge } from "@/components/ui";

export function FindingsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [findings, setFindings] = useState<Finding[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const d = await apiGet<{ findings: Finding[] }>(`/api/projects/${projectId}/findings`);
        setFindings(d.findings ?? []);
        setError(null);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, [projectId]);

  const confirmed = findings.filter((f) => f.status === "confirmed");
  const unconfirmed = findings.filter((f) => f.status !== "confirmed");

  return (
    <div className="p-8 max-w-4xl">
      <Link to={`/projects/${projectId}`} className="inline-flex items-center text-sm text-muted-foreground hover:text-foreground mb-4">
        <ArrowLeft className="h-4 w-4 mr-1" /> Back to dashboard
      </Link>
      <h2 className="text-xl font-semibold mb-6">Findings</h2>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      <Section title="Confirmed" projectId={projectId!} items={confirmed} />
      <Section title="Unconfirmed" projectId={projectId!} items={unconfirmed} muted />
      {findings.length === 0 && !error && <p className="text-sm text-muted-foreground">No findings recorded yet.</p>}
    </div>
  );
}

function Section({
  title,
  projectId,
  items,
  muted = false,
}: {
  title: string;
  projectId: string;
  items: Finding[];
  muted?: boolean;
}) {
  return (
    <div className="mb-6">
      <h3 className={`text-sm font-medium mb-2 ${muted ? "text-muted-foreground" : ""}`}>{title} ({items.length})</h3>
      <div className="space-y-2">
        {items.map((f) => (
          <FindingCard key={f.id} projectId={projectId} finding={f} />
        ))}
      </div>
    </div>
  );
}

function FindingCard({ projectId, finding }: { projectId: string; finding: Finding }) {
  const [open, setOpen] = useState(false);
  const [versions, setVersions] = useState<FindingVersion[] | null>(null);

  useEffect(() => {
    if (!open) {
      setVersions(null);
      return;
    }
    let cancelled = false;
    apiGet<{ versions: FindingVersion[] }>(`/api/projects/${projectId}/findings/${encodeURIComponent(finding.finding_key)}/versions`)
      .then((d) => !cancelled && setVersions(d.versions ?? []))
      .catch(() => !cancelled && setVersions([]));
    return () => {
      cancelled = true;
    };
  }, [open, projectId, finding.finding_key]);

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2">
          <FlaskConical className="h-4 w-4" /> {finding.title}
        </CardTitle>
        <div className="flex gap-1">
          <SeverityBadge severity={finding.severity} />
          {finding.cvss_pending && (
            <Badge variant="warning"><AlertTriangle className="h-3 w-3 mr-1" />CVSS pending</Badge>
          )}
        </div>
      </CardHeader>
      <div className="text-xs text-muted-foreground space-y-1">
        {finding.target && <p><span className="text-foreground">Target:</span> {finding.target}</p>}
        {finding.cvss_vector && <p><span className="text-foreground">CVSS {finding.cvss_version}:</span> <code>{finding.cvss_vector}</code></p>}
        {finding.impact && <p><span className="text-foreground">Impact:</span> {finding.impact}</p>}
        {finding.recommendation && <p><span className="text-foreground">Recommendation:</span> {finding.recommendation}</p>}
      </div>

      {/* Versions — historical revisions of this finding key. */}
      <button
        className="mt-3 flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        onClick={() => setOpen((v) => !v)}
      >
        {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
        <History className="h-3.5 w-3.5" />
        Versions {versions ? `(${versions.length})` : ""}
      </button>
      {open && (
        <div className="mt-2">
          {versions === null ? (
            <p className="text-xs text-muted-foreground">Loading…</p>
          ) : versions.length === 0 ? (
            <p className="text-xs text-muted-foreground">No versions.</p>
          ) : (
            <ol className="space-y-1">
              {[...versions].reverse().map((v) => (
                <li key={v.id} className="text-xs flex flex-wrap items-center gap-2">
                  <code className="text-muted-foreground">v{v.version}</code>
                  <SeverityBadge severity={v.severity} />
                  <Badge variant={v.status === "confirmed" ? "success" : "outline"}>{v.status}</Badge>
                  {v.cvss_vector && <code className="text-muted-foreground">{v.cvss_vector}</code>}
                  <span className="text-muted-foreground">{v.title}</span>
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
    </Card>
  );
}

function SeverityBadge({ severity }: { severity: string }) {
  const variant =
    severity === "critical" ? "destructive" :
    severity === "high" ? "warning" :
    severity === "medium" ? "primary" :
    "outline";
  return <Badge variant={variant}>{severity}</Badge>;
}
