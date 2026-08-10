import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { NavLink, useLocation } from "react-router-dom";
import {
  Archive,
  BookOpen,
  ChevronRight,
  CircleAlert,
  CircleDot,
  Cpu,
  FolderKanban,
  KeyRound,
  LoaderCircle,
  MoreHorizontal,
  Network,
  Plus,
  WifiOff,
} from "lucide-react";
import {
  apiGet,
  archiveSession,
  getSession,
  listSessions,
  renameSession,
  type Project,
  type RuntimeActivity,
  type Session,
  type Task,
  type WorkspaceNavigation,
  type WorkspaceProjectSummary,
} from "@/lib/api";
import { ThemeToggle } from "@/components/ThemeProvider";
import { PromptDialog } from "@/components/ConfirmDialog";
import { useDocumentVisibility } from "@/lib/useDocumentVisibility";
import { cn } from "@/lib/utils";

type TaskState = {
  tasks: Task[];
  loading: boolean;
  error: string | null;
};

type WorkspaceSidebarProps = {
  onNavigate?: () => void;
};

export function WorkspaceSidebar({ onNavigate }: WorkspaceSidebarProps) {
  const { pathname } = useLocation();
  const currentSessionId = ownerIdFromPath(pathname, "sessions");
  const currentProjectId = ownerIdFromPath(pathname, "projects");
  const currentTaskId = taskIdFromPath(pathname);

  // One bounded navigation projection replaces the former one-Task-list-fetch-
  // per-Project fan-out (#193). The daemon returns every Project with its most
  // recent Tasks inlined and a last_activity_at, so the Sidebar makes a single
  // constant-size request regardless of how many Projects exist. Expanding a
  // non-current Project loads its full Task list on demand below.
  //
  // Conditional refresh (#201): every response carries an opaque revision, and
  // each refresh sends it back plus the selected Task. When the revision is
  // current the daemon answers changed=false with an empty projection, so
  // polling never reserializes the Sidebar rows. The revision and the selected
  // Task live in refs so a response never retriggers the load that produced it.
  const [navigation, setNavigation] = useState<WorkspaceProjectSummary[]>([]);
  const navigationRevisionRef = useRef<string | undefined>(undefined);
  const currentTaskIdRef = useRef<string | null>(currentTaskId);
  const [projectLoading, setProjectLoading] = useState(true);
  const [projectError, setProjectError] = useState<string | null>(null);
  const [expandedTasks, setExpandedTasks] = useState<Record<string, TaskState>>({});
  const [projectDisclosure, setProjectDisclosure] = useState<Record<string, boolean>>({});
  const [sessions, setSessions] = useState<Session[]>([]);
  const [sessionLoading, setSessionLoading] = useState(true);
  const [sessionError, setSessionError] = useState<string | null>(null);
  const [sessionActionId, setSessionActionId] = useState<string | null>(null);
  const [renameTarget, setRenameTarget] = useState<Session | null>(null);
  const isVisible = useDocumentVisibility();
  // Whether any tracked owner currently has a live, busy runtime. While true the
  // sidebar polls fast (2s) to surface runtime progress; once idle it backs off
  // to a slow cadence so an open-but-unused tab does not hammer the daemon.
  const hasActive = useMemo(
    () =>
      sessions.some(isSessionBusy) ||
      navigation.some((summary) => summary.tasks.some(isTaskBusy)),
    [sessions, navigation],
  );

  const loadNavigation = useCallback(async (showLoading = true) => {
    if (showLoading) setProjectLoading(true);
    try {
      const params = new URLSearchParams();
      if (navigationRevisionRef.current) params.set("revision", navigationRevisionRef.current);
      if (currentTaskIdRef.current) params.set("selected_task", currentTaskIdRef.current);
      const suffix = params.size > 0 ? `?${params.toString()}` : "";
      const data = await apiGet<WorkspaceNavigation>(`/api/workspace/navigation${suffix}`);
      navigationRevisionRef.current = data.revision ?? undefined;
      if (data.changed === false) {
        // Unchanged refresh: the daemon kept the cached revision, so the
        // current projection is still current and was not reserialized.
        setProjectError(null);
      } else {
        setNavigation(data.projects ?? []);
        setProjectError(null);
      }
    } catch (reason) {
      setProjectError((reason as Error).message);
    } finally {
      if (showLoading) setProjectLoading(false);
    }
  }, []);

  const loadSessions = useCallback(async (showLoading = true) => {
    if (showLoading) setSessionLoading(true);
    try {
      const data = await listSessions(undefined, 5);
      const loaded = data.sessions ?? [];
      if (currentSessionId && !loaded.some((session) => session.id === currentSessionId)) {
        try {
          const current = await getSession(currentSessionId);
          if (current.lifecycle === "open") loaded.push(current);
        } catch {
          // A stale/deleted route should not hide the remaining recent Sessions.
        }
      }
      setSessions(loaded);
      setSessionError(null);
    } catch (reason) {
      setSessionError((reason as Error).message);
    } finally {
      if (showLoading) setSessionLoading(false);
    }
  }, [currentSessionId]);

  // On-demand full Task list: called only when the operator expands a Project.
  // The current Project renders its inlined Tasks from the navigation
  // projection; other Projects fetch their complete list lazily so the Sidebar
  // never sends one request per Project on mount or refresh.
  const loadTasks = useCallback(async (projectId: string, showLoading = true) => {
    setExpandedTasks((previous) => ({
      ...previous,
      [projectId]: {
        tasks: previous[projectId]?.tasks ?? [],
        loading: showLoading || previous[projectId]?.loading !== false,
        error: null,
      },
    }));
    try {
      const data = await apiGet<{ tasks: Task[] }>(`/api/projects/${encodeURIComponent(projectId)}/tasks`);
      setExpandedTasks((previous) => ({
        ...previous,
        [projectId]: { tasks: data.tasks ?? [], loading: false, error: null },
      }));
    } catch (reason) {
      setExpandedTasks((previous) => ({
        ...previous,
        [projectId]: {
          tasks: previous[projectId]?.tasks ?? [],
          loading: false,
          error: (reason as Error).message,
        },
      }));
    }
  }, []);

  // Eager initial loads: fill the sidebar on first mount even when the tab is
  // already hidden. Navigation and sessions each load once; subsequent
  // refreshes are driven by the gated poll below.
  useEffect(() => {
    void loadNavigation();
  }, [loadNavigation]);

  useEffect(() => {
    void loadSessions();
  }, [loadSessions]);

  // The navigation projection is keyed to the selected Task, so moving to a
  // different Task (or leaving a Task route) refetches once with the new
  // selection context instead of waiting for the next poll (#201).
  useEffect(() => {
    if (currentTaskIdRef.current === currentTaskId) return;
    currentTaskIdRef.current = currentTaskId;
    void loadNavigation(false);
  }, [currentTaskId, loadNavigation]);

  // Single gated poll replacing the former fixed 2s intervals. It suspends
  // entirely while the tab is hidden and backs off from 2s → 30s once no owner
  // has a live busy runtime. `hasActive` lags by one tick, which is acceptable.
  useEffect(() => {
    if (!isVisible) return;
    const period = hasActive ? 2000 : 30000;
    const refresh = window.setInterval(() => {
      void loadNavigation(false);
      void loadSessions(false);
    }, period);
    return () => window.clearInterval(refresh);
  }, [isVisible, hasActive, loadNavigation, loadSessions]);

  const sortedProjects = useMemo<Project[]>(
    () =>
      [...navigation].sort((a, b) => {
        const activityDelta = navigationActivity(b) - navigationActivity(a);
        if (activityDelta !== 0) return activityDelta;
        return a.name.localeCompare(b.name);
      }),
    [navigation],
  );

  const visibleSessions = useMemo(
    () => takeRecentWithCurrent(sessions, currentSessionId, isSessionBusy, sessionActivity),
    [currentSessionId, sessions],
  );

  const toggleProject = (projectId: string, defaultOpen: boolean) => {
    setProjectDisclosure((previous) => {
      const next = !(previous[projectId] ?? defaultOpen);
      writeDisclosure(projectDisclosureKey(projectId), next);
      // Expanding a non-current Project loads its full Task list on demand;
      // collapsing keeps the cached list for the next expansion.
      if (next && projectId !== currentProjectId) {
        void loadTasks(projectId);
      }
      return { ...previous, [projectId]: next };
    });
  };

  const handleSessionRename = useCallback(
    async (session: Session, nextTitle: string) => {
      if (!nextTitle.trim() || nextTitle.trim() === session.title) return;
      setSessionActionId(session.id);
      try {
        await renameSession(session.id, nextTitle.trim());
        await loadSessions();
      } catch (reason) {
        setSessionError((reason as Error).message);
      } finally {
        setSessionActionId(null);
      }
    },
    [loadSessions],
  );

  const handleSessionArchive = useCallback(
    async (session: Session) => {
      setSessionActionId(session.id);
      try {
        await archiveSession(session.id);
        await loadSessions();
      } catch (reason) {
        setSessionError((reason as Error).message);
      } finally {
        setSessionActionId(null);
      }
    },
    [loadSessions],
  );

  return (
    <>
      <nav aria-label="Primary routes" className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <section aria-labelledby="non-project-navigation" className="border-b border-sidebar-border/70 pb-3">
          <div className="mb-1 flex items-center gap-1">
            <NavLink
              to="/sessions"
              onClick={onNavigate}
              id="non-project-navigation"
              className="inline-flex h-9 min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar"
            >
              Non-project
            </NavLink>
            <NavLink
              to="/sessions#new-session"
              aria-label="New session"
              onClick={onNavigate}
              className="inline-flex size-8 shrink-0 items-center justify-center rounded-md border border-transparent text-muted-foreground transition-colors hover:border-sidebar-border hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar"
            >
              <Plus className="size-4" aria-hidden="true" />
            </NavLink>
          </div>
          <div id="non-project-work" className="space-y-1">
            {sessionLoading ? (
              <SidebarStatus label="Loading sessions" />
            ) : sessionError ? (
              <SidebarError message={sessionError} />
            ) : sessions.length === 0 ? (
              <SidebarEmpty message="No open sessions" link={{ to: "/sessions#new-session", label: "New session" }} onNavigate={onNavigate} />
            ) : (
              <>
                {visibleSessions.map((session) => (
                  <SessionRow
                    key={session.id}
                    session={session}
                    current={session.id === currentSessionId}
                    busy={sessionActionId === session.id}
                    onNavigate={onNavigate}
                    onRename={(session) => setRenameTarget(session)}
                    onArchive={handleSessionArchive}
                  />
                ))}
                <NavLink
                  to="/sessions"
                  onClick={onNavigate}
                  className="inline-flex h-8 w-full items-center rounded-md px-2 text-xs text-muted-foreground transition-colors hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar"
                >
                  Show more
                </NavLink>
              </>
            )}
          </div>
        </section>

        <section aria-labelledby="projects-navigation" className="pt-3">
          <div className="mb-1 flex items-center gap-1">
            <NavItem to="/" end onNavigate={onNavigate} className="min-w-0 flex-1 font-medium">
              <span id="projects-navigation" className="truncate">
                Projects
              </span>
            </NavItem>
            <NavLink
              to="/?new=1"
              aria-label="New project"
              onClick={onNavigate}
              className="inline-flex size-8 shrink-0 items-center justify-center rounded-md border border-transparent text-muted-foreground transition-colors hover:border-sidebar-border hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar"
            >
              <Plus className="size-4" aria-hidden="true" />
            </NavLink>
          </div>

          {projectLoading ? (
            <SidebarStatus label="Loading projects" />
          ) : projectError ? (
            <SidebarError message={projectError} />
          ) : sortedProjects.length === 0 ? (
            <SidebarEmpty message="No projects" link={{ to: "/?new=1", label: "New project" }} onNavigate={onNavigate} />
          ) : (
            <div className="space-y-1">
              {sortedProjects.map((project) => {
                const summary = navigation.find((entry) => entry.id === project.id);
                const defaultOpen = project.id === currentProjectId;
                const open = projectDisclosure[project.id] ?? readDisclosure(projectDisclosureKey(project.id), defaultOpen);
                return (
                  <ProjectRow
                    key={project.id}
                    project={project}
                    // The current Project renders its inlined Tasks immediately;
                    // other Projects load their full list on demand when opened.
                    tasks={summary?.tasks ?? []}
                    onDemandTasks={expandedTasks[project.id]?.tasks ?? []}
                    tasksLoading={expandedTasks[project.id]?.loading ?? false}
                    tasksError={expandedTasks[project.id]?.error ?? null}
                    useOnDemandTasks={project.id !== currentProjectId && (projectDisclosure[project.id] ?? readDisclosure(projectDisclosureKey(project.id), defaultOpen))}
                    open={open}
                    currentTaskId={currentTaskId}
                    currentProject={project.id === currentProjectId}
                    onToggle={() => toggleProject(project.id, defaultOpen)}
                    onNavigate={onNavigate}
                  />
                );
              })}
            </div>
          )}
        </section>
      </nav>

      <aside aria-label="Global settings" className="shrink-0 border-t border-sidebar-border bg-sidebar p-3">
        <div className="mb-2 px-2 text-xs font-medium text-muted-foreground/80">Settings</div>
        <div className="space-y-1">
          <SettingsLink to="/profiles" icon={<Cpu className="size-4" />} onNavigate={onNavigate}>
            Runtime profiles
          </SettingsLink>
          <SettingsLink to="/model-providers" icon={<Network className="size-4" />} onNavigate={onNavigate}>
            Model providers
          </SettingsLink>
          <SettingsLink to="/credentials" icon={<KeyRound className="size-4" />} onNavigate={onNavigate}>
            Credentials
          </SettingsLink>
          <SettingsLink to="/skills" icon={<BookOpen className="size-4" />} onNavigate={onNavigate}>
            Skills
          </SettingsLink>
        </div>
        <div className="mt-2 flex items-center justify-between border-t border-sidebar-border/70 pt-2">
          <span className="px-2 text-xs text-muted-foreground">Theme</span>
          <ThemeToggle />
        </div>
      </aside>
      <PromptDialog
        open={renameTarget !== null}
        title={renameTarget ? `Rename ${renameTarget.title}` : "Rename session"}
        label="Session title"
        initialValue={renameTarget?.title ?? ""}
        confirmLabel="Save"
        onConfirm={(value) => {
          const target = renameTarget;
          setRenameTarget(null);
          if (target) void handleSessionRename(target, value);
        }}
        onCancel={() => setRenameTarget(null)}
      />
    </>
  );
}

function ProjectRow({
  project,
  tasks,
  onDemandTasks,
  tasksLoading,
  tasksError,
  useOnDemandTasks,
  open,
  currentTaskId,
  currentProject,
  onToggle,
  onNavigate,
}: {
  project: Project;
  tasks: Task[];
  onDemandTasks: Task[];
  tasksLoading: boolean;
  tasksError: string | null;
  useOnDemandTasks: boolean;
  open: boolean;
  currentTaskId: string | null;
  currentProject: boolean;
  onToggle: () => void;
  onNavigate?: () => void;
}) {
  const taskPanelId = `project-tasks-${project.id}`;
  // The current Project renders its daemon-bounded inlined Tasks from the
  // navigation projection as-is: busy Runtimes first, then the five ordinary
  // recent Tasks, then the selected Task (#201). Other Projects show their
  // on-demand full list, which is bounded by takeRecentWithCurrent for display.
  const visibleTasks = useOnDemandTasks ? takeRecentWithCurrent(onDemandTasks, currentTaskId, isTaskBusy, taskActivity) : tasks;

  return (
    <section aria-labelledby={`project-name-${project.id}`} aria-current={currentProject ? "location" : undefined}>
      <div className="group flex items-center gap-1">
        <button
          type="button"
          aria-label={`${open ? "Collapse" : "Expand"} ${project.name}`}
          aria-controls={taskPanelId}
          aria-expanded={open}
          onClick={onToggle}
          className="inline-flex size-8 shrink-0 items-center justify-center rounded-md border border-transparent text-muted-foreground transition-colors hover:border-sidebar-border hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar"
        >
          <ChevronRight className={cn("size-3.5 transition-transform", open && "rotate-90")} aria-hidden="true" />
        </button>
        <NavLink
          id={`project-name-${project.id}`}
          to={`/projects/${encodeURIComponent(project.id)}`}
          aria-label={`Open ${project.name} project dashboard`}
          onClick={onNavigate}
          className={({ isActive }) => navItemClasses(isActive, "min-w-0 flex-1")}
        >
          {({ isActive }) => (
            <>
              <FolderKanban className="size-4 shrink-0" aria-hidden="true" />
              <span className="truncate">{project.name}</span>
              <span className="sr-only"> project dashboard</span>
              <ActiveIndicator active={isActive} />
            </>
          )}
        </NavLink>
      </div>

      <div id={taskPanelId} hidden={!open} className="ml-4 space-y-1 border-l border-sidebar-border/70 pl-2">
        {tasksLoading ? (
          <SidebarStatus label={`Loading tasks for ${project.name}`} />
        ) : tasksError ? (
          <SidebarError message={tasksError} />
        ) : visibleTasks.length === 0 ? (
          <SidebarEmpty message="No tasks" link={{ to: `/projects/${encodeURIComponent(project.id)}/tasks/new`, label: "Launch task" }} onNavigate={onNavigate} />
        ) : (
          <>
            {visibleTasks.map((task) => (
              <TaskRow key={task.id} task={task} current={task.id === currentTaskId} onNavigate={onNavigate} />
            ))}
            <NavLink
              to={`/projects/${encodeURIComponent(project.id)}/tasks`}
              onClick={onNavigate}
              className="inline-flex h-8 w-full items-center rounded-md px-2 text-xs text-muted-foreground transition-colors hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar"
            >
              Show more
            </NavLink>
          </>
        )}
      </div>
    </section>
  );
}

function SessionRow({
  session,
  current,
  busy,
  onNavigate,
  onRename,
  onArchive,
}: {
  session: Session;
  current: boolean;
  busy: boolean;
  onNavigate?: () => void;
  onRename: (session: Session) => void;
  onArchive: (session: Session) => Promise<void>;
}) {
  return (
    <div className="group flex items-center gap-1">
      <NavLink
        to={`/sessions/${encodeURIComponent(session.id)}`}
        end
        aria-label={`Open ${session.title} session conversation. ${runtimeActivityState(session.runtime_activity, session.latest_continuation?.status === "failed").label}.`}
        onClick={onNavigate}
        className={({ isActive }) => navItemClasses(isActive, "min-w-0 flex-1")}
      >
        {({ isActive }) => (
          <>
            <RuntimeActivityIndicator activity={session.runtime_activity} failed={session.latest_continuation?.status === "failed"} />
            <span className="truncate">{session.title}</span>
            <span className="sr-only"> session conversation</span>
            <ActiveIndicator active={isActive || current} />
          </>
        )}
      </NavLink>
      <SessionOverflowMenu session={session} busy={busy} onRename={onRename} onArchive={onArchive} />
    </div>
  );
}

function TaskRow({ task, current, onNavigate }: { task: Task; current: boolean; onNavigate?: () => void }) {
  return (
    <NavLink
      to={`/projects/${encodeURIComponent(task.project_id)}/tasks/${encodeURIComponent(task.id)}`}
      end
      aria-label={`Open ${task.goal || "Untitled task"} task conversation. ${runtimeActivityState(task.runtime_activity, task.status === "failed").label}.`}
      onClick={onNavigate}
      className={({ isActive }) => navItemClasses(isActive || current, "min-w-0 w-full")}
    >
      {({ isActive }) => (
        <>
          <RuntimeActivityIndicator activity={task.runtime_activity} failed={task.status === "failed"} />
          <span className="truncate">{task.goal || "Untitled task"}</span>
          <span className="sr-only"> task conversation</span>
          <ActiveIndicator active={isActive || current} />
        </>
      )}
    </NavLink>
  );
}

function SessionOverflowMenu({
  session,
  busy,
  onRename,
  onArchive,
}: {
  session: Session;
  busy: boolean;
  onRename: (session: Session) => void;
  onArchive: (session: Session) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      setOpen(false);
      queueMicrotask(() => buttonRef.current?.focus());
    };
    const closeOnOutsidePointer = (event: PointerEvent) => {
      if (!menuRef.current?.contains(event.target as Node) && !buttonRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    window.addEventListener("pointerdown", closeOnOutsidePointer);
    return () => {
      window.removeEventListener("keydown", closeOnEscape);
      window.removeEventListener("pointerdown", closeOnOutsidePointer);
    };
  }, [open]);

  return (
    <div className="relative shrink-0">
      <button
        ref={buttonRef}
        type="button"
        aria-label={`More actions for ${session.title}`}
        aria-haspopup="menu"
        aria-expanded={open}
        disabled={busy}
        onClick={() => setOpen((previous) => !previous)}
        className="inline-flex size-8 items-center justify-center rounded-md border border-transparent text-muted-foreground transition-colors hover:border-sidebar-border hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar"
      >
        <MoreHorizontal className="size-4" aria-hidden="true" />
      </button>
      {open && (
        <div
          ref={menuRef}
          role="menu"
          aria-label={`${session.title} actions`}
          className="absolute right-0 top-9 z-30 min-w-36 rounded-md border border-sidebar-border bg-sidebar p-1 text-sidebar-foreground shadow-lg"
        >
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              void onRename(session);
            }}
            className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-sidebar-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring"
          >
            Rename
          </button>
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              void onArchive(session);
            }}
            className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-sidebar-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring"
          >
            <Archive className="size-3.5" aria-hidden="true" />
            Archive
          </button>
        </div>
      )}
    </div>
  );
}

function SettingsLink({
  to,
  icon,
  children,
  onNavigate,
}: {
  to: string;
  icon: ReactNode;
  children: ReactNode;
  onNavigate?: () => void;
}) {
  return (
    <NavLink
      to={to}
      onClick={onNavigate}
      className={({ isActive }) => navItemClasses(isActive, "w-full")}
    >
      {({ isActive }) => (
        <>
          <span aria-hidden="true" className="shrink-0">
            {icon}
          </span>
          <span className="truncate">{children}</span>
          <ActiveIndicator active={isActive} />
        </>
      )}
    </NavLink>
  );
}

function NavItem({
  to,
  icon,
  children,
  end,
  onNavigate,
  className,
}: {
  to: string;
  icon?: ReactNode;
  children: ReactNode;
  end?: boolean;
  onNavigate?: () => void;
  className?: string;
}) {
  return (
    <NavLink to={to} end={end} onClick={onNavigate} className={({ isActive }) => navItemClasses(isActive, className)}>
      {({ isActive }) => (
        <>
          {icon && (
            <span aria-hidden="true" className="shrink-0">
              {icon}
            </span>
          )}
          {children}
          <ActiveIndicator active={isActive} />
        </>
      )}
    </NavLink>
  );
}

function navItemClasses(isActive: boolean, className?: string) {
  return cn(
    "group relative inline-flex h-9 w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar",
    isActive
      ? "bg-sidebar-accent font-semibold text-sidebar-accent-foreground"
      : "text-muted-foreground hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground",
    className,
  );
}

function ActiveIndicator({ active }: { active: boolean }) {
  return (
    <span
      aria-hidden="true"
      data-nav-indicator={active ? "active" : undefined}
      className={cn("absolute left-0 top-1/2 h-4 w-0.5 -translate-y-1/2 rounded-full bg-signal", active ? "opacity-100" : "opacity-0")}
    />
  );
}

function SidebarStatus({ label }: { label: string }) {
  return (
    <div role="status" aria-label={label} className="px-2 py-2 text-xs text-muted-foreground">
      {label}
    </div>
  );
}

function SidebarError({ message }: { message: string }) {
  return (
    <div role="alert" className="rounded-md border border-destructive/20 bg-destructive/5 px-2 py-2 text-xs text-destructive">
      {message}
    </div>
  );
}

function SidebarEmpty({
  message,
  link,
  onNavigate,
}: {
  message: string;
  link: { to: string; label: string };
  onNavigate?: () => void;
}) {
  return (
    <div className="space-y-1 px-2 py-1">
      <p className="text-xs text-muted-foreground">{message}</p>
      <NavItem to={link.to} onNavigate={onNavigate} className="h-8 justify-start px-0 text-xs text-signal hover:bg-transparent">
        {link.label}
      </NavItem>
    </div>
  );
}

function RuntimeActivityIndicator({ activity, failed = false }: { activity?: RuntimeActivity; failed?: boolean }) {
  const state = runtimeActivityState(activity, failed);
  const Icon = state.icon;
  return (
    <span role="img" title={state.label} aria-label={state.label} className="inline-flex size-4 shrink-0 items-center justify-center text-muted-foreground">
      <Icon className={cn("size-3.5", state.busy && "animate-pulse motion-reduce:animate-none")} aria-hidden="true" />
    </span>
  );
}

function runtimeActivityState(activity?: RuntimeActivity, failed = false) {
  if (failed) return { label: "Runtime failure", icon: CircleAlert, busy: false };
  if (activity?.liveness === "live" && activity.turn_activity === "busy") {
    return { label: "Runtime busy", icon: LoaderCircle, busy: true };
  }
  if (activity?.liveness === "live") return { label: "Runtime live idle", icon: CircleDot, busy: false };
  if (activity?.liveness === "offline") return { label: "Runtime offline", icon: WifiOff, busy: false };
  if (activity?.liveness === "orphaned") {
    return { label: "Runtime failure (orphaned)", icon: CircleAlert, busy: false };
  }
  if (activity?.liveness === "unknown") return { label: "Runtime unavailable (unknown)", icon: CircleAlert, busy: false };
  return { label: "Runtime activity unavailable", icon: CircleDot, busy: false };
}

function isSessionBusy(session: Session) {
  return session.runtime_activity?.liveness === "live" && session.runtime_activity.turn_activity === "busy";
}

function isTaskBusy(task: Task) {
  return task.runtime_activity?.liveness === "live" && task.runtime_activity.turn_activity === "busy";
}

function sessionActivity(session: Session) {
  return activityTime(session.last_activity_at, session.updated_at, session.created_at);
}

function taskActivity(task: Task) {
  return activityTime(task.updated_at, task.created_at);
}

// The navigation projection (#193) carries last_activity_at precomputed by the
// daemon (max of Project and Task activity), so ordering is a single read.
function navigationActivity(summary: WorkspaceProjectSummary) {
  return activityTime(summary.last_activity_at, summary.updated_at, summary.created_at);
}

function activityTime(...values: (string | undefined)[]) {
  return Math.max(...values.map((value) => (value ? Date.parse(value) : Number.NEGATIVE_INFINITY)));
}

function takeRecentWithCurrent<T>(
  items: T[],
  currentId: string | null,
  busy: (item: T) => boolean,
  activity: (item: T) => number,
) {
  const ordered = [...items].sort((a, b) => Number(busy(b)) - Number(busy(a)) || activity(b) - activity(a));
  const visible = ordered.slice(0, 5);
  if (currentId && !visible.some((item) => itemId(item) === currentId)) {
    const current = ordered.find((item) => itemId(item) === currentId);
    if (current) {
      if (visible.length === 5) visible[visible.length - 1] = current;
      else visible.push(current);
    }
  }
  return visible;
}

function itemId(item: unknown) {
  return typeof item === "object" && item !== null && "id" in item && typeof item.id === "string" ? item.id : "";
}

function ownerIdFromPath(pathname: string, owner: "projects" | "sessions") {
  const match = pathname.match(new RegExp(`/${owner}/([^/]+)`));
  return match ? decodeURIComponent(match[1]) : null;
}

function taskIdFromPath(pathname: string) {
  const match = pathname.match(/\/projects\/[^/]+\/tasks\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : null;
}

function projectDisclosureKey(projectId: string) {
  return `cyberpenda.sidebar.project.${projectId}`;
}

function readDisclosure(key: string, fallback: boolean) {
  try {
    const value = window.sessionStorage.getItem(key);
    return value === null ? fallback : value === "true";
  } catch {
    return fallback;
  }
}

function writeDisclosure(key: string, value: boolean) {
  try {
    window.sessionStorage.setItem(key, String(value));
  } catch {
    // Storage is a convenience; navigation remains usable when it is blocked.
  }
}
