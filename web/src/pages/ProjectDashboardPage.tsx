import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  AlertTriangle,
  ArrowUpRight,
  CheckCircle2,
  Circle,
  ClipboardList,
  Compass,
  FileText,
  FlaskConical,
  FolderLock,
  LayoutGrid,
  ListChecks,
  RefreshCw,
  Plus,
  Rocket,
} from "lucide-react";
import { apiGet, apiPost, type Dashboard, type Project, type ProjectKind, type Task } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, buttonVariants, Card, CardDescription, CardTitle, Chip } from "@/components/ui";
import { formatRelativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

/** Lifecycle states whose Task is still open work. Mirrors TasksPage's ACTIVE set plus pending. */
const OPEN_TASK_STATUSES: Record<string, true> = { pending: true, running: true, paused: true };

export function ProjectDashboardPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [dash, setDash] = useState<Dashboard | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [conversionPreview, setConversionPreview] = useState<ProjectKindConversionPreview | null>(null);
  const [converting, setConverting] = useState(false);

  useEffect(() => {
    if (!projectId) return;
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
        setDash(d);
        setProject(p);
        setTasks(t.tasks ?? []);
        setError(null);
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setLoading(false);
      }
    })();
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
  const targetKind: ProjectKind = project.kind === "ctf_challenge" ? "pentest" : "ctf_challenge";

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
  const latestTask = recentTasks[0];
  const runningCount = tasks.filter((task) => task.status === "running").length;
  // Blackboard objective/attempt/solution counts are not part of the dashboard
  // payload, so Current work surfaces the closest loaded metrics: open Tasks
  // stand in for open exploration objectives, running Tasks for active
  // attempts, and current Findings for verified solutions.
  const openTaskCount = tasks.filter((task) => OPEN_TASK_STATUSES[task.status]).length;

  async function previewKindConversion() {
    if (!projectId) return;
    setConverting(true);
    try {
      const preview = await apiPost<ProjectKindConversionPreview>(`/api/projects/${projectId}/kind-conversion/preview`, {
        target_kind: targetKind,
      });
      setConversionPreview(preview);
      setError(null);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setConverting(false);
    }
  }

  async function confirmKindConversion() {
    if (!projectId || !conversionPreview?.ready) return;
    setConverting(true);
    try {
      const converted = await apiPost<Project>(`/api/projects/${projectId}/kind-conversion`, {
        target_kind: targetKind,
        confirm: true,
      });
      setProject(converted);
      setConversionPreview(null);
      setError(null);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setConverting(false);
    }
  }

  return (
    <ProjectPageShell
      title={
        <div>
          <p className="mb-1 font-mono text-xs uppercase tracking-[0.14em] text-muted-foreground">
            Engagement
          </p>
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

      <div className="grid min-w-0 max-w-full grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard
          icon={<ListChecks className="h-3.5 w-3.5" />}
          label="Tasks"
          n={dash.counts.tasks}
          to={`${base}/tasks`}
          chip={runningCount > 0 ? `${runningCount} running` : undefined}
          sub={latestTask ? `Latest: ${latestTask.goal} · ${formatRelativeTime(latestTask.updated_at)}` : undefined}
        />
        <StatCard icon={<FileText className="h-3.5 w-3.5" />} label="Facts" n={dash.counts.facts} to={`${base}/facts`} />
        <StatCard
          icon={<FlaskConical className="h-3.5 w-3.5" />}
          label="Findings"
          n={dash.counts.findings}
          to={`${base}/findings`}
          zeroHint="No Findings yet — generated from Task conclusions"
        />
        <StatCard icon={<FolderLock className="h-3.5 w-3.5" />} label="Evidence" n={dash.counts.evidence} to={`${base}/evidence`} />
      </div>

      <div className="grid min-w-0 max-w-full gap-5 lg:grid-cols-5">
        <section
          aria-labelledby="recent-activity-title"
          className="min-w-0 rounded-lg border border-border bg-card shadow-sm lg:col-span-3"
        >
          <div className="flex items-center justify-between border-b border-border px-4 py-3">
            <span id="recent-activity-title" className="text-sm font-medium">Recent activity</span>
            <Link to={`${base}/tasks`} className="text-xs text-muted-foreground hover:text-foreground">
              View all
            </Link>
          </div>
          {recentTasks.length === 0 ? (
            <p className="px-4 py-6 text-center text-xs text-muted-foreground">No recent task activity.</p>
          ) : (
            <ul className="divide-y divide-border text-sm">
              {recentTasks.map((task) => (
                <li key={task.id} className="flex items-center gap-3 px-4 py-2.5">
                  <span className={cn("h-1.5 w-1.5 flex-none rounded-full", statusDotClass(task.status))} />
                  <span className="min-w-0 flex-1 truncate">
                    {task.goal} — {activityStatusText(task)}
                  </span>
                  <span className="flex-none text-xs text-muted-foreground">
                    {formatRelativeTime(task.updated_at)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section
          aria-labelledby="current-work-title"
          className="min-w-0 rounded-lg border border-border bg-card shadow-sm lg:col-span-2"
        >
          <div className="border-b border-border px-4 py-3">
            <span id="current-work-title" className="text-sm font-medium">Current work</span>
          </div>
          <div className="space-y-3 px-4 py-3.5 text-sm">
            <div className="flex items-center justify-between">
              <span className="flex items-center gap-2">
                <Compass className="h-4 w-4 text-muted-foreground" /> Exploration objectives
              </span>
              <span className="font-semibold">{openTaskCount} open</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="flex items-center gap-2">
                <FlaskConical className="h-4 w-4 text-muted-foreground" /> Attempts
              </span>
              <span className="font-semibold">{runningCount} in progress</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="flex items-center gap-2">
                <CheckCircle2 className="h-4 w-4 text-muted-foreground" />{" "}
                {isCTF ? "Solutions" : "Findings"}
              </span>
              <span className="font-semibold">{dash.counts.findings} current</span>
            </div>
            <Link
              to={`${base}/blackboard`}
              className="mt-1 flex h-8 items-center justify-center gap-1.5 rounded-md border border-border text-xs font-medium hover:bg-muted"
            >
              <LayoutGrid className="h-3.5 w-3.5" /> Open Blackboard
            </Link>
          </div>
        </section>
      </div>

      <Card role="region" aria-labelledby="project-kind-title" className="min-w-0 gap-4">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div>
            <CardTitle id="project-kind-title">Project kind</CardTitle>
            <CardDescription className="mt-1">
              Project Kind controls valid Finding, Solution, and report semantics.
            </CardDescription>
          </div>
          <Button variant="outline" size="sm" onClick={previewKindConversion} disabled={converting}>
            <RefreshCw className="h-4 w-4" /> Change Project kind
          </Button>
        </div>
        {conversionPreview && (
          <div className="rounded-md border border-border bg-muted/40 p-3 text-sm">
            {conversionPreview.ready ? (
              <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                <p>Conversion is ready. No active Task or incompatible Project Knowledge blocks it.</p>
                <Button size="sm" onClick={confirmKindConversion} disabled={converting}>Confirm conversion</Button>
              </div>
            ) : (
              <div>
                <p className="font-medium text-warning">Conversion is blocked.</p>
                <ul className="mt-2 list-disc space-y-1 pl-5 text-muted-foreground">
                  {conversionPreview.blockers.map((blocker) => (
                    <li key={blocker.code}>{kindConversionBlockerLabel(blocker)}</li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
      </Card>

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

interface ProjectKindConversionPreview {
  project_id: string;
  current_kind: ProjectKind;
  target_kind: ProjectKind;
  ready: boolean;
  blockers: Array<{ code: string; count: number }>;
}

function kindConversionBlockerLabel(blocker: { code: string; count: number }) {
  switch (blocker.code) {
    case "active_tasks":
      return `${blocker.count} non-terminal Task${blocker.count === 1 ? "" : "s"}`;
    case "incompatible_findings":
      return `${blocker.count} current Finding${blocker.count === 1 ? "" : "s"}`;
    case "incompatible_solutions":
      return `${blocker.count} current Solution${blocker.count === 1 ? "" : "s"}`;
    default:
      return `${blocker.code}: ${blocker.count}`;
  }
}

function StatCard({
  icon,
  label,
  n,
  to,
  chip,
  sub,
  zeroHint,
}: {
  icon: React.ReactNode;
  label: string;
  n: number;
  to: string;
  chip?: string;
  sub?: string;
  zeroHint?: string;
}) {
  const empty = n === 0;
  return (
    <Link
      to={to}
      aria-label={`View ${n} ${countLabel(label, n)}`}
      className={cn(
        "group min-w-0 overflow-hidden rounded-lg border p-4 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        empty
          ? "border-dashed border-border bg-card/50 hover:border-ring"
          : "border-border bg-card shadow-sm hover:border-ring",
      )}
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
      {(sub || (empty && zeroHint)) && (
        <div className="mt-1 truncate text-xs text-muted-foreground">
          {empty && zeroHint ? zeroHint : sub}
        </div>
      )}
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
