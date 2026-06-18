import { NavLink, Outlet, useRouteError } from "react-router-dom";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { ShieldAlert } from "lucide-react";
import { ProjectListPage } from "@/pages/ProjectListPage";
import { ProjectDashboardPage } from "@/pages/ProjectDashboardPage";
import { ScopeEditorPage } from "@/pages/ScopeEditorPage";
import { RuntimeProfilesPage } from "@/pages/RuntimeProfilesPage";
import { CredentialBindingsPage } from "@/pages/CredentialBindingsPage";
import { TaskLaunchPage } from "@/pages/TaskLaunchPage";
import { TaskDetailPage } from "@/pages/TaskDetailPage";
import { FactsPage } from "@/pages/FactsPage";
import { FindingsPage } from "@/pages/FindingsPage";
import { EvidencePage } from "@/pages/EvidencePage";
import { ReportPage } from "@/pages/ReportPage";
import { TasksPage } from "@/pages/TasksPage";
import { ApprovalsPage } from "@/pages/ApprovalsPage";
import { AuditLogPage } from "@/pages/AuditLogPage";

function ErrorBoundary() {
  const err = useRouteError() as Error;
  return (
    <div className="p-8">
      <div className="flex items-center gap-2 text-destructive mb-2">
        <ShieldAlert className="h-5 w-5" />
        <h2 className="text-lg font-semibold">Something went wrong</h2>
      </div>
      <pre className="text-sm text-muted-foreground whitespace-pre-wrap">{err?.message ?? String(err)}</pre>
    </div>
  );
}

function Layout() {
  return (
    <div className="flex h-screen w-screen overflow-hidden">
      <aside className="w-56 shrink-0 border-r border-border bg-card flex flex-col">
        <div className="p-4 border-b border-border">
          <h1 className="text-base font-semibold flex items-center gap-2">
            <ShieldAlert className="h-5 w-5 text-primary" />
            pentest
          </h1>
        </div>
        <nav className="flex-1 p-2 space-y-0.5 overflow-y-auto">
          <SideLink to="/">Projects</SideLink>
          <SideLink to="/profiles">Runtime profiles</SideLink>
          <SideLink to="/credentials">Credentials</SideLink>
        </nav>
      </aside>
      <main className="flex-1 overflow-y-auto">
        <Outlet />
      </main>
    </div>
  );
}

function SideLink({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        `block rounded-md px-3 py-1.5 text-sm ${
          isActive ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-accent/50"
        }`
      }
    >
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
      { path: "/credentials", element: <CredentialBindingsPage /> },
      { path: "/projects/:projectId", element: <ProjectDashboardPage /> },
      { path: "/projects/:projectId/scope", element: <ScopeEditorPage /> },
      { path: "/projects/:projectId/tasks", element: <TasksPage /> },
      { path: "/projects/:projectId/tasks/new", element: <TaskLaunchPage /> },
      { path: "/projects/:projectId/approvals", element: <ApprovalsPage /> },
      { path: "/projects/:projectId/audit", element: <AuditLogPage /> },
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
