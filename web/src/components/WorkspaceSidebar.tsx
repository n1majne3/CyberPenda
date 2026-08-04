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
} from "@/lib/api";
import { ThemeToggle } from "@/components/ThemeProvider";
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

  const [projects, setProjects] = useState<Project[]>([]);
  const [projectLoading, setProjectLoading] = useState(true);
  const [projectError, setProjectError] = useState<string | null>(null);
  const [taskStates, setTaskStates] = useState<Record<string, TaskState>>({});
  const [projectDisclosure, setProjectDisclosure] = useState<Record<string, boolean>>({});
  const [sessions, setSessions] = useState<Session[]>([]);
  const [sessionLoading, setSessionLoading] = useState(true);
  const [sessionError, setSessionError] = useState<string | null>(null);
  const [sessionActionId, setSessionActionId] = useState<string | null>(null);
  const projectIdsKey = useMemo(() => projects.map((project) => project.id).join("\u0000"), [projects]);

  const loadProjects = useCallback(async (showLoading = true) => {
    if (showLoading) setProjectLoading(true);
    try {
      const data = await apiGet<{ projects: Project[] }>("/api/projects");
      setProjects(data.projects ?? []);
      setProjectError(null);
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

  const loadTasks = useCallback(async (projectId: string, showLoading = true) => {
    setTaskStates((previous) => ({
      ...previous,
      [projectId]: {
        tasks: previous[projectId]?.tasks ?? [],
        loading: showLoading || previous[projectId]?.loading !== false,
        error: null,
      },
    }));
    try {
      const data = await apiGet<{ tasks: Task[] }>(`/api/projects/${encodeURIComponent(projectId)}/tasks`);
      setTaskStates((previous) => ({
        ...previous,
        [projectId]: { tasks: data.tasks ?? [], loading: false, error: null },
      }));
    } catch (reason) {
      setTaskStates((previous) => ({
        ...previous,
        [projectId]: {
          tasks: previous[projectId]?.tasks ?? [],
          loading: false,
          error: (reason as Error).message,
        },
      }));
    }
  }, []);

  useEffect(() => {
    void loadProjects();
    const refresh = window.setInterval(() => void loadProjects(false), 2000);
    return () => window.clearInterval(refresh);
  }, [loadProjects]);

  useEffect(() => {
    void loadSessions();
    const refresh = window.setInterval(() => void loadSessions(false), 2000);
    return () => window.clearInterval(refresh);
  }, [loadSessions]);

  // Task summaries supply the latest Project-or-Task activity used for
  // ordering. The same summaries also populate an expanded Project row.
  useEffect(() => {
    for (const projectId of projectIdsKey.split("\u0000").filter(Boolean)) void loadTasks(projectId);
  }, [loadTasks, projectIdsKey]);

  useEffect(() => {
    if (!projectIdsKey) return;
    const refresh = window.setInterval(() => {
      for (const projectId of projectIdsKey.split("\u0000").filter(Boolean)) void loadTasks(projectId, false);
    }, 2000);
    return () => window.clearInterval(refresh);
  }, [loadTasks, projectIdsKey]);

  const sortedProjects = useMemo(
    () =>
      [...projects].sort((a, b) => {
        const activityDelta = projectActivity(b, taskStates[b.id]?.tasks) - projectActivity(a, taskStates[a.id]?.tasks);
        if (activityDelta !== 0) return activityDelta;
        return a.name.localeCompare(b.name);
      }),
    [projects, taskStates],
  );

  const visibleSessions = useMemo(
    () => takeRecentWithCurrent(sessions, currentSessionId, isSessionBusy, sessionActivity),
    [currentSessionId, sessions],
  );

  const toggleProject = (projectId: string, defaultOpen: boolean) => {
    setProjectDisclosure((previous) => {
      const next = !(previous[projectId] ?? defaultOpen);
      writeDisclosure(projectDisclosureKey(projectId), next);
      return { ...previous, [projectId]: next };
    });
  };

  const handleSessionRename = useCallback(
    async (session: Session) => {
      const nextTitle = window.prompt("Rename session", session.title);
      if (!nextTitle?.trim() || nextTitle.trim() === session.title) return;
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
                    onRename={handleSessionRename}
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
                const defaultOpen = project.id === currentProjectId;
                const open = projectDisclosure[project.id] ?? readDisclosure(projectDisclosureKey(project.id), defaultOpen);
                const state = taskStates[project.id] ?? { tasks: [], loading: true, error: null };
                return (
                  <ProjectRow
                    key={project.id}
                    project={project}
                    tasks={state.tasks}
                    tasksLoading={state.loading}
                    tasksError={state.error}
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
    </>
  );
}

function ProjectRow({
  project,
  tasks,
  tasksLoading,
  tasksError,
  open,
  currentTaskId,
  currentProject,
  onToggle,
  onNavigate,
}: {
  project: Project;
  tasks: Task[];
  tasksLoading: boolean;
  tasksError: string | null;
  open: boolean;
  currentTaskId: string | null;
  currentProject: boolean;
  onToggle: () => void;
  onNavigate?: () => void;
}) {
  const taskPanelId = `project-tasks-${project.id}`;
  const visibleTasks = takeRecentWithCurrent(tasks, currentTaskId, isTaskBusy, taskActivity);

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
  onRename: (session: Session) => Promise<void>;
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
  onRename: (session: Session) => Promise<void>;
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

function projectActivity(project: Project, tasks: Task[] = []) {
  return Math.max(activityTime(project.last_activity_at, project.updated_at, project.created_at), ...tasks.map(taskActivity));
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
