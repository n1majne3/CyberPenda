import {
  type RuntimeActivity,
  type Session,
  type Task,
  type WorkspaceProjectSummary,
} from "@/lib/api";
import { activeStatusWord, isActiveStatus, sessionContinuationStatus } from "@/lib/runtimeOwner/status";

// Direction A status dots keep the visible state text aligned with the
// accessible Runtime Activity label. Durable failures remain distinct from an
// offline Runtime so operators never mistake failure for a normal stop.
export function runtimeActivityState(activity?: RuntimeActivity, failed = false) {
  if (failed) return { label: "Runtime failure", text: "Failed", dot: "bg-destructive", busy: false };
  if (activity?.liveness === "live" && activity.turn_activity === "busy") {
    return { label: "Runtime busy", text: "Running", dot: "bg-success", busy: true };
  }
  if (activity?.liveness === "live") return { label: "Runtime live idle", text: "Idle", dot: "bg-info", busy: false };
  if (activity?.liveness === "offline") return { label: "Runtime offline", text: "Stopped", dot: "bg-muted-foreground/40", busy: false };
  if (activity?.liveness === "orphaned") {
    return { label: "Runtime failure (orphaned)", text: "Unknown", dot: "bg-warning", busy: false };
  }
  if (activity?.liveness === "unknown") return { label: "Runtime unavailable (unknown)", text: "Unknown", dot: "bg-warning", busy: false };
  return { label: "Runtime activity unavailable", text: "Stopped", dot: "bg-muted-foreground/40", busy: false };
}

// Sidebar rows are durable-first (#268): while the owner's continuation is
// active, the visible word and row label are the durable status (from the
// shared runtimeOwner status vocabulary, so they cannot drift from the detail
// header's primary badge) instead of contradicting it with turn activity. The
// dot always encodes the Runtime Activity Indicator, and owners without an
// active continuation keep the activity wording above.
export function durableFirstRowState(activityState: ReturnType<typeof runtimeActivityState>, durableStatus: string | undefined) {
  const word = activeStatusWord(durableStatus);
  return word ? { ...activityState, label: word, text: word } : activityState;
}

export function isRuntimeBusy(activity?: RuntimeActivity) {
  return activity?.liveness === "live" && activity.turn_activity === "busy";
}

export function isSessionBusy(session: Session) {
  return isRuntimeBusy(session.runtime_activity);
}

export function isTaskBusy(task: Task) {
  return isRuntimeBusy(task.runtime_activity);
}

export function isSessionActive(session: Session) {
  return isSessionBusy(session) || isActiveStatus(sessionContinuationStatus(session));
}

export function isTaskActive(task: Task) {
  return isTaskBusy(task) || isActiveStatus(task.status);
}

export function sessionActivity(session: Session) {
  return activityTime(session.last_activity_at, session.updated_at, session.created_at);
}

export function taskActivity(task: Task) {
  return activityTime(task.updated_at, task.created_at);
}

// The navigation projection (#193) carries last_activity_at precomputed by the
// daemon (max of Project and Task activity), so ordering is a single read.
export function navigationActivity(summary: WorkspaceProjectSummary) {
  return activityTime(summary.last_activity_at, summary.updated_at, summary.created_at);
}

export function activityTime(...values: (string | undefined)[]) {
  return Math.max(...values.map((value) => (value ? Date.parse(value) : Number.NEGATIVE_INFINITY)));
}

export function takeRecentWithCurrent<T>(
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

export function itemId(item: unknown) {
  return typeof item === "object" && item !== null && "id" in item && typeof item.id === "string" ? item.id : "";
}

export function ownerIdFromPath(pathname: string, owner: "projects" | "sessions") {
  const match = pathname.match(new RegExp(`/${owner}/([^/]+)`));
  return match ? decodeURIComponent(match[1]) : null;
}

export function taskIdFromPath(pathname: string) {
  const match = pathname.match(/\/projects\/[^/]+\/tasks\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : null;
}

export function projectDisclosureKey(projectId: string) {
  return `cyberpenda.sidebar.project.${projectId}`;
}

export function readDisclosure(key: string, fallback: boolean) {
  try {
    const value = window.sessionStorage.getItem(key);
    return value === null ? fallback : value === "true";
  } catch {
    return fallback;
  }
}

export function writeDisclosure(key: string, value: boolean) {
  try {
    window.sessionStorage.setItem(key, String(value));
  } catch {
    // Storage is a convenience; navigation remains usable when it is blocked.
  }
}
