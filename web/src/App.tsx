import { Outlet, useRouteError } from "react-router-dom";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { ShieldAlert, Menu, X } from "lucide-react";
import { Logo } from "@/components/Logo";
import { WorkspaceSidebar } from "@/components/WorkspaceSidebar";
import { cn } from "@/lib/utils";

const ProjectListPage = lazy(() =>
  import("@/pages/ProjectListPage").then(({ ProjectListPage }) => ({ default: ProjectListPage })),
);
const SessionHomePage = lazy(() =>
  import("@/pages/SessionHomePage").then(({ SessionHomePage }) => ({ default: SessionHomePage })),
);
const SessionDetailPage = lazy(() =>
  import("@/pages/SessionDetailPage").then(({ SessionDetailPage }) => ({ default: SessionDetailPage })),
);
const RuntimeProfilesPage = lazy(() =>
  import("@/pages/RuntimeProfilesPage").then(({ RuntimeProfilesPage }) => ({ default: RuntimeProfilesPage })),
);
const ModelProvidersPage = lazy(() =>
  import("@/pages/ModelProvidersPage").then(({ ModelProvidersPage }) => ({ default: ModelProvidersPage })),
);
const CredentialBindingsPage = lazy(() =>
  import("@/pages/CredentialBindingsPage").then(({ CredentialBindingsPage }) => ({ default: CredentialBindingsPage })),
);
const SkillsPage = lazy(() =>
  import("@/pages/SkillsPage").then(({ SkillsPage }) => ({ default: SkillsPage })),
);
const ProjectDashboardPage = lazy(() =>
  import("@/pages/ProjectDashboardPage").then(({ ProjectDashboardPage }) => ({ default: ProjectDashboardPage })),
);
const ScopeEditorPage = lazy(() =>
  import("@/pages/ScopeEditorPage").then(({ ScopeEditorPage }) => ({ default: ScopeEditorPage })),
);
const TasksPage = lazy(() =>
  import("@/pages/TasksPage").then(({ TasksPage }) => ({ default: TasksPage })),
);
const TaskLaunchPage = lazy(() =>
  import("@/pages/TaskLaunchPage").then(({ TaskLaunchPage }) => ({ default: TaskLaunchPage })),
);
const TaskDetailPage = lazy(() =>
  import("@/pages/TaskDetailPage").then(({ TaskDetailPage }) => ({ default: TaskDetailPage })),
);
const ChallengeWorkflowPage = lazy(() =>
  import("@/pages/ChallengeWorkflowPage").then(({ ChallengeWorkflowPage }) => ({ default: ChallengeWorkflowPage })),
);
const FactsPage = lazy(() =>
  import("@/pages/FactsPage").then(({ FactsPage }) => ({ default: FactsPage })),
);
const BlackboardPage = lazy(() =>
  import("@/pages/BlackboardPage").then(({ BlackboardPage }) => ({ default: BlackboardPage })),
);
const FindingsPage = lazy(() =>
  import("@/pages/FindingsPage").then(({ FindingsPage }) => ({ default: FindingsPage })),
);
const EvidencePage = lazy(() =>
  import("@/pages/EvidencePage").then(({ EvidencePage }) => ({ default: EvidencePage })),
);
const ReportPage = lazy(() =>
  import("@/pages/ReportPage").then(({ ReportPage }) => ({ default: ReportPage })),
);
const SolutionPage = lazy(() =>
  import("@/pages/SolutionPage").then(({ SolutionPage }) => ({ default: SolutionPage })),
);

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

function useIsDesktopMd() {
  const [isDesktop, setIsDesktop] = useState(() => {
    if (typeof window === "undefined" || !window.matchMedia) return false;
    return window.matchMedia("(min-width: 768px)").matches;
  });

  useEffect(() => {
    if (!window.matchMedia) return;
    const mq = window.matchMedia("(min-width: 768px)");
    const onChange = () => setIsDesktop(mq.matches);
    onChange();
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);

  return isDesktop;
}

export function ShellLayout() {
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const isDesktop = useIsDesktopMd();
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const sidebarRef = useRef<HTMLElement>(null);
  // On mobile, a closed off-canvas drawer must leave the a11y tree; desktop is always open.
  const sidebarAvailable = isDesktop || mobileNavOpen;

  const closeMobileNav = (options?: { restoreFocus?: boolean }) => {
    setMobileNavOpen(false);
    if (options?.restoreFocus !== false) {
      // Restore focus after close so keyboard users return to the opener.
      queueMicrotask(() => menuButtonRef.current?.focus());
    }
  };

  const openMobileNav = () => {
    setMobileNavOpen(true);
  };

  const toggleMobileNav = () => {
    if (mobileNavOpen) {
      closeMobileNav();
    } else {
      openMobileNav();
    }
  };

  useEffect(() => {
    if (!mobileNavOpen || isDesktop) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeMobileNav();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [mobileNavOpen, isDesktop]);

  useEffect(() => {
    if (!mobileNavOpen || isDesktop) return;
    // Move focus into the open drawer for keyboard users.
    const firstFocusable = sidebarRef.current?.querySelector<HTMLElement>(
      'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])',
    );
    firstFocusable?.focus();
  }, [mobileNavOpen, isDesktop]);

  return (
    <>
      <a
        href="#main-content"
        className="sr-only focus:fixed focus:left-3 focus:top-3 focus:z-50 focus:h-auto focus:w-auto focus:overflow-visible focus:rounded-md focus:border focus:border-border focus:bg-background focus:px-3 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground focus:shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      >
        Skip to main content
      </a>
      <div className="flex h-svh w-screen overflow-hidden bg-background text-foreground">
        {/* Mobile top bar: primary nav is off-canvas so it cannot squeeze main at ~390px. */}
        <header className="fixed inset-x-0 top-0 z-30 flex h-14 items-center gap-2 border-b border-border bg-background/95 px-3 backdrop-blur-sm md:hidden">
          <button
            ref={menuButtonRef}
            type="button"
            aria-label={mobileNavOpen ? "Close navigation" : "Open navigation"}
            aria-expanded={mobileNavOpen}
            aria-controls="workspace-sidebar"
            onClick={toggleMobileNav}
            className="inline-flex size-9 items-center justify-center rounded-md border border-border bg-background text-foreground shadow-sm transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring md:hidden"
          >
            {mobileNavOpen ? (
              <X className="size-4" aria-hidden="true" />
            ) : (
              <Menu className="size-4" aria-hidden="true" />
            )}
          </button>
          <Logo className="h-5 w-5" spin />
          <span className="text-sm font-semibold">CyberPenda</span>
        </header>

        {mobileNavOpen && !isDesktop && (
          <button
            type="button"
            aria-label="Dismiss navigation"
            className="fixed inset-0 z-40 bg-background/60 backdrop-blur-[1px] md:hidden"
            onClick={() => closeMobileNav()}
          />
        )}

        <aside
          ref={sidebarRef}
          id="workspace-sidebar"
          aria-label="CyberPenda workspace"
          aria-hidden={sidebarAvailable ? undefined : true}
          inert={sidebarAvailable ? undefined : true}
          className={cn(
            "flex w-72 shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground shadow-sm",
            // Off-canvas below md: out of document flow so main uses full viewport width.
            "fixed inset-y-0 left-0 z-50 transition-transform duration-200 ease-geist md:static md:z-auto md:translate-x-0",
            mobileNavOpen ? "translate-x-0" : "-translate-x-full md:translate-x-0",
          )}
        >
          <div className="flex h-14 items-center gap-2 border-b border-sidebar-border px-4">
            <Logo className="h-5 w-5" spin />
            <h2 className="text-sm font-semibold">CyberPenda</h2>
          </div>
          <WorkspaceSidebar onNavigate={() => closeMobileNav({ restoreFocus: false })} />
        </aside>
        <main
          id="main-content"
          tabIndex={-1}
          className="flex min-h-0 min-w-0 flex-1 flex-col overflow-x-hidden overflow-y-auto bg-background pt-14 md:pt-0"
        >
          <Suspense fallback={<div role="status" className="p-6 text-sm text-muted-foreground">Loading…</div>}>
            <Outlet />
          </Suspense>
        </main>
      </div>
    </>
  );
}

function createAppRouter() {
  return createBrowserRouter([
    {
      element: <ShellLayout />,
      errorElement: <ShellErrorBoundary />,
      children: [
        { path: "/", element: <ProjectListPage /> },
        { path: "/sessions", element: <SessionHomePage /> },
        { path: "/sessions/archived", element: <SessionHomePage view="archived" /> },
        { path: "/sessions/:sessionId", element: <SessionDetailPage /> },
        { path: "/profiles", element: <RuntimeProfilesPage /> },
        { path: "/model-providers", element: <ModelProvidersPage /> },
        { path: "/credentials", element: <CredentialBindingsPage /> },
        { path: "/skills", element: <SkillsPage /> },
        { path: "/projects/:projectId", element: <ProjectDashboardPage /> },
        { path: "/projects/:projectId/scope", element: <ScopeEditorPage /> },
        { path: "/projects/:projectId/tasks", element: <TasksPage /> },
        { path: "/projects/:projectId/tasks/new", element: <TaskLaunchPage /> },
        { path: "/projects/:projectId/tasks/:taskId", element: <TaskDetailPage /> },
        { path: "/projects/:projectId/tasks/:taskId/challenges", element: <ChallengeWorkflowPage /> },
        // Legacy Facts bookmark → Blackboard Work filtered to ProjectFact.
        { path: "/projects/:projectId/facts", element: <FactsPage /> },
        { path: "/projects/:projectId/blackboard/*", element: <BlackboardPage /> },
        { path: "/projects/:projectId/blackboard", element: <BlackboardPage /> },
        { path: "/projects/:projectId/findings", element: <FindingsPage /> },
        { path: "/projects/:projectId/evidence", element: <EvidencePage /> },
        { path: "/projects/:projectId/report", element: <ReportPage /> },
        { path: "/projects/:projectId/solution", element: <SolutionPage /> },
      ],
    },
  ]);
}

export default function App() {
  const router = useMemo(() => createAppRouter(), []);
  return <RouterProvider router={router} />;
}
