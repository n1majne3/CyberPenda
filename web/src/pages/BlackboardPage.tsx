import { useEffect, useMemo, useRef, useState } from "react";
import { Link, NavLink, useParams } from "react-router-dom";
import { Compass, History, Layers3, Library, Radar } from "lucide-react";
import { apiGet, type Project } from "@/lib/api";
import {
  blackboardHref,
  buildGraphExplorer,
  formatBlackboardV2Error,
  knowledgeGroupsForProjectKind,
  listSnapshotEntries,
  missingEvidenceEntries,
  primaryLabelForDetail,
  readCurrentDetail,
  readSemanticHistory,
  readSnapshot,
  recordHref,
  type CurrentDetail,
  type HistoryItem,
  type KnowledgeGroup,
  type RuntimeSnapshot,
  type SnapshotListEntry,
} from "@/lib/blackboardv2";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Card, CardDescription, CardTitle } from "@/components/ui";
import { cn } from "@/lib/utils";

type BlackboardTab = "work" | "knowledge" | "explorer" | "record";

function useBlackboardTab(): { tab: BlackboardTab; recordKey?: string } {
  const params = useParams<{ projectId: string; "*": string }>();
  const splat = (params["*"] ?? "").replace(/^\/+|\/+$/g, "");
  if (!splat || splat === "work") return { tab: "work" };
  if (splat === "knowledge") return { tab: "knowledge" };
  if (splat === "explorer") return { tab: "explorer" };
  if (splat.startsWith("records/")) {
    return { tab: "record", recordKey: decodeURIComponent(splat.slice("records/".length)) };
  }
  return { tab: "work" };
}

export function BlackboardPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const { tab, recordKey } = useBlackboardTab();

  return (
    <ProjectPageShell title="Blackboard" bodyClassName="min-w-0 space-y-4" className="min-w-0">
      <div data-testid="blackboard-page" className="min-w-0 w-full space-y-4">
        <BlackboardSubnav projectId={projectId} active={tab === "record" ? "work" : tab} />
        {tab === "work" && <BoardPanel projectId={projectId} focus="work" />}
        {tab === "knowledge" && <BoardPanel projectId={projectId} focus="knowledge" />}
        {tab === "explorer" && <ExplorerPanel projectId={projectId} />}
        {tab === "record" && recordKey && (
          <RecordPanel
            key={`${projectId}:${recordKey}`}
            projectId={projectId}
            recordKey={recordKey}
          />
        )}
      </div>
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
  const links = [
    { to: blackboardHref(projectId, "work"), label: "Work", icon: Layers3, key: "work" as const },
    {
      to: blackboardHref(projectId, "knowledge"),
      label: "Knowledge",
      icon: Library,
      key: "knowledge" as const,
    },
    {
      to: blackboardHref(projectId, "explorer"),
      label: "Explorer",
      icon: Compass,
      key: "explorer" as const,
    },
  ];

  return (
    <nav
      aria-label="Blackboard views"
      className="flex flex-wrap gap-1 border-b border-border bg-muted/40 p-2"
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
                ? "border-border bg-background font-medium text-foreground shadow-sm"
                : "border-transparent text-muted-foreground hover:border-border hover:bg-background/70 hover:text-foreground",
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

function useSnapshotAndProject(projectId: string) {
  const [snapshot, setSnapshot] = useState<RuntimeSnapshot | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const [snap, proj] = await Promise.all([
          readSnapshot(projectId),
          apiGet<Project>(`/api/projects/${projectId}`),
        ]);
        if (cancelled) return;
        setSnapshot(snap);
        setProject(proj);
        setError(null);
      } catch (e) {
        if (!cancelled) setError(formatBlackboardV2Error(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  return { snapshot, project, error, loading };
}

function BoardPanel({
  projectId,
  focus,
}: {
  projectId: string;
  focus: "work" | "knowledge";
}) {
  const { snapshot, project, error, loading } = useSnapshotAndProject(projectId);

  if (error) return <ErrorBanner message={error} />;
  if (loading || !snapshot) {
    return (
      <Card role="status" className="m-4 min-h-24 items-center justify-center text-sm text-muted-foreground">
        Loading Blackboard
      </Card>
    );
  }

  const kind = project?.kind ?? "pentest";
  const entries = listSnapshotEntries(snapshot, kind);
  const workEntries = entries.filter((e) => e.section === "work");
  const knowledgeEntries = entries.filter((e) => e.section === "knowledge");
  const missingEvidence = missingEvidenceEntries(snapshot);
  const groups = knowledgeGroupsForProjectKind(kind);

  return (
    <div className="min-w-0 space-y-0">
      <StatusStrip revision={snapshot.revision} kind={kind} entries={entries} />
      {focus === "work" && (
        <>
          <WorkSection projectId={projectId} entries={workEntries} />
          <KnowledgeSection
            projectId={projectId}
            entries={knowledgeEntries}
            groups={groups}
            compact
          />
        </>
      )}
      {focus === "knowledge" && (
        <KnowledgeSection projectId={projectId} entries={knowledgeEntries} groups={groups} />
      )}
      {missingEvidence.length > 0 && (
        <section className="min-w-0 border-t border-border p-4" aria-label="Missing Evidence">
          <SectionHeading title="Missing Evidence" detail={`${missingEvidence.length}`} />
          <RecordList projectId={projectId} rows={missingEvidence} empty="" />
        </section>
      )}
    </div>
  );
}

function StatusStrip({
  revision,
  kind,
  entries,
}: {
  revision: number;
  kind: string;
  entries: SnapshotListEntry[];
}) {
  const openWork = entries.filter((e) => e.section === "work").length;
  const knowledge = entries.filter((e) => e.section === "knowledge").length;
  const cells = [
    { label: "Revision", value: String(revision) },
    { label: "Kind", value: kind === "ctf_challenge" ? "CTF" : "Pentest" },
    { label: "Current Work", value: String(openWork) },
    { label: "Knowledge", value: String(knowledge) },
  ];

  return (
    <section
      aria-label="Blackboard status"
      className="grid grid-cols-2 gap-px border-b border-border bg-border sm:grid-cols-4"
    >
      {cells.map((cell) => (
        <div key={cell.label} className="bg-muted/30 px-3 py-2">
          <p className="font-mono text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            {cell.label}
          </p>
          <p className="mt-1 text-sm font-medium tabular-nums text-foreground">
            {cell.label === "Revision" ? (
              <span>
                revision {cell.value}
              </span>
            ) : (
              cell.value
            )}
          </p>
        </div>
      ))}
    </section>
  );
}

function WorkSection({
  projectId,
  entries,
}: {
  projectId: string;
  entries: SnapshotListEntry[];
}) {
  const objectives = entries.filter((e) => e.group === "objectives");
  const attempts = entries.filter((e) => e.group === "attempts");

  return (
    <section className="min-w-0 border-b border-border p-4" aria-label="Current Work">
      <SectionHeading title="Current Work" detail={`${entries.length} item(s)`} />
      {entries.length === 0 ? (
        <p className="py-4 text-sm text-muted-foreground">No open Objectives or Attempts.</p>
      ) : (
        <div className="space-y-4">
          <GroupBlock
            projectId={projectId}
            title="Objectives"
            rows={objectives}
            empty="No open Objectives."
          />
          <GroupBlock
            projectId={projectId}
            title="Attempts"
            rows={attempts}
            empty="No open Attempts."
          />
        </div>
      )}
    </section>
  );
}

function KnowledgeSection({
  projectId,
  entries,
  groups,
  compact = false,
}: {
  projectId: string;
  entries: SnapshotListEntry[];
  groups: KnowledgeGroup[];
  compact?: boolean;
}) {
  const titles: Record<KnowledgeGroup, string> = {
    entities: "Entities",
    facts: "Facts",
    findings: "Findings",
    solutions: "Solutions",
    evidence: "Evidence",
  };

  return (
    <section
      className={cn("min-w-0 p-4", !compact && "border-b border-border")}
      aria-label="Project Knowledge"
    >
      <SectionHeading title="Project Knowledge" detail={`${entries.length} item(s)`} />
      {entries.length === 0 ? (
        <p className="py-4 text-sm text-muted-foreground">No Project Knowledge records.</p>
      ) : (
        <div className="space-y-4">
          {groups.map((group) => {
            const rows = entries.filter((e) => e.group === group);
            if (compact && rows.length === 0) return null;
            return (
              <GroupBlock
                key={group}
                projectId={projectId}
                title={titles[group]}
                rows={rows}
                empty={`No ${titles[group].toLowerCase()}.`}
              />
            );
          })}
        </div>
      )}
    </section>
  );
}

function GroupBlock({
  projectId,
  title,
  rows,
  empty,
}: {
  projectId: string;
  title: string;
  rows: SnapshotListEntry[];
  empty: string;
}) {
  return (
    <section aria-label={title} className="space-y-1">
      <h4 className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
        {title}
      </h4>
      <RecordList projectId={projectId} rows={rows} empty={empty} />
    </section>
  );
}

function RecordList({
  projectId,
  rows,
  empty,
}: {
  projectId: string;
  rows: SnapshotListEntry[];
  empty: string;
}) {
  if (rows.length === 0) {
    return empty ? <p className="py-2 text-sm text-muted-foreground">{empty}</p> : null;
  }

  return (
    <ul className="divide-y divide-border border-y border-border" role="list">
      {rows.map((row) => (
        <li key={row.key}>
          <Link
            to={recordHref(projectId, row.key)}
            className="flex w-full flex-col gap-1 bg-transparent p-3 text-left transition-colors hover:bg-background/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center sm:justify-between"
          >
            <div className="min-w-0">
              <p className="truncate text-sm font-medium text-foreground">{row.primary}</p>
              <p className="truncate font-mono text-xs text-muted-foreground">
                {row.key}
                {row.secondary ? ` · ${row.secondary}` : ""}
              </p>
            </div>
            <div className="flex flex-wrap gap-1">
              <Badge variant="outline">v{row.version}</Badge>
              {row.badges.map((badge) => (
                <Badge
                  key={badge}
                  variant="outline"
                  className={
                    badge === "out-of-scope" || badge === "missing"
                      ? "border-warning/25 bg-warning/10"
                      : undefined
                  }
                >
                  {badge}
                </Badge>
              ))}
            </div>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function ExplorerPanel({ projectId }: { projectId: string }) {
  const { snapshot, error, loading } = useSnapshotAndProject(projectId);
  const graph = useMemo(
    () => (snapshot ? buildGraphExplorer(snapshot) : { nodes: [], edges: [] }),
    [snapshot],
  );

  if (error) return <ErrorBanner message={error} />;
  if (loading || !snapshot) {
    return (
      <Card role="status" className="m-4 min-h-24 items-center justify-center text-sm text-muted-foreground">
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
              <tr key={node.key} className="hover:bg-background/70">
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
              <tr key={`${edge.from}-${edge.relation}-${edge.to}-${index}`} className="hover:bg-background/70">
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

function RecordPanel({ projectId, recordKey }: { projectId: string; recordKey: string }) {
  const [detail, setDetail] = useState<CurrentDetail | null>(null);
  const [snapshotRevision, setSnapshotRevision] = useState<number | null>(null);
  const [snapshotError, setSnapshotError] = useState<string | null>(null);
  const [historyItems, setHistoryItems] = useState<HistoryItem[] | null>(null);
  const [historyCursor, setHistoryCursor] = useState<string | undefined>(undefined);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  // Bumped on unmount so in-flight history for a previous mount cannot apply.
  const mountedGenRef = useRef(0);

  useEffect(() => {
    const gen = ++mountedGenRef.current;
    let cancelled = false;
    (async () => {
      try {
        const current = await readCurrentDetail(projectId, recordKey);
        if (cancelled || gen !== mountedGenRef.current) return;

        let revision: number | null = null;
        let snapErr: string | null = null;
        try {
          const snapshot = await readSnapshot(projectId);
          if (cancelled || gen !== mountedGenRef.current) return;
          revision = snapshot.revision;
        } catch (e) {
          if (cancelled || gen !== mountedGenRef.current) return;
          // Surface snapshot failure as degraded state; do not swallow silently.
          snapErr = formatBlackboardV2Error(e);
        }

        setDetail(current);
        setSnapshotRevision(revision);
        setSnapshotError(snapErr);
        setError(null);
      } catch (e) {
        if (!cancelled && gen === mountedGenRef.current) {
          setError(formatBlackboardV2Error(e));
        }
      }
    })();
    return () => {
      cancelled = true;
      // Invalidate any in-flight history tied to this mount.
      mountedGenRef.current += 1;
    };
  }, [projectId, recordKey]);

  async function loadHistory(cursor?: string) {
    const gen = mountedGenRef.current;
    setHistoryLoading(true);
    setHistoryError(null);
    try {
      const page = await readSemanticHistory(projectId, recordKey, {
        limit: 20,
        cursor,
      });
      if (gen !== mountedGenRef.current) return;
      setHistoryItems((prev) => (cursor && prev ? [...prev, ...page.items] : page.items));
      setHistoryCursor(page.next_cursor);
    } catch (e) {
      if (gen !== mountedGenRef.current) return;
      setHistoryError(formatBlackboardV2Error(e));
    } finally {
      if (gen === mountedGenRef.current) setHistoryLoading(false);
    }
  }

  if (error) return <ErrorBanner message={error} />;
  if (!detail) {
    return (
      <Card role="status" className="m-4 min-h-24 items-center justify-center text-sm text-muted-foreground">
        Loading record
      </Card>
    );
  }

  const stale =
    snapshotRevision !== null && detail.revision !== snapshotRevision
      ? { detail: detail.revision, snapshot: snapshotRevision }
      : null;

  const fieldEntries = Object.entries(detail.record).filter(
    ([, value]) => value !== undefined && value !== null && value !== "",
  );

  return (
    <section className="min-w-0 space-y-4 p-4" aria-label="Record detail">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            {detail.type}
          </p>
          <h3 className="break-words text-base font-semibold text-foreground">
            {primaryLabelForDetail(detail)}
          </h3>
          <p className="mt-1 break-all font-mono text-xs text-muted-foreground">{detail.key}</p>
        </div>
        <div className="flex flex-wrap gap-1">
          <Badge variant="outline">v{detail.version}</Badge>
          <Badge variant="outline">revision {detail.revision}</Badge>
        </div>
      </div>

      {snapshotError && (
        <p
          role="status"
          aria-label="snapshot unavailable"
          className="rounded-md border border-destructive/25 bg-destructive/5 px-3 py-2 text-sm text-foreground"
        >
          Snapshot unavailable (degraded): {snapshotError}
        </p>
      )}

      {stale && (
        <p
          role="status"
          aria-label="stale revision"
          className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-foreground"
        >
          Stale revision: detail revision {stale.detail}; snapshot revision {stale.snapshot}.
        </p>
      )}

      <section aria-label="Semantic fields">
        <SectionHeading title="Fields" />
        <dl className="grid gap-2 sm:grid-cols-2">
          {fieldEntries.map(([key, value]) => (
            <div key={key} className="rounded-md border border-border bg-background/40 px-3 py-2">
              <dt className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                {key}
              </dt>
              <dd className="mt-1 break-words text-sm text-foreground">
                {typeof value === "boolean" ? String(value) : String(value)}
              </dd>
            </div>
          ))}
        </dl>
      </section>

      <section aria-label="Relationships">
        <SectionHeading title="Relationships" detail={`${detail.relationships.length}`} />
        {detail.relationships.length === 0 ? (
          <p className="text-sm text-muted-foreground">No current relationships.</p>
        ) : (
          <ul className="divide-y divide-border border-y border-border text-sm">
            {detail.relationships.map((edge, index) => (
              <li key={`${edge.from}-${edge.relation}-${edge.to}-${index}`} className="px-3 py-2">
                <span className="font-mono text-xs">
                  <Link to={recordHref(projectId, edge.from)} className="underline-offset-4 hover:underline">
                    {edge.from}
                  </Link>
                  {" — "}
                  {edge.relation}
                  {" → "}
                  <Link to={recordHref(projectId, edge.to)} className="underline-offset-4 hover:underline">
                    {edge.to}
                  </Link>
                </span>
                {edge.reason && (
                  <p className="mt-0.5 text-muted-foreground">{edge.reason}</p>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={() => void loadHistory()}
            disabled={historyLoading}
            className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-2.5 py-1.5 text-sm font-medium shadow-sm transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
          >
            <History className="size-3.5" aria-hidden="true" />
            Semantic History
          </button>
        </div>
        {historyError && (
          <p role="alert" className="text-sm text-destructive">
            {historyError}
          </p>
        )}
        {historyItems && (
          <section aria-label="Semantic History" className="space-y-2">
            <SectionHeading title="Semantic History" detail={`${historyItems.length} item(s)`} />
            <ul className="divide-y divide-border border-y border-border">
              {historyItems.map((item, index) => (
                <li key={`${item.kind}-${item.version}-${index}`} className="px-3 py-2 text-sm">
                  <span className="font-mono text-xs text-muted-foreground">
                    v{item.version}
                    {item.type ? ` · ${item.type}` : ""}
                  </span>
                  {item.record && (
                    <p className="mt-0.5 text-foreground">
                      {item.record.title ??
                        item.record.name ??
                        item.record.summary ??
                        item.record.objective ??
                        item.kind}
                    </p>
                  )}
                  {item.relation && (
                    <p className="font-mono text-xs text-muted-foreground">
                      {item.from} — {item.relation} → {item.to}
                      {item.reason ? ` · ${item.reason}` : ""}
                    </p>
                  )}
                </li>
              ))}
              {historyItems.length === 0 && (
                <li className="px-3 py-4 text-sm text-muted-foreground">No history items.</li>
              )}
            </ul>
            {historyCursor && (
              <button
                type="button"
                onClick={() => void loadHistory(historyCursor)}
                disabled={historyLoading}
                className="rounded-md border border-border bg-background px-2.5 py-1.5 text-sm shadow-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
              >
                Load more
              </button>
            )}
          </section>
        )}
      </section>

      <div className="flex flex-wrap gap-3 text-sm">
        <Link
          to={blackboardHref(projectId, "work")}
          className="text-muted-foreground underline-offset-4 hover:underline"
        >
          Back to Work
        </Link>
        <Link
          to={blackboardHref(projectId, "explorer")}
          className="text-muted-foreground underline-offset-4 hover:underline"
        >
          Open in Explorer
        </Link>
      </div>
    </section>
  );
}

function SectionHeading({ title, detail }: { title: string; detail?: string }) {
  return (
    <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
      <h3 className="text-sm font-semibold tracking-tight text-foreground">{title}</h3>
      {detail && <p className="font-mono text-[11px] text-muted-foreground">{detail}</p>}
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
