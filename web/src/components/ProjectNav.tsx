import { NavLink, useParams } from "react-router-dom";
import { cn } from "@/lib/utils";

const links = [
  { to: "", label: "Dashboard", end: true },
  { to: "/tasks", label: "Tasks", end: false },
  { to: "/scope", label: "Scope", end: false },
  { to: "/facts", label: "Blackboard", end: false },
  { to: "/findings", label: "Findings", end: false },
  { to: "/evidence", label: "Evidence", end: false },
  { to: "/approvals", label: "Approvals", end: false },
  { to: "/audit", label: "Audit log", end: false },
  { to: "/report", label: "Report", end: false },
] as const;

export function ProjectNav() {
  const { projectId } = useParams<{ projectId: string }>();
  const base = `/projects/${projectId}`;

  return (
    <nav className="flex flex-wrap gap-1 mb-6 border-b border-border pb-3">
      {links.map((link) => (
        <NavLink
          key={link.to}
          to={`${base}${link.to}`}
          end={link.end}
          className={({ isActive }) =>
            cn(
              "rounded-md px-3 py-1.5 text-sm transition-colors",
              isActive ? "bg-accent text-accent-foreground font-medium" : "text-muted-foreground hover:bg-accent/50"
            )
          }
        >
          {link.label}
        </NavLink>
      ))}
    </nav>
  );
}