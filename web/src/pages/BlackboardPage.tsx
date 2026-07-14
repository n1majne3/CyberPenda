import { useEffect, useState } from "react";
import { Link, NavLink, useParams, useSearchParams } from "react-router-dom";
import {
  Activity,
  Compass,
  HeartPulse,
  Layers3,
  Network,
  Radar,
} from "lucide-react";
import type {
  BlackboardHealth,
  BlackboardWorkView,
  EntityCollection,
  GraphExplorer,
  NodeRow,
  RecordDetail,
} from "@/lib/api";
import {
  blackboardHref,
  readEntities,
  readGraphExplorer,
  readHealth,
  readRecordDetail,
  readRecordHistory,
  readRecordProvenance,
  readRecords,
  readWorkView,
  recordHref,
} from "@/lib/blackboard";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Card, CardDescription, CardTitle } from "@/components/ui";
import { cn } from "@/lib/utils";

type BlackboardTab = "work" | "entities" | "explorer" | "health" | "record";

function useBlackboardTab(): { tab: BlackboardTab; recordId?: string } {
  const params = useParams<{ projectId: string; "*": string }>();
  const splat = (params["*"] ?? "").replace(/^\/+|\/+$/g, "");
  if (!splat || splat === "work") return { tab: "work" };
  if (splat === "entities") return { tab: "entities" };
  if (splat === "explorer") return { tab: "explorer" };
  if (splat === "health") return { tab: "health" };
  if (splat.startsWith("records/")) {
    return { tab: "record", recordId: decodeURIComponent(splat.slice("records/".length)) };
  }
  return { tab: "work" };
}

export function BlackboardPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const { tab, recordId } = useBlackboardTab();
  const [searchParams] = useSearchParams();
  const nodeTypeFilter = searchParams.get("node_type") ?? undefined;

  return (
    <ProjectPageShell title="Blackboard" bodyClassName="space-y-4">
      <BlackboardSubnav projectId={projectId} active={tab === "record" ? "work" : tab} />
      {tab === "work" && <WorkPanel projectId={projectId} nodeTypeFilter={nodeTypeFilter} />}
      {tab === "entities" && <EntitiesPanel projectId={projectId} />}
      {tab === "explorer" && <ExplorerPanel projectId={projectId} />}
      {tab === "health" && <HealthPanel projectId={projectId} />}
      {tab === "record" && recordId && <RecordPanel projectId={projectId} nodeId={recordId} />}
    </ProjectPageShell>
  );
}

function BlackboardSubnav({
  projectId,
  active,
}: {
  projectId: string;
  active: Exclude<BlackboardTab, "record">;
}) {
  // Blackboard tabs per §19.1: Work / Entities / Explorer. Health is a status
  // route opened from Work (and Overview), not a peer knowledge tab.
  const links = [
    { to: blackboardHref(projectId, "work"), label: "Work", icon: Layers3, key: "work" },
    { to: blackboardHref(projectId, "entities"), label: "Entities", icon: Network, key: "entities" },
    { to: blackboardHref(projectId, "explorer"), label: "Explorer", icon: Compass, key: "explorer" },
  ] as const;

  return (
    <nav
      aria-label="Blackboard views"
      className="flex flex-wrap gap-1 border-b border-slate-300 bg-[#f0ebe1] p-2"
    >
      {links.map((link) => {
        const Icon = link.icon;
        const isActive = active === link.key;
        return (
          <NavLink
            key={link.key}
            to={link.to}
            end={link.key === "work"}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              isActive
                ? "border-slate-400 bg-white font-medium text-slate-950 shadow-sm"
                : "border-transparent text-slate-600 hover:border-slate-300 hover:bg-white/70 hover:text-slate-950",
            )}
          >
            <Icon className="size-3.5" aria-hidden="true" />
            {link.label}
          </NavLink>
        );
      })}
    </nav>
  );
}

function WorkPanel({
  projectId,
  nodeTypeFilter,
}: {
  projectId: string;
  nodeTypeFilter?: string;
}) {
  const [work, setWork] = useState<BlackboardWorkView | null>(null);
  const [filtered, setFiltered] = useState<NodeRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        if (nodeTypeFilter) {
          const envelope = await readRecords(projectId, {
            node_type: nodeTypeFilter,
            sort: "updated_desc",
            limit: 50,
          });
          if (cancelled) return;
          setFiltered(envelope.result.items ?? []);
          setWork(null);
        } else {
          const envelope = await readWorkView(projectId);
          if (cancelled) return;
          setWork(envelope.result);
          setFiltered(null);
        }
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId, nodeTypeFilter]);

  if (error) {
    return <ErrorBanner message={error} />;
  }

  if (nodeTypeFilter) {
    return (
      <section className="space-y-3 p-4" aria-label="Filtered records">
        <header className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.14em] text-slate-500">
              Filtered ledger
            </p>
            <h3 className="text-sm font-semibold text-slate-950">
              node_type={nodeTypeFilter}
            </h3>
          </div>
          <Link
            to={blackboardHref(projectId, "work")}
            className="text-sm text-slate-600 underline-offset-4 hover:underline"
          >
            Clear filter
          </Link>
        </header>
        <RecordLedger projectId={projectId} rows={filtered ?? []} empty="No matching records." />
      </section>
    );
  }

  if (!work) {
    return (
      <Card role="status" className="m-4 min-h-24 items-center justify-center text-sm text-muted-foreground">
        Loading Blackboard Work
      </Card>
    );
  }

  const recentRows = work.recent_changes.items
    .map((change) => change.node)
    .filter((row): row is NodeRow => Boolean(row));

  return (
    <div className="space-y-0">
      <StatusStrip summary={work.summary} />
      <section className="grid border-b border-slate-300 lg:grid-cols-[220px_minmax(0,1fr)]">
        <FacetRail projectId={projectId} facets={work.facets} />
        <div className="min-w-0">
          <section className="border-b border-slate-300 p-4" aria-label="Attention">
            <SectionHeading
              title="Attention"
              detail={`${work.attention.page.total_items} item(s)`}
            />
            <RecordLedger
              projectId={projectId}
              rows={work.attention.items}
              empty="No attention items."
            />
          </section>
          <section className="border-b border-slate-300 p-4" aria-label="Frontier">
            <SectionHeading
              title="Frontier"
              detail={`${work.frontier.page.total_items} open objective(s)`}
            />
            <RecordLedger
              projectId={projectId}
              rows={work.frontier.items}
              empty="No open Frontier objectives."
            />
          </section>
          <section className="p-4" aria-label="Recent changes">
            <SectionHeading
              title="Recent changes"
              detail={`${work.recent_changes.page.total_items} change(s)`}
            />
            <RecordLedger
              projectId={projectId}
              rows={recentRows}
              empty="No recent semantic changes."
            />
          </section>
        </div>
      </section>
    </div>
  );
}

function StatusStrip({ summary }: { summary: BlackboardWorkView["summary"] }) {
  const cells = [
    { label: "Revision", value: String(summary.graph_revision) },
    { label: "Health", value: summary.health.status },
    { label: "Budget", value: summary.budget.state },
    { label: "Current Truth", value: String(summary.current_truth) },
    { label: "Frontier", value: String(summary.frontier) },
    { label: "Attempts", value: String(summary.open_attempts) },
    {
      label: "Findings",
      value: `${summary.confirmed_findings} conf / ${summary.unconfirmed_findings} open`,
    },
  ];

  return (
    <section
      aria-label="Blackboard status"
      className="grid grid-cols-2 gap-px border-b border-slate-300 bg-slate-300 sm:grid-cols-4 lg:grid-cols-7"
    >
      {cells.map((cell) => (
        <div key={cell.label} className="bg-[#f7f4ed] px-3 py-2">
          <p className="font-mono text-[10px] font-semibold uppercase tracking-[0.14em] text-slate-500">
            {cell.label}
          </p>
          <p className="mt-1 text-sm font-medium tabular-nums text-slate-950">{cell.value}</p>
        </div>
      ))}
    </section>
  );
}

function FacetRail({
  projectId,
  facets,
}: {
  projectId: string;
  facets?: Record<string, unknown>;
}) {
  const nodeTypes = (facets?.node_type ?? {}) as Record<string, number>;
  const entries = Object.entries(nodeTypes).sort(([a], [b]) => a.localeCompare(b));

  return (
    <aside
      aria-label="Record facets"
      className="border-b border-slate-300 p-3 lg:border-b-0 lg:border-r"
    >
      <p className="mb-2 font-mono text-[10px] font-semibold uppercase tracking-[0.14em] text-slate-500">
        Facets
      </p>
      <ul className="space-y-1">
        <li>
          <Link
            to={blackboardHref(projectId, "work")}
            className="block rounded-md px-2 py-1.5 text-sm text-slate-700 hover:bg-white"
          >
            All records
          </Link>
        </li>
        {entries.map(([type, count]) => (
          <li key={type}>
            <Link
              to={blackboardHref(projectId, "work", { node_type: type })}
              className="flex items-center justify-between rounded-md px-2 py-1.5 text-sm text-slate-700 hover:bg-white"
            >
              <span className="truncate">{type}</span>
              <span className="font-mono text-xs tabular-nums text-slate-500">{count}</span>
            </Link>
          </li>
        ))}
      </ul>
      <div className="mt-4 space-y-1 border-t border-slate-300 pt-3">
        <Link
          to={blackboardHref(projectId, "explorer")}
          className="inline-flex items-center gap-1.5 text-sm text-slate-600 underline-offset-4 hover:underline"
        >
          <Compass className="size-3.5" aria-hidden="true" /> Open Explorer
        </Link>
        <Link
          to={blackboardHref(projectId, "health")}
          className="inline-flex items-center gap-1.5 text-sm text-slate-600 underline-offset-4 hover:underline"
        >
          <HeartPulse className="size-3.5" aria-hidden="true" /> Open Health
        </Link>
      </div>
    </aside>
  );
}

function EntitiesPanel({ projectId }: { projectId: string }) {
  const [data, setData] = useState<EntityCollection | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const envelope = await readEntities(projectId, { limit: 50 });
        if (cancelled) return;
        setData(envelope.result);
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) {
    return (
      <Card role="status" className="m-4 min-h-24 items-center justify-center text-sm text-muted-foreground">
        Loading entities
      </Card>
    );
  }

  return (
    <section className="space-y-2 p-4" aria-label="Entities">
      <SectionHeading title="Entities" detail={`${data.page.total_items} entity(ies)`} />
      <ul className="divide-y divide-slate-300 border-y border-slate-300">
        {data.items.map((item) => (
          <li key={item.entity.id}>
            <Link
              to={recordHref(projectId, item.entity.id)}
              className="flex flex-col gap-1 px-3 py-3 transition-colors hover:bg-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center sm:justify-between"
            >
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-slate-950">
                  {item.name || item.entity.label}
                </p>
                <p className="truncate font-mono text-xs text-slate-500">{item.entity.stable_key}</p>
              </div>
              <div className="flex flex-wrap gap-1">
                <Badge variant="outline">{item.kind}</Badge>
                {item.scope_status && (
                  <Badge
                    variant="outline"
                    className={
                      item.scope_status === "out_of_scope"
                        ? "border-warning/25 bg-warning/10"
                        : undefined
                    }
                  >
                    {item.scope_status.replaceAll("_", "-")}
                  </Badge>
                )}
              </div>
            </Link>
          </li>
        ))}
        {data.items.length === 0 && (
          <li className="px-3 py-6 text-sm text-slate-500">No entities recorded yet.</li>
        )}
      </ul>
    </section>
  );
}

function ExplorerPanel({ projectId }: { projectId: string }) {
  const [data, setData] = useState<GraphExplorer | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const envelope = await readGraphExplorer(projectId, { max_nodes: 200, max_edges: 500 });
        if (cancelled) return;
        setData(envelope.result);
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) {
    return (
      <Card role="status" className="m-4 min-h-24 items-center justify-center text-sm text-muted-foreground">
        Loading Graph Explorer
      </Card>
    );
  }

  const labels = data.table.nodes.map((row) => row.ref.label);

  return (
    <section className="space-y-4 p-4" aria-label="Graph Explorer">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.14em] text-slate-500">
            Secondary topology view
          </p>
          <h3 className="text-sm font-semibold text-slate-950">Graph Explorer</h3>
          <p className="mt-1 text-sm text-slate-600">
            {data.limits.node_count} node(s), {data.limits.edge_count} edge(s). Canvas labels match
            the accessible table.
          </p>
        </div>
        <Link
          to={blackboardHref(projectId, "work")}
          className="inline-flex items-center gap-1.5 text-sm text-slate-600 underline-offset-4 hover:underline"
        >
          <Radar className="size-3.5" aria-hidden="true" /> Return to Work
        </Link>
      </div>

      <div
        aria-label="Graph canvas summary"
        className="rounded-md border border-slate-300 bg-white p-3 text-sm text-slate-700"
      >
        <p className="mb-2 font-medium text-slate-950">Canvas (secondary)</p>
        <ul className="flex flex-wrap gap-2">
          {labels.map((label) => (
            <li key={label}>
              <Badge variant="outline">{label}</Badge>
            </li>
          ))}
          {labels.length === 0 && <li className="text-slate-500">No nodes in projection.</li>}
        </ul>
      </div>

      <div className="overflow-x-auto">
        <table
          aria-label="Graph Explorer table"
          className="w-full min-w-[36rem] border-collapse text-left text-sm"
        >
          <thead className="border-b border-slate-300 bg-[#f0ebe1] font-mono text-[11px] uppercase tracking-[0.12em] text-slate-500">
            <tr>
              <th className="px-3 py-2 font-semibold">Label</th>
              <th className="px-3 py-2 font-semibold">Type</th>
              <th className="px-3 py-2 font-semibold">Stable key</th>
              <th className="px-3 py-2 font-semibold">Lifecycle</th>
              <th className="px-3 py-2 font-semibold">Scope</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-300">
            {data.table.nodes.map((row) => (
              <tr key={row.ref.id} className="bg-transparent hover:bg-white/70">
                <td className="px-3 py-2">
                  <Link
                    to={recordHref(projectId, row.ref.id)}
                    className="font-medium text-slate-950 underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {row.ref.label}
                  </Link>
                </td>
                <td className="px-3 py-2 font-mono text-xs text-slate-600">{row.ref.node_type}</td>
                <td className="px-3 py-2 font-mono text-xs text-slate-600">{row.ref.stable_key}</td>
                <td className="px-3 py-2 text-slate-700">{row.lifecycle?.value ?? "—"}</td>
                <td className="px-3 py-2 text-slate-700">
                  {(row.scope_status ?? "—").replaceAll("_", "-")}
                </td>
              </tr>
            ))}
            {data.table.nodes.length === 0 && (
              <tr>
                <td colSpan={5} className="px-3 py-6 text-slate-500">
                  No explorer rows.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function HealthPanel({ projectId }: { projectId: string }) {
  const [data, setData] = useState<BlackboardHealth | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const envelope = await readHealth(projectId);
        if (cancelled) return;
        setData(envelope.result);
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) {
    return (
      <Card role="status" className="m-4 min-h-24 items-center justify-center text-sm text-muted-foreground">
        Loading Health
      </Card>
    );
  }

  return (
    <section className="space-y-3 p-4" aria-label="Health">
      <SectionHeading title="Health" detail={`overall ${data.overall}`} />
      <Card className="border-slate-300 bg-white/50">
        <CardTitle className="flex items-center gap-2 text-sm">
          <Activity className="size-4" aria-hidden="true" /> Graph revision{" "}
          {data.current_graph.revision}
        </CardTitle>
        <CardDescription className="mt-1 font-mono text-xs">
          state {data.current_graph.state_hash.slice(0, 12)}…
        </CardDescription>
        <p className="mt-3 text-sm text-slate-700">
          {data.latest_run
            ? `Latest run ${data.latest_run.run_id} · ${data.latest_run.overall}`
            : "No Health run has completed yet for this Project."}
        </p>
      </Card>
    </section>
  );
}

function RecordPanel({ projectId, nodeId }: { projectId: string; nodeId: string }) {
  const [detail, setDetail] = useState<RecordDetail | null>(null);
  const [history, setHistory] = useState<
    | {
        versions: {
          version: number;
          disposition: string;
          properties: Record<string, unknown>;
          updated_at: string;
        }[];
        merge?: { id: string; stable_key: string; label: string } | null;
      }
    | null
  >(null);
  const [provenance, setProvenance] = useState<Record<string, unknown> | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [detailEnv, historyEnv, provenanceEnv] = await Promise.all([
          readRecordDetail(projectId, nodeId),
          readRecordHistory(projectId, nodeId),
          readRecordProvenance(projectId, nodeId),
        ]);
        if (cancelled) return;
        setDetail(detailEnv.result);
        setHistory({
          versions: historyEnv.result.versions ?? [],
          merge: historyEnv.result.merge
            ? {
                id: historyEnv.result.merge.id,
                stable_key: historyEnv.result.merge.stable_key,
                label: historyEnv.result.merge.label,
              }
            : null,
        });
        setProvenance(provenanceEnv.result as Record<string, unknown>);
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId, nodeId]);

  if (error) return <ErrorBanner message={error} />;
  if (!detail) {
    return (
      <Card role="status" className="m-4 min-h-24 items-center justify-center text-sm text-muted-foreground">
        Loading record
      </Card>
    );
  }

  const props = detail.node.properties ?? {};

  return (
    <section className="space-y-4 p-4" aria-label="Record inspector">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.14em] text-slate-500">
            {detail.node.node_type}
          </p>
          <h3 className="text-base font-semibold text-slate-950">
            {String(props.title ?? props.summary ?? props.label ?? detail.node.stable_key)}
          </h3>
          <p className="mt-1 font-mono text-xs text-slate-600">{detail.node.stable_key}</p>
        </div>
        <div className="flex flex-wrap gap-1">
          <Badge variant="outline">v{detail.node.version}</Badge>
          <Badge variant="outline">{detail.node.disposition}</Badge>
        </div>
      </div>

      <section aria-label="Properties">
        <SectionHeading title="Properties" />
        <dl className="grid gap-2 sm:grid-cols-2">
          {Object.entries(props).map(([key, value]) => (
            <div key={key} className="rounded-md border border-slate-300 bg-white/40 px-3 py-2">
              <dt className="font-mono text-[10px] uppercase tracking-[0.12em] text-slate-500">
                {key}
              </dt>
              <dd className="mt-1 break-words text-sm text-slate-900">
                {typeof value === "string" || typeof value === "number" || typeof value === "boolean"
                  ? String(value)
                  : JSON.stringify(value)}
              </dd>
            </div>
          ))}
        </dl>
      </section>

      <section aria-label="Relationships">
        <SectionHeading title="Relationships" />
        <div className="grid gap-2 text-sm sm:grid-cols-2">
          <p>
            Incoming: {detail.relationships?.incoming.total_items ?? 0}
          </p>
          <p>
            Outgoing: {detail.relationships?.outgoing.total_items ?? 0}
          </p>
          <p>
            Evidence: {detail.evidence?.total_items ?? 0}
          </p>
          <p>
            Supporting: {detail.support?.supporting?.total_items ?? 0}
          </p>
        </div>
      </section>

      <section aria-label="Provenance">
        <SectionHeading title="Provenance" />
        {provenance ? (
          <pre className="max-h-48 overflow-auto rounded-md border border-slate-300 bg-white/40 p-3 font-mono text-xs whitespace-pre-wrap">
            {JSON.stringify(provenance, null, 2)}
          </pre>
        ) : (
          <p className="text-sm text-slate-500">No provenance available.</p>
        )}
      </section>

      <section aria-label="Version history">
        <SectionHeading
          title="Version history"
          detail={history ? `${history.versions.length} version(s)` : undefined}
        />
        {history?.merge && (
          <p className="mb-2 text-sm text-slate-600">
            Merged into{" "}
            <Link
              to={recordHref(projectId, history.merge.id)}
              className="font-mono underline-offset-4 hover:underline"
            >
              {history.merge.stable_key}
            </Link>
          </p>
        )}
        <ul className="divide-y divide-slate-300 border-y border-slate-300">
          {(history?.versions ?? []).map((version) => (
            <li key={version.version} className="px-3 py-2 text-sm">
              <span className="font-mono text-xs text-slate-500">v{version.version}</span>{" "}
              <span className="text-slate-700">{version.disposition}</span>{" "}
              <span className="text-slate-500">{version.updated_at}</span>
            </li>
          ))}
          {(history?.versions.length ?? 0) === 0 && (
            <li className="px-3 py-4 text-sm text-slate-500">No version history.</li>
          )}
        </ul>
      </section>

      <div className="flex flex-wrap gap-3 text-sm">
        <Link
          to={blackboardHref(projectId, "work")}
          className="text-slate-600 underline-offset-4 hover:underline"
        >
          Back to Work
        </Link>
        <Link
          to={blackboardHref(projectId, "explorer")}
          className="text-slate-600 underline-offset-4 hover:underline"
        >
          Open in Explorer
        </Link>
        <Link
          to={blackboardHref(projectId, "health")}
          className="inline-flex items-center gap-1 text-slate-600 underline-offset-4 hover:underline"
        >
          <HeartPulse className="size-3.5" aria-hidden="true" /> Health
        </Link>
      </div>
    </section>
  );
}

function RecordLedger({
  projectId,
  rows,
  empty,
}: {
  projectId: string;
  rows: NodeRow[];
  empty: string;
}) {
  if (rows.length === 0) {
    return <p className="py-4 text-sm text-slate-500">{empty}</p>;
  }

  return (
    <ul className="divide-y divide-slate-300 border-y border-slate-300" role="list">
      {rows.map((row) => (
        <li key={row.ref.id}>
          <Link
            to={recordHref(projectId, row.ref.id)}
            className="flex w-full flex-col gap-1 border-b border-slate-300 bg-transparent p-4 text-left transition-colors hover:bg-white/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center sm:justify-between"
          >
            <div className="min-w-0">
              <p className="truncate text-sm font-medium text-slate-950">{row.ref.label}</p>
              <p className="truncate font-mono text-xs text-slate-500">
                {row.secondary || row.ref.stable_key}
              </p>
            </div>
            <div className="flex flex-wrap gap-1">
              <Badge variant="outline">{row.ref.node_type}</Badge>
              {row.lifecycle?.value && <Badge variant="outline">{row.lifecycle.value}</Badge>}
              {row.severity && <Badge variant="outline">{row.severity}</Badge>}
              {row.scope_status && (
                <Badge
                  variant="outline"
                  className={
                    row.scope_status === "out_of_scope"
                      ? "border-warning/25 bg-warning/10"
                      : undefined
                  }
                >
                  {row.scope_status.replaceAll("_", "-")}
                </Badge>
              )}
              {row.scope_status === "out_of_scope" && (
                <Badge variant="outline" className="border-warning/25 bg-warning/10">
                  non-actionable
                </Badge>
              )}
            </div>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function SectionHeading({ title, detail }: { title: string; detail?: string }) {
  return (
    <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
      <h3 className="text-sm font-semibold tracking-tight text-slate-950">{title}</h3>
      {detail && <p className="font-mono text-[11px] text-slate-500">{detail}</p>}
    </div>
  );
}

function ErrorBanner({ message }: { message: string }) {
  return (
    <Card role="alert" className="m-4 border-destructive/25">
      <CardTitle className="text-sm">Couldn't load Blackboard</CardTitle>
      <CardDescription className="mt-1">{message}</CardDescription>
    </Card>
  );
}
