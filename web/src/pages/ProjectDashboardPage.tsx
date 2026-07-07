import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { AlertTriangle, Target, ListChecks, FileText, FlaskConical, FolderLock, ClipboardList, Rocket } from "lucide-react";
import { apiGet, type Dashboard, type Project } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { Card, CardTitle, CardHeader, Badge, Button } from "@/components/ui";
import { PageContainer } from "@/components/shared";

export function ProjectDashboardPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [dash, setDash] = useState<Dashboard | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId) return;
    (async () => {
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
      }
    })();
  }, [projectId]);

  if (error) return <PageContainer className="text-destructive">{error}</PageContainer>;
  if (!dash || !project) return <PageContainer className="text-muted-foreground">Loading…</PageContainer>;

  const base = `/projects/${projectId}`;

  return (
    <PageContainer>
      <ProjectNav />
      <div className="mb-6 flex items-center justify-between">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">{project.name}</h2>
          {project.description && <p className="mt-1 text-sm text-muted-foreground">{project.description}</p>}
        </div>
        <Link to={`${base}/tasks/new`}>
          <Button size="sm">
            <Rocket className="h-4 w-4" /> Launch task
          </Button>
        </Link>
      </div>

      {/* Scope status — visually loud when not ready */}
      <Card className={`mb-6 ${!dash.scope.ready ? "ring-warning/50" : ""}`}>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="flex items-center gap-2">
            <Target className="h-4 w-4 text-primary" /> Scope status
          </CardTitle>
          {!dash.scope.ready && (
            <Badge variant="warning">
              <AlertTriangle className="h-3 w-3" /> Not ready
            </Badge>
          )}
        </CardHeader>
        <div className="flex flex-wrap gap-2 text-xs">
          <ScopeChip label="Domains" n={dash.scope.domains} />
          <ScopeChip label="IPs" n={dash.scope.ips} />
          <ScopeChip label="CIDRs" n={dash.scope.cidrs} />
          <ScopeChip label="URLs" n={dash.scope.urls} />
          <ScopeChip label="Ports" n={dash.scope.ports} />
          <ScopeChip label="Excluded" n={dash.scope.excluded} />
          {dash.scope.has_testing_limits && <Badge variant="warning">Testing limits set</Badge>}
          {dash.scope.has_notes && <Badge variant="outline">Scope notes</Badge>}
        </div>
        <div>
          <Link to={`${base}/scope`}>
            <Button size="sm" variant="outline">Edit scope</Button>
          </Link>
        </div>
      </Card>

      {/* Count cards */}
      <div className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <CountCard icon={<ListChecks className="h-4 w-4" />} label="Tasks" n={dash.counts.tasks} to={`${base}/tasks`} />
        <CountCard icon={<FileText className="h-4 w-4" />} label="Facts" n={dash.counts.facts} to={`${base}/facts`} />
        <CountCard icon={<FlaskConical className="h-4 w-4" />} label="Findings" n={dash.counts.findings} to={`${base}/findings`} />
        <CountCard icon={<FolderLock className="h-4 w-4" />} label="Evidence" n={dash.counts.evidence} to={`${base}/evidence`} />
      </div>

      <div className="flex flex-wrap gap-2">
        <Link to={`${base}/report`}>
          <Button variant="secondary" size="sm">
            <ClipboardList className="h-4 w-4" /> Generate report
          </Button>
        </Link>
      </div>
    </PageContainer>
  );
}

function ScopeChip({ label, n }: { label: string; n: number }) {
  return <Badge variant={n > 0 ? "primary" : "outline"}>{n} {label}</Badge>;
}

function CountCard({ icon, label, n, to }: { icon: React.ReactNode; label: string; n: number; to: string }) {
  return (
    <Link to={to} className="group">
      <Card className="transition-[background-color,box-shadow] hover:bg-accent/40 hover:ring-foreground/20">
        <CardTitle className="flex items-center gap-1.5 text-sm text-muted-foreground group-hover:text-foreground">
          {icon}{label}
        </CardTitle>
        <p className="text-3xl font-semibold tracking-tight">{n}</p>
      </Card>
    </Link>
  );
}
