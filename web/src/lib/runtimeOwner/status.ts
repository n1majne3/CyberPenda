import type { Session } from "@/lib/api";

export const ACTIVE_OWNER_STATUSES = new Set(["running", "paused"]);

export function isActiveStatus(status: string | undefined): status is string {
  return status !== undefined && ACTIVE_OWNER_STATUSES.has(status);
}

export function statusWord(status: string) {
  return status[0]!.toUpperCase() + status.slice(1);
}

/** The status word for an active continuation, e.g. "Running"; undefined otherwise. */
export function activeStatusWord(status: string | undefined) {
  return isActiveStatus(status) ? statusWord(status) : undefined;
}

export function sessionContinuationStatus(session: Session) {
  return session.active_continuation?.status ?? session.latest_continuation?.status;
}
