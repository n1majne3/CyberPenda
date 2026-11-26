import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { NavLink, useLocation } from "react-router-dom";
import {
  BookOpen,
  Cpu,
  KeyRound,
  Network,
  Plus,
  Search,
  LayoutGrid,
  MessageSquare,
  CircleUserRound,
} from "lucide-react";
import {
  apiGet,
  archiveSession,
  clearOperatorCredential,
  getSession,
  listSessions,
  renameSession,
  type Project,
  type Session,
  type Task,
  type WorkspaceNavigation,
  type WorkspaceProjectSummary,
} from "@/lib/api";
import { PromptDialog } from "@/components/ConfirmDialog";
import { useDocumentVisibility } from "@/lib/useDocumentVisibility";
import { WORKSPACE_NAVIGATION_CHANGED_EVENT } from "@/lib/workspaceNavigation";
import { cn } from "@/lib/utils";
import { sidebarFocusClass } from "@/components/workspaceSidebarStyles";
import {
  ProjectRow,
  SessionRow,
  SettingsLink,
  NavItem,
  SidebarStatus,
  SidebarError,
  SidebarEmpty,
  type TaskState,
} from "@/components/WorkspaceSidebarRows";
import {
  isSessionBusy,
  isSessionActive,
  isTaskActive,
  sessionActivity,
  navigationActivity,
  takeRecentWithCurrent,
  ownerIdFromPath,
  taskIdFromPath,
  projectDisclosureKey,
  readDisclosure,
  writeDisclosure,
} from "@/components/workspaceSidebarActivity";

type WorkspaceSidebarProps = {
  onNavigate?: () => void;
};

export function WorkspaceSidebar({ onNavigate }: WorkspaceSidebarProps) {
  const { pathname } = useLocation();
  const currentSessionId = ownerIdFromPath(pathname, "sessions");
  const currentProjectId = ownerIdFromPath(pathname, "projects");
  const currentTaskId = taskIdFromPath(pathname);

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
  const [searchQuery, setSearchQuery] = useState("");
  const [showFilter, setShowFilter] = useState(false);
  const normalizedQuery = searchQuery.trim().toLowerCase();
  const isVisible = useDocumentVisibility();
  const hasActive = useMemo(
    () =>
      sessions.some(isSessionActive) ||
      navigation.some((summary) => summary.tasks.some(isTaskActive)),
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

  useEffect(() => {
    const refresh = () => void loadNavigation(false);
    window.addEventListener(WORKSPACE_NAVIGATION_CHANGED_EVENT, refresh);
    return () => window.removeEventListener(WORKSPACE_NAVIGATION_CHANGED_EVENT, refresh);
  }, [loadNavigation]);

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

  // The search box filters Sessions and Projects by name (case-insensitive)
  // before the recent/current trimming so a match is never cut by the cap.
  const filteredSessions = useMemo(
    () =>
      normalizedQuery
        ? sessions.filter((session) => session.title.toLowerCase().includes(normalizedQuery))
        : sessions,
    [sessions, normalizedQuery],
  );

  const visibleSessions = useMemo(
    () => takeRecentWithCurrent(filteredSessions, currentSessionId, isSessionBusy, sessionActivity),
    [currentSessionId, filteredSessions],
  );

  const visibleProjects = useMemo(
    () =>
      normalizedQuery
        ? sortedProjects.filter((project) => project.name.toLowerCase().includes(normalizedQuery))
        : sortedProjects,
    [sortedProjects, normalizedQuery],
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
      <nav aria-label="Primary routes" className="min-h-0 flex-1 overflow-y-auto px-[15px] pb-3">
        <div className="space-y-0.5">
          <NavItem to="/" end onNavigate={onNavigate}><LayoutGrid className="size-4" />Projects</NavItem>
          <NavItem to="/sessions" onNavigate={onNavigate}><MessageSquare className="size-4" />Sessions</NavItem>
        </div>
        <div className="mb-2 mt-6 flex items-center justify-between px-3">
          <span id="projects-navigation" className="text-[10px] uppercase tracking-[1.2px] text-muted-foreground">Your projects</span>
          <div className="flex items-center gap-1">
            <button type="button" aria-label="Search workspace" aria-expanded={showFilter}
              onClick={() => { setShowFilter(!showFilter); if (showFilter) setSearchQuery(""); }}
              className="inline-flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-sidebar-accent">
              <Search className="size-3" />
            </button>
            <NavLink to="/?new=1" aria-label="New project" onClick={onNavigate}
              className={cn("inline-flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-sidebar-accent", sidebarFocusClass)}>
              <Plus className="size-3.5" />
            </NavLink>
          </div>
        </div>
        {showFilter && (
          <div className="mb-3 px-2">
            <input type="search" aria-label="Filter sessions and projects" placeholder="Filter…"
              value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)}
              className="h-8 w-full rounded-md border border-input bg-card px-2 text-xs placeholder:text-muted-foreground" />
          </div>
        )}
        <section aria-labelledby="projects-navigation">
          {projectLoading ? <SidebarStatus label="Loading projects" /> : projectError ? <SidebarError message={projectError} /> : sortedProjects.length === 0 ? (
            <SidebarEmpty message="No projects" link={{ to: "/?new=1", label: "New project" }} onNavigate={onNavigate} />
          ) : visibleProjects.length === 0 ? <SidebarStatus label={`No projects match "${searchQuery.trim()}"`} /> : (
            <div className="space-y-0.5">
              {visibleProjects.map((project) => {
                const summary = navigation.find((entry) => entry.id === project.id);
                const defaultOpen = project.id === currentProjectId && Boolean(currentTaskId);
                const open = projectDisclosure[project.id] ?? readDisclosure(projectDisclosureKey(project.id), defaultOpen);
                return <ProjectRow key={project.id} project={project} tasks={summary?.tasks ?? []}
                  onDemandTasks={expandedTasks[project.id]?.tasks ?? []} tasksLoading={expandedTasks[project.id]?.loading ?? false}
                  tasksError={expandedTasks[project.id]?.error ?? null} useOnDemandTasks={project.id !== currentProjectId && open}
                  open={open} currentTaskId={currentTaskId} currentProject={project.id === currentProjectId}
                  onToggle={() => toggleProject(project.id, defaultOpen)} onNavigate={onNavigate} />;
              })}
            </div>
          )}
        </section>
        <details aria-labelledby="non-project-navigation" className="mt-5 px-2" open={currentSessionId || normalizedQuery ? true : undefined}>
          <summary id="non-project-navigation" className="cursor-pointer text-[11px] text-muted-foreground">
            Recent sessions <span className="ml-1 tabular-nums">{filteredSessions.length}</span>
          </summary>
          <div id="non-project-work" className="mt-2 space-y-1">
            {sessionLoading ? <SidebarStatus label="Loading sessions" /> : sessionError ? <SidebarError message={sessionError} /> : sessions.length === 0 ? (
              <SidebarEmpty message="No open sessions" link={{ to: "/sessions#new-session", label: "New session" }} onNavigate={onNavigate} />
            ) : visibleSessions.length === 0 ? <SidebarStatus label={`No sessions match "${searchQuery.trim()}"`} /> : (
              <>
                {visibleSessions.map((session) => (
                  <SessionRow key={session.id} session={session} current={session.id === currentSessionId}
                    busy={sessionActionId === session.id} onNavigate={onNavigate}
                    onRename={(session) => setRenameTarget(session)} onArchive={handleSessionArchive} />
                ))}
                <NavLink to="/sessions" onClick={onNavigate} className={cn("inline-flex h-8 w-full items-center px-2 text-xs text-muted-foreground hover:text-foreground", sidebarFocusClass)}>Show more</NavLink>
              </>
            )}
          </div>
        </details>
      </nav>
      <section aria-label="Global settings" className="shrink-0 bg-sidebar px-[15px] pb-4 pt-5">
        <div className="mb-2 px-3 text-[10px] uppercase tracking-[1.2px] text-muted-foreground">Configuration</div>
        <div className="space-y-0.5">
          <SettingsLink to="/profiles" icon={<Cpu className="size-4" />} onNavigate={onNavigate}>Runtime profiles</SettingsLink>
          <SettingsLink to="/model-providers" icon={<Network className="size-4" />} onNavigate={onNavigate}>Model providers</SettingsLink>
          <SettingsLink to="/credentials" icon={<KeyRound className="size-4" />} onNavigate={onNavigate}>Credentials</SettingsLink>
          <SettingsLink to="/skills" icon={<BookOpen className="size-4" />} onNavigate={onNavigate}>Skills</SettingsLink>
        </div>
        <div className="mt-4 flex items-center gap-2.5 border-t border-sidebar-border px-2 pt-4">
          <CircleUserRound className="size-7 shrink-0 text-muted-foreground" strokeWidth={1} />
          <div className="min-w-0 flex-1"><div className="text-xs">Local workspace</div><div className="text-[10px] text-muted-foreground">On this device</div></div>
          {import.meta.env.VITE_DEMO_MODE !== "true" && (
            <button type="button" onClick={() => void clearOperatorCredential()}
              className={cn("rounded px-1 py-2 text-[10px] text-muted-foreground hover:text-foreground", sidebarFocusClass)}>Sign out</button>
          )}
        </div>
      </section>
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
