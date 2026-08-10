import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, CheckCircle2, Flag, RotateCcw, Send, XCircle } from "lucide-react";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Button, Card, CardHeader, CardTitle, Input, Label, Textarea } from "@/components/ui";
import { ErrorState, LoadingState } from "@/components/shared";
import { apiGet, apiPost, type ChallengeAttempt, type FinishReadiness, type Project } from "@/lib/api";

export function ChallengeWorkflowPage() {
  const { projectId, taskId } = useParams<{ projectId: string; taskId: string }>();
  const navigate = useNavigate();
  const [project, setProject] = useState<Project | null>(null);
  const [attempts, setAttempts] = useState<ChallengeAttempt[]>([]);
  const [readiness, setReadiness] = useState<FinishReadiness | null>(null);
  const [platform, setPlatform] = useState("arena");
  const [challengeId, setChallengeId] = useState("");
  const [externalAttemptId, setExternalAttemptId] = useState("");
  const [candidate, setCandidate] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState("");
  const [result, setResult] = useState("");
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!projectId || !taskId) return;
    const [loadedProject, attemptData, finishData] = await Promise.all([
      apiGet<Project>(`/api/projects/${projectId}`),
      apiGet<{ attempts: ChallengeAttempt[] }>(`/api/projects/${projectId}/tasks/${taskId}/challenges`),
      apiGet<FinishReadiness>(`/api/projects/${projectId}/tasks/${taskId}/finish-readiness`),
    ]);
    setProject(loadedProject);
    setAttempts(attemptData.attempts ?? []);
    setReadiness(finishData);
	setExternalAttemptId((current) => current || attemptData.attempts?.[0]?.external_attempt_id || "");
	}, [projectId, taskId]);

	/* eslint-disable react-hooks/set-state-in-effect -- route load synchronizes three external projections. */
  useEffect(() => { void refresh().catch((cause) => setError((cause as Error).message)); }, [refresh]);
	/* eslint-enable react-hooks/set-state-in-effect */

  async function run(operation: "claim" | "submit" | "abandon" | "finalize") {
    if (!projectId || !taskId) return;
    setBusy(operation); setError(null);
    try {
      const response = await apiPost<Record<string, unknown>>(`/api/projects/${projectId}/tasks/${taskId}/challenges/${operation}`, {
        platform,
        operation_id: newOperationID(operation),
        ...(operation === "claim" ? { challenge_id: challengeId } : { external_attempt_id: externalAttemptId }),
        ...(operation === "submit" ? { candidate } : {}),
        ...(operation === "abandon" ? { reason } : {}),
      });
      setResult(JSON.stringify(response, null, 2));
      if (typeof response.external_attempt_id === "string") setExternalAttemptId(response.external_attempt_id);
      await refresh();
    } catch (cause) { setError((cause as Error).message); }
    finally { setBusy(""); }
  }

  if (error && !project) return <ProjectPageShell><ErrorState error={error} title="Couldn't load Challenge Workflow" /></ProjectPageShell>;
  if (!project) return <ProjectPageShell><LoadingState label="Loading Challenge Workflow" /></ProjectPageShell>;

  return (
    <ProjectPageShell
      title={<h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight"><Flag className="h-5 w-5 text-signal" /> Challenge Workflow</h1>}
      description="Use durable claim, submit, abandon, and finalize operations. Each Platform response is retained as Evidence."
      actions={<Button size="sm" variant="outline" onClick={() => navigate(`/projects/${projectId}/tasks/${taskId}`)}><ArrowLeft className="h-4 w-4" /> Task</Button>}
      bodyClassName="space-y-4"
    >
      {project.kind !== "ctf_challenge" && <p role="alert" className="rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-warning">Challenge Workflow requires a CTF Challenge Project.</p>}
      <Card as="section">
        <CardHeader><CardTitle>Platform operation</CardTitle></CardHeader>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div><Label htmlFor="challenge-platform">Platform</Label><Input id="challenge-platform" value={platform} onChange={(event) => setPlatform(event.target.value)} /></div>
          <div><Label htmlFor="challenge-id">Challenge ID</Label><Input id="challenge-id" value={challengeId} onChange={(event) => setChallengeId(event.target.value)} /></div>
          <div className="md:col-span-2"><Button onClick={() => void run("claim")} disabled={busy !== "" || project.kind !== "ctf_challenge" || !challengeId.trim()}><Flag className="h-4 w-4" /> {busy === "claim" ? "Claiming…" : "Claim"}</Button></div>
          <div className="md:col-span-2"><Label htmlFor="external-attempt">External Attempt ID</Label><Input id="external-attempt" value={externalAttemptId} onChange={(event) => setExternalAttemptId(event.target.value)} /></div>
          <div className="md:col-span-2"><Label htmlFor="candidate">Candidate</Label><Input id="candidate" type="password" value={candidate} onChange={(event) => setCandidate(event.target.value)} autoComplete="off" /></div>
          <div className="flex flex-wrap gap-2 md:col-span-2"><Button onClick={() => void run("submit")} disabled={busy !== "" || !externalAttemptId || !candidate}><Send className="h-4 w-4" /> Submit</Button><Button variant="outline" onClick={() => void run("finalize")} disabled={busy !== "" || !externalAttemptId}><CheckCircle2 className="h-4 w-4" /> Finalize</Button></div>
          <div className="md:col-span-2"><Label htmlFor="abandon-reason">Abandon reason</Label><Textarea id="abandon-reason" value={reason} onChange={(event) => setReason(event.target.value)} /></div>
          <div className="md:col-span-2"><Button variant="destructive" onClick={() => void run("abandon")} disabled={busy !== "" || !externalAttemptId || !reason.trim()}><XCircle className="h-4 w-4" /> Abandon</Button></div>
        </div>
        {error && <p role="alert" className="mt-3 text-sm text-destructive">{error}</p>}
        {result && <pre className="mt-3 overflow-auto rounded-md border border-border bg-muted/20 p-3 text-xs">{result}</pre>}
      </Card>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card as="section"><CardHeader><CardTitle>Attempts</CardTitle></CardHeader><div className="space-y-2">{attempts.map((attempt) => <button type="button" key={`${attempt.platform}:${attempt.external_attempt_id}`} onClick={() => { setPlatform(attempt.platform); setExternalAttemptId(attempt.external_attempt_id); }} className="flex w-full items-center justify-between rounded-md border border-border p-2 text-left"><span><span className="font-mono text-sm">{attempt.external_attempt_id}</span><span className="block text-xs text-muted-foreground">Challenge {attempt.challenge_id} · wrong {attempt.wrong_submissions}</span></span><Badge variant={attempt.status === "succeeded" ? "success" : attempt.status === "open" ? "warning" : "outline"}>{attempt.status}</Badge></button>)}{attempts.length === 0 && <p className="text-sm text-muted-foreground">No Challenge Attempts.</p>}</div></Card>
		<Card as="section"><CardHeader><CardTitle className="flex items-center justify-between">Finish Readiness <Button size="icon" variant="ghost" onClick={() => void refresh()} aria-label="Refresh Finish Readiness"><RotateCcw className="h-4 w-4" /></Button></CardTitle></CardHeader>{readiness?.ready_to_finish ? <p className="flex items-center gap-2 text-sm text-success"><CheckCircle2 className="h-4 w-4" /> Ready to finish</p> : <div className="space-y-2">{readiness?.blockers.map((blocker) => <div key={blocker.code} className="rounded-md border border-warning/30 bg-warning/5 p-2"><p className="text-sm font-medium">{blocker.message}</p><p className="font-mono text-xs text-muted-foreground">{blocker.code} · {blocker.count}</p>{blocker.links?.map((link) => <a key={link} href={link} className="mt-1 inline-block text-xs font-medium text-signal hover:underline">Open related surface</a>)}</div>)}</div>}</Card>
      </div>
    </ProjectPageShell>
  );
}

function newOperationID(operation: string): string {
  const id = typeof crypto !== "undefined" && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `${operation}-${id}`;
}
