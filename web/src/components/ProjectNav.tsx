import { useEffect, useState } from "react";
import { NavLink, useParams } from "react-router-dom";
import { apiGet, type Project } from "@/lib/api";
import { cn } from "@/lib/utils";

type NavItem = { to: string; label: string; end?: boolean };

/**
 * Project navigation order from the operator IA (read contract §19.1):
 * Overview → Tasks → Blackboard → Findings|Solution → Evidence → Report? → Scope.
 */
export function ProjectNav() {
  const { projectId } = useParams<{ projectId: string }>();
  const [kind, setKind] = useState<string>("pentest");

  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    (async () => {
      try {
        const project = await apiGet<Project>(`/api/projects/${projectId}`);
        if (!cancelled) setKind(project.kind || "pentest");
      } catch {
        if (!cancelled) setKind("pentest");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const isCTF = kind === "ctf_challenge";
  const base = `/projects/${projectId}`;
  const links: NavItem[] = [
    { to: "", label: "Overview", end: true },
    { to: "/tasks", label: "Tasks" },
    { to: "/blackboard", label: "Blackboard" },
    isCTF
      ? { to: "/solution", label: "Solution" }
      : { to: "/findings", label: "Findings" },
    { to: "/evidence", label: "Evidence" },
    ...(!isCTF ? [{ to: "/report", label: "Report" } satisfies NavItem] : []),
    { to: "/scope", label: "Scope" },
  ];

  return (
    <nav
      aria-label="Project sections"
      className="flex w-full gap-1 rounded-lg border border-border bg-card p-1 shadow-sm"
    >
      {links.map((link) => (
        <NavLink
          key={link.to}
          to={`${base}${link.to}`}
          end={link.end}
          className={({ isActive }) =>
            cn(
              "min-w-0 flex-1 rounded-md border px-1.5 py-1.5 text-center text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background sm:px-2",
              isActive
                ? "border-border bg-secondary font-medium text-foreground shadow-sm"
                : "border-transparent text-muted-foreground hover:bg-accent hover:text-foreground",
            )
          }
        >
          <span className="block truncate">{link.label}</span>
        </NavLink>
      ))}
    </nav>
  );
}
