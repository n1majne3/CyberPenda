import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Plus, FolderOpen, Globe, Server } from "lucide-react";
import { apiGet, apiPost, type Project } from "@/lib/api";
import { Button, Card, Input, Label, Badge } from "@/components/ui";
import { PageContainer } from "@/components/shared";

export function ProjectListPage() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");

  async function load() {
    try {
      const data = await apiGet<{ projects: Project[] }>("/api/projects");
      setProjects(data.projects ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
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
    <PageContainer className="mx-auto max-w-5xl">
      <div className="mb-6 flex items-center justify-between">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">Projects</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Bounded security-testing engagements, each with its own scope, tasks, and memory.
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating((v) => !v)}>
          <Plus className="h-4 w-4" /> New project
        </Button>
      </div>

      {creating && (
        <Card className="mb-4 flex flex-row items-end gap-3 !py-3">
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

      {error && <p className="mb-4 text-sm text-destructive">{error}</p>}

      {projects.length === 0 && !error && !creating && (
        <Card className="flex flex-col items-center justify-center gap-3 !py-16 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-muted">
            <FolderOpen className="h-6 w-6 text-muted-foreground" />
          </div>
          <div>
            <p className="text-sm font-medium">No projects yet</p>
            <p className="mt-1 text-sm text-muted-foreground">Create one to get started.</p>
          </div>
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-4 w-4" /> New project
          </Button>
        </Card>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        {sortNewestFirst(projects).map((p) => (
          <Link key={p.id} to={`/projects/${p.id}`} className="group">
            <Card className="h-full transition-[background-color,box-shadow] hover:bg-accent/40 hover:ring-foreground/20">
              <div className="flex items-center gap-2">
                <FolderOpen className="h-4 w-4 text-primary" />
                <span className="font-medium group-hover:text-foreground">{p.name}</span>
              </div>
              {p.description && <p className="text-sm text-muted-foreground">{p.description}</p>}
              <div className="flex flex-wrap gap-1">
                {(p.scope.domains?.length ?? 0) > 0 && (
                  <Badge variant="outline">
                    <Globe className="h-3 w-3" />
                    {p.scope.domains!.length} domains
                  </Badge>
                )}
                {(p.scope.ips?.length ?? 0) > 0 && (
                  <Badge variant="outline">
                    <Server className="h-3 w-3" />
                    {p.scope.ips!.length} IPs
                  </Badge>
                )}
              </div>
            </Card>
          </Link>
        ))}
      </div>
    </PageContainer>
  );
}

function sortNewestFirst<T extends { created_at: string }>(items: T[]): T[] {
  return [...items].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
}
