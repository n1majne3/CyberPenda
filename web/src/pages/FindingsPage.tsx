import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import type { NodeRow } from "@/lib/api";
import { readRecords, recordHref } from "@/lib/blackboard";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Card } from "@/components/ui";

/**
 * Focused Finding view over RecordCollectionV1. Bookmark-compatible with the
 * legacy /findings route; does not call frozen-table fallbacks.
 */
export function FindingsPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const [rows, setRows] = useState<NodeRow[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const envelope = await readRecords(projectId, {
          node_type: "finding",
          sort: "severity",
          limit: 100,
        });
        if (cancelled) return;
        setRows(envelope.result.items ?? []);
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const confirmed = rows.filter((row) => row.lifecycle?.value === "confirmed");
  const other = rows.filter((row) => row.lifecycle?.value !== "confirmed");

  return (
    <ProjectPageShell title="Findings" bodyClassName="space-y-4">
      {error && <p className="text-sm text-destructive">{error}</p>}
      <FindingSection projectId={projectId} title="Confirmed" items={confirmed} />
      <FindingSection projectId={projectId} title="Unconfirmed" items={other} muted />
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
  items: NodeRow[];
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
          <li key={row.ref.stable_key || row.ref.id}>
            <Link
              to={recordHref(projectId, row.ref.stable_key)}
              className="flex w-full flex-col gap-1 border-b border-slate-300 bg-transparent p-4 text-left transition-colors hover:bg-white/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center sm:justify-between"
            >
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-slate-950">{row.ref.label}</p>
                <p className="truncate font-mono text-xs text-slate-500">
                  {row.secondary || row.ref.stable_key}
                </p>
              </div>
              <div className="flex flex-wrap gap-1">
                {row.severity && <Badge variant="outline">{row.severity}</Badge>}
                {row.lifecycle?.value && <Badge variant="outline">{row.lifecycle.value}</Badge>}
                {row.scope_status === "out_of_scope" && (
                  <Badge variant="outline" className="border-warning/25 bg-warning/10">
                    out-of-scope
                  </Badge>
                )}
              </div>
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}
