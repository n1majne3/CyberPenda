import { useEffect, useState } from "react";
import { NavLink, useParams } from "react-router-dom";
import { apiGet, type Project } from "@/lib/api";
import { cn } from "@/lib/utils";

type NavItem = { to: string; label: string; end?: boolean; count?: number };

/**
 * Project navigation order from the operator IA (read contract §19.1):
 * Overview → Tasks → Blackboard → Findings|Solution → Evidence → Report? → Scope.
 */
export function ProjectNav() {
  const { projectId } = useParams<{ projectId: string }>();
  const [kind, setKind] = useState<string>("pentest");
  const [counts, setCounts] = useState<Record<string, number>>({});

  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    (async () => {
      try {
        const [project, dashboard] = await Promise.all([
          apiGet<Project>(`/api/projects/${projectId}`),
          // Counts are advisory chrome; a dashboard failure must not break nav.
          apiGet<{ counts?: { tasks?: number; findings?: number; evidence?: number } }>(
            `/api/projects/${projectId}/dashboard`,
          ).catch(() => ({ counts: {} as { tasks?: number; findings?: number; evidence?: number } })),
        ]);
        if (cancelled) return;
        setKind(project.kind || "pentest");
        setCounts({
          tasks: dashboard.counts?.tasks ?? 0,
          findings: dashboard.counts?.findings ?? 0,
          evidence: dashboard.counts?.evidence ?? 0,
        });
      } catch {
        if (!cancelled) setKind("pentest");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const base = `/projects/${projectId}`;
  const isCTF = kind === "ctf_challenge";
  const links: NavItem[] = [
    { to: "", label: "Overview", end: true },
    { to: "/tasks", label: "Tasks", count: counts.tasks },
    { to: "/blackboard", label: "Blackboard" },
    isCTF
      ? { to: "/solution", label: "Solution" }
      : { to: "/findings", label: "Findings", count: counts.findings },
    { to: "/evidence", label: "Evidence", count: counts.evidence },
    ...(!isCTF ? [{ to: "/report", label: "Report" } satisfies NavItem] : []),
    { to: "/scope", label: "Scope" },
  ];

  return (
    <nav aria-label="Project sections" className="flex w-full flex-wrap gap-1 text-sm">
      {links.map((link) => (
        <NavLink
          key={link.to}
          to={`${base}${link.to}`}
          end={link.end}
          className={({ isActive }) =>
            cn(
              "rounded-md px-3 py-1.5 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
              isActive
                ? "bg-secondary font-medium text-foreground"
                : "text-muted-foreground hover:bg-muted hover:text-foreground",
            )
          }
        >
          {link.label}
          {link.count != null && link.count > 0 && (
            <span className="ml-1 rounded-sm bg-muted px-1 text-[10px]">{link.count}</span>
          )}
        </NavLink>
      ))}
    </nav>
  );
}
