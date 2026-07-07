import { NavLink, useParams } from "react-router-dom";
import { cn } from "@/lib/utils";

const links = [
  { to: "", label: "Dashboard", end: true },
  { to: "/tasks", label: "Tasks", end: false },
  { to: "/scope", label: "Scope", end: false },
  { to: "/facts", label: "Blackboard", end: false },
  { to: "/findings", label: "Findings", end: false },
  { to: "/evidence", label: "Evidence", end: false },
  { to: "/report", label: "Report", end: false },
] as const;

export function ProjectNav() {
  const { projectId } = useParams<{ projectId: string }>();
  const base = `/projects/${projectId}`;

  return (
    <nav className="mb-6 flex flex-wrap gap-1 border-b border-border">
      {links.map((link) => (
        <NavLink
          key={link.to}
          to={`${base}${link.to}`}
          end={link.end}
          className={({ isActive }) =>
            cn(
              "-mb-px border-b-2 px-3 py-2 text-sm transition-colors",
              isActive
                ? "border-primary font-medium text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )
          }
        >
          {link.label}
        </NavLink>
      ))}
    </nav>
  );
}
