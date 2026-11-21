import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  AlertTriangle,
  ArrowUpRight,
  CheckCircle2,
  Circle,
  ClipboardList,
  FileText,
  FlaskConical,
  FolderLock,
  ListChecks,
  Plus,
  Rocket,
} from "lucide-react";
import { apiGet, type Dashboard, type Project, type Task } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { buttonVariants } from "@/components/buttonVariants";
import { Card, CardDescription, CardTitle, Chip } from "@/components/ui";
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
  const progressPercent = Math.round((completedCount / checklistItems.length) * 100);

  // Task-derived breakdowns for the stat cards, activity feed, and Current work.
  const recentTasks = [...tasks]
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
    .slice(0, 4);
  const runningCount = tasks.filter((task) => task.status === "running").length;
  const activeTasks = tasks.filter((task) => OPEN_TASK_STATUSES[task.status]);
  const attentionTasks = tasks.filter((task) => ["failed", "interrupted", "paused"].includes(task.status));

  return (
    <ProjectPageShell
      title={
        <div>
          <div className="mt-0.5 flex flex-wrap items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight">{project.name}</h1>
            <Chip variant="signal" dot>{projectKindLabel}</Chip>
          </div>
        </div>
      }
      description={project.description || undefined}
      actions={
        <Link to={`${base}/tasks/new`} className={buttonVariants()}>
          <Rocket className="h-4 w-4" /> Launch task
        </Link>
      }
      bodyClassName="min-w-0 max-w-full space-y-5"
      contentClassName="mx-auto min-w-0 max-w-6xl px-6 pb-6 lg:px-8 lg:pb-8"
    >

      {!scopeReady && (
        <section
          role="region"
          aria-labelledby="scope-readiness-title"
          className="rounded-lg border border-warning/30 bg-warning/[0.06] shadow-sm"
        >
          <div className="flex items-center justify-between border-b border-warning/20 px-4 py-3">
            <div id="scope-readiness-title" className="flex items-center gap-2 text-sm font-medium">
              <AlertTriangle className="h-4 w-4 text-warning" /> Scope readiness
            </div>
            <span className="text-xs font-medium text-[hsl(28_90%_32%)]">
              {completedCount} / {checklistItems.length} complete
            </span>
          </div>
          <div className="px-4 py-3.5">
            <div className="h-1.5 w-full overflow-hidden rounded-full bg-warning/15">
              <div className="h-full rounded-full bg-warning" style={{ width: `${progressPercent}%` }} />
            </div>
            <ul className="mt-3 space-y-2 text-sm">
              {checklistItems.map((item) => (
                <li key={item.id} className="flex items-center gap-2.5">
                  {item.done ? (
                    <CheckCircle2 className="h-4 w-4 flex-none text-success" />
                  ) : (
                    <Circle className="h-4 w-4 flex-none text-muted-foreground" />
                  )}
                  <span className={cn(item.optional && !item.done && "text-muted-foreground")}>
                    {item.label}
                  </span>
                  {item.action && (
                    <Link
                      to={item.action.to}
                      className={cn(
                        buttonVariants({ variant: "outline", size: "sm" }),
                        "ml-auto h-7 flex-none px-2.5 text-xs",
                      )}
                    >
                      <Plus className="h-3.5 w-3.5" /> {item.action.label}
                    </Link>
                  )}
                </li>
              ))}
            </ul>
          </div>
        </section>
      )}

      <div aria-label="Project totals" className={cn("grid grid-cols-2 divide-x divide-border border-y border-border", isCTF && project.blackboard_protocol !== "fgs" ? "sm:grid-cols-3" : "sm:grid-cols-4")}>
        <StatCard icon={<ListChecks className="h-3.5 w-3.5" />} label="Tasks" n={dash.counts.tasks} to={`${base}/tasks`} chip={runningCount > 0 ? `${runningCount} running` : undefined} />
        <StatCard icon={<FileText className="h-3.5 w-3.5" />} label="Facts" n={dash.counts.facts} to={`${base}/blackboard`} />
        {project.blackboard_protocol === "fgs" ? (
          <>
            <StatCard icon={<FlaskConical className="h-3.5 w-3.5" />} label="Goals" n={dash.counts.goals ?? 0} to={`${base}/blackboard`} />
            <StatCard icon={<FolderLock className="h-3.5 w-3.5" />} label="Steps" n={dash.counts.steps ?? 0} to={`${base}/blackboard`} />
          </>
        ) : (
          <>
            {!isCTF && <StatCard icon={<FlaskConical className="h-3.5 w-3.5" />} label="Findings" n={dash.counts.findings} to={`${base}/findings`} />}
            <StatCard icon={<FolderLock className="h-3.5 w-3.5" />} label="Evidence" n={dash.counts.evidence} to={`${base}/evidence`} />
          </>
        )}
      </div>

      <div className="grid min-w-0 gap-8 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <section aria-labelledby="current-work-title" className="min-w-0">
          <div className="mb-3 flex items-center justify-between gap-3">
            <h2 id="current-work-title" className="text-base font-medium">Active Tasks</h2>
            <Link to={`${base}/tasks`} className="text-xs text-muted-foreground hover:text-foreground">View all Tasks</Link>
          </div>
          {activeTasks.length === 0 ? <p className="py-6 text-sm text-muted-foreground">No active Tasks.</p> : (
            <ul className="divide-y divide-border border-y border-border">
              {activeTasks.slice(0, 5).map((task) => (
                <li key={task.id}>
                  <Link to={`${base}/tasks/${task.id}`} aria-label={`${task.goal} ${task.status}`} className="flex items-center gap-3 py-4 hover:text-signal">
                    <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", statusDotClass(task.status))} />
                    <span className="min-w-0 flex-1 break-words text-sm font-medium">{task.goal}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">{task.status}</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
          <details className="mt-5 border-t border-border pt-3">
            <summary id="recent-activity-title" className="cursor-pointer text-sm text-muted-foreground hover:text-foreground">Recent activity</summary>
            <ul className="mt-3 divide-y divide-border text-sm">
              {recentTasks.map((task) => (
                <li key={task.id}>
                  <Link to={`${base}/tasks/${task.id}`} className="flex items-center gap-3 py-3">
                    <span className="min-w-0 flex-1 truncate">{task.goal} — {activityStatusText(task)}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">{formatRelativeTime(task.updated_at)}</span>
                  </Link>
                </li>
              ))}
              {recentTasks.length === 0 && <li className="py-3 text-sm text-muted-foreground">No recent task activity.</li>}
            </ul>
          </details>
        </section>
        <section aria-labelledby="attention-title" className="min-w-0">
          <h2 id="attention-title" className="mb-3 text-base font-medium">Needs your attention</h2>
          {attentionTasks.length === 0 ? <p className="py-3 text-sm text-muted-foreground">No Tasks need attention.</p> : (
            <ul className="divide-y divide-border">
              {attentionTasks.slice(0, 5).map((task) => (
                <li key={task.id}>
                  <Link to={`${base}/tasks/${task.id}`} className="block py-3 text-sm hover:text-signal">
                    <span className="mb-1 block text-xs capitalize text-warning">{task.status}</span>
                    <span className="line-clamp-2 break-words">{task.goal}</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
          <Link to={`${base}/blackboard`} className="mt-4 inline-block text-sm text-signal hover:underline">Open Blackboard →</Link>
          {isCTF && <Link to={`${base}/solution`} className="mt-3 block text-sm text-signal hover:underline">Open Solution →</Link>}
        </section>
      </div>

      <details className="border-t border-border pt-4">
        <summary className="cursor-pointer text-sm text-muted-foreground hover:text-foreground">Project settings</summary>
        <div className="mt-4"><ProjectKindConversionCard key={project.id} project={project} onConverted={setProject} /></div>
      </details>

      {!isCTF && (
        <div className="flex flex-wrap items-center gap-2 border-t border-border/60 pt-6">
          <Link to={`${base}/report`} className={buttonVariants({ variant: "outline", size: "sm" })}>
            <ClipboardList className="h-4 w-4" /> Open report
          </Link>
        </div>
      )}
    </ProjectPageShell>
  );
}

function StatCard({
  icon,
  label,
  n,
  to,
  chip,
}: {
  icon: React.ReactNode;
  label: string;
  n: number;
  to: string;
  chip?: string;
}) {
  const empty = n === 0;
  return (
    <Link
      to={to}
      aria-label={`View ${n} ${countLabel(label, n)}`}
      className="group min-w-0 px-4 py-4 transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span className="flex items-center gap-1.5">
          {icon}
          {label}
        </span>
        {!empty && (
          <ArrowUpRight className="h-3.5 w-3.5 opacity-0 transition-opacity group-hover:opacity-60" />
        )}
      </div>
      <div className="mt-2 flex items-baseline gap-2">
        <span className={cn("text-2xl font-semibold tracking-tight", empty && "text-muted-foreground/60")}>
          {n}
        </span>
        {chip && (
          <Chip variant="success" dot className="h-[18px] text-[10px]">
            {chip}
          </Chip>
        )}
      </div>
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
