import { useEffect, useState } from "react";
import { useParams, Link } from "react-router-dom";
import { ArrowLeft, AlertTriangle, FlaskConical } from "lucide-react";
import { apiGet, type Finding } from "@/lib/api";
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

      <Section title="Confirmed" items={confirmed} />
      <Section title="Unconfirmed" items={unconfirmed} muted />
      {findings.length === 0 && !error && <p className="text-sm text-muted-foreground">No findings recorded yet.</p>}
    </div>
  );
}

function Section({ title, items, muted = false }: { title: string; items: Finding[]; muted?: boolean }) {
  return (
    <div className="mb-6">
      <h3 className={`text-sm font-medium mb-2 ${muted ? "text-muted-foreground" : ""}`}>{title} ({items.length})</h3>
      <div className="space-y-2">
        {items.map((f) => (
          <Card key={f.id}>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle className="flex items-center gap-2">
                <FlaskConical className="h-4 w-4" /> {f.title}
              </CardTitle>
              <div className="flex gap-1">
                <SeverityBadge severity={f.severity} />
                {f.cvss_pending && (
                  <Badge variant="warning"><AlertTriangle className="h-3 w-3 mr-1" />CVSS pending</Badge>
                )}
              </div>
            </CardHeader>
            <div className="text-xs text-muted-foreground space-y-1">
              {f.target && <p><span className="text-foreground">Target:</span> {f.target}</p>}
              {f.cvss_vector && <p><span className="text-foreground">CVSS {f.cvss_version}:</span> <code>{f.cvss_vector}</code></p>}
              {f.impact && <p><span className="text-foreground">Impact:</span> {f.impact}</p>}
              {f.recommendation && <p><span className="text-foreground">Recommendation:</span> {f.recommendation}</p>}
            </div>
          </Card>
        ))}
      </div>
    </div>
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
