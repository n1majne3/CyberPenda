// Typed API client for the pentest daemon. Response shapes match the Go structs
// documented in the backend audit.

const base = "";

export class ApiError extends Error {
  status: number;
  body: unknown;

  constructor(message: string, status: number, body: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(base + path, {
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    let body: unknown;
    try {
      body = await res.json();
      if (isErrorBody(body)) message = body.error;
    } catch {
      // non-JSON error; keep status text
    }
    throw new ApiError(message, res.status, body);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export function apiGet<T>(path: string) {
  return request<T>(path);
}
export function apiPost<T>(path: string, body?: unknown) {
  return request<T>(path, { method: "POST", body: body ? JSON.stringify(body) : undefined });
}
export function apiPut<T>(path: string, body?: unknown) {
  return request<T>(path, { method: "PUT", body: body ? JSON.stringify(body) : undefined });
}
export function apiPatch<T>(path: string, body: unknown) {
  return request<T>(path, { method: "PATCH", body: JSON.stringify(body) });
}
export function apiDelete(path: string) {
  return request<void>(path, { method: "DELETE" });
}

// ---- Domain types ----

export interface Scope {
  domains?: string[];
  ips?: string[];
  cidrs?: string[];
  urls?: string[];
  ports?: string[];
  excluded?: string[];
  testing_limits?: string[];
  notes?: string;
}

export interface ProjectDefaults {
  runtime_profile?: string;
  runner?: string;
  task_policy?: string;
}

export interface Project {
  id: string;
  name: string;
  description: string;
  scope: Scope;
  defaults: ProjectDefaults;
  created_at: string;
  updated_at: string;
}

export interface Dashboard {
  project_id: string;
  name: string;
  scope: {
    domains: number;
    ips: number;
    cidrs: number;
    urls: number;
    ports: number;
    excluded: number;
    has_testing_limits: boolean;
    has_notes: boolean;
    ready: boolean;
  };
  counts: {
    tasks: number;
    facts: number;
    findings: number;
    evidence: number;
    pending_approvals: number;
  };
}

export interface Approval {
  id: string;
  project_id: string;
  task_id?: string;
  kind: string;
  status: string;
  requester?: string;
  requested_action: string;
  rationale?: string;
  payload?: unknown;
  reviewer?: string;
  decision?: string;
  created_at: string;
  updated_at: string;
}

export interface AuditEntry {
  id: string;
  project_id: string;
  task_id?: string;
  kind: string;
  summary: string;
  payload?: unknown;
  created_at: string;
}

export interface RuntimeProfile {
  id: string;
  name: string;
  provider: string;
  fields: {
    binary_path?: string;
    model?: string;
    endpoint?: string;
    custom_args?: string[];
    env?: Record<string, string>;
    api_keys?: Record<string, string>;
    credential_refs?: string[];
    mcp_servers?: { name?: string; mode?: string; command?: string; url?: string; args?: string[]; env?: Record<string, string> }[];
    default_runner?: string;
    sandbox_image?: string;
  };
  created_at: string;
  updated_at: string;
}

export interface CredentialBinding {
  id: string;
  credential_ref: string;
  scope: string;
  scope_id?: string;
  source: { kind: string; value?: string };
  disabled?: boolean;
  created_at: string;
  updated_at: string;
}

export interface Task {
  id: string;
  project_id: string;
  goal: string;
  status: string;
  runner: string;
  runtime_profile_id: string;
  run_controls: { yolo?: boolean; notes?: string; extras?: Record<string, string> };
  scope_snapshot: Scope;
  created_at: string;
  updated_at: string;
}

export interface TaskEvent {
  id: string;
  task_id: string;
  seq: number;
  kind: string;
  payload: Record<string, unknown>;
  created_at: string;
}

export interface PreflightCheck {
  name: string;
  status: "pass" | "fail";
  detail?: string;
}

export interface PreflightResult {
  pass: boolean;
  checks: PreflightCheck[];
}

export interface FactIndexEntry {
  fact_key: string;
  category: string;
  summary: string;
  confidence: string;
  scope_status?: string;
}

export interface Fact extends FactIndexEntry {
  id: string;
  project_id: string;
  body: string;
  created_at: string;
  updated_at: string;
}

export interface FactVersion {
  id: string;
  project_id: string;
  fact_key: string;
  version: number;
  category: string;
  summary: string;
  body: string;
  confidence: string;
  scope_status?: string;
  created_at: string;
}

export interface FactRelation {
  id: string;
  project_id: string;
  source_fact_key: string;
  target_fact_key: string;
  relation: string;
  summary: string;
  created_at: string;
  updated_at: string;
}

export interface FindingVersion {
  id: string;
  project_id: string;
  finding_key: string;
  version: number;
  title: string;
  description: string;
  status: string;
  target: string;
  proof: string;
  impact: string;
  recommendation: string;
  cvss_version: string;
  cvss_vector: string;
  cvss_pending: boolean;
  severity: string;
  created_at: string;
}

export interface TaskSummaryVersion {
  id: string;
  task_id: string;
  version: number;
  summary: string;
  submitted_by?: string;
  created_at: string;
}

export interface TaskSummaryResponse {
  summary?: TaskSummaryVersion;
  versions: TaskSummaryVersion[];
}

export interface Finding {
  id: string;
  project_id: string;
  finding_key: string;
  version: number;
  title: string;
  description: string;
  status: string;
  target: string;
  proof: string;
  impact: string;
  recommendation: string;
  cvss_version: string;
  cvss_vector: string;
  cvss_pending: boolean;
  severity: string;
  created_at: string;
  updated_at: string;
}

export interface EvidenceArtifact {
  id: string;
  project_id: string;
  evidence_key: string;
  attach_to_type: string;
  attach_to_key: string;
  artifact_type: string;
  source_path: string;
  managed_path: string;
  sha256: string;
  summary: string;
  created_at: string;
  updated_at: string;
}

// ---- Health ----

export interface Health {
  version: string;
  database: { status: string };
}

function isErrorBody(body: unknown): body is { error: string } {
  return typeof body === "object" && body !== null && "error" in body && typeof (body as { error?: unknown }).error === "string";
}
