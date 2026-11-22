import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  AlertTriangle,
  ArrowUpRight,
  CheckCircle2,
  Circle,
  Flag,
  ShieldCheck,
  ListChecks,
  Plus,
} from "lucide-react";
import { apiGet, type Dashboard, type Project, type Task } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { buttonVariants } from "@/components/buttonVariants";
import { Card, CardDescription, CardTitle } from "@/components/ui";
import { formatRelativeTime } from "@/lib/format";
import { ProjectKindConversionCard } from "./ProjectKindConversionCard";
import { cn } from "@/lib/utils";

const OPEN_TASK_STATUSES: Record<string, true> = { pending: true, running: true, paused: true };

export function ProjectDashboardPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [dash, setDash] = useState<Dashboard | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const [d, p, t] = await Promise.all([
          apiGet<Dashboard>(`/api/projects/${projectId}/dashboard`),
          apiGet<Project>(`/api/projects/${projectId}`),
          // The existing task-list endpoint (same one TasksPage polls) powers the
          // stat breakdowns and the Recent activity feed. A failure here must not
          // take down the whole dashboard, so degrade to an empty list.
          apiGet<{ tasks: Task[] }>(`/api/projects/${projectId}/tasks`).catch(() => ({ tasks: [] as Task[] })),
        ]);
        if (cancelled) return;
        setDash(d);
        setProject(p);
        setTasks(t.tasks ?? []);
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  if (loading) {
    return (
      <ProjectPageShell>
        <Card
          role="status"
          aria-label="Loading dashboard"
          className="min-h-32 items-center justify-center text-center text-sm text-muted-foreground"
        >
          Loading dashboard
        </Card>
      </ProjectPageShell>
    );
  }

  if (error) {
    return (
      <ProjectPageShell>
        <Card role="alert" className="border-destructive/25">
          <div className="flex items-start gap-3">
            <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-destructive/20 bg-destructive/10 text-destructive">
              <AlertTriangle className="h-4 w-4" />
            </div>
            <div>
              <CardTitle className="text-sm">Couldn't load dashboard</CardTitle>
              <CardDescription className="mt-1">{error}</CardDescription>
            </div>
          </div>
        </Card>
      </ProjectPageShell>
    );
  }

  if (!dash || !project) return null;

  const base = `/projects/${projectId}`;
  const scopeReady = dash.scope.ready;
  const projectKindLabel = project.kind === "ctf_challenge" ? "CTF Challenge Project" : "Pentest Project";

  // Count badges on the section tabs come from the shared ProjectNav chrome,
  // so the dashboard renders no second nav (keeps every tab on one plane).
  const isCTF = project.kind === "ctf_challenge";
  const usesSolution = isCTF && project.blackboard_protocol !== "fgs";

  // Scope readiness checklist derived from the scope summary the dashboard
  // already loads: one required item (at least one named in-scope asset — the
  // backend's own `ready` definition) plus optional hardening items.
  const namedAssets =
    dash.scope.domains + dash.scope.ips + dash.scope.cidrs + dash.scope.urls + dash.scope.ports;
  const checklistItems: {
    id: string;
    done: boolean;
    label: string;
    optional?: boolean;
    action?: { label: string; to: string };
  }[] = [
    {
      id: "assets",
      done: namedAssets > 0,
      label: "Add at least one in-scope asset (domain, IP, CIDR, or URL) before relying on Task results.",
      action: { label: "Add asset", to: `${base}/scope` },
    },
    {
      id: "notes",
      done: dash.scope.has_notes,
      optional: true,
      label: "Optional: add Scope notes that explain the authorization boundary.",
    },
    {
      id: "limits",
      done: dash.scope.has_testing_limits,
      optional: true,
      label: "Optional: define Testing limits for the permitted testing window.",
    },
  ];
  const completedCount = checklistItems.filter((item) => item.done).length;

  // Task-derived breakdowns for the stat cards, activity feed, and Current work.
  const recentTasks = [...tasks]
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
    .slice(0, 4);
  const runningCount = tasks.filter((task) => task.status === "running").length;
  const activeTasks = tasks.filter((task) => OPEN_TASK_STATUSES[task.status]);
  const attentionTasks = tasks.filter((task) => ["failed", "interrupted", "paused"].includes(task.status));

  return (
    <ProjectPageShell title={project.name}
      description={project.description || "A focused view of your authorized security review."}
      actions={<Link to={`${base}/tasks/new`} className={buttonVariants()}><Plus className="size-3.5" />New task</Link>}
      bodyClassName="min-w-0">
      <div className="mb-6 flex flex-wrap items-center gap-2.5 text-xs text-muted-foreground">
        <span className="inline-flex items-center gap-2 text-signal"><span className="size-1.5 rounded-full bg-current" />{runningCount > 0 ? "In progress" : scopeReady ? "Ready" : "Scope needed"}</span>
        <span aria-hidden="true">·</span><span>{projectKindLabel}</span>
        <span className="h-px min-w-5 flex-1 bg-border" />
        {project.updated_at && <span className="font-mono text-[10px]">Updated {formatRelativeTime(project.updated_at)}</span>}
      </div>
      <div aria-label="Project totals" className={cn("mb-[35px] grid grid-cols-2 divide-x divide-border border-y border-border py-[22px]", usesSolution ? "sm:grid-cols-3" : "sm:grid-cols-4")}>
        <StatCard label="Tasks" n={dash.counts.tasks} to={`${base}/tasks`} chip={runningCount > 0 ? `${runningCount} running` : undefined} detail={`${tasks.filter((task) => task.status === "completed").length} completed`} />
        <StatCard label="Facts" n={dash.counts.facts} to={`${base}/blackboard`} detail="Across this Project" />
        {project.blackboard_protocol === "fgs" ? <>
          <StatCard label="Goals" n={dash.counts.goals ?? 0} to={`${base}/blackboard`} detail="Accepted FGS records" />
          <StatCard label="Steps" n={dash.counts.steps ?? 0} to={`${base}/blackboard`} detail="Accepted FGS records" />
        </> : <>
          {!isCTF && <StatCard label="Findings" n={dash.counts.findings} to={`${base}/findings`} detail="Ready for review" />}
          <StatCard label="Evidence" n={dash.counts.evidence} to={`${base}/evidence`} detail="Retained and traceable" />
        </>}
      </div>
      <div className="grid min-w-0 gap-[35px] lg:grid-cols-[minmax(0,1fr)_258px]">
        <div className="min-w-0">
          <section aria-labelledby="current-work-title">
            <div className="mb-[15px] flex items-center justify-between gap-3">
              <h2 id="current-work-title" className="text-[15px] font-semibold tracking-[-0.2px]">Active Tasks</h2>
              <Link to={`${base}/tasks`} className="text-xs text-signal hover:underline">View all Tasks →</Link>
            </div>
            {activeTasks.length === 0 ? <p className="border-b border-border py-8 text-xs text-muted-foreground">No active Tasks.</p> : <ul>
              {activeTasks.slice(0, 5).map((task) => (
                <li key={task.id} className="border-b border-border last:border-0">
                  <Link to={`${base}/tasks/${task.id}`} aria-label={`${task.goal} ${task.status}`} className="group flex items-center gap-[13px] py-[17px]">
                    <span className={cn("flex size-8 shrink-0 items-center justify-center rounded-[7px] border border-border bg-card", task.status === "running" && "border-transparent bg-signal/5 text-signal")}><ListChecks className="size-4" /></span>
                    <span className="min-w-0 flex-1"><span className="block break-words text-xs font-medium group-hover:text-signal">{task.goal}</span>
                      <span className="mt-1 block text-[10px] text-muted-foreground">Updated {formatRelativeTime(task.updated_at)}{task.runtime_activity?.liveness ? ` · Runtime ${task.runtime_activity.liveness}` : ""}</span>
                    </span>
                    <span className="inline-flex shrink-0 items-center gap-1.5 text-[11px] capitalize text-muted-foreground"><span className={cn("size-1.5 rounded-full", statusDotClass(task.status))} />{task.status}</span>
                    <ArrowUpRight className="size-3 shrink-0 text-muted-foreground" />
                  </Link>
                </li>
              ))}
            </ul>}
          </section>
          <section className="mt-8" aria-labelledby="recent-activity-title">
            <div className="mb-[15px] flex items-center justify-between gap-3"><h2 id="recent-activity-title" className="text-[15px] font-semibold tracking-[-0.2px]">Recent activity</h2><Link to={`${base}/blackboard`} className="text-xs text-signal hover:underline">Open Blackboard →</Link></div>
            <ul className="text-[11px]">
              {recentTasks.map((task) => <li key={task.id}>
                <Link to={`${base}/tasks/${task.id}`} className="flex items-baseline gap-3 py-2.5 hover:text-signal">
                  <span className="size-[5px] shrink-0 self-center rounded-full bg-border" />
                  <span className="min-w-0 flex-1 truncate"><strong className="font-medium">{task.goal}</strong> — {activityStatusText(task)}</span>
                  <span className="shrink-0 text-[10px] text-muted-foreground">{formatRelativeTime(task.updated_at)}</span>
                </Link>
              </li>)}
              {recentTasks.length === 0 && <li className="py-3 text-muted-foreground">No recent task activity.</li>}
            </ul>
          </section>
        </div>
        <aside className="min-w-0">
          <section aria-labelledby="attention-title" className="rounded-lg border border-border bg-card p-[19px]">
            <div className="mb-[18px] flex items-center gap-2"><h2 id="attention-title" className="flex items-center gap-2 text-xs font-semibold"><Flag className="size-4" />Needs your attention</h2><span className="font-mono text-[10px] font-normal text-muted-foreground">{attentionTasks.length}</span></div>
            {attentionTasks.length === 0 ? <p className="text-[11px] text-muted-foreground">No Tasks need attention.</p> : <ul className="divide-y divide-border">
              {attentionTasks.slice(0, 5).map((task) => <li key={task.id} className="py-4 first:pt-0 last:pb-0">
                <span className="mb-1 block text-[11px] capitalize text-warning">{task.status}</span><h3 className="mb-2 text-xs font-medium">{task.goal}</h3>
                <Link to={`${base}/tasks/${task.id}`} className="text-xs text-signal hover:underline">Review Task →</Link>
              </li>)}
            </ul>}
          </section>
          <section role="region" aria-labelledby="scope-readiness-title" className="flex items-start gap-2.5 px-0.5 py-5">
            <ShieldCheck className={cn("mt-0.5 size-4 shrink-0", scopeReady ? "text-signal" : "text-warning")} />
            <div className="min-w-0">
              <h2 id="scope-readiness-title" className="text-xs font-medium">{scopeReady ? "Scope is defined" : "Scope readiness"}</h2>
              <p className="my-1 text-[11px] leading-5 text-muted-foreground">{scopeReady ? "Authorized targets are defined. Exclusions and Testing limits apply." : "Add an authorized target before relying on Task results."}</p>
              <Link to={`${base}/scope`} className="text-[11px] text-signal hover:underline">Review Scope →</Link>
              {!scopeReady && <details className="mt-3 text-[11px] text-muted-foreground">
                <summary className="cursor-pointer">Readiness checklist · {completedCount} / {checklistItems.length}</summary>
                <ul className="mt-3 space-y-3">{checklistItems.map((item) => <li key={item.id} className="flex items-start gap-2">
                  {item.done ? <CheckCircle2 className="mt-0.5 size-3 shrink-0 text-success" /> : <Circle className="mt-0.5 size-3 shrink-0" />}
                  <span>{item.label}{item.action && <Link to={item.action.to} className="mt-1 block text-signal hover:underline">{item.action.label} →</Link>}</span>
                </li>)}</ul>
              </details>}
            </div>
          </section>
        </aside>
      </div>
      <footer className="mt-10 flex items-start justify-between gap-4 border-t border-border pt-4 text-[10px] text-muted-foreground">
        <details className="min-w-0 flex-1"><summary className="cursor-pointer hover:text-foreground">Project settings</summary><div className="mt-4"><ProjectKindConversionCard key={project.id} project={project} onConverted={setProject} /></div></details>
        <Link to={`${base}/${usesSolution ? "solution" : "report"}`} className="shrink-0 text-signal hover:underline">Open {usesSolution ? "Solution" : "report"} →</Link>
      </footer>
    </ProjectPageShell>
  );
}

function StatCard({
  label,
  n,
  to,
  chip,
  detail,
}: {
  label: string;
  n: number;
  to: string;
  chip?: string;
  detail: string;
}) {
  const empty = n === 0;
  return (
    <Link
      to={to}
      aria-label={`View ${n} ${countLabel(label, n)}`}
      className="group min-w-0 px-4 first:pl-0 sm:px-[25px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span className="flex items-center gap-1.5">
          {label}
        </span>
        {!empty && (
          <ArrowUpRight className="h-3.5 w-3.5 opacity-0 transition-opacity group-hover:opacity-60" />
        )}
      </div>
      <div className="mt-2 flex items-baseline gap-2">
        <span className="text-[29px] font-medium tracking-[-1px]">
          {n}
        </span>
        {chip && (
          <span className="text-[10px] text-muted-foreground">{chip}</span>
        )}
      </div>
      <p className="mt-0.5 text-[10px] text-muted-foreground">{detail}</p>
    </Link>
  );
}

function statusDotClass(status: string): string {
  switch (status) {
    case "running":
      return "bg-success";
    case "pending":
      return "bg-info";
    case "paused":
    case "interrupted":
      return "bg-warning";
    case "failed":
      return "bg-destructive";
    default:
      return "bg-muted-foreground/40";
  }
}

/** Maps durable Task state and live Runtime Activity to operator-facing text. */
function activityStatusText(task: Task): string {
  const activity = task.runtime_activity;
  if (activity?.liveness === "live") {
    return activity.turn_activity === "busy" ? "Running" : "Idle, awaiting input";
  }
  switch (task.status) {
    case "running":
      return "Running";
    case "completed":
      return "Completed";
    case "failed":
      return "Failed";
    case "paused":
      return "Paused";
    case "pending":
      return "Queued";
    default:
      return "Stopped";
  }
}

function countLabel(label: string, count: number) {
  if (label === "Evidence") return count === 1 ? "evidence item" : "evidence items";
  const singular = label.toLowerCase().replace(/s$/, "");
  return count === 1 ? singular : label.toLowerCase();
}
