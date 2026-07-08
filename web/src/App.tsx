import { NavLink, Outlet, useLocation, useRouteError } from "react-router-dom";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { useMemo, useState, type ReactNode } from "react";
import { ShieldAlert, FolderKanban, Cpu, KeyRound, BookOpen, Network, ChevronRight } from "lucide-react";
import { ProjectListPage } from "@/pages/ProjectListPage";
import { ProjectDashboardPage } from "@/pages/ProjectDashboardPage";
import { ScopeEditorPage } from "@/pages/ScopeEditorPage";
import { RuntimeProfilesPage } from "@/pages/RuntimeProfilesPage";
import { ModelProvidersPage } from "@/pages/ModelProvidersPage";
import { CredentialBindingsPage } from "@/pages/CredentialBindingsPage";
import { SkillsPage } from "@/pages/SkillsPage";
import { TaskLaunchPage } from "@/pages/TaskLaunchPage";
import { TaskDetailPage } from "@/pages/TaskDetailPage";
import { FactsPage } from "@/pages/FactsPage";
import { FindingsPage } from "@/pages/FindingsPage";
import { EvidencePage } from "@/pages/EvidencePage";
import { ReportPage } from "@/pages/ReportPage";
import { TasksPage } from "@/pages/TasksPage";
import { Logo } from "@/components/Logo";
import { ThemeToggle } from "@/components/ThemeProvider";

export function ShellErrorBoundary() {
  const err = useRouteError() as Error;
  return (
    <div className="flex min-h-svh items-start justify-center bg-background p-8 text-foreground">
      <div
        role="alert"
        className="w-full max-w-2xl rounded-lg border border-destructive/25 bg-card p-5 text-card-foreground shadow-sm"
      >
        <div className="mb-2 flex items-center gap-2 text-destructive">
          <ShieldAlert className="h-5 w-5" aria-hidden="true" />
          <h2 className="text-lg font-semibold">Something went wrong</h2>
        </div>
        <pre className="whitespace-pre-wrap text-sm text-muted-foreground">{err?.message ?? String(err)}</pre>
      </div>
    </div>
  );
}

export function ShellLayout() {
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const advancedState = advancedOpen ? "open" : "closed";

  return (
    <>
      <a
        href="#main-content"
        className="sr-only focus:fixed focus:left-3 focus:top-3 focus:z-50 focus:h-auto focus:w-auto focus:overflow-visible focus:rounded-md focus:border focus:border-border focus:bg-background focus:px-3 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground focus:shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      >
        Skip to main content
      </a>
      <div className="flex h-svh w-screen overflow-hidden bg-background text-foreground">
        <aside
          aria-label="CyberPenda workspace"
          className="flex w-64 shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground shadow-sm"
        >
          <div className="flex h-14 items-center gap-2 border-b border-sidebar-border px-4">
            <Logo className="h-5 w-5" spin />
            <h1 className="text-sm font-semibold">CyberPenda</h1>
          </div>
          <nav aria-label="Primary routes" className="flex-1 space-y-5 overflow-y-auto p-3">
            <NavSection label="Workspace">
              <SideLink to="/" icon={<FolderKanban className="size-4" />}>
                Projects
              </SideLink>
            </NavSection>
            <NavSection label="Settings">
              <SideLink to="/model-providers" icon={<Network className="size-4" />}>
                Model providers
              </SideLink>
              <SideLink to="/credentials" icon={<KeyRound className="size-4" />}>
                Credentials
              </SideLink>
              <SideLink to="/skills" icon={<BookOpen className="size-4" />}>
                Skills
              </SideLink>
            </NavSection>
            <div>
              <button
                type="button"
                aria-label={advancedOpen ? "Hide advanced routes" : "Show advanced routes"}
                aria-controls="advanced-routes"
                aria-expanded={advancedOpen}
                data-state={advancedState}
                onClick={() => setAdvancedOpen((open) => !open)}
                className="mb-1 flex h-8 w-full items-center gap-2 rounded-md border border-transparent px-2 text-xs font-medium text-muted-foreground transition-colors hover:border-sidebar-border hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar data-[state=open]:border-sidebar-border data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
              >
                <ChevronRight
                  aria-hidden="true"
                  className={`size-3 transition-transform ${advancedOpen ? "rotate-90" : ""}`}
                />
                Advanced
              </button>
              {advancedOpen && (
                <div id="advanced-routes" className="space-y-1">
                  <SideLink to="/profiles" icon={<Cpu className="size-4" />}>
                    Runtime profiles
                  </SideLink>
                </div>
              )}
            </div>
          </nav>
          <div className="flex items-center justify-between border-t border-sidebar-border px-3 py-2">
            <span className="px-1 text-xs text-muted-foreground">Theme</span>
            <ThemeToggle />
          </div>
        </aside>
        <main id="main-content" tabIndex={-1} className="flex-1 overflow-y-auto bg-background">
          <Outlet />
        </main>
      </div>
    </>
  );
}

function NavSection({ label, children }: { label: string; children: ReactNode }) {
  const headingId = `${label.toLowerCase().replace(/\s+/g, "-")}-navigation`;

  return (
    <section aria-labelledby={headingId}>
      <p id={headingId} className="mb-1 px-2 text-xs font-medium text-muted-foreground/80">
        {label}
      </p>
      <div className="space-y-1">{children}</div>
    </section>
  );
}

function SideLink({
  to,
  icon,
  children,
}: {
  to: string;
  icon: ReactNode;
  children: ReactNode;
}) {
  const { pathname } = useLocation();
  const isCurrentPath = pathname === to;

  return (
    <NavLink
      to={to}
      end={to === "/"}
      data-active={isCurrentPath ? "true" : "false"}
      className={({ isActive }) =>
        `group relative flex h-9 items-center gap-2 rounded-md border px-2 py-1.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar ${
          isActive
            ? "border-sidebar-border bg-sidebar-accent font-semibold text-sidebar-accent-foreground"
            : "border-transparent text-muted-foreground hover:border-sidebar-border hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground"
        }`
      }
    >
      {({ isActive }) => (
        <>
          <span
            aria-hidden="true"
            data-nav-indicator={isActive ? "active" : undefined}
            className={`absolute left-0 top-1/2 h-4 w-0.5 -translate-y-1/2 rounded-full bg-sidebar-accent-foreground transition-opacity ${
              isActive ? "opacity-100" : "opacity-0"
            }`}
          />
          <span aria-hidden="true" className="shrink-0">
            {icon}
          </span>
          <span className="truncate">{children}</span>
        </>
      )}
    </NavLink>
  );
}

function createAppRouter() {
  return createBrowserRouter([
    {
      element: <ShellLayout />,
      errorElement: <ShellErrorBoundary />,
      children: [
        { path: "/", element: <ProjectListPage /> },
        { path: "/profiles", element: <RuntimeProfilesPage /> },
        { path: "/model-providers", element: <ModelProvidersPage /> },
        { path: "/credentials", element: <CredentialBindingsPage /> },
        { path: "/skills", element: <SkillsPage /> },
        { path: "/projects/:projectId", element: <ProjectDashboardPage /> },
        { path: "/projects/:projectId/scope", element: <ScopeEditorPage /> },
        { path: "/projects/:projectId/tasks", element: <TasksPage /> },
        { path: "/projects/:projectId/tasks/new", element: <TaskLaunchPage /> },
        { path: "/projects/:projectId/tasks/:taskId", element: <TaskDetailPage /> },
        { path: "/projects/:projectId/facts", element: <FactsPage /> },
        { path: "/projects/:projectId/findings", element: <FindingsPage /> },
        { path: "/projects/:projectId/evidence", element: <EvidencePage /> },
        { path: "/projects/:projectId/report", element: <ReportPage /> },
      ],
    },
  ]);
}

export default function App() {
  const router = useMemo(() => createAppRouter(), []);
  return <RouterProvider router={router} />;
}
