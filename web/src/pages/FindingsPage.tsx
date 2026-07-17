import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  formatBlackboardV2Error,
  listFindingEntries,
  readSnapshot,
  recordHref,
  type SnapshotListEntry,
} from "@/lib/blackboardv2";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Card } from "@/components/ui";

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
      {error && <p className="text-sm text-destructive">{error}</p>}
      <FindingSection projectId={projectId} title="Confirmed" items={confirmed} />
      <FindingSection projectId={projectId} title="Unconfirmed" items={unconfirmed} muted />
      {rows.length === 0 && !error && (
        <Card as="section" variant="flat" className="border-dashed bg-muted/30 text-sm text-muted-foreground">
          No findings recorded yet.
        </Card>
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
      <h3
        className={`text-sm font-medium tracking-tight ${muted ? "text-muted-foreground" : ""}`}
      >
        {title} ({items.length})
      </h3>
      <ul className="divide-y divide-slate-300 border-y border-slate-300" role="list">
        {items.map((row) => (
          <li key={row.key}>
            <Link
              to={recordHref(projectId, row.key)}
              className="flex w-full flex-col gap-1 border-b border-slate-300 bg-transparent p-4 text-left transition-colors hover:bg-white/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center sm:justify-between"
            >
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-slate-950">{row.primary}</p>
                <p className="truncate font-mono text-xs text-slate-500">
                  {row.secondary || row.key}
                </p>
              </div>
              <div className="flex flex-wrap gap-1">
                {typeof row.fields.severity === "string" && row.fields.severity && (
                  <Badge variant="outline">{row.fields.severity}</Badge>
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
    </section>
  );
}
