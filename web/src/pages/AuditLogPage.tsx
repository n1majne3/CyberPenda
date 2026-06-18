import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, ScrollText } from "lucide-react";
import { apiGet, type AuditEntry } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { Badge, Card } from "@/components/ui";

export function AuditLogPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId) return;
    apiGet<{ entries: AuditEntry[] }>(`/api/projects/${projectId}/audit-log`)
      .then((d) => {
        setEntries(d.entries ?? []);
        setError(null);
      })
      .catch((e) => setError((e as Error).message));
  }, [projectId]);

  return (
    <div className="p-8 max-w-4xl">
      <Link to="/" className="inline-flex items-center text-sm text-muted-foreground hover:text-foreground mb-4">
        <ArrowLeft className="h-4 w-4 mr-1" /> All projects
      </Link>
      <ProjectNav />

      <h2 className="text-xl font-semibold flex items-center gap-2 mb-4">
        <ScrollText className="h-5 w-5" /> Audit log
      </h2>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      {entries.length === 0 && !error && (
        <p className="text-sm text-muted-foreground">No audit entries yet.</p>
      )}

      <div className="space-y-2">
        {entries.map((entry) => (
          <Card key={entry.id}>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap gap-1 mb-1">
                  <Badge variant="outline">{entry.kind}</Badge>
                  {entry.task_id && <Badge variant="primary">task</Badge>}
                </div>
                <p className="text-sm">{entry.summary}</p>
                <p className="text-xs text-muted-foreground mt-1">
                  {new Date(entry.created_at).toLocaleString()}
                </p>
              </div>
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}