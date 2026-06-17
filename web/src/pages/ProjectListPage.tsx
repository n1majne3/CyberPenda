import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Plus, FolderOpen } from "lucide-react";
import { apiGet, apiPost, type Project } from "@/lib/api";
import { Button, Card, Input, Label, Badge } from "@/components/ui";

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

  useEffect(() => {
    load();
  }, []);

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
    <div className="p-8 max-w-5xl">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-semibold">Projects</h2>
        <Button size="sm" onClick={() => setCreating((v) => !v)}>
          <Plus className="h-4 w-4 mr-1" /> New project
        </Button>
      </div>

      {creating && (
        <Card className="mb-4 flex items-end gap-3">
          <div className="flex-1">
            <Label htmlFor="proj-name">Project name</Label>
            <Input id="proj-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Acme External" />
          </div>
          <Button size="sm" onClick={create} disabled={!name.trim()}>
            Create
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setCreating(false)}>
            Cancel
          </Button>
        </Card>
      )}

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      {projects.length === 0 && !error && (
        <p className="text-sm text-muted-foreground">No projects yet. Create one to get started.</p>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        {projects.map((p) => (
          <Link key={p.id} to={`/projects/${p.id}`}>
            <Card className="hover:bg-accent/50 transition-colors cursor-pointer">
              <div className="flex items-center gap-2 mb-1">
                <FolderOpen className="h-4 w-4 text-primary" />
                <span className="font-medium">{p.name}</span>
              </div>
              {p.description && <p className="text-xs text-muted-foreground">{p.description}</p>}
              <div className="flex gap-1 mt-2">
                {(p.scope.domains?.length ?? 0) > 0 && <Badge variant="primary">{p.scope.domains!.length} domains</Badge>}
                {(p.scope.ips?.length ?? 0) > 0 && <Badge variant="primary">{p.scope.ips!.length} IPs</Badge>}
              </div>
            </Card>
          </Link>
        ))}
      </div>
    </div>
  );
}
