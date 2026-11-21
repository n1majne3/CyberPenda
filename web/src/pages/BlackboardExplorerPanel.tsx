import { useMemo } from "react";
import { Link } from "react-router-dom";
import { Loader2, Radar } from "lucide-react";
import { blackboardHref, buildGraphExplorer, recordHref } from "@/lib/blackboardv2";
import { Badge, Card } from "@/components/ui";
import { useSnapshotAndProject } from "./useSnapshotAndProject";
import { ErrorBanner } from "./BlackboardPanelChrome";

export function ExplorerPanel({ projectId }: { projectId: string }) {
  const { snapshot, error, loading } = useSnapshotAndProject(projectId);
  const graph = useMemo(
    () => (snapshot ? buildGraphExplorer(snapshot) : { nodes: [], edges: [] }),
    [snapshot],
  );

  if (error) return <ErrorBanner message={error} />;
  if (loading || !snapshot) {
    return (
      <Card role="status" aria-label="Loading Graph Explorer" className="m-4 min-h-24 items-center justify-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        Loading Graph Explorer
      </Card>
    );
  }

  return (
    <section className="min-w-0 space-y-4 p-4" aria-label="Graph Explorer">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold text-foreground">Graph Explorer</h3>
          <p className="mt-1 font-mono text-xs text-muted-foreground">
            {graph.nodes.length} record(s) · {graph.edges.length} relationship(s) · revision{" "}
            {snapshot.revision}
          </p>
        </div>
        <Link
          to={blackboardHref(projectId, "work")}
          className="inline-flex items-center gap-1.5 text-sm text-muted-foreground underline-offset-4 hover:underline"
        >
          <Radar className="size-3.5" aria-hidden="true" /> Return to Work
        </Link>
      </div>

      <div
        aria-label="Graph canvas summary"
        className="min-w-0 rounded-md border border-border bg-background/50 p-3 text-sm"
      >
        <ul className="flex flex-wrap gap-2">
          {graph.nodes.map((node) => (
            <li key={node.key} className="min-w-0">
              <Link to={recordHref(projectId, node.key)}>
                <Badge variant="outline" className="max-w-full truncate font-mono text-xs">
                  {node.key}
                </Badge>
              </Link>
            </li>
          ))}
          {graph.nodes.length === 0 && (
            <li className="text-muted-foreground">No current records.</li>
          )}
        </ul>
      </div>

      <div className="min-w-0 overflow-x-auto">
        <table
          aria-label="Graph Explorer records"
          className="w-full min-w-[36rem] border-collapse text-left text-sm"
        >
          <thead className="border-b border-border bg-muted/40 font-mono text-[11px] uppercase tracking-[0.12em] text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-semibold">Key</th>
              <th className="px-3 py-2 font-semibold">Type</th>
              <th className="px-3 py-2 font-semibold">Label</th>
              <th className="px-3 py-2 font-semibold">Status</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {graph.nodes.map((node) => (
              <tr key={node.key} className="hover:bg-accent">
                <td className="px-3 py-2 font-mono text-xs">
                  <Link
                    to={recordHref(projectId, node.key)}
                    className="underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {node.key}
                  </Link>
                </td>
                <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{node.type}</td>
                <td className="px-3 py-2 text-foreground">{node.label}</td>
                <td className="px-3 py-2 text-muted-foreground">{node.status ?? "—"}</td>
              </tr>
            ))}
            {graph.nodes.length === 0 && (
              <tr>
                <td colSpan={4} className="px-3 py-6 text-muted-foreground">
                  No explorer rows.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="min-w-0 overflow-x-auto">
        <table
          aria-label="Graph Explorer relationships"
          className="w-full min-w-[36rem] border-collapse text-left text-sm"
        >
          <thead className="border-b border-border bg-muted/40 font-mono text-[11px] uppercase tracking-[0.12em] text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-semibold">From</th>
              <th className="px-3 py-2 font-semibold">Relation</th>
              <th className="px-3 py-2 font-semibold">To</th>
              <th className="px-3 py-2 font-semibold">Reason</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {graph.edges.map((edge, index) => (
              <tr key={`${edge.from}-${edge.relation}-${edge.to}-${index}`} className="hover:bg-accent">
                <td className="px-3 py-2 font-mono text-xs">
                  <Link
                    to={recordHref(projectId, edge.from)}
                    className="underline-offset-4 hover:underline"
                  >
                    {edge.from}
                  </Link>
                </td>
                <td className="px-3 py-2 font-mono text-xs text-foreground">{edge.relation}</td>
                <td className="px-3 py-2 font-mono text-xs">
                  <Link
                    to={recordHref(projectId, edge.to)}
                    className="underline-offset-4 hover:underline"
                  >
                    {edge.to}
                  </Link>
                </td>
                <td className="px-3 py-2 text-muted-foreground">{edge.reason ?? "—"}</td>
              </tr>
            ))}
            {graph.edges.length === 0 && (
              <tr>
                <td colSpan={4} className="px-3 py-6 text-muted-foreground">
                  No relationships.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}
