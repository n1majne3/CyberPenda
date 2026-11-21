
export function projectBlackboardV2Base(projectId: string): string {
  return `/api/v2/projects/${encodeURIComponent(projectId)}/blackboard`;
}

export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    search.set(key, String(value));
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : "";
}

export function recordHref(projectId: string, key: string): string {
  return `/projects/${projectId}/blackboard/records/${encodeURIComponent(key)}`;
}

export function blackboardHref(
  projectId: string,
  tab: "work" | "knowledge" | "explorer" = "work",
  params: Record<string, string | undefined> = {},
): string {
  const base =
    tab === "work"
      ? `/projects/${projectId}/blackboard`
      : `/projects/${projectId}/blackboard/${tab}`;
  return `${base}${qs(params)}`;
}

export function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function requireString(value: unknown, path: string): string {
  if (typeof value !== "string" || value === "") {
    throw new Error(`${path} must be a non-empty string`);
  }
  return value;
}

export function requireNumber(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`${path} must be a finite number`);
  }
  return value;
}

export function requireNonNegativeInteger(value: unknown, path: string): number {
  const number = requireNumber(value, path);
  if (!Number.isInteger(number) || number < 0) {
    throw new Error(`${path} must be a non-negative integer`);
  }
  return number;
}

export function requireBoolean(value: unknown, path: string): boolean {
  if (typeof value !== "boolean") {
    throw new Error(`${path} must be a boolean`);
  }
  return value;
}

/** Closed blackboardKey: printable ASCII, 1..96 code units. */
export function requireBlackboardKey(value: unknown, path: string): string {
  const key = requireString(value, path);
  if (key.length > 96) {
    throw new Error(`${path} exceeds 96 character blackboardKey limit`);
  }
  if (!/^[\x20-\x7e]+$/.test(key)) {
    throw new Error(`${path} is not a printable ASCII blackboardKey`);
  }
  return key;
}

export function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

export function optionalBlackboardKey(value: unknown, path: string): string | undefined {
  if (value === undefined || value === null) return undefined;
  return requireBlackboardKey(value, path);
}

export function assertAllowlist(
  raw: Record<string, unknown>,
  allowed: readonly string[],
  path: string,
): void {
  const allow = new Set(allowed);
  for (const key of Object.keys(raw)) {
    if (!allow.has(key)) {
      throw new Error(`${path} has non-allowlisted field ${key}`);
    }
  }
}

/** Schema allows empty markdown; only type is enforced. */
export function requireStringAllowEmpty(value: unknown, path: string): string {
  if (typeof value !== "string") {
    throw new Error(`${path} must be a string`);
  }
  return value;
}
