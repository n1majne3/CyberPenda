import { useEffect, useState, type RefObject } from "react";
import { Link, useLocation } from "react-router-dom";
import { ChevronRight, Menu, X } from "lucide-react";
import { apiGet, type Project } from "@/lib/api";
import { ThemeToggle } from "@/components/ThemeProvider";

const pageNames: Record<string, string> = {
  tasks: "Tasks", scope: "Scope", blackboard: "Blackboard", facts: "Facts",
  findings: "Findings", evidence: "Evidence", report: "Report", solution: "Solution",
  profiles: "Runtime profiles", "model-providers": "Model providers",
  credentials: "Credentials", skills: "Skills", sessions: "Sessions",
};

export function WorkspaceTopbar({
  navigationOpen, onToggleNavigation, menuButtonRef,
}: {
  navigationOpen: boolean;
  onToggleNavigation: () => void;
  menuButtonRef: RefObject<HTMLButtonElement | null>;
}) {
  const { pathname, hash } = useLocation();
  const parts = pathname.split("/").filter(Boolean);
  const projectId = parts[0] === "projects" ? parts[1] : undefined;
  const [project, setProject] = useState<Project | null>(null);
  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    apiGet<Project>(`/api/projects/${projectId}`).then((value) => {
      if (!cancelled) setProject(value);
    }).catch(() => {});
    return () => { cancelled = true; };
  }, [projectId]);
  const configuration = ["profiles", "model-providers", "credentials", "skills"].includes(parts[0]);
  let title = pageNames[projectId ? parts[2] : parts[0]] ?? (projectId ? "Overview" : "Projects");
  if (projectId && parts[2] === "tasks" && parts[3]) title = parts[3] === "new" ? "New task" : "Task";
  if (parts.at(-1) === "challenges") title = "Challenge history";
  if (parts[0] === "sessions" && parts[1]) title = parts[1] === "archived" ? "Archived Sessions" : "Session";
  if (hash === "#new-session") title = "New session";

  return (
    <header className="flex h-[58px] shrink-0 sm:h-[68px] items-center justify-between gap-4 border-b border-border bg-card px-5 sm:px-9 lg:px-10">
      <div className="flex min-w-0 items-center gap-3">
        <button ref={menuButtonRef} type="button" aria-label={navigationOpen ? "Close navigation" : "Open navigation"}
          aria-expanded={navigationOpen} aria-controls="workspace-sidebar" onClick={onToggleNavigation}
          className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-signal md:hidden">
          {navigationOpen ? <X className="size-4" /> : <Menu className="size-4" />}
        </button>
        <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-3 text-xs text-muted-foreground">
          {projectId ? <Link to="/" className="hidden hover:text-foreground sm:inline">Projects</Link> : <span className="hidden sm:inline">{configuration ? "Configuration" : "Workspace"}</span>}
          <ChevronRight className="hidden size-3 shrink-0 sm:block" aria-hidden="true" />
          {projectId && <><Link to={`/projects/${projectId}`} className="truncate hover:text-foreground">{project?.id === projectId ? project.name : "Project"}</Link><ChevronRight className="size-3 shrink-0" aria-hidden="true" /></>}
          <span aria-current="page" className="truncate text-foreground">{title}</span>
        </nav>
      </div>
      <ThemeToggle />
    </header>
  );
}
