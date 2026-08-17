/** Operator-visible idempotency ids for owner writes. */

export function newSteerRequestID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `steer-${Math.random().toString(36).slice(2)}-${performance.now().toString(36)}`;
}

export function newBlackboardRetryID() {
  return `blackboard-retry-${newSteerRequestID()}`;
}

export function newPermissionRequestID() {
  return `permission-${newSteerRequestID()}`;
}
