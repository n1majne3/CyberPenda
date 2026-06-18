import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { AlertTriangle, Target, ListChecks, FileText, FlaskConical, FolderLock, ClipboardList, ShieldAlert } from "lucide-react";
import { apiGet, type Dashboard, type Project } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { Card, CardTitle, CardHeader, Badge, Button } from "@/components/ui";

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

  if (error) return <div className="p-8 text-destructive">{error}</div>;
  if (!dash || !project) return <div className="p-8 text-muted-foreground">Loading…</div>;

  const base = `/projects/${projectId}`;

  return (
    <div className="p-8">
      <ProjectNav />
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-xl font-semibold">{project.name}</h2>
          {project.description && <p className="text-sm text-muted-foreground">{project.description}</p>}
        </div>
        <Link to={`${base}/tasks/new`}>
          <Button size="sm">Launch task</Button>
        </Link>
      </div>

      {/* Scope status — visually loud when not ready */}
      <Card className={`mb-6 ${!dash.scope.ready ? "border-warning" : ""}`}>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle className="flex items-center gap-2">
            <Target className="h-4 w-4" /> Scope status
          </CardTitle>
          {!dash.scope.ready && (
            <Badge variant="warning">
              <AlertTriangle className="h-3 w-3 mr-1" /> Not ready
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
        <Link to={`${base}/scope`} className="inline-block mt-3">
          <Button size="sm" variant="outline">Edit scope</Button>
        </Link>
      </Card>

      {/* Count cards */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-6">
        <CountCard icon={<ListChecks className="h-4 w-4" />} label="Tasks" n={dash.counts.tasks} to={`${base}/tasks`} />
        <CountCard icon={<FileText className="h-4 w-4" />} label="Facts" n={dash.counts.facts} to={`${base}/facts`} />
        <CountCard icon={<FlaskConical className="h-4 w-4" />} label="Findings" n={dash.counts.findings} to={`${base}/findings`} />
        <CountCard icon={<FolderLock className="h-4 w-4" />} label="Evidence" n={dash.counts.evidence} to={`${base}/evidence`} />
      </div>

      <div className="flex flex-wrap gap-2">
        {dash.counts.pending_approvals > 0 && (
          <Link to={`${base}/approvals`}>
            <Button variant="warning" size="sm">
              <ShieldAlert className="h-4 w-4 mr-1" />
              {dash.counts.pending_approvals} pending approval{dash.counts.pending_approvals === 1 ? "" : "s"}
            </Button>
          </Link>
        )}
        <Link to={`${base}/report`}>
          <Button variant="secondary" size="sm">
            <ClipboardList className="h-4 w-4 mr-1" /> Generate report
          </Button>
        </Link>
      </div>
    </div>
  );
}

function ScopeChip({ label, n }: { label: string; n: number }) {
  return (
    <Badge variant={n > 0 ? "primary" : "outline"}>
      {n} {label}
    </Badge>
  );
}

function CountCard({ icon, label, n, to }: { icon: React.ReactNode; label: string; n: number; to: string }) {
  return (
    <Link to={to}>
      <Card className="hover:bg-accent/50 transition-colors">
        <CardTitle className="flex items-center gap-1.5 mb-1">{icon}{label}</CardTitle>
        <p className="text-2xl font-semibold">{n}</p>
      </Card>
    </Link>
  );
}
