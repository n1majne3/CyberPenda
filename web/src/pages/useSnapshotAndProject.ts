import { useEffect, useState } from "react";
import { apiGet, type Project } from "@/lib/api";
import { formatBlackboardV2Error, readSemanticHealth, readSnapshot, type RuntimeSnapshot, type SemanticHealth } from "@/lib/blackboardv2";

export function useSnapshotAndProject(projectId: string) {
  const [snapshot, setSnapshot] = useState<RuntimeSnapshot | null>(null);
  const [health, setHealth] = useState<SemanticHealth | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const [snap, healthDto, proj] = await Promise.all([
          readSnapshot(projectId),
          readSemanticHealth(projectId),
          apiGet<Project>(`/api/projects/${projectId}`),
        ]);
        if (cancelled) return;
        setSnapshot(snap);
        setHealth(healthDto);
        setProject(proj);
        setError(null);
      } catch (e) {
        if (!cancelled) setError(formatBlackboardV2Error(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  return { snapshot, health, project, error, loading };
}
