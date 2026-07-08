import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { FolderLock } from "lucide-react";
import { apiGet, type EvidenceArtifact } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { BackLink, PageContainer } from "@/components/shared";
import { Card, Badge, CardHeader, CardTitle } from "@/components/ui";
import { formatDateTime } from "@/lib/format";

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
    <PageContainer className="max-w-4xl space-y-6">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>
      <ProjectNav />
      <div>
        <h2 className="text-xl font-semibold tracking-tight">Evidence</h2>
      </div>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      {Object.entries(byTarget).map(([target, items]) => (
        <section key={target} className="space-y-2">
          <h3 className="text-sm font-medium text-muted-foreground">{target}</h3>
          <div className="space-y-2">
            {items.map((e) => (
              <Card key={e.id} as="article" className="flex-col gap-3 sm:flex-row sm:items-center">
                <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-primary/5 text-primary">
                  <FolderLock className="h-4 w-4" />
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm">{e.summary || e.evidence_key}</p>
                  <p className="text-xs text-muted-foreground font-mono truncate">{e.managed_path}</p>
                  {e.created_at && <p className="text-xs text-muted-foreground">{formatDateTime(e.created_at)}</p>}
                </div>
                <div className="flex max-w-full flex-wrap gap-1 sm:justify-end">
                  <Badge variant="outline">{e.artifact_type}</Badge>
                  {e.sha256 && <Badge variant="outline" className="max-w-full truncate">sha256: {e.sha256.slice(0, 8)}</Badge>}
                </div>
              </Card>
            ))}
          </div>
        </section>
      ))}
      {evidence.length === 0 && !error && (
        <Card as="section" variant="flat" className="border-dashed bg-muted/30 text-sm text-muted-foreground">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <FolderLock className="h-4 w-4" /> No evidence attached.
            </CardTitle>
          </CardHeader>
          Runtime workdir files require explicit attach or retain.
        </Card>
      )}
    </PageContainer>
  );
}
