import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { FolderLock } from "lucide-react";
import type { NodeRow } from "@/lib/api";
import { readRecords, recordHref } from "@/lib/blackboard";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Card, CardHeader, CardTitle } from "@/components/ui";

/**
 * Focused Evidence view over RecordCollectionV1. Bookmark-compatible with the
 * legacy /evidence route; does not call frozen-table fallbacks.
 */
export function EvidencePage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const [rows, setRows] = useState<NodeRow[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const envelope = await readRecords(projectId, {
          node_type: "evidence_artifact",
          sort: "updated_desc",
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

  return (
    <ProjectPageShell title="Evidence" bodyClassName="space-y-4">
      {error && <p className="text-sm text-destructive">{error}</p>}

      <ul className="divide-y divide-slate-300 border-y border-slate-300" role="list">
        {rows.map((row) => (
          <li key={row.ref.id}>
            <Link
              to={recordHref(projectId, row.ref.id)}
              className="flex w-full flex-col gap-2 border-b border-slate-300 bg-transparent p-4 text-left transition-colors hover:bg-white/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center"
            >
              <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-primary/5 text-primary">
                <FolderLock className="h-4 w-4" aria-hidden="true" />
              </div>
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-slate-950">{row.ref.label}</p>
                <p className="truncate font-mono text-xs text-slate-500">
                  {row.secondary || row.ref.stable_key}
                </p>
              </div>
              <div className="flex max-w-full flex-wrap gap-1 sm:justify-end">
                {row.lifecycle?.value && <Badge variant="outline">{row.lifecycle.value}</Badge>}
                <Badge variant="outline">{row.ref.node_type}</Badge>
              </div>
            </Link>
          </li>
        ))}
      </ul>

      {rows.length === 0 && !error && (
        <Card as="section" variant="flat" className="border-dashed bg-muted/30 text-sm text-muted-foreground">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <FolderLock className="h-4 w-4" /> No evidence attached.
            </CardTitle>
          </CardHeader>
          Runtime workdir files require explicit attach or retain.
        </Card>
      )}
    </ProjectPageShell>
  );
}
