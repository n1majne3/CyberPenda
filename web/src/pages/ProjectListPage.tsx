import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AlertTriangle, FolderOpen, Globe, Plus, Search, Server, ShieldAlert } from "lucide-react";
import { apiGet, apiPost, type Project, type ProjectKind } from "@/lib/api";
import { Button, Card, CardDescription, CardTitle, Input, Label, Select } from "@/components/ui";
import { formatRelativeTime } from "@/lib/format";
import { PageContainer } from "@/components/shared";
import { notifyWorkspaceNavigationChanged } from "@/lib/workspaceNavigation";

export function ProjectListPage() {
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [query, setQuery] = useState("");
	const [kind, setKind] = useState<ProjectKind | "">("");
  const createRequested = searchParams.get("new") === "1";
  const showCreateForm = creating || createRequested;

  function dismissCreateForm() {
    setCreating(false);
    if (createRequested) navigate("/", { replace: true });
  }

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
	if (!name.trim() || !kind) return;
    try {
      await apiPost<Project>("/api/projects", { name, kind, scope: {} });
      notifyWorkspaceNavigationChanged();
      setName("");
	  setKind("");
      dismissCreateForm();
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <PageContainer className="space-y-7">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="max-w-2xl">
          <h1 className="text-[29px] font-semibold leading-[1.3] tracking-[-1px]">Projects</h1>
          <p className="mt-2 text-xs leading-6 text-muted-foreground">
            Bounded engagements. Clear Scope. Traceable results.
          </p>
        </div>
        <Button onClick={() => (showCreateForm ? dismissCreateForm() : setCreating(true))}>
          <Plus className="h-4 w-4" /> New project
        </Button>
      </div>

      {showCreateForm && (
        <Card id="new-project" className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_14rem_auto_auto] sm:items-end">
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
          <div>
            <Label htmlFor="proj-kind">Project kind</Label>
            <Select
              id="proj-kind"
              name="project_kind"
              value={kind}
              onChange={(event) => setKind(event.target.value as ProjectKind)}
              className="mt-1"
            >
			  <option value="" disabled>Select Project kind</option>
              <option value="pentest">Pentest</option>
              <option value="ctf_challenge">CTF Challenge</option>
            </Select>
          </div>
		  <Button size="sm" onClick={create} disabled={!name.trim() || !kind}>
            Create
          </Button>
          <Button size="sm" variant="ghost" onClick={dismissCreateForm}>
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

      {projects.length === 0 && !error && !showCreateForm && !loading && (
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

      {projects.length > 0 && <div className="flex items-center justify-between gap-4">
        <label className="flex min-w-0 max-w-80 flex-1 items-center gap-2"><Search className="size-3.5 text-muted-foreground" /><input type="search" aria-label="Search Projects" placeholder="Search Projects…" value={query} onChange={(event) => setQuery(event.target.value)} className="min-w-0 flex-1 bg-transparent py-1.5 text-xs placeholder:text-muted-foreground" /></label>
        <span className="text-xs text-muted-foreground">{projects.length} Projects</span>
      </div>}
      <div className="grid gap-5 sm:grid-cols-2 xl:grid-cols-3">
        {sortNewestFirst(projects).filter((p) => `${p.name} ${p.description ?? ""}`.toLowerCase().includes(query.trim().toLowerCase())).map((p) => (
          <ProjectCard key={p.id} project={p} />
        ))}
      </div>
    </PageContainer>
  );
}

function ProjectCard({ project }: { project: Project }) {
  const scope = projectScopeSummary(project);
  const kindLabel = project.kind === "ctf_challenge" ? "CTF Challenge" : "Pentest";
  return (
    <Link to={`/projects/${project.id}`} aria-label={`Open ${project.name} project dashboard`}
      className="group block rounded-lg border border-border bg-card p-[22px] transition-colors hover:border-signal/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-signal/40">
      <div className="flex items-center justify-between gap-3">
        <span className="flex size-[30px] items-center justify-center rounded border border-signal/15 bg-signal/5 text-[13px] text-signal">{project.name.slice(0, 1).toUpperCase()}</span>
        <span className={`inline-flex items-center gap-1.5 text-[11px] ${scope.ready ? "text-signal" : "text-warning"}`}><span className="size-1.5 rounded-full bg-current" />{scope.ready ? "Scope ready" : "Needs scope"}</span>
      </div>
      <h2 className="mt-4 truncate text-[15px] font-semibold tracking-[-0.2px]">{project.name}</h2>
      <p className="mt-1 min-h-10 text-xs leading-5 text-muted-foreground">{project.description || `${kindLabel} Project`}</p>
      <div className="mt-6 flex items-center justify-between gap-3 text-[11px] text-muted-foreground">
        <span>{kindLabel}</span><span className="truncate font-mono text-[10px]">{formatRelativeTime(project.updated_at)}</span>
      </div>
      <p className="mt-3 border-t border-border pt-3 text-[10px] leading-5 text-muted-foreground">
        {scope.assets.length > 0 ? scope.assets.map((asset) => <span key={asset.label} className="mr-2 inline-block">{asset.label}</span>) : "No scoped assets"}
        {scope.testingLimits > 0 && <span className="mr-2 inline-block">{pluralize(scope.testingLimits, "testing limit")}</span>}
        {scope.hasNotes && <span className="inline-block">Scope notes</span>}
      </p>
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
