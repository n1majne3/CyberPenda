import { useEffect, useState, type ReactNode } from "react";
import { useParams } from "react-router-dom";
import { apiGet, type Project } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { downloadMarkdown } from "@/lib/download";

export function ProjectReportProtocol({ legacy, fgs }: { legacy: ReactNode; fgs?: ReactNode }) {
  const { projectId = "" } = useParams();
  const [project, setProject] = useState<Project>();
  const [failure, setFailure] = useState<{ projectId: string; message: string }>();
  useEffect(() => {
    let active = true;
    apiGet<Project>(`/api/projects/${projectId}`).then((p) => { if (active) setProject(p); }).catch((e: unknown) => { if (active) setFailure({ projectId, message: e instanceof Error ? e.message : "Cannot load Project" }); });
    return () => { active = false; };
  }, [projectId]);
  if (failure?.projectId === projectId) return <p role="alert">{failure.message}</p>;
  if (!project || project.id !== projectId) return <p className="p-6">Loading report…</p>;
  return project.blackboard_protocol === "fgs" ? fgs ?? <FGSReport key={projectId} projectId={projectId} /> : legacy;
}

function FGSReport({ projectId }: { projectId: string }) {
  const [report, setReport] = useState<{ revision: number; markdown: string }>();
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    apiGet<{ revision: number; markdown: string }>(`/api/v2/projects/${projectId}/fgs/report`).then((value) => { if (active) setReport(value); }).catch((e: unknown) => { if (active) setError(e instanceof Error ? e.message : "Cannot load report"); });
    return () => { active = false; };
  }, [projectId]);
  const download = () => {
    if (!report) return;
    downloadMarkdown(report.markdown, "fgs-report.md");
  };
  return <ProjectPageShell title="Report" bodyClassName="space-y-4">
    <p className="text-sm text-muted-foreground">Accepted Goals, Steps, and Facts from one Blackboard revision.</p>
    {error && <p role="alert">{error}</p>}
    {report ? <><button className="rounded border px-3 py-2 text-sm" onClick={download}>Download Markdown</button><pre className="overflow-auto whitespace-pre-wrap rounded-lg border bg-card p-6 text-sm leading-7">{report.markdown}</pre></> : !error && <p>Loading report…</p>}
  </ProjectPageShell>;
}
