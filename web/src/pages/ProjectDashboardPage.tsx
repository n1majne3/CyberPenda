import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  AlertTriangle,
  CheckCircle2,
  ClipboardList,
  FileText,
  FlaskConical,
  FolderLock,
  ListChecks,
  Rocket,
  ShieldAlert,
  Target,
} from "lucide-react";
import { apiGet, type Dashboard, type Project } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, buttonVariants, Card, CardDescription, CardTitle } from "@/components/ui";
import { cn } from "@/lib/utils";

export function ProjectDashboardPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [dash, setDash] = useState<Dashboard | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!projectId) return;
    (async () => {
      setLoading(true);
      try {
        const [d, p] = await Promise.all([
          apiGet<Dashboard>(`/api/projects/${projectId}/dashboard`),
          apiGet<Project>(`/api/projects/${projectId}`),
        ]);
        setDash(d);
        setProject(p);
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

  return (
    <ProjectPageShell
      title={<h1 className="text-2xl font-semibold tracking-tight">{project.name}</h1>}
      description={project.description || undefined}
      actions={
        <Link to={`${base}/tasks/new`} className={buttonVariants()}>
          <Rocket className="h-4 w-4" /> Launch task
        </Link>
      }
      bodyClassName="space-y-6"
    >
      <Card
        role="region"
        aria-labelledby="scope-readiness-title"
        className={cn("gap-5", !scopeReady && "border-warning/40 ring-1 ring-warning/20")}
      >
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div>
            <CardTitle id="scope-readiness-title" className="flex items-center gap-2">
              <Target className="h-4 w-4 text-muted-foreground" /> Scope readiness
            </CardTitle>
            <CardDescription className="mt-1">
              {scopeReady
                ? "Scope has in-scope assets for new Tasks."
                : "Add at least one in-scope asset before relying on task results."}
            </CardDescription>
          </div>
          <Badge variant={scopeReady ? "success" : "warning"} className="w-fit">
            {scopeReady ? <CheckCircle2 className="h-3 w-3" /> : <ShieldAlert className="h-3 w-3" />}
            {scopeReady ? "Scope ready" : "Scope needs attention"}
          </Badge>
        </div>

        <div className="grid gap-2 sm:grid-cols-3 lg:grid-cols-6">
          <ScopeChip label="domain" n={dash.scope.domains} />
          <ScopeChip label="IP" n={dash.scope.ips} />
          <ScopeChip label="CIDR" n={dash.scope.cidrs} />
          <ScopeChip label="URL" n={dash.scope.urls} />
          <ScopeChip label="port" n={dash.scope.ports} />
          <ScopeChip label="excluded" n={dash.scope.excluded} />
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {dash.scope.has_testing_limits && (
            <Badge variant="warning">
              <AlertTriangle className="h-3 w-3" /> Testing limits set
            </Badge>
          )}
          {dash.scope.has_notes && (
            <Badge variant="outline">
              <FileText className="h-3 w-3" /> Scope notes
            </Badge>
          )}
          {!dash.scope.has_testing_limits && !dash.scope.has_notes && (
            <span className="text-sm text-muted-foreground">No testing limits or scope notes recorded.</span>
          )}
        </div>

        <Link to={`${base}/scope`} className={cn(buttonVariants({ variant: "outline", size: "sm" }), "w-fit")}>
          Edit scope
        </Link>
      </Card>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <CountCard icon={<ListChecks className="h-4 w-4" />} label="Tasks" n={dash.counts.tasks} to={`${base}/tasks`} />
        <CountCard icon={<FileText className="h-4 w-4" />} label="Facts" n={dash.counts.facts} to={`${base}/facts`} />
        <CountCard icon={<FlaskConical className="h-4 w-4" />} label="Findings" n={dash.counts.findings} to={`${base}/findings`} />
        <CountCard icon={<FolderLock className="h-4 w-4" />} label="Evidence" n={dash.counts.evidence} to={`${base}/evidence`} />
      </div>

      <div className="flex flex-wrap gap-2">
        <Link to={`${base}/report`} className={buttonVariants({ variant: "secondary", size: "sm" })}>
          <ClipboardList className="h-4 w-4" /> Generate report
        </Link>
      </div>
    </ProjectPageShell>
  );
}

function ScopeChip({ label, n }: { label: "domain" | "IP" | "CIDR" | "URL" | "port" | "excluded"; n: number }) {
  return (
    <div className="rounded-md border border-border bg-background px-3 py-2">
      <p className="text-xs font-medium text-muted-foreground">{pluralize(n, label)}</p>
    </div>
  );
}

function CountCard({ icon, label, n, to }: { icon: React.ReactNode; label: string; n: number; to: string }) {
  return (
    <Link
      to={to}
      aria-label={`View ${n} ${countLabel(label, n)}`}
      className="group flex min-h-28 flex-col justify-between rounded-lg border border-border bg-card p-4 text-card-foreground shadow-sm transition-[border-color,box-shadow,background-color] hover:border-foreground/20 hover:bg-accent/40 hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
    >
      <span className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground group-hover:text-foreground">
        {icon}
        {label}
      </span>
      <span className="text-3xl font-semibold tracking-tight">{n}</span>
    </Link>
  );
}

function pluralize(count: number, singular: "domain" | "IP" | "CIDR" | "URL" | "port" | "excluded") {
  if (singular === "IP" || singular === "CIDR" || singular === "URL") {
    return `${count} ${count === 1 ? singular : `${singular}s`}`;
  }
  if (singular === "excluded") return `${count} excluded`;
  return `${count} ${count === 1 ? singular : `${singular}s`}`;
}

function countLabel(label: string, count: number) {
  if (label === "Evidence") return count === 1 ? "evidence item" : "evidence items";
  const singular = label.toLowerCase().replace(/s$/, "");
  return count === 1 ? singular : label.toLowerCase();
}
