import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Card, CardHeader, CardTitle } from "@/components/ui";
import { ErrorState, LoadingState } from "@/components/shared";
import { apiGet, type ChallengeAttempt, type ChallengeOperationHistory } from "@/lib/api";

type History = { attempts: ChallengeAttempt[]; operations: ChallengeOperationHistory[] };

export function ChallengeWorkflowPage() {
  const { projectId, taskId } = useParams<{ projectId: string; taskId: string }>();
  return <ChallengeHistory key={`${projectId}:${taskId}`} projectId={projectId} taskId={taskId} />;
}

function ChallengeHistory({ projectId, taskId }: { projectId?: string; taskId?: string }) {
  const [history, setHistory] = useState<History | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    void apiGet<History>(`/api/projects/${projectId}/tasks/${taskId}/challenges`)
      .then((data) => { if (!cancelled) setHistory({ attempts: data.attempts ?? [], operations: data.operations ?? [] }); })
      .catch((cause: unknown) => { if (!cancelled) setError(cause instanceof Error ? cause.message : "Could not load history"); });
    return () => { cancelled = true; };
  }, [projectId, taskId]);

  if (error) return <ProjectPageShell><ErrorState error={error} title="Couldn't load Challenge history" /></ProjectPageShell>;
  if (!history) return <ProjectPageShell><LoadingState label="Loading Challenge history" /></ProjectPageShell>;
  const unresolved = history.attempts.some((attempt) => attempt.status !== "finalized") || history.operations.some((operation) => operation.state !== "completed");

  return (
    <ProjectPageShell
      title={<h1 className="text-xl font-semibold tracking-tight">Challenge history</h1>}
      description="Challenge Workflow is retired. These records keep their original state; operations are not replayed."
      actions={<Link className="text-sm text-signal hover:underline" to={`/projects/${projectId}/tasks/${taskId}`}>Back to Task</Link>}
      bodyClassName="space-y-4"
    >
      {unresolved && <p role="alert" className="rounded-md border border-warning/30 bg-warning/10 p-3 text-sm">Unfinished historical operations need review on the original Platform. Task Finish does not confirm Platform completion.</p>}
      <Card as="section">
        <CardHeader><CardTitle>Attempts</CardTitle></CardHeader>
        <div className="space-y-2">
          {history.attempts.map((attempt) => <div key={`${attempt.platform}:${attempt.external_attempt_id}`} className="flex items-center justify-between gap-3 rounded-md border border-border p-3">
            <div><p className="font-mono text-sm">{attempt.external_attempt_id}</p><p className="text-xs text-muted-foreground">{attempt.platform} · Challenge {attempt.challenge_id} · wrong {attempt.wrong_submissions}</p></div>
            <Badge variant="outline">{attempt.status}</Badge>
          </div>)}
          {history.attempts.length === 0 && <p className="text-sm text-muted-foreground">No retained Challenge Attempts.</p>}
        </div>
      </Card>
      <Card as="section">
        <CardHeader><CardTitle>Operations</CardTitle></CardHeader>
        <div className="space-y-2">
          {history.operations.map((operation) => <div key={operation.operation_id} className="rounded-md border border-border p-3">
            <div className="flex items-center justify-between gap-3"><p className="font-mono text-sm">{operation.operation_id}</p><Badge variant="outline">{operation.state}</Badge></div>
            <p className="mt-1 text-xs text-muted-foreground">{operation.platform} · {operation.kind} · Attempt {operation.external_attempt_id || "not recorded"}</p>
            {operation.evidence_key && <p className="mt-1 text-xs">Evidence: {operation.evidence_key}</p>}
          </div>)}
          {history.operations.length === 0 && <p className="text-sm text-muted-foreground">No retained Challenge operations.</p>}
        </div>
      </Card>
      <Link className="text-sm text-signal hover:underline" to={`/projects/${projectId}/evidence`}>View retained Evidence</Link>
    </ProjectPageShell>
  );
}
