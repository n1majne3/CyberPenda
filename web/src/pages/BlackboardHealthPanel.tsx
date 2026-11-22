import { Link } from "react-router-dom";
import { attentionLabel, recordHref, type HealthAnomaly, type SemanticHealth } from "@/lib/blackboardv2";
import { Badge } from "@/components/ui";
import { SectionHeading } from "./BlackboardPanelChrome";

export function HealthPanel({
  projectId,
  health,
}: {
  projectId: string;
  health: SemanticHealth;
}) {
  const anomalies = health.anomalies;
  const tokens = health.attention.estimated_tokens;

  return (
    <section aria-label="Semantic health">
    <details
      open={health.status !== "healthy" || anomalies.length > 0 || !health.attention.complete}
      className="min-w-0 space-y-3 border-b border-border p-4"
      data-testid="blackboard-health"
    >
      <summary className="cursor-pointer text-sm text-muted-foreground">Semantic health · {health.status} · {anomalies.length} anomalies</summary>
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <SectionHeading
            title="Semantic health"
            detail={`${health.status} · ${tokens.toLocaleString()} tokens`}
          />
          <p className="mt-1 text-sm text-muted-foreground">
            {health.attention.complete
              ? "Complete Snapshot remains launchable."
              : "Canonical Runtime Snapshot is unavailable; diagnostic attention remains launchable."}
          </p>
        </div>
        <div className="flex flex-wrap gap-1">
          <Badge
            variant="outline"
            className={
              health.status === "critical"
                ? "border-destructive/30 bg-destructive/10"
                : health.status === "warning"
                  ? "border-warning/25 bg-warning/10"
                  : undefined
            }
          >
            {health.status}
          </Badge>
          <Badge variant="outline">{attentionLabel(health.attention.state)}</Badge>
        </div>
      </div>

      {anomalies.length === 0 ? (
        <p className="text-sm text-muted-foreground">No actionable anomalies.</p>
      ) : (
        <ul className="divide-y divide-border border-y border-border" role="list" aria-label="Health anomalies">
          {anomalies.map((anomaly) => (
            <HealthAnomalyRow key={`${anomaly.code}:${anomaly.subject_key ?? ""}:${anomaly.message}`} projectId={projectId} anomaly={anomaly} />
          ))}
        </ul>
      )}
    </details>
    </section>
  );
}

export function HealthAnomalyRow({
  projectId,
  anomaly,
}: {
  projectId: string;
  anomaly: HealthAnomaly;
}) {
  const linkKeys = anomalyLinkKeys(anomaly);
  return (
    <li className="flex min-w-0 flex-col gap-1 p-3 sm:flex-row sm:items-start sm:justify-between">
      <div className="min-w-0 space-y-1">
        <p className="text-sm font-medium text-foreground">{anomaly.message}</p>
        <p className="font-mono text-xs text-muted-foreground break-all">
          {anomaly.code}
          {anomaly.subject_key ? ` · ${anomaly.subject_key}` : ""}
        </p>
        {linkKeys.length > 0 && (
          <div className="flex min-w-0 flex-col gap-1">
            {linkKeys.map((key) => (
              <Link
                key={key}
                to={recordHref(projectId, key)}
                className="inline-flex min-w-0 max-w-full text-sm text-muted-foreground underline-offset-4 hover:underline break-all"
              >
                Open {key}
              </Link>
            ))}
          </div>
        )}
      </div>
      <Badge
        variant="outline"
        className={
          anomaly.severity === "critical"
            ? "border-destructive/30 bg-destructive/10"
            : anomaly.severity === "warning"
              ? "border-warning/25 bg-warning/10"
              : undefined
        }
      >
        {anomaly.severity}
      </Badge>
    </li>
  );
}

/** Prefer subject_key, then related_keys, so dangling subjects still surface surviving links. */
function anomalyLinkKeys(anomaly: HealthAnomaly): string[] {
  const keys: string[] = [];
  if (anomaly.subject_key) keys.push(anomaly.subject_key);
  for (const key of anomaly.related_keys ?? []) {
    if (!keys.includes(key)) keys.push(key);
  }
  return keys;
}
