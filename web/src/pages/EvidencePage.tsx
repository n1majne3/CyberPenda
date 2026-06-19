import { useEffect, useState } from "react";
import { useParams, Link } from "react-router-dom";
import { ArrowLeft, FolderLock } from "lucide-react";
import { apiGet, type EvidenceArtifact } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { Card, Badge } from "@/components/ui";

export function EvidencePage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [evidence, setEvidence] = useState<EvidenceArtifact[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const d = await apiGet<{ evidence: EvidenceArtifact[] }>(`/api/projects/${projectId}/evidence`);
        setEvidence(d.evidence ?? []);
        setError(null);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, [projectId]);

  const byTarget = evidence.reduce<Record<string, EvidenceArtifact[]>>((acc, e) => {
    const key = `${e.attach_to_type}: ${e.attach_to_key}`;
    (acc[key] ||= []).push(e);
    return acc;
  }, {});

  return (
    <div className="p-8 max-w-4xl">
      <Link to={`/projects/${projectId}`} className="inline-flex items-center text-sm text-muted-foreground hover:text-foreground mb-4">
        <ArrowLeft className="h-4 w-4 mr-1" /> Back to dashboard
      </Link>
      <ProjectNav />
      <h2 className="text-xl font-semibold mb-6">Evidence</h2>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      {Object.entries(byTarget).map(([target, items]) => (
        <div key={target} className="mb-4">
          <h3 className="text-sm font-medium text-muted-foreground mb-2">{target}</h3>
          <div className="space-y-2">
            {items.map((e) => (
              <Card key={e.id} className="flex items-center gap-3">
                <FolderLock className="h-4 w-4 text-primary shrink-0" />
                <div className="flex-1 min-w-0">
                  <p className="text-sm">{e.summary || e.evidence_key}</p>
                  <p className="text-xs text-muted-foreground font-mono truncate">{e.managed_path}</p>
                  {e.created_at && <p className="text-xs text-muted-foreground">{new Date(e.created_at).toLocaleString()}</p>}
                </div>
                <Badge variant="outline">{e.artifact_type}</Badge>
                {e.sha256 && <Badge variant="outline">sha256: {e.sha256.slice(0, 8)}</Badge>}
              </Card>
            ))}
          </div>
        </div>
      ))}
      {evidence.length === 0 && !error && (
        <p className="text-sm text-muted-foreground">No evidence attached. Runtime workdir files require explicit attach or retain.</p>
      )}
    </div>
  );
}
