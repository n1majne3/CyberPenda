import type { Session } from "@/lib/api";

// Durable owner-status vocabulary shared by the detail header and the sidebar
// (#268) so the two surfaces cannot drift: the same active-status set, the same
// capitalized status word, and the same Session continuation precedence.

/** Durable statuses the UI treats as an active continuation; paused stays interactive. */
export const ACTIVE_OWNER_STATUSES = new Set(["running", "paused"]);

export function isActiveStatus(status: string | undefined): status is string {
  return status !== undefined && ACTIVE_OWNER_STATUSES.has(status);
}

/** The capitalized durable status word, e.g. "Running". */
export function statusWord(status: string) {
  return status[0]!.toUpperCase() + status.slice(1);
}

/** The status word for an active continuation, e.g. "Running"; undefined otherwise. */
export function activeStatusWord(status: string | undefined) {
  return isActiveStatus(status) ? statusWord(status) : undefined;
}

/** Durable Session status precedence: the active continuation outranks the latest one. */
export function sessionContinuationStatus(session: Session) {
  return session.active_continuation?.status ?? session.latest_continuation?.status;
}
