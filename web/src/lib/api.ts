// Typed API client for the pentest daemon. Response shapes match the Go structs
// documented in the backend audit.

const base = "";
const authTokenParam = "token";
const authTokenStorageKey = "pentest.authToken";

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
    headers: requestHeaders(init?.headers),
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

function requestHeaders(initHeaders?: HeadersInit): Record<string, string> {
  const headers: Record<string, string> = {};
  setHeader(headers, "Content-Type", "application/json");
  new Headers(initHeaders).forEach((value, key) => {
    setHeader(headers, key, value);
  });
  const token = dashboardAuthToken();
  if (token && !hasHeader(headers, "Authorization")) {
    setHeader(headers, "Authorization", `Bearer ${token}`);
  }
  return headers;
}

function setHeader(headers: Record<string, string>, name: string, value: string) {
  for (const key of Object.keys(headers)) {
    if (key.toLowerCase() === name.toLowerCase()) delete headers[key];
  }
  headers[name] = value;
}

function hasHeader(headers: Record<string, string>, name: string): boolean {
  return Object.keys(headers).some((key) => key.toLowerCase() === name.toLowerCase());
}

function dashboardAuthToken(): string {
  if (typeof window === "undefined") return "";
  const token = new URLSearchParams(window.location.search).get(authTokenParam)?.trim() ?? "";
  if (token) {
    try {
      window.sessionStorage.setItem(authTokenStorageKey, token);
    } catch {
      // Session storage may be disabled; the URL token still works for this request.
    }
    return token;
  }
  try {
    return window.sessionStorage.getItem(authTokenStorageKey)?.trim() ?? "";
  } catch {
    return "";
  }
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

export type ProjectKind = "pentest" | "ctf_challenge" | string;

export interface Project {
  id: string;
  name: string;
  description: string;
  kind?: ProjectKind;
  scope: Scope;
  defaults: ProjectDefaults;
  created_at: string;
  updated_at: string;
}

export interface Dashboard {
  project_id: string;
  name: string;
  project_kind?: ProjectKind;
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
  tasks?: {
    total: number;
    running: number;
    paused: number;
    needs_attention: number;
  };
  blackboard?: {
    observed_graph_revision: number;
    nodes_by_type?: Record<string, number>;
    current_truth: number;
    frontier: number;
    open_attempts: number;
    confirmed_findings: number;
    unconfirmed_findings: number;
    available_evidence: number;
    missing_evidence: number;
    budget_state: string;
    estimated_tokens: number;
  };
  health?: {
    status: string;
    stale: boolean;
    critical: number;
    warning: number;
    info: number;
    latest_run_id: string;
  };
  ctf?: {
    solved: boolean;
    verified_flag_count: number;
    candidate_solution_count: number;
    primary_solution?: NodeRef | null;
  } | null;
  counts: {
    tasks: number;
    facts: number;
    findings: number;
    evidence: number;
  };
  next_actions?: string[];
  _read?: ReadMeta;
}

/** Shared BlackboardReadV1 envelope metadata. */
export interface ReadMeta {
  protocol_version: number;
  projection: string;
  observed_graph_revision?: number;
  observed_state_hash?: string;
  source_pins?: Record<string, unknown>;
  projection_hash?: string;
}

export interface ReadEnvelope<T> {
  protocol_version: number;
  projection: string;
  project_id: string;
  project_kind: ProjectKind;
  observed_graph_revision: number;
  observed_state_hash: string;
  source_pins?: Record<string, unknown>;
  projection_hash: string;
  result: T;
}

export interface NodeRef {
  id: string;
  node_type: string;
  stable_key: string;
  label: string;
}

export interface Lifecycle {
  field: string;
  value: string;
}

export interface NodeRow {
  ref: NodeRef;
  version: number;
  disposition: string;
  lifecycle?: Lifecycle | null;
  scope_status?: string | null;
  severity?: string | null;
  secondary?: string | null;
  updated_at: string;
  about_entities?: NodeRef[];
  relationship_counts?: {
    about_entities: number;
    incoming: number;
    outgoing: number;
    evidence: number;
    contradictions: number;
  };
  updated_provenance?: {
    actor_type: string;
    actor_id: string;
    task_id?: string | null;
    continuation_id?: string | null;
    runtime_profile_id?: string | null;
    runner?: string | null;
    source_event_count: number;
    migration_source?: unknown;
    recorded_at: string;
  };
}

export interface PageInfo {
  limit: number;
  total_items: number;
  next_cursor?: string;
}

export interface RecordCollection {
  items: NodeRow[];
  facets?: Record<string, unknown>;
  page: PageInfo;
}

export interface BlackboardWorkView {
  summary: {
    graph_revision: number;
    node_counts: Record<string, number>;
    edge_counts: Record<string, number>;
    current_truth: number;
    frontier: number;
    open_attempts: number;
    confirmed_findings: number;
    unconfirmed_findings: number;
    verified_solutions: number;
    evidence_missing: number;
    budget: {
      state: string;
      projection_bytes: number;
      estimated_tokens: number;
      target_tokens: number;
      warning_tokens: number;
      required_tokens: number;
    };
    health: {
      status: string;
      stale: boolean;
      critical: number;
      warning: number;
      info: number;
      latest_run_id: string;
    };
  };
  attention: RecordCollection;
  frontier: RecordCollection;
  active_attempts: RecordCollection;
  recent_changes: {
    items: {
      kind: string;
      node?: NodeRow | null;
      edge?: unknown;
      updated_at: string;
    }[];
    page: PageInfo;
  };
  facets?: Record<string, unknown>;
}

export interface EntityItem {
  entity: NodeRef;
  kind: string;
  name: string;
  locator?: string;
  description?: string;
  scope_status?: string;
  status?: string;
  parent_entities?: NodeRef[];
  child_count?: number;
  record_counts?: Record<string, number>;
  highest_finding_severity?: string | null;
  health_severity?: string | null;
}

export interface EntityCollection {
  items: EntityItem[];
  page: PageInfo;
}

export interface GraphExplorer {
  graph: {
    nodes: { row: NodeRow; x_group: string; is_seed: boolean }[];
    edges: unknown[];
  };
  table: {
    nodes: NodeRow[];
    edges: unknown[];
  };
  legend: {
    node_types: Record<string, number>;
    edge_types: Record<string, number>;
    lifecycle_values: Record<string, number>;
  };
  limits: {
    max_nodes: number;
    max_edges: number;
    node_count: number;
    edge_count: number;
    truncated?: boolean;
  };
  equivalent_record_query?: Record<string, unknown>;
}

export interface BlackboardHealth {
  current_graph: {
    revision: number;
    state_hash: string;
    main_projection_hash: string;
  };
  latest_run?: {
    run_id: string;
    status: string;
    overall: string;
    counts?: { critical: number; warning: number; info: number };
    top_results?: unknown[];
  } | null;
  overall: string;
}

export interface NodeDetail {
  id: string;
  node_type: string;
  stable_key: string;
  version: number;
  disposition: string;
  properties: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  merge_target?: NodeRef | null;
}

export interface RecordDetail {
  node: NodeDetail;
  resolved_from_merged_id?: string | null;
  derived?: Record<string, unknown>;
  about_entities?: { items: NodeRef[]; total_items: number; records_href?: string };
  relationships?: {
    incoming: { items: unknown[]; total_items: number; traversal_href?: string };
    outgoing: { items: unknown[]; total_items: number; traversal_href?: string };
  };
  evidence?: { items: NodeRef[]; total_items: number; records_href?: string };
  support?: Record<string, { items: NodeRef[]; total_items: number; traversal_href?: string }>;
  capabilities?: Record<string, unknown>;
}

export interface ReportMarkdown {
  source: {
    project_id: string;
    project_name: string;
    graph_revision: number;
    state_hash: string;
    source_hash: string;
    renderer_version: string;
    scope_context?: string;
  };
  markdown: string;
}

export interface RuntimeProfile {
  id: string;
  name: string;
  provider: string;
  kind?: "manual" | "launch_resolve";
  fields: {
    binary_path?: string;
    model?: string;
    endpoint?: string;
    model_provider_id?: string;
    model_provider_protocol?: string;
    model_override?: string;
    custom_args?: string[];
    env?: Record<string, string>;
    api_keys?: Record<string, string>;
    credential_refs?: string[];
    runtime_extensions?: { id: string; enabled?: boolean; config?: Record<string, string> }[];
    mcp_servers?: { name?: string; mode?: string; command?: string; url?: string; args?: string[]; env?: Record<string, string> }[];
    default_runner?: string;
    sandbox_image?: string;
  };
  created_at: string;
  updated_at: string;
}

export interface ModelProviderEndpoint {
  protocol: string;
  base_url: string;
}

export interface ModelProvider {
  id: string;
  name: string;
  base_url: string;
  protocols?: string[];
  endpoints?: ModelProviderEndpoint[];
  api_key_env: string;
  catalog?: {
    manual?: string[];
    refreshed?: string[];
    default_model?: string;
  };
  created_at?: string;
  updated_at?: string;
}

export interface ModelProviderMigrationPreview {
  profile_id: string;
  profile_name: string;
  runtime_provider: string;
  eligible: boolean;
  reason?: string;
  proposed: {
    name: string;
    base_url: string;
    model?: string;
    protocols?: string[];
    endpoints?: ModelProviderEndpoint[];
    suggested_protocol?: string;
    api_key_env?: string;
  };
  matches: { provider: ModelProvider }[];
  api_key_sources: {
    kind: string;
    credential_ref?: string;
    env_var?: string;
    configured?: boolean;
  }[];
}

export interface ModelProviderMigrationResult {
  profile: RuntimeProfile;
  provider: ModelProvider;
}

export interface RuntimePluginCapabilities {
  sandbox: boolean;
  host: boolean;
  mcp_config: boolean;
  streaming_transcript: boolean;
  resume: boolean;
}

export interface RuntimePluginProfileField {
  name: string;
  type: string;
  label: string;
}

export interface RuntimePluginProfileSchema {
  fields: RuntimePluginProfileField[];
}

export interface RuntimePlugin {
  schema_version: number;
  id: string;
  name: string;
  description?: string;
  binary: {
    default: string;
    profile_field?: string;
  };
  capabilities: RuntimePluginCapabilities;
  model_provider?: {
    requirement: string;
    supported_protocols?: string[];
    protocol_preference?: string[];
  };
  profile_schema: RuntimePluginProfileSchema;
  config_projection: {
    primitive: string;
    config_path?: string;
    mcp_config_path?: string;
  };
  launch: {
    args: string[];
    singleton_options?: { options: string[]; arity: number }[];
  };
  process_env?: Record<string, string>;
  credential_env?: string[];
  transcript: {
    parser: string;
  };
}

export interface RuntimeExtension {
  schema_version: number;
  id: string;
  name: string;
  description?: string;
  compatible_runtime_plugins: string[];
  source: {
    type: "local_dir" | "local_file" | string;
    path: string;
  };
  projection: {
    location: "provider_home" | "runtime_home" | "workdir" | string;
    path: string;
  };
  config?: Record<string, string>;
}

export interface RuntimeExtensionCatalogItem {
  id: string;
  name: string;
  description?: string;
  provider: string;
  registry: string;
  registry_url: string;
  install_ref?: string;
  source_url?: string;
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

export interface Skill {
  id: string;
  name: string;
  description?: string;
  source_provenance?: {
    kind?: string;
    package?: string;
    ref?: string;
    source_url?: string;
    last_imported_at?: string;
    local_modified?: boolean;
  };
  files?: Record<string, string>;
  enabled: boolean;
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
  run_controls: { host_activated?: boolean; sandbox_network?: string; notes?: string; extras?: Record<string, string> };
  scope_snapshot: Scope;
  runtime_controls?: RuntimeControls;
  active_continuation?: TaskContinuation;
  latest_continuation?: TaskContinuation;
  created_at: string;
  updated_at: string;
}

export interface RuntimeControls {
  native_resume_available: boolean;
  native_resume_reason?: string;
  handoff_resume_available: boolean;
  queue_steer_available: boolean;
  interrupt_steer_available: boolean;
  interrupt_steer_reason?: string;
  native_session_captured: boolean;
  same_runtime_provider_only: boolean;
  runtime_provider?: string;
}

export interface TaskContinuation {
  id: string;
  task_id: string;
  number: number;
  runtime_profile_id: string;
  runtime_provider: string;
  runner: string;
  status: string;
  container_id?: string;
  native_session_id?: string;
  native_session_path?: string;
  started_at: string;
  updated_at: string;
  ended_at?: string;
}

export interface TaskEvent {
  id: string;
  task_id: string;
  seq: number;
  kind: string;
  payload: Record<string, unknown>;
  created_at: string;
}

export interface TaskTranscriptEntry {
  id: string;
  seq: number;
  continuation: number;
  kind: "message" | "tool_call" | "tool_result" | "runtime_output" | "continuation" | string;
  role: "user" | "assistant" | "system" | "runtime" | "tool" | string;
  text?: string;
  tool_call_id?: string;
  tool_name?: string;
  details?: Record<string, unknown>;
  stream?: string;
  status?: string;
  created_at: string;
}

export interface TaskTranscript {
  task_id: string;
  entries: TaskTranscriptEntry[];
}

export interface TaskTimelineItem {
  seq: number;
  type: "tool_use" | "tool_result" | "thinking" | "text" | "error" | "lifecycle" | "steering";
  tool?: string;
  content?: string;
  input?: Record<string, unknown>;
  output?: string;
  created_at?: string;
}

export interface TaskTimeline {
  task_id: string;
  items: TaskTimelineItem[];
}

export interface PreflightCheck {
  name: string;
  status: "pass" | "fail";
  detail?: string;
}

export interface PreflightSkill {
  id: string;
  name: string;
}

export interface PreflightModelProvider {
  model_provider_id?: string;
  model_provider_name?: string;
  endpoint_base_url?: string;
  base_url?: string;
  protocol?: string;
  model?: string;
  api_key_env?: string;
  api_key_source?: string;
  projection_target?: string;
}

export interface PreflightRuntimeExtension {
  id: string;
  name?: string;
  source: "registry" | "catalog" | string;
  install_ref?: string;
  registry?: string;
}

export interface PreflightResult {
  pass: boolean;
  checks: PreflightCheck[];
  skills?: PreflightSkill[];
  runtime_extensions?: PreflightRuntimeExtension[];
  model_provider?: PreflightModelProvider;
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
