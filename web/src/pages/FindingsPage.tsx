import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { AlertTriangle, FlaskConical, History, ChevronDown, ChevronRight, GitMerge } from "lucide-react";
import { apiGet, apiPost, type Finding, type FindingVersion } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { BackLink, PageContainer } from "@/components/shared";
import { Card, CardTitle, CardHeader, Badge, Button, Select } from "@/components/ui";
import { formatDateTime } from "@/lib/format";

export function FindingsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [findings, setFindings] = useState<Finding[]>([]);
  const [error, setError] = useState<string | null>(null);

  async function loadFindings() {
    try {
      const d = await apiGet<{ findings: Finding[] }>(`/api/projects/${projectId}/findings`);
      setFindings(d.findings ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    // Initial load on mount/project. loadFindings() is reused by event handlers.
    loadFindings();
  }, [projectId]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  const confirmed = findings.filter((f) => f.status === "confirmed");
  const unconfirmed = findings.filter((f) => f.status !== "confirmed");
  const allFindingKeys = findings.map((f) => f.finding_key);
  const base = `/api/projects/${projectId}`;

  return (
    <PageContainer className="max-w-4xl">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>
      <ProjectNav />
      <h2 className="text-xl font-semibold mb-6">Findings</h2>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      <Section title="Confirmed" base={base} items={confirmed} allFindingKeys={allFindingKeys} onMerged={loadFindings} />
      <Section title="Unconfirmed" base={base} items={unconfirmed} allFindingKeys={allFindingKeys} onMerged={loadFindings} muted />
      {findings.length === 0 && !error && <p className="text-sm text-muted-foreground">No findings recorded yet.</p>}
    </PageContainer>
  );
}

function Section({
  title,
  base,
  items,
  allFindingKeys,
  onMerged,
  muted = false,
}: {
  title: string;
  base: string;
  items: Finding[];
  allFindingKeys: string[];
  onMerged: () => void;
  muted?: boolean;
}) {
  return (
    <div className="mb-6">
      <h3 className={`text-sm font-medium mb-2 ${muted ? "text-muted-foreground" : ""}`}>{title} ({items.length})</h3>
      <div className="space-y-2">
        {items.map((f) => (
          <FindingCard
            key={f.id}
            base={base}
            finding={f}
            allFindingKeys={allFindingKeys.filter((k) => k !== f.finding_key)}
            onMerged={onMerged}
          />
        ))}
      </div>
    </div>
  );
}

function FindingCard({
  base,
  finding,
  allFindingKeys,
  onMerged,
}: {
  base: string;
  finding: Finding;
  allFindingKeys: string[];
  onMerged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [versions, setVersions] = useState<FindingVersion[] | null>(null);
  const [mergeOpen, setMergeOpen] = useState(false);
  const [mergeTarget, setMergeTarget] = useState("");
  const [mergeError, setMergeError] = useState<string | null>(null);
  const [mergeBusy, setMergeBusy] = useState(false);

  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    if (!open) {
      // Reset stale versions synchronously when the card collapses.
      setVersions(null);
      return;
    }
    let cancelled = false;
    apiGet<{ versions: FindingVersion[] }>(`${base}/findings/${encodeURIComponent(finding.finding_key)}/versions`)
      .then((d) => !cancelled && setVersions(d.versions ?? []))
      .catch(() => !cancelled && setVersions([]));
    return () => {
      cancelled = true;
    };
  }, [open, base, finding.finding_key]);
  /* eslint-enable react-hooks/set-state-in-effect */

  async function confirmMerge() {
    if (!mergeTarget) return;
    setMergeBusy(true);
    setMergeError(null);
    try {
      await apiPost(`${base}/findings/merge`, {
        source_finding_key: finding.finding_key,
        canonical_finding_key: mergeTarget,
      });
      setMergeOpen(false);
      setMergeTarget("");
      onMerged();
    } catch (e) {
      setMergeError((e as Error).message);
    } finally {
      setMergeBusy(false);
    }
  }

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
        <p><span className="text-foreground">Key:</span> <code>{finding.finding_key}</code></p>
        {finding.target && <p><span className="text-foreground">Target:</span> {finding.target}</p>}
        {finding.cvss_vector && <p><span className="text-foreground">CVSS {finding.cvss_version}:</span> <code>{finding.cvss_vector}</code></p>}
        {finding.impact && <p><span className="text-foreground">Impact:</span> {finding.impact}</p>}
        {finding.recommendation && <p><span className="text-foreground">Recommendation:</span> {finding.recommendation}</p>}
        {finding.updated_at && <p><span className="text-foreground">Updated:</span> {formatDateTime(finding.updated_at)}</p>}
      </div>

      {/* Versions — historical revisions of this finding key. */}
      <button
        type="button"
        aria-expanded={open}
        className="mt-3 flex items-center gap-1 rounded-md text-xs text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
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
                  {v.created_at && <span className="text-muted-foreground">{formatDateTime(v.created_at)}</span>}
                </li>
              ))}
            </ol>
          )}
        </div>
      )}

      {allFindingKeys.length > 0 && (
        <div className="mt-3 border-t border-border pt-3">
          {!mergeOpen ? (
            <Button size="sm" variant="outline" onClick={() => setMergeOpen(true)}>
              <GitMerge className="h-3.5 w-3.5 mr-1" /> Merge into…
            </Button>
          ) : (
            <div className="space-y-2">
              <p className="text-xs text-muted-foreground">
                Merge <code>{finding.finding_key}</code> into the canonical finding. The old key becomes an alias; history is preserved.
              </p>
              <Select
                aria-label="Canonical finding key"
                name="canonical_finding_key"
                className="max-w-md text-xs"
                value={mergeTarget}
                onChange={(e) => setMergeTarget(e.target.value)}
              >
                <option value="">Choose canonical finding key</option>
                {allFindingKeys.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </Select>
              {mergeError && <p className="text-xs text-destructive">{mergeError}</p>}
              <div className="flex gap-2">
                <Button size="sm" onClick={confirmMerge} disabled={!mergeTarget || mergeBusy}>
                  Confirm merge
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    setMergeOpen(false);
                    setMergeTarget("");
                    setMergeError(null);
                  }}
                >
                  Cancel
                </Button>
              </div>
            </div>
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
