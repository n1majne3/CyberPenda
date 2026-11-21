import { useEffect, useRef, useState, type ReactNode } from "react";
import { NavLink } from "react-router-dom";
import { Archive, ChevronRight, FolderKanban, MoreHorizontal } from "lucide-react";
import { type Project, type RuntimeActivity, type Session, type Task } from "@/lib/api";
import { sessionContinuationStatus } from "@/lib/runtimeOwner/status";
import { cn } from "@/lib/utils";
import { navItemClasses, sidebarFocusClass } from "@/components/workspaceSidebarStyles";
import { formatRelativeTime } from "@/lib/format";
import {
  runtimeActivityState,
  durableFirstRowState,
  isTaskBusy,
  taskActivity,
  takeRecentWithCurrent,
} from "@/components/workspaceSidebarActivity";

export type TaskState = {
  tasks: Task[];
  loading: boolean;
  error: string | null;
};

export function ProjectRow({
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
  // Nested Task routes also match /projects/:id as a path prefix. When a Task
  // is the primary selection, only the Task row keeps the active wash so the
  // Project header and first Task do not merge into one box.
  const projectSelected = currentProject && !currentTaskId;

  return (
    <section aria-labelledby={`project-name-${project.id}`} aria-current={currentProject ? "location" : undefined}>
      <div className="group flex items-center gap-1">
        <button
          type="button"
          aria-label={`${open ? "Collapse" : "Expand"} ${project.name}`}
          aria-controls={taskPanelId}
          aria-expanded={open}
          onClick={onToggle}
          className={cn(
            "inline-flex size-8 shrink-0 items-center justify-center rounded-md border border-transparent text-muted-foreground transition-colors hover:border-sidebar-border hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground",
            sidebarFocusClass,
          )}
        >
          <ChevronRight className={cn("size-3.5 transition-transform", open && "rotate-90")} aria-hidden="true" />
        </button>
        <NavLink
          id={`project-name-${project.id}`}
          to={`/projects/${encodeURIComponent(project.id)}`}
          aria-label={`Open ${project.name} project dashboard`}
          // Pass false (not undefined): NavLink defaults missing aria-current to
          // "page" whenever the path prefix matches, including nested Task routes.
          aria-current={projectSelected ? "page" : false}
          onClick={onNavigate}
          className={navItemClasses(projectSelected, "min-w-0 flex-1")}
        >
          <FolderKanban className="size-4 shrink-0" aria-hidden="true" />
          <span className="truncate">{project.name}</span>
          <span className="sr-only"> project dashboard</span>
          <ActiveIndicator active={projectSelected} />
        </NavLink>
      </div>

      <div id={taskPanelId} hidden={!open} className="mt-0.5 space-y-0.5">
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
              className={cn(
                "inline-flex h-8 w-full items-center rounded-md pl-7 pr-2 text-xs text-muted-foreground transition-colors hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground",
                sidebarFocusClass,
              )}
            >
              Show more
            </NavLink>
          </>
        )}
      </div>
    </section>
  );
}

export function SessionRow({
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
  const durableStatus = sessionContinuationStatus(session);
  const failed = durableStatus === "failed";
  const state = durableFirstRowState(runtimeActivityState(session.runtime_activity, failed), durableStatus);
  const time = formatRelativeTime(session.last_activity_at ?? session.updated_at);
  return (
    <div className="group flex items-start gap-1">
      <NavLink
        to={`/sessions/${encodeURIComponent(session.id)}`}
        end
        aria-label={`Open ${session.title} session conversation. ${state.label}.`}
        onClick={onNavigate}
        className={({ isActive }) => navItemClasses(isActive, "h-auto min-w-0 flex-1 items-start")}
      >
        {({ isActive }) => (
          <>
            <RuntimeActivityIndicator activity={session.runtime_activity} failed={failed} className="mt-0.5" />
            <span className="min-w-0 flex-1">
              <span className="block truncate">{session.title}</span>
              <span className="mt-0.5 block truncate text-[11px] leading-[1.3] text-muted-foreground">
                {time ? `${time} · ${state.text}` : state.text}
              </span>
            </span>
            <span className="sr-only"> session conversation</span>
            <ActiveIndicator active={isActive || current} />
          </>
        )}
      </NavLink>
      <SessionOverflowMenu session={session} busy={busy} onRename={onRename} onArchive={onArchive} />
    </div>
  );
}

export function TaskRow({ task, current, onNavigate }: { task: Task; current: boolean; onNavigate?: () => void }) {
  return (
    <NavLink
      to={`/projects/${encodeURIComponent(task.project_id)}/tasks/${encodeURIComponent(task.id)}`}
      end
      aria-label={`Open ${task.goal || "Untitled task"} task conversation. ${durableFirstRowState(runtimeActivityState(task.runtime_activity, task.status === "failed"), task.status).label}.`}
      onClick={onNavigate}
      className={({ isActive }) => navItemClasses(isActive || current, "h-auto min-w-0 w-full py-1 pl-7 text-xs")}
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

export function SessionOverflowMenu({
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
        className={cn(
          "inline-flex size-8 items-center justify-center rounded-md border border-transparent text-muted-foreground transition-colors hover:border-sidebar-border hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground",
          // Mockup parity: the overflow action is hover-revealed, but stays
          // visible while the menu is open or the button has keyboard focus.
          open ? "opacity-100" : "opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
          sidebarFocusClass,
        )}
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
            className={cn(
              "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-sidebar-accent",
              sidebarFocusClass,
            )}
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
            className={cn(
              "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-sidebar-accent",
              sidebarFocusClass,
            )}
          >
            <Archive className="size-3.5" aria-hidden="true" />
            Archive
          </button>
        </div>
      )}
    </div>
  );
}

export function SettingsLink({
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

export function NavItem({
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

export function ActiveIndicator({ active }: { active: boolean }) {
  return (
    <span
      aria-hidden="true"
      data-nav-indicator={active ? "active" : undefined}
      className={cn(
        "absolute left-0 top-1/2 h-4 w-0.5 -translate-y-1/2 rounded-full bg-signal",
        active ? "opacity-100" : "opacity-0",
      )}
    />
  );
}

export function SidebarStatus({ label }: { label: string }) {
  return (
    <div role="status" aria-label={label} className="px-2 py-2 text-xs text-muted-foreground">
      {label}
    </div>
  );
}

export function SidebarError({ message }: { message: string }) {
  return (
    <div role="alert" className="rounded-md border border-destructive/20 bg-destructive/5 px-2 py-2 text-xs text-destructive">
      {message}
    </div>
  );
}

export function SidebarEmpty({
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

export function RuntimeActivityIndicator({ activity, failed = false, className }: { activity?: RuntimeActivity; failed?: boolean; className?: string }) {
  const state = runtimeActivityState(activity, failed);
  return (
    <span role="img" title={state.label} aria-label={state.label} className={cn("inline-flex size-4 shrink-0 items-center justify-center", className)}>
      <span
        aria-hidden="true"
        className={cn("size-1.5 rounded-full", state.dot, state.busy && "animate-pulse motion-reduce:animate-none")}
      />
    </span>
  );
}
