import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, CheckCircle2, FileText, FolderOpen, Globe, Plus, Server, ShieldAlert } from "lucide-react";
import { apiGet, apiPost, type Project } from "@/lib/api";
import { Badge, Button, Card, CardDescription, CardTitle, Input, Label } from "@/components/ui";
import { PageContainer } from "@/components/shared";

export function ProjectListPage() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");

  async function load() {
    setLoading(true);
    try {
      const data = await apiGet<{ projects: Project[] }>("/api/projects");
      setProjects(data.projects ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    // Initial load on mount. load() is also reused by event handlers, so it is
    // kept as a standalone function; the mount fetch is an intentional, known
    // pattern that the rule cannot distinguish from a problematic cascade.
    load();
  }, []);
  /* eslint-enable react-hooks/set-state-in-effect */

  async function create() {
    try {
      await apiPost<Project>("/api/projects", { name, scope: {} });
      setName("");
      setCreating(false);
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <PageContainer className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="max-w-2xl">
          <h1 className="text-2xl font-semibold tracking-tight">Projects</h1>
          <p className="mt-2 text-sm leading-6 text-muted-foreground">
            Bounded security-testing engagements, each with its own scope, tasks, and memory.
          </p>
        </div>
        <Button onClick={() => setCreating((v) => !v)}>
          <Plus className="h-4 w-4" /> New project
        </Button>
      </div>

      {creating && (
        <Card className="grid gap-3 sm:grid-cols-[1fr_auto_auto] sm:items-end">
          <div className="flex-1">
            <Label htmlFor="proj-name">Project name</Label>
            <Input
              id="proj-name"
              name="project_name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Acme External…"
              autoComplete="off"
              className="mt-1"
            />
          </div>
          <Button size="sm" onClick={create} disabled={!name.trim()}>
            Create
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setCreating(false)}>
            Cancel
          </Button>
        </Card>
      )}

      {loading && (
        <Card
          role="status"
          aria-label="Loading projects"
          className="min-h-32 items-center justify-center text-center text-sm text-muted-foreground"
        >
          Loading projects
        </Card>
      )}

      {error && !loading && (
        <Card role="alert" className="border-destructive/25">
          <div className="flex items-start gap-3">
            <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-destructive/20 bg-destructive/10 text-destructive">
              <AlertTriangle className="h-4 w-4" />
            </div>
            <div>
              <CardTitle className="text-sm">Couldn't load projects</CardTitle>
              <CardDescription className="mt-1">{error}</CardDescription>
            </div>
          </div>
        </Card>
      )}

      {projects.length === 0 && !error && !creating && !loading && (
        <Card
          role="status"
          aria-label="No projects"
          className="items-center justify-center border-dashed py-14 text-center"
        >
          <div className="flex h-10 w-10 items-center justify-center rounded-md border bg-muted text-muted-foreground">
            <FolderOpen className="h-6 w-6 text-muted-foreground" />
          </div>
          <div>
            <p className="text-sm font-medium">No projects</p>
            <p className="mt-1 text-sm text-muted-foreground">Create a Project to start tracking scope and tasks.</p>
          </div>
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-4 w-4" /> New project
          </Button>
        </Card>
      )}

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {sortNewestFirst(projects).map((p) => (
          <ProjectCard key={p.id} project={p} />
        ))}
      </div>
    </PageContainer>
  );
}

function ProjectCard({ project }: { project: Project }) {
  const scope = projectScopeSummary(project);

  return (
    <Link
      to={`/projects/${project.id}`}
      aria-label={`Open ${project.name} project dashboard`}
      className="group block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
    >
      <Card className="h-full transition-[border-color,box-shadow,background-color,transform] duration-150 ease-geist group-hover:-translate-y-0.5 group-hover:border-foreground/20 group-hover:bg-accent/40 group-hover:shadow-md motion-reduce:transition-none motion-reduce:group-hover:translate-y-0">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground">
                <FolderOpen className="h-4 w-4" />
              </span>
              <div className="min-w-0">
                <CardTitle className="truncate text-sm group-hover:text-foreground">{project.name}</CardTitle>
                {project.description && (
                  <CardDescription className="mt-1 line-clamp-2">{project.description}</CardDescription>
                )}
              </div>
            </div>
          </div>
          <Badge variant={scope.ready ? "success" : "warning"} className="shrink-0">
            {scope.ready ? <CheckCircle2 className="h-3 w-3" /> : <ShieldAlert className="h-3 w-3" />}
            {scope.ready ? "Scope ready" : "Needs scope"}
          </Badge>
        </div>

        <div className="flex flex-wrap gap-1.5">
          {scope.assets.length > 0 ? (
            scope.assets.map((item) => (
              <Badge key={item.label} variant="outline">
                {item.icon}
                {item.label}
              </Badge>
            ))
          ) : (
            <Badge variant="outline">No scoped assets</Badge>
          )}
          {scope.testingLimits > 0 && (
            <Badge variant="warning">
              <AlertTriangle className="h-3 w-3" />
              {pluralize(scope.testingLimits, "testing limit")}
            </Badge>
          )}
          {scope.hasNotes && (
            <Badge variant="outline">
              <FileText className="h-3 w-3" />
              Scope notes
            </Badge>
          )}
        </div>
      </Card>
    </Link>
  );
}

function projectScopeSummary(project: Project) {
  const scope = project.scope ?? {};
  const assetGroups = [
    { count: scope.domains?.length ?? 0, singular: "domain", icon: <Globe className="h-3 w-3" /> },
    { count: scope.ips?.length ?? 0, singular: "IP", icon: <Server className="h-3 w-3" /> },
    { count: scope.cidrs?.length ?? 0, singular: "CIDR", icon: <Server className="h-3 w-3" /> },
    { count: scope.urls?.length ?? 0, singular: "URL", icon: <Globe className="h-3 w-3" /> },
    { count: scope.ports?.length ?? 0, singular: "port", icon: <Server className="h-3 w-3" /> },
    { count: scope.excluded?.length ?? 0, singular: "excluded", icon: <ShieldAlert className="h-3 w-3" /> },
  ];
  const assets = assetGroups
    .filter((group) => group.count > 0)
    .map((group) => ({
      label: pluralize(group.count, group.singular),
      icon: group.icon,
    }));

  return {
    ready: assets.length > 0,
    assets,
    testingLimits: scope.testing_limits?.length ?? 0,
    hasNotes: Boolean(scope.notes?.trim()),
  };
}

function pluralize(count: number, singular: string) {
  if (singular === "IP" || singular === "CIDR" || singular === "URL") {
    return `${count} ${count === 1 ? singular : `${singular}s`}`;
  }
  if (singular === "excluded") {
    return `${count} excluded`;
  }
  return `${count} ${count === 1 ? singular : `${singular}s`}`;
}

function sortNewestFirst<T extends { created_at: string }>(items: T[]): T[] {
  return [...items].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
}
