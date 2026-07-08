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
