import { useEffect, useState, type ReactNode } from "react";
import { useParams } from "react-router-dom";
import { apiGet, type Project } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, Card } from "@/components/ui";
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
  return (
    <ProjectPageShell
      title="Report"
      description="Accepted Goals, Steps, and Facts from one Blackboard revision."
      actions={report && <Button variant="outline" size="sm" onClick={download}>Download Markdown</Button>}
      bodyClassName="space-y-6"
    >
      {error && <p role="alert">{error}</p>}
      {report ? (
        <Card as="section" className="mx-auto max-w-4xl gap-6 p-6 sm:p-10">
          <p className="text-xs text-muted-foreground">Blackboard revision {report.revision}</p>
          <pre className="whitespace-pre-wrap break-words font-sans text-sm leading-7">{report.markdown}</pre>
        </Card>
      ) : !error && <p>Loading report…</p>}
    </ProjectPageShell>
  );
}
