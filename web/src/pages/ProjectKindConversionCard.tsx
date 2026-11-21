import { useLayoutEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { apiPost, type Project, type ProjectKind } from "@/lib/api";
import { Button, Card, CardDescription, CardTitle } from "@/components/ui";

interface Preview {
  current_kind: ProjectKind;
  target_kind: ProjectKind;
  ready: boolean;
  blockers: Array<{ code: string; count: number }>;
}

const kindLabel = (kind?: ProjectKind) => kind === "ctf_challenge" ? "CTF Challenge" : "Pentest";

export function ProjectKindConversionCard({ project, onConverted }: {
  project: Project;
  onConverted: (project: Project) => void;
}) {
  const [preview, setPreview] = useState<Preview | null>(null);
  const [error, setError] = useState<{ message: string; action: "preview" | "confirm" } | null>(null);
  const [pending, setPending] = useState<"preview" | "confirm" | null>(null);
  const [success, setSuccess] = useState("");
  const busy = useRef(false);
  const changeButton = useRef<HTMLButtonElement>(null);
  const previewPanel = useRef<HTMLDivElement>(null);
  const base = `/projects/${project.id}`;
  const target: ProjectKind = project.kind === "ctf_challenge" ? "pentest" : "ctf_challenge";

  useLayoutEffect(() => {
    if (preview) previewPanel.current?.focus();
  }, [preview]);

  async function request(action: "preview" | "confirm") {
    if (busy.current || (action === "confirm" && (!preview?.ready || preview.blockers.length > 0))) return;
    busy.current = true;
    setPending(action);
    setError(null);
    setSuccess("");
    try {
      if (action === "preview") {
        const result = await apiPost<Preview>(`/api/projects/${project.id}/kind-conversion/preview`, { target_kind: target });
        setPreview(result);
      } else {
        const result = await apiPost<Project>(`/api/projects/${project.id}/kind-conversion`, {
          target_kind: preview!.target_kind,
          confirm: true,
        });
        onConverted(result);
        setPreview(null);
        setSuccess(`Project Kind changed to ${kindLabel(result.kind)}.`);
        changeButton.current?.focus();
      }
    } catch (cause) {
      setError({ message: (cause as Error).message, action });
    } finally {
      busy.current = false;
      setPending(null);
    }
  }

  return (
    <Card role="region" aria-labelledby="project-kind-title" className="min-w-0 gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <CardTitle id="project-kind-title">Project kind</CardTitle>
          <CardDescription className="mt-1">
            Current Project Kind: {kindLabel(project.kind)}.
          </CardDescription>
        </div>
        <Button ref={changeButton} variant="outline" size="sm" onClick={() => request("preview")} disabled={!!pending}>
          {pending === "preview" ? "Checking…" : preview ? "Re-check" : "Change Project kind"}
        </Button>
      </div>
      {success && <p role="status" className="text-sm text-success">{success}</p>}
      {pending && <p role="status" className="text-sm text-muted-foreground">{pending === "preview" ? "Checking…" : "Converting…"}</p>}
      {error && (
        <div role="alert" className="rounded-md border border-destructive/25 p-3 text-sm">
          <p>{error.message}</p>
          <Button className="mt-2" variant="outline" size="sm" disabled={!!pending} onClick={() => request(error.action)}>
            Retry {error.action === "preview" ? "check" : "conversion"}
          </Button>
        </div>
      )}
      {preview && (
        <div ref={previewPanel} tabIndex={-1} aria-label="Project Kind Conversion preview" className="space-y-3 rounded-md border border-border bg-muted/40 p-3 text-sm">
          <p className="font-medium">{kindLabel(preview.current_kind)} → {kindLabel(preview.target_kind)}</p>
          <p>Only Project Kind changes. Historical Task Types stay unchanged. Findings and Solutions are not converted into each other.</p>
          {preview.ready && preview.blockers.length === 0 ? (
            <>
              <p>Conversion is ready. No non-terminal Task or incompatible Project Knowledge blocks it.</p>
              <Button size="sm" disabled={!!pending} onClick={() => request("confirm")}>
                {pending === "confirm" ? "Converting…" : `Confirm conversion to ${kindLabel(preview.target_kind)}`}
              </Button>
            </>
          ) : (
            <>
              <p className="font-medium text-warning">Conversion is blocked.</p>
              <ul className="list-disc space-y-2 pl-5 text-muted-foreground">
                {preview.blockers.map((blocker) => (
                  <li key={blocker.code}>
                    {blocker.code === "active_tasks" ? (
                      <>{blocker.count} non-terminal Tasks. <Link className="underline" to={`${base}/tasks`}>Review Tasks</Link> and finish or stop them before you re-check.</>
                    ) : blocker.code === "incompatible_findings" ? (
                      <>{blocker.count} current Findings. <Link className="underline" to={`${base}/findings`}>Review Findings</Link>. Resolve incompatible current Findings before you re-check.</>
                    ) : blocker.code === "incompatible_solutions" ? (
                      <>{blocker.count} current Solutions. <Link className="underline" to={`${base}/blackboard`}>Review Solutions in Blackboard</Link>. Resolve incompatible current Solutions before you re-check.</>
                    ) : (
                      <>{blocker.code}: {blocker.count}. Review Project Knowledge and re-check before conversion.</>
                    )}
                  </li>
                ))}
              </ul>
            </>
          )}
          <Button className="ml-2" variant="outline" size="sm" disabled={!!pending} onClick={() => {
            setPreview(null);
            setError(null);
            changeButton.current?.focus();
          }}>Cancel</Button>
        </div>
      )}
    </Card>
  );
}
