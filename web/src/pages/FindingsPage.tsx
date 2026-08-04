import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  formatBlackboardV2Error,
  listFindingEntries,
  readSnapshot,
  recordHref,
  type SnapshotListEntry,
} from "@/lib/blackboardv2";
import { ShieldAlert } from "lucide-react";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge } from "@/components/ui";
import { ErrorState, RichEmptyState, SectionHeading } from "@/components/shared";

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
 * Focused Finding view over the current Blackboard v2 Snapshot.
 * Bookmark-compatible with /findings; detail loads by Blackboard Key.
 * Grouping is presentation-only and preserves each identity/severity.
 */
export function FindingsPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const [rows, setRows] = useState<SnapshotListEntry[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const snapshot = await readSnapshot(projectId);
        if (cancelled) return;
        setRows(listFindingEntries(snapshot));
        setError(null);
      } catch (e) {
        if (!cancelled) setError(formatBlackboardV2Error(e));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const confirmed = rows.filter((row) => row.status === "confirmed");
  const unconfirmed = rows.filter((row) => row.status !== "confirmed");

  return (
    <ProjectPageShell title="Findings" bodyClassName="space-y-4">
      {error && <ErrorState error={error} title="Couldn't load findings" />}
      {rows.length === 0 && !error && (
        <RichEmptyState
          icon={<ShieldAlert className="h-6 w-6" aria-hidden="true" />}
          title="No findings recorded yet"
          description="Reportable issues from the Blackboard will appear here once confirmed."
        />
      )}
      {rows.length > 0 && !error && (
        <>
          <FindingSection projectId={projectId} title="Confirmed" items={confirmed} />
          <FindingSection projectId={projectId} title="Unconfirmed" items={unconfirmed} muted />
        </>
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
  items: SnapshotListEntry[];
  muted?: boolean;
}) {
  return (
    <section className="space-y-2 p-4">
      <SectionHeading count={items.length} muted={muted}>{title}</SectionHeading>
      {items.length === 0 ? (
        <p className="py-2 text-sm text-muted-foreground">No records.</p>
      ) : (
        <ul className="divide-y divide-border border-y border-border" role="list">
          {items.map((row) => (
            <li key={row.key}>
              <Link
                to={recordHref(projectId, row.key)}
                className="flex w-full flex-col gap-1 border-b border-border bg-transparent p-4 text-left transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium text-foreground">{row.primary}</p>
                  <p className="truncate font-mono text-xs text-muted-foreground">
                    {row.secondary || row.key}
                  </p>
                </div>
                <div className="flex flex-wrap gap-1">
                  {typeof row.fields.severity === "string" && row.fields.severity && (
                    <Badge variant={severityVariant(row.fields.severity)}>{row.fields.severity}</Badge>
                  )}
                  {row.status && <Badge variant="outline">{row.status}</Badge>}
                  {row.fields.cvss_pending === true && (
                    <Badge variant="outline">cvss-pending</Badge>
                  )}
                </div>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
