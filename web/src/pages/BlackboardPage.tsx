import { useEffect, useState } from "react";
import { Link, NavLink, useParams } from "react-router-dom";
import { Compass, Layers3, Library, Loader2 } from "lucide-react";
import { apiGet, type Project } from "@/lib/api";
import { attentionLabel, blackboardHref, knowledgeGroupsForProjectKind, listSnapshotEntries, missingEvidenceEntries, recordHref, type KnowledgeGroup, type SemanticHealth, type SnapshotListEntry } from "@/lib/blackboardv2";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Card } from "@/components/ui";
import { cn } from "@/lib/utils";

import { FGSBoard } from "./FGSPage";
import { RecordPanel } from "./BlackboardRecordPanel";
import { ExplorerPanel } from "./BlackboardExplorerPanel";
import { HealthPanel } from "./BlackboardHealthPanel";
import { SectionHeading, ErrorBanner } from "./BlackboardPanelChrome";
import { useSnapshotAndProject } from "./useSnapshotAndProject";

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
 const [project, setProject] = useState<Project>();
 const [error, setError] = useState<{ projectId: string; message: string } | null>(null);
 useEffect(() => {
  let active = true;
  apiGet<Project>(`/api/projects/${projectId}`).then((value) => { if (active) setProject(value); }).catch((err: unknown) => { if (active) setError({ projectId, message: err instanceof Error ? err.message : "Cannot read Project" }); });
  return () => { active = false; };
 }, [projectId]);
 if (error?.projectId === projectId) return <p role="alert">{error.message}</p>;
 if (!project || project.id !== projectId) return <p className="p-6">Loading Blackboard…</p>;
 if (project.blackboard_protocol === "fgs") return <ProjectPageShell title="Blackboard"><FGSBoard key={projectId} scope="projects" id={projectId} /></ProjectPageShell>;
 return <LegacyBlackboardPage />;
}

function LegacyBlackboardPage() {
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
                ? "border-signal/30 bg-signal/10 font-medium text-foreground shadow-sm"
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

function BoardPanel({
  projectId,
  focus,
}: {
  projectId: string;
  focus: "work" | "knowledge";
}) {
  const { snapshot, health, project, error, loading } = useSnapshotAndProject(projectId);

  if (error) return <ErrorBanner message={error} />;
  if (loading || !snapshot || !health) {
    return (
      <Card role="status" aria-label="Loading Blackboard" className="m-4 min-h-24 items-center justify-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
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
  const revisionMismatch = health.revision !== snapshot.revision;

  return (
    <div className="min-w-0 space-y-0">
      <StatusStrip revision={snapshot.revision} kind={kind} entries={entries} health={health} />
      {revisionMismatch ? (
        <RevisionMismatchBanner snapshotRevision={snapshot.revision} healthRevision={health.revision} />
      ) : (
        <HealthPanel projectId={projectId} health={health} />
      )}
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
  health,
}: {
  revision: number;
  kind: string;
  entries: SnapshotListEntry[];
  health: SemanticHealth;
}) {
  const openWork = entries.filter((e) => e.section === "work").length;
  const knowledge = entries.filter((e) => e.section === "knowledge").length;
  const cells = [
    { label: "Revision", value: String(revision) },
    { label: "Kind", value: kind === "ctf_challenge" ? "CTF" : "Pentest" },
    { label: "Health", value: health.status },
    { label: "Attention", value: attentionLabel(health.attention.state) },
    { label: "Current Work", value: String(openWork) },
    { label: "Knowledge", value: String(knowledge) },
  ];

  return (
    <section
      aria-label="Blackboard status"
      className="grid grid-cols-2 gap-px border-b border-border bg-border sm:grid-cols-3 lg:grid-cols-6"
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

function RevisionMismatchBanner({
  snapshotRevision,
  healthRevision,
}: {
  snapshotRevision: number;
  healthRevision: number;
}) {
  return (
    <section
      role="status"
      aria-label="Stale health"
      className="border-b border-border bg-warning/10 p-4 text-sm text-foreground"
      data-testid="blackboard-health-stale"
    >
      <p className="font-medium">Semantic health is stale due to a revision mismatch.</p>
      <p className="mt-1 text-muted-foreground">
        Snapshot revision {snapshotRevision} and health revision {healthRevision} disagree. Refresh
        to load a consistent pair instead of mixing diagnoses across revisions.
      </p>
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
      <SectionHeading title="Current Work" detail={`${entries.length} ${entries.length === 1 ? "item" : "items"}`} />
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
      <SectionHeading title="Project Knowledge" detail={`${entries.length} ${entries.length === 1 ? "item" : "items"}`} />
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
            className="flex w-full flex-col gap-1 bg-transparent p-3 text-left transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center sm:justify-between"
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
