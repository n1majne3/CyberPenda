import { NavLink, Outlet, useRouteError } from "react-router-dom";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { useState, type ReactNode } from "react";
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

function ErrorBoundary() {
  const err = useRouteError() as Error;
  return (
    <div className="p-8">
      <div className="mb-2 flex items-center gap-2 text-destructive">
        <ShieldAlert className="h-5 w-5" />
        <h2 className="text-lg font-semibold">Something went wrong</h2>
      </div>
      <pre className="whitespace-pre-wrap text-sm text-muted-foreground">{err?.message ?? String(err)}</pre>
    </div>
  );
}

function Layout() {
  const [advancedOpen, setAdvancedOpen] = useState(false);

  return (
    <>
      <a
        href="#main-content"
        className="sr-only focus:fixed focus:left-3 focus:top-3 focus:z-50 focus:h-auto focus:w-auto focus:overflow-visible focus:rounded-md focus:bg-background focus:px-3 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground focus:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        Skip to main content
      </a>
      <div className="flex h-svh w-screen overflow-hidden">
      {/* Sidebar uses the shared page chrome surface and fixed navigation rail. */}
      <aside className="flex w-64 shrink-0 flex-col border-r border-sidebar-border bg-sidebar">
        <div className="flex h-12 items-center gap-2 border-b border-sidebar-border px-4">
          <Logo className="h-5 w-5" spin />
          <h1 className="sr-only">CyberPenda</h1>
          <span className="text-sm font-semibold tracking-tight">CyberPenda</span>
        </div>
        <nav className="flex-1 space-y-4 overflow-y-auto p-3">
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
              aria-expanded={advancedOpen}
              onClick={() => setAdvancedOpen((open) => !open)}
              className="mb-1 flex w-full items-center gap-1 rounded-md px-2 text-xs font-medium text-muted-foreground/70 hover:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            >
              <ChevronRight className={`size-3 transition-transform ${advancedOpen ? "rotate-90" : ""}`} />
              Advanced
            </button>
            {advancedOpen && (
              <div className="space-y-0.5">
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
      <main id="main-content" tabIndex={-1} className="flex-1 overflow-y-auto">
        <Outlet />
      </main>
      </div>
    </>
  );
}

function NavSection({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <p className="mb-1 px-2 text-xs font-medium text-muted-foreground/70">{label}</p>
      <div className="space-y-0.5">{children}</div>
    </div>
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
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        `flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${
          isActive
            ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
            : "text-muted-foreground hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground"
        }`
      }
    >
      {icon}
      {children}
    </NavLink>
  );
}

const router = createBrowserRouter([
  {
    element: <Layout />,
    errorElement: <ErrorBoundary />,
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

export default function App() {
  return <RouterProvider router={router} />;
}
