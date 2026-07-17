import { apiGet, ApiError } from "@/lib/api";

/** runtime-blackboard/v2 schema identifier. */
export const RUNTIME_SNAPSHOT_SCHEMA = "runtime-blackboard/v2" as const;
/** blackboard-record/v2 schema identifier. */
export const RECORD_DETAIL_SCHEMA = "blackboard-record/v2" as const;
/** semantic-history/v2 schema identifier. */
export const SEMANTIC_HISTORY_SCHEMA = "semantic-history/v2" as const;

export const SNAPSHOT_SEMANTICS =
  "work is active; knowledge is current; history and details are available by key" as const;

/** Closed relationship vocabulary (11 types). */
export const RELATIONSHIP_TYPES = [
  "about",
  "part_of",
  "tests",
  "produced",
  "evidences",
  "supports",
  "contradicts",
  "derived_from",
  "depends_on",
  "satisfies",
  "supersedes",
] as const;

export type RelationshipType = (typeof RELATIONSHIP_TYPES)[number];

export const RECORD_TYPES = [
  "objective",
  "attempt",
  "entity",
  "fact",
  "finding",
  "solution",
  "evidence",
] as const;

export type RecordType = (typeof RECORD_TYPES)[number];

/** Snapshot field allowlists (lists only — detail may add bodies/proof fields). */
export const SNAPSHOT_FIELD_ALLOWLIST = {
  objectives: ["version", "status", "objective"] as const,
  attempts: ["version", "status", "summary"] as const,
  entities: [
    "version",
    "status",
    "kind",
    "name",
    "locator",
    "description",
    "scope_status",
    "credential_ref",
  ] as const,
  facts: ["version", "category", "summary", "confidence", "scope_status"] as const,
  findings: [
    "version",
    "status",
    "title",
    "target",
    "description",
    "severity",
    "cvss_pending",
  ] as const,
  solutions: [
    "version",
    "status",
    "kind",
    "summary",
    "value",
    "verification_summary",
  ] as const,
  evidence: [
    "version",
    "status",
    "artifact_type",
    "summary",
    "media_type",
    "captured_at",
  ] as const,
} as const;

export type WorkGroup = "objectives" | "attempts";
export type KnowledgeGroup = "entities" | "facts" | "findings" | "solutions" | "evidence";

export interface SnapshotObjective {
  version: number;
  status: string;
  objective: string;
}

export interface SnapshotAttempt {
  version: number;
  status: string;
  summary: string;
}

export interface SnapshotEntity {
  version: number;
  status: string;
  kind: string;
  name: string;
  locator?: string;
  description?: string;
  scope_status: string;
  credential_ref?: string;
}

export interface SnapshotFact {
  version: number;
  category: string;
  summary: string;
  confidence: string;
  scope_status: string;
}

export interface SnapshotFinding {
  version: number;
  status: string;
  title: string;
  target?: string;
  description?: string;
  severity?: string;
  cvss_pending: boolean;
}

export interface SnapshotSolution {
  version: number;
  status: string;
  kind: string;
  summary: string;
  value?: string;
  verification_summary?: string;
}

export interface SnapshotEvidence {
  version: number;
  status: string;
  artifact_type: string;
  summary: string;
  media_type?: string;
  captured_at?: string;
}

export interface SnapshotWork {
  objectives?: Record<string, SnapshotObjective>;
  attempts?: Record<string, SnapshotAttempt>;
}

export interface SnapshotKnowledge {
  entities?: Record<string, SnapshotEntity>;
  facts?: Record<string, SnapshotFact>;
  findings?: Record<string, SnapshotFinding>;
  solutions?: Record<string, SnapshotSolution>;
  evidence?: Record<string, SnapshotEvidence>;
}

/** Parsed relationship: [from, type, to] or [from, type, to, reason]. */
export interface RelationshipEdge {
  from: string;
  relation: RelationshipType;
  to: string;
  reason?: string;
}

export interface RuntimeSnapshot {
  schema: typeof RUNTIME_SNAPSHOT_SCHEMA;
  semantics: string;
  revision: number;
  work: SnapshotWork;
  knowledge: SnapshotKnowledge;
  relations: RelationshipEdge[];
}

export interface SemanticRecord {
  status?: string;
  objective?: string;
  resolution_summary?: string;
  kind?: string;
  name?: string;
  locator?: string;
  description?: string;
  scope_status?: string;
  credential_ref?: string;
  category?: string;
  summary?: string;
  body?: string;
  confidence?: string;
  title?: string;
  target?: string;
  proof?: string;
  impact?: string;
  recommendation?: string;
  cvss_version?: string;
  cvss_vector?: string;
  severity?: string;
  cvss_pending?: boolean;
  value?: string;
  verification_summary?: string;
  artifact_type?: string;
  media_type?: string;
  source_path?: string;
  managed_path?: string;
  sha256?: string;
  size?: number;
  captured_at?: string;
}

export interface CurrentDetail {
  schema: typeof RECORD_DETAIL_SCHEMA;
  revision: number;
  key: string;
  type: RecordType | string;
  version: number;
  record: SemanticRecord;
  relationships: RelationshipEdge[];
}

export interface HistoryItem {
  kind: string;
  key?: string;
  version: number;
  type?: string;
  record?: SemanticRecord;
  from?: string;
  relation?: string;
  to?: string;
  reason?: string;
}

export interface SemanticHistory {
  schema: typeof SEMANTIC_HISTORY_SCHEMA;
  revision: number;
  key: string;
  items: HistoryItem[];
  next_cursor?: string;
}

/** blackboard-health/v2 schema identifier. */
export const HEALTH_SCHEMA = "blackboard-health/v2" as const;
/** report-markdown/v2 schema identifier. */
export const REPORT_MARKDOWN_SCHEMA = "report-markdown/v2" as const;
/** pentest-report/v2 schema identifier. */
export const PENTEST_REPORT_SCHEMA = "pentest-report/v2" as const;
/** ctf-solution/v2 schema identifier. */
export const CTF_SOLUTION_SCHEMA = "ctf-solution/v2" as const;

export type HealthStatus = "healthy" | "attention" | "warning" | "critical";
export type HealthSeverity = "info" | "warning" | "critical";
export type AttentionBudgetState =
  | "within_target"
  | "above_target"
  | "warning"
  | "required";

export interface HealthAttention {
  bytes: number;
  estimated_tokens: number;
  state: AttentionBudgetState;
  complete: boolean;
  launchable: boolean;
  consolidation_offered: boolean;
  consolidation_required: boolean;
}

export interface HealthAnomaly {
  code: string;
  severity: HealthSeverity;
  message: string;
  subject_key?: string;
  related_keys?: string[];
}

/** Approval-required operator proposal suggested by health (no mutation/scheduler). */
export interface HealthProposal {
  code: "consolidation_reason_task";
  action: "start_reason_task";
  approval_required: true;
  required: boolean;
}

export interface SemanticHealth {
  schema: typeof HEALTH_SCHEMA;
  revision: number;
  status: HealthStatus;
  attention: HealthAttention;
  anomalies: HealthAnomaly[];
  proposals: HealthProposal[];
}

export interface BlackboardV2Error {
  code: string;
  message: string;
  path?: string;
  retryable: boolean;
  details?: Record<string, unknown>;
}

export interface ReportMarkdown {
  schema: typeof REPORT_MARKDOWN_SCHEMA;
  markdown: string;
}

export interface ReportFactDTO {
  key: string;
  category: string;
  summary: string;
  body?: string;
  confidence: string;
  scope_status: string;
}

export interface ReportEvidenceDTO {
  key: string;
  status: string;
  artifact_type: string;
  summary: string;
  media_type?: string;
  captured_at?: string;
}

export interface ReportFindingDTO {
  key: string;
  title: string;
  status: string;
  severity?: string;
  cvss_version?: string;
  cvss_vector?: string;
  cvss_pending: boolean;
  target?: string;
  description?: string;
  proof?: string;
  impact?: string;
  recommendation?: string;
  supporting_facts: ReportFactDTO[];
  contradictions: ReportFactDTO[];
  evidence: ReportEvidenceDTO[];
}

export interface ReportSolutionDTO {
  key: string;
  kind: string;
  status: string;
  summary: string;
  value?: string;
  verification_summary?: string;
}

export interface PentestReportProjection {
  schema: typeof PENTEST_REPORT_SCHEMA;
  project: { name: string; description?: string };
  confirmed_findings: ReportFindingDTO[];
  unconfirmed_findings: ReportFindingDTO[];
  confirmed_facts: ReportFactDTO[];
  tentative_facts: ReportFactDTO[];
}

export interface CTFSolutionProjection {
  schema: typeof CTF_SOLUTION_SCHEMA;
  project: { name: string; description?: string };
  solved: boolean;
  verified_flags: ReportSolutionDTO[];
  candidate_flags: ReportSolutionDTO[];
  answers: ReportSolutionDTO[];
  procedures: ReportSolutionDTO[];
  confirmed_facts: ReportFactDTO[];
  tentative_facts: ReportFactDTO[];
  evidence: ReportEvidenceDTO[];
}

export interface GraphNode {
  key: string;
  type: RecordType;
  label: string;
  status?: string;
  secondary?: string;
}

export interface GraphExplorerModel {
  nodes: GraphNode[];
  edges: RelationshipEdge[];
}

/** Forbidden ordinary-Blackboard surfaces (v1/audit-first). */
export const FORBIDDEN_ORDINARY_UI_TERMS = [
  "Provenance",
  "Fact Index",
  "Recent changes",
  "Recent Changes",
  "source events",
  "state_hash",
  "projection_hash",
  "observed_state_hash",
  "semantic_hash",
  "Frontier",
  "Current Truth",
  "health_run",
  "checker_version",
  "graph hash",
] as const;

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

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requireString(value: unknown, path: string): string {
  if (typeof value !== "string" || value === "") {
    throw new Error(`${path} must be a non-empty string`);
  }
  return value;
}

function requireNumber(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`${path} must be a finite number`);
  }
  return value;
}

function requireNonNegativeInteger(value: unknown, path: string): number {
  const number = requireNumber(value, path);
  if (!Number.isInteger(number) || number < 0) {
    throw new Error(`${path} must be a non-negative integer`);
  }
  return number;
}

function requireBoolean(value: unknown, path: string): boolean {
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

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

function optionalBlackboardKey(value: unknown, path: string): string | undefined {
  if (value === undefined || value === null) return undefined;
  return requireBlackboardKey(value, path);
}

const RELATIONSHIP_SET = new Set<string>(RELATIONSHIP_TYPES);

/**
 * Parse a closed relationship tuple: [from, type, to] or [from, type, to, reason].
 * Structured array parsing only — never ad-hoc string splitting.
 */
export function parseRelationship(raw: unknown, path = "relation"): RelationshipEdge {
  if (!Array.isArray(raw)) {
    throw new Error(`${path} must be an array tuple`);
  }
  if (raw.length !== 3 && raw.length !== 4) {
    throw new Error(`${path} must have 3 or 4 elements`);
  }
  const from = requireString(raw[0], `${path}[0]`);
  const relationRaw = requireString(raw[1], `${path}[1]`);
  if (!RELATIONSHIP_SET.has(relationRaw)) {
    throw new Error(`${path}[1] is not a closed relationship type: ${relationRaw}`);
  }
  const to = requireString(raw[2], `${path}[2]`);
  const reason = raw.length === 4 ? optionalString(raw[3]) : undefined;
  if (raw.length === 4 && reason === undefined) {
    throw new Error(`${path}[3] reason must be a non-empty string when present`);
  }
  return {
    from,
    relation: relationRaw as RelationshipType,
    to,
    ...(reason !== undefined ? { reason } : {}),
  };
}

function parseKeyedGroup<T>(
  raw: unknown,
  path: string,
  parseEntry: (entry: unknown, entryPath: string) => T,
): Record<string, T> | undefined {
  if (raw === undefined || raw === null) return undefined;
  if (!isPlainObject(raw)) {
    throw new Error(`${path} must be an object map`);
  }
  const out: Record<string, T> = {};
  for (const [key, value] of Object.entries(raw)) {
    out[key] = parseEntry(value, `${path}.${key}`);
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function parseObjective(raw: unknown, path: string): SnapshotObjective {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, SNAPSHOT_FIELD_ALLOWLIST.objectives, path);
  return {
    version: requireNumber(raw.version, `${path}.version`),
    status: requireString(raw.status, `${path}.status`),
    objective: requireString(raw.objective, `${path}.objective`),
  };
}

function parseAttempt(raw: unknown, path: string): SnapshotAttempt {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, SNAPSHOT_FIELD_ALLOWLIST.attempts, path);
  return {
    version: requireNumber(raw.version, `${path}.version`),
    status: requireString(raw.status, `${path}.status`),
    summary: requireString(raw.summary, `${path}.summary`),
  };
}

function parseEntity(raw: unknown, path: string): SnapshotEntity {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, SNAPSHOT_FIELD_ALLOWLIST.entities, path);
  return {
    version: requireNumber(raw.version, `${path}.version`),
    status: requireString(raw.status, `${path}.status`),
    kind: requireString(raw.kind, `${path}.kind`),
    name: requireString(raw.name, `${path}.name`),
    locator: optionalString(raw.locator),
    description: optionalString(raw.description),
    scope_status: requireString(raw.scope_status, `${path}.scope_status`),
    credential_ref: optionalString(raw.credential_ref),
  };
}

function parseFact(raw: unknown, path: string): SnapshotFact {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, SNAPSHOT_FIELD_ALLOWLIST.facts, path);
  return {
    version: requireNumber(raw.version, `${path}.version`),
    category: requireString(raw.category, `${path}.category`),
    summary: requireString(raw.summary, `${path}.summary`),
    confidence: requireString(raw.confidence, `${path}.confidence`),
    scope_status: requireString(raw.scope_status, `${path}.scope_status`),
  };
}

function parseFinding(raw: unknown, path: string): SnapshotFinding {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, SNAPSHOT_FIELD_ALLOWLIST.findings, path);
  return {
    version: requireNumber(raw.version, `${path}.version`),
    status: requireString(raw.status, `${path}.status`),
    title: requireString(raw.title, `${path}.title`),
    target: optionalString(raw.target),
    description: optionalString(raw.description),
    severity: optionalString(raw.severity),
    cvss_pending: Boolean(raw.cvss_pending),
  };
}

function parseSolution(raw: unknown, path: string): SnapshotSolution {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, SNAPSHOT_FIELD_ALLOWLIST.solutions, path);
  return {
    version: requireNumber(raw.version, `${path}.version`),
    status: requireString(raw.status, `${path}.status`),
    kind: requireString(raw.kind, `${path}.kind`),
    summary: requireString(raw.summary, `${path}.summary`),
    value: optionalString(raw.value),
    verification_summary: optionalString(raw.verification_summary),
  };
}

function parseEvidence(raw: unknown, path: string): SnapshotEvidence {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, SNAPSHOT_FIELD_ALLOWLIST.evidence, path);
  return {
    version: requireNumber(raw.version, `${path}.version`),
    status: requireString(raw.status, `${path}.status`),
    artifact_type: requireString(raw.artifact_type, `${path}.artifact_type`),
    summary: requireString(raw.summary, `${path}.summary`),
    media_type: optionalString(raw.media_type),
    captured_at: optionalString(raw.captured_at),
  };
}

function assertAllowlist(
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

/** Parse and validate a runtime-blackboard/v2 document with structured field rules. */
export function parseRuntimeSnapshot(raw: unknown): RuntimeSnapshot {
  if (!isPlainObject(raw)) {
    throw new Error("snapshot must be an object");
  }
  const schema = requireString(raw.schema, "schema");
  if (schema !== RUNTIME_SNAPSHOT_SCHEMA) {
    throw new Error(`schema must be ${RUNTIME_SNAPSHOT_SCHEMA}`);
  }
  const semantics = requireString(raw.semantics, "semantics");
  const revision = requireNumber(raw.revision, "revision");
  if (!isPlainObject(raw.work)) {
    throw new Error("work must be an object");
  }
  if (!isPlainObject(raw.knowledge)) {
    throw new Error("knowledge must be an object");
  }
  if (!Array.isArray(raw.relations)) {
    throw new Error("relations must be an array");
  }

  for (const key of Object.keys(raw.work)) {
    if (key !== "objectives" && key !== "attempts") {
      throw new Error(`work has unknown group ${key}`);
    }
  }
  for (const key of Object.keys(raw.knowledge)) {
    if (
      key !== "entities" &&
      key !== "facts" &&
      key !== "findings" &&
      key !== "solutions" &&
      key !== "evidence"
    ) {
      throw new Error(`knowledge has unknown group ${key}`);
    }
  }

  const work: SnapshotWork = {
    objectives: parseKeyedGroup(raw.work.objectives, "work.objectives", parseObjective),
    attempts: parseKeyedGroup(raw.work.attempts, "work.attempts", parseAttempt),
  };
  const knowledge: SnapshotKnowledge = {
    entities: parseKeyedGroup(raw.knowledge.entities, "knowledge.entities", parseEntity),
    facts: parseKeyedGroup(raw.knowledge.facts, "knowledge.facts", parseFact),
    findings: parseKeyedGroup(raw.knowledge.findings, "knowledge.findings", parseFinding),
    solutions: parseKeyedGroup(raw.knowledge.solutions, "knowledge.solutions", parseSolution),
    evidence: parseKeyedGroup(raw.knowledge.evidence, "knowledge.evidence", parseEvidence),
  };
  const relations = raw.relations.map((item, index) =>
    parseRelationship(item, `relations[${index}]`),
  );

  return {
    schema: RUNTIME_SNAPSHOT_SCHEMA,
    semantics,
    revision,
    work,
    knowledge,
    relations,
  };
}

function parseSemanticRecord(raw: unknown, path: string): SemanticRecord {
  if (!isPlainObject(raw)) {
    throw new Error(`${path} must be an object`);
  }
  const record: SemanticRecord = {};
  const assignString = (field: keyof SemanticRecord) => {
    if (typeof raw[field] === "string") {
      (record as Record<string, unknown>)[field] = raw[field];
    }
  };
  const fields: (keyof SemanticRecord)[] = [
    "status",
    "objective",
    "resolution_summary",
    "kind",
    "name",
    "locator",
    "description",
    "scope_status",
    "credential_ref",
    "category",
    "summary",
    "body",
    "confidence",
    "title",
    "target",
    "proof",
    "impact",
    "recommendation",
    "cvss_version",
    "cvss_vector",
    "severity",
    "value",
    "verification_summary",
    "artifact_type",
    "media_type",
    "source_path",
    "managed_path",
    "sha256",
    "captured_at",
  ];
  for (const field of fields) assignString(field);
  if (typeof raw.cvss_pending === "boolean") record.cvss_pending = raw.cvss_pending;
  if (typeof raw.size === "number") record.size = raw.size;
  return record;
}

/** Parse blackboard-record/v2 current detail. */
export function parseCurrentDetail(raw: unknown): CurrentDetail {
  if (!isPlainObject(raw)) throw new Error("detail must be an object");
  const schema = requireString(raw.schema, "schema");
  if (schema !== RECORD_DETAIL_SCHEMA) {
    throw new Error(`schema must be ${RECORD_DETAIL_SCHEMA}`);
  }
  if (!Array.isArray(raw.relationships)) {
    throw new Error("relationships must be an array");
  }
  return {
    schema: RECORD_DETAIL_SCHEMA,
    revision: requireNumber(raw.revision, "revision"),
    key: requireString(raw.key, "key"),
    type: requireString(raw.type, "type"),
    version: requireNumber(raw.version, "version"),
    record: parseSemanticRecord(raw.record, "record"),
    relationships: raw.relationships.map((item, index) =>
      parseRelationship(item, `relationships[${index}]`),
    ),
  };
}

const ATTENTION_STATES = new Set<string>([
  "within_target",
  "above_target",
  "warning",
  "required",
]);
const HEALTH_STATUSES = new Set<string>(["healthy", "attention", "warning", "critical"]);
const HEALTH_SEVERITIES = new Set<string>(["info", "warning", "critical"]);
const HEALTH_ROOT_FIELDS = [
  "schema",
  "revision",
  "status",
  "attention",
  "anomalies",
  "proposals",
  "sync",
] as const;
const HEALTH_ATTENTION_FIELDS = [
  "bytes",
  "estimated_tokens",
  "state",
  "complete",
  "launchable",
  "consolidation_offered",
  "consolidation_required",
] as const;
const HEALTH_ANOMALY_FIELDS = [
  "code",
  "severity",
  "message",
  "subject_key",
  "related_keys",
] as const;
const HEALTH_PROPOSAL_FIELDS = [
  "code",
  "action",
  "approval_required",
  "required",
] as const;

/** Parse blackboard-health/v2 with closed schema and typed field rules. */
export function parseSemanticHealth(raw: unknown): SemanticHealth {
  if (!isPlainObject(raw)) throw new Error("health must be an object");
  assertAllowlist(raw, HEALTH_ROOT_FIELDS, "health");
  const schema = requireString(raw.schema, "schema");
  if (schema !== HEALTH_SCHEMA) {
    throw new Error(`schema must be ${HEALTH_SCHEMA}`);
  }
  const status = requireString(raw.status, "status");
  if (!HEALTH_STATUSES.has(status)) {
    throw new Error(`status is not a closed health status: ${status}`);
  }
  if (!isPlainObject(raw.attention)) {
    throw new Error("attention must be an object");
  }
  const attentionRaw = raw.attention;
  assertAllowlist(attentionRaw, HEALTH_ATTENTION_FIELDS, "attention");
  const state = requireString(attentionRaw.state, "attention.state");
  if (!ATTENTION_STATES.has(state)) {
    throw new Error(`attention.state is not a closed budget state: ${state}`);
  }
  if (!Array.isArray(raw.anomalies)) {
    throw new Error("anomalies must be an array");
  }
  if (!Array.isArray(raw.proposals)) {
    throw new Error("proposals must be an array");
  }
  const anomalies: HealthAnomaly[] = raw.anomalies.map((item, index) => {
    if (!isPlainObject(item)) {
      throw new Error(`anomalies[${index}] must be an object`);
    }
    assertAllowlist(item, HEALTH_ANOMALY_FIELDS, `anomalies[${index}]`);
    const severity = requireString(item.severity, `anomalies[${index}].severity`);
    if (!HEALTH_SEVERITIES.has(severity)) {
      throw new Error(`anomalies[${index}].severity is not closed: ${severity}`);
    }
    const related =
      item.related_keys === undefined
        ? undefined
        : Array.isArray(item.related_keys)
          ? item.related_keys.map((key, keyIndex) =>
              requireBlackboardKey(key, `anomalies[${index}].related_keys[${keyIndex}]`),
            )
          : (() => {
              throw new Error(`anomalies[${index}].related_keys must be an array`);
            })();
    return {
      code: requireString(item.code, `anomalies[${index}].code`),
      severity: severity as HealthSeverity,
      message: requireString(item.message, `anomalies[${index}].message`),
      subject_key: optionalBlackboardKey(item.subject_key, `anomalies[${index}].subject_key`),
      ...(related !== undefined ? { related_keys: related } : {}),
    };
  });
  const proposals: HealthProposal[] = raw.proposals.map((item, index) => {
    if (!isPlainObject(item)) {
      throw new Error(`proposals[${index}] must be an object`);
    }
    assertAllowlist(item, HEALTH_PROPOSAL_FIELDS, `proposals[${index}]`);
    const code = requireString(item.code, `proposals[${index}].code`);
    if (code !== "consolidation_reason_task") {
      throw new Error(`proposals[${index}].code is not a closed proposal code: ${code}`);
    }
    const action = requireString(item.action, `proposals[${index}].action`);
    if (action !== "start_reason_task") {
      throw new Error(`proposals[${index}].action is not a closed proposal action: ${action}`);
    }
    if (item.approval_required !== true) {
      throw new Error(`proposals[${index}].approval_required must be true`);
    }
    return {
      code: "consolidation_reason_task",
      action: "start_reason_task",
      approval_required: true,
      required: requireBoolean(item.required, `proposals[${index}].required`),
    };
  });
  return {
    schema: HEALTH_SCHEMA,
    revision: requireNonNegativeInteger(raw.revision, "revision"),
    status: status as HealthStatus,
    attention: {
      bytes: requireNonNegativeInteger(attentionRaw.bytes, "attention.bytes"),
      estimated_tokens: requireNonNegativeInteger(
        attentionRaw.estimated_tokens,
        "attention.estimated_tokens",
      ),
      state: state as AttentionBudgetState,
      complete: requireBoolean(attentionRaw.complete, "attention.complete"),
      launchable: requireBoolean(attentionRaw.launchable, "attention.launchable"),
      consolidation_offered: requireBoolean(
        attentionRaw.consolidation_offered,
        "attention.consolidation_offered",
      ),
      consolidation_required: requireBoolean(
        attentionRaw.consolidation_required,
        "attention.consolidation_required",
      ),
    },
    anomalies,
    proposals,
  };
}

/** Human-readable attention budget label for the ordinary UI. */
export function attentionLabel(state: AttentionBudgetState): string {
  switch (state) {
    case "within_target":
      return "Within 16K target";
    case "above_target":
      return "Above 16K target";
    case "warning":
      return "32K warning — offer Reason Task";
    case "required":
      return "64K consolidation required";
  }
}

/** Parse semantic-history/v2. */
export function parseSemanticHistory(raw: unknown): SemanticHistory {
  if (!isPlainObject(raw)) throw new Error("history must be an object");
  const schema = requireString(raw.schema, "schema");
  if (schema !== SEMANTIC_HISTORY_SCHEMA) {
    throw new Error(`schema must be ${SEMANTIC_HISTORY_SCHEMA}`);
  }
  if (!Array.isArray(raw.items)) {
    throw new Error("items must be an array");
  }
  const items: HistoryItem[] = raw.items.map((item, index) => {
    if (!isPlainObject(item)) {
      throw new Error(`items[${index}] must be an object`);
    }
    const historyItem: HistoryItem = {
      kind: requireString(item.kind, `items[${index}].kind`),
      version: requireNumber(item.version, `items[${index}].version`),
    };
    if (typeof item.key === "string") historyItem.key = item.key;
    if (typeof item.type === "string") historyItem.type = item.type;
    if (item.record !== undefined) {
      historyItem.record = parseSemanticRecord(item.record, `items[${index}].record`);
    }
    if (typeof item.from === "string") historyItem.from = item.from;
    if (typeof item.relation === "string") historyItem.relation = item.relation;
    if (typeof item.to === "string") historyItem.to = item.to;
    if (typeof item.reason === "string") historyItem.reason = item.reason;
    return historyItem;
  });
  return {
    schema: SEMANTIC_HISTORY_SCHEMA,
    revision: requireNumber(raw.revision, "revision"),
    key: requireString(raw.key, "key"),
    items,
    next_cursor: optionalString(raw.next_cursor),
  };
}

/** Extract stable Blackboard v2 error from HTTP error body. */
export function parseBlackboardV2Error(body: unknown): BlackboardV2Error | null {
  if (!isPlainObject(body)) return null;
  const error = body.error;
  if (!isPlainObject(error)) return null;
  if (typeof error.code !== "string" || typeof error.message !== "string") return null;
  return {
    code: error.code,
    message: error.message,
    path: typeof error.path === "string" ? error.path : undefined,
    retryable: Boolean(error.retryable),
    details: isPlainObject(error.details) ? error.details : undefined,
  };
}

export function formatBlackboardV2Error(err: unknown): string {
  if (err instanceof ApiError) {
    const parsed = parseBlackboardV2Error(err.body);
    if (parsed) {
      const path = parsed.path ? ` (${parsed.path})` : "";
      return `${parsed.code}: ${parsed.message}${path}`;
    }
    return err.message;
  }
  if (err instanceof Error) return err.message;
  return String(err);
}

export function knowledgeGroupsForProjectKind(
  kind: string | undefined,
): KnowledgeGroup[] {
  const isCtf = kind === "ctf_challenge";
  return isCtf
    ? ["entities", "facts", "solutions", "evidence"]
    : ["entities", "facts", "findings", "evidence"];
}

export type SnapshotListEntry = {
  key: string;
  type: RecordType;
  group: WorkGroup | KnowledgeGroup;
  section: "work" | "knowledge";
  version: number;
  primary: string;
  secondary?: string;
  status?: string;
  badges: string[];
  fields: Record<string, string | number | boolean>;
};

function sortedKeys(map: Record<string, unknown> | undefined): string[] {
  return Object.keys(map ?? {}).sort((a, b) => a.localeCompare(b));
}

/** Flatten snapshot allowlist fields into list rows for Work + Knowledge. */
export function listSnapshotEntries(
  snapshot: RuntimeSnapshot,
  projectKind?: string,
): SnapshotListEntry[] {
  const entries: SnapshotListEntry[] = [];
  for (const key of sortedKeys(snapshot.work.objectives as Record<string, unknown> | undefined)) {
    const row = snapshot.work.objectives![key];
    entries.push({
      key,
      type: "objective",
      group: "objectives",
      section: "work",
      version: row.version,
      primary: row.objective,
      status: row.status,
      badges: [row.status],
      fields: { version: row.version, status: row.status, objective: row.objective },
    });
  }
  for (const key of sortedKeys(snapshot.work.attempts as Record<string, unknown> | undefined)) {
    const row = snapshot.work.attempts![key];
    entries.push({
      key,
      type: "attempt",
      group: "attempts",
      section: "work",
      version: row.version,
      primary: row.summary,
      status: row.status,
      badges: [row.status],
      fields: { version: row.version, status: row.status, summary: row.summary },
    });
  }

  const groups = knowledgeGroupsForProjectKind(projectKind);
  for (const group of groups) {
    const map = snapshot.knowledge[group] as Record<string, Record<string, unknown>> | undefined;
    for (const key of sortedKeys(map)) {
      const row = map![key];
      entries.push(knowledgeEntry(group, key, row));
    }
  }
  return entries;
}

function knowledgeEntry(
  group: KnowledgeGroup,
  key: string,
  row: Record<string, unknown>,
): SnapshotListEntry {
  const version = Number(row.version);
  switch (group) {
    case "entities": {
      const entity = row as unknown as SnapshotEntity;
      return {
        key,
        type: "entity",
        group,
        section: "knowledge",
        version,
        primary: entity.name,
        secondary: entity.locator ?? entity.kind,
        status: entity.status,
        badges: [entity.status, entity.kind, entity.scope_status.replaceAll("_", "-")],
        fields: {
          version: entity.version,
          status: entity.status,
          kind: entity.kind,
          name: entity.name,
          ...(entity.locator ? { locator: entity.locator } : {}),
          ...(entity.description ? { description: entity.description } : {}),
          scope_status: entity.scope_status,
          ...(entity.credential_ref ? { credential_ref: entity.credential_ref } : {}),
        },
      };
    }
    case "facts": {
      const fact = row as unknown as SnapshotFact;
      return {
        key,
        type: "fact",
        group,
        section: "knowledge",
        version,
        primary: fact.summary,
        secondary: fact.category,
        badges: [fact.confidence, fact.scope_status.replaceAll("_", "-")],
        fields: {
          version: fact.version,
          category: fact.category,
          summary: fact.summary,
          confidence: fact.confidence,
          scope_status: fact.scope_status,
        },
      };
    }
    case "findings": {
      const finding = row as unknown as SnapshotFinding;
      return {
        key,
        type: "finding",
        group,
        section: "knowledge",
        version,
        primary: finding.title,
        secondary: finding.target,
        status: finding.status,
        badges: [
          finding.status,
          ...(finding.severity ? [finding.severity] : []),
          ...(finding.cvss_pending ? ["cvss-pending"] : []),
        ],
        fields: {
          version: finding.version,
          status: finding.status,
          title: finding.title,
          ...(finding.target ? { target: finding.target } : {}),
          ...(finding.description ? { description: finding.description } : {}),
          ...(finding.severity ? { severity: finding.severity } : {}),
          cvss_pending: finding.cvss_pending,
        },
      };
    }
    case "solutions": {
      const solution = row as unknown as SnapshotSolution;
      return {
        key,
        type: "solution",
        group,
        section: "knowledge",
        version,
        primary: solution.summary,
        secondary: solution.kind,
        status: solution.status,
        badges: [solution.status, solution.kind],
        fields: {
          version: solution.version,
          status: solution.status,
          kind: solution.kind,
          summary: solution.summary,
          ...(solution.value ? { value: solution.value } : {}),
          ...(solution.verification_summary
            ? { verification_summary: solution.verification_summary }
            : {}),
        },
      };
    }
    case "evidence": {
      const evidence = row as unknown as SnapshotEvidence;
      return {
        key,
        type: "evidence",
        group,
        section: "knowledge",
        version,
        primary: evidence.summary,
        secondary: evidence.artifact_type,
        status: evidence.status,
        badges: [evidence.status, evidence.artifact_type],
        fields: {
          version: evidence.version,
          status: evidence.status,
          artifact_type: evidence.artifact_type,
          summary: evidence.summary,
          ...(evidence.media_type ? { media_type: evidence.media_type } : {}),
          ...(evidence.captured_at ? { captured_at: evidence.captured_at } : {}),
        },
      };
    }
  }
}

/** Build Graph Explorer model from current Snapshot (all types + closed grammar). */
export function buildGraphExplorer(snapshot: RuntimeSnapshot): GraphExplorerModel {
  const nodes: GraphNode[] = [];
  const pushMap = (
    map: Record<string, object> | undefined,
    type: RecordType,
    labelOf: (row: Record<string, unknown>) => string,
    secondaryOf?: (row: Record<string, unknown>) => string | undefined,
  ) => {
    if (!map) return;
    for (const key of Object.keys(map).sort((a, b) => a.localeCompare(b))) {
      const row = map[key] as Record<string, unknown>;
      nodes.push({
        key,
        type,
        label: labelOf(row),
        status: typeof row.status === "string" ? row.status : undefined,
        secondary: secondaryOf?.(row),
      });
    }
  };

  pushMap(snapshot.work.objectives, "objective", (row) => String(row.objective));
  pushMap(snapshot.work.attempts, "attempt", (row) => String(row.summary));
  pushMap(
    snapshot.knowledge.entities,
    "entity",
    (row) => String(row.name),
    (row) => (typeof row.kind === "string" ? row.kind : undefined),
  );
  pushMap(
    snapshot.knowledge.facts,
    "fact",
    (row) => String(row.summary),
    (row) => (typeof row.category === "string" ? row.category : undefined),
  );
  pushMap(snapshot.knowledge.findings, "finding", (row) => String(row.title));
  pushMap(
    snapshot.knowledge.solutions,
    "solution",
    (row) => String(row.summary),
    (row) => (typeof row.kind === "string" ? row.kind : undefined),
  );
  pushMap(
    snapshot.knowledge.evidence,
    "evidence",
    (row) => String(row.summary),
    (row) => (typeof row.artifact_type === "string" ? row.artifact_type : undefined),
  );

  nodes.sort((a, b) => a.key.localeCompare(b.key));
  return { nodes, edges: [...snapshot.relations] };
}

/** Evidence rows that are not available (missing / degraded proof). */
export function missingEvidenceEntries(snapshot: RuntimeSnapshot): SnapshotListEntry[] {
  return listSnapshotEntries(snapshot).filter(
    (entry) => entry.type === "evidence" && entry.status !== "available",
  );
}

export async function readSnapshot(projectId: string): Promise<RuntimeSnapshot> {
  const raw = await apiGet<unknown>(`${projectBlackboardV2Base(projectId)}/snapshot`);
  return parseRuntimeSnapshot(raw);
}

export async function readSemanticHealth(projectId: string): Promise<SemanticHealth> {
  const raw = await apiGet<unknown>(`${projectBlackboardV2Base(projectId)}/health`);
  return parseSemanticHealth(raw);
}

export async function readCurrentDetail(projectId: string, key: string): Promise<CurrentDetail> {
  const raw = await apiGet<unknown>(
    `${projectBlackboardV2Base(projectId)}/records/${encodeURIComponent(key)}`,
  );
  return parseCurrentDetail(raw);
}

export async function readSemanticHistory(
  projectId: string,
  key: string,
  params: { limit?: number; cursor?: string } = {},
): Promise<SemanticHistory> {
  const raw = await apiGet<unknown>(
    `${projectBlackboardV2Base(projectId)}/records/${encodeURIComponent(key)}/history${qs({
      limit: params.limit ?? 20,
      cursor: params.cursor,
    })}`,
  );
  return parseSemanticHistory(raw);
}

const REPORT_MARKDOWN_FIELDS = ["schema", "markdown", "sync"] as const;
const PENTEST_REPORT_FIELDS = [
  "schema",
  "project",
  "confirmed_findings",
  "unconfirmed_findings",
  "confirmed_facts",
  "tentative_facts",
  "sync",
] as const;
const CTF_SOLUTION_FIELDS = [
  "schema",
  "project",
  "solved",
  "verified_flags",
  "candidate_flags",
  "answers",
  "procedures",
  "confirmed_facts",
  "tentative_facts",
  "evidence",
  "sync",
] as const;
const REPORT_PROJECT_FIELDS = ["name", "description"] as const;
const REPORT_FACT_FIELDS = ["key", "category", "summary", "body", "confidence", "scope_status"] as const;
const REPORT_EVIDENCE_FIELDS = [
  "key",
  "status",
  "artifact_type",
  "summary",
  "media_type",
  "captured_at",
] as const;
const REPORT_FINDING_FIELDS = [
  "key",
  "title",
  "status",
  "severity",
  "cvss_version",
  "cvss_vector",
  "cvss_pending",
  "target",
  "description",
  "proof",
  "impact",
  "recommendation",
  "supporting_facts",
  "contradictions",
  "evidence",
] as const;
const REPORT_SOLUTION_FIELDS = [
  "key",
  "kind",
  "status",
  "summary",
  "value",
  "verification_summary",
] as const;

/** Schema allows empty markdown; only type is enforced. */
function requireStringAllowEmpty(value: unknown, path: string): string {
  if (typeof value !== "string") {
    throw new Error(`${path} must be a string`);
  }
  return value;
}

function parseReportProject(raw: unknown, path: string): { name: string; description?: string } {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, REPORT_PROJECT_FIELDS, path);
  return {
    name: requireString(raw.name, `${path}.name`),
    description: optionalString(raw.description),
  };
}

function parseReportFact(raw: unknown, path: string): ReportFactDTO {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, REPORT_FACT_FIELDS, path);
  return {
    key: requireBlackboardKey(raw.key, `${path}.key`),
    category: requireString(raw.category, `${path}.category`),
    summary: requireString(raw.summary, `${path}.summary`),
    body: optionalString(raw.body),
    confidence: requireString(raw.confidence, `${path}.confidence`),
    scope_status: requireString(raw.scope_status, `${path}.scope_status`),
  };
}

function parseReportEvidence(raw: unknown, path: string): ReportEvidenceDTO {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, REPORT_EVIDENCE_FIELDS, path);
  return {
    key: requireBlackboardKey(raw.key, `${path}.key`),
    status: requireString(raw.status, `${path}.status`),
    artifact_type: requireString(raw.artifact_type, `${path}.artifact_type`),
    summary: requireString(raw.summary, `${path}.summary`),
    media_type: optionalString(raw.media_type),
    captured_at: optionalString(raw.captured_at),
  };
}

function parseReportFinding(raw: unknown, path: string): ReportFindingDTO {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, REPORT_FINDING_FIELDS, path);
  if (!Array.isArray(raw.supporting_facts)) {
    throw new Error(`${path}.supporting_facts must be an array`);
  }
  if (!Array.isArray(raw.contradictions)) {
    throw new Error(`${path}.contradictions must be an array`);
  }
  if (!Array.isArray(raw.evidence)) {
    throw new Error(`${path}.evidence must be an array`);
  }
  return {
    key: requireBlackboardKey(raw.key, `${path}.key`),
    title: requireString(raw.title, `${path}.title`),
    status: requireString(raw.status, `${path}.status`),
    severity: optionalString(raw.severity),
    cvss_version: optionalString(raw.cvss_version),
    cvss_vector: optionalString(raw.cvss_vector),
    cvss_pending: requireBoolean(raw.cvss_pending, `${path}.cvss_pending`),
    target: optionalString(raw.target),
    description: optionalString(raw.description),
    proof: optionalString(raw.proof),
    impact: optionalString(raw.impact),
    recommendation: optionalString(raw.recommendation),
    supporting_facts: raw.supporting_facts.map((item, index) =>
      parseReportFact(item, `${path}.supporting_facts[${index}]`),
    ),
    contradictions: raw.contradictions.map((item, index) =>
      parseReportFact(item, `${path}.contradictions[${index}]`),
    ),
    evidence: raw.evidence.map((item, index) =>
      parseReportEvidence(item, `${path}.evidence[${index}]`),
    ),
  };
}

function parseReportSolution(raw: unknown, path: string): ReportSolutionDTO {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, REPORT_SOLUTION_FIELDS, path);
  return {
    key: requireBlackboardKey(raw.key, `${path}.key`),
    kind: requireString(raw.kind, `${path}.kind`),
    status: requireString(raw.status, `${path}.status`),
    summary: requireString(raw.summary, `${path}.summary`),
    value: optionalString(raw.value),
    verification_summary: optionalString(raw.verification_summary),
  };
}

/**
 * Parse report-markdown/v2 deliverable body with a closed top-level shape.
 * Empty markdown is allowed by schema.
 */
export function parseReportMarkdown(raw: unknown): ReportMarkdown {
  if (!isPlainObject(raw)) throw new Error("report markdown must be an object");
  assertAllowlist(raw, REPORT_MARKDOWN_FIELDS, "report-markdown");
  const schema = requireString(raw.schema, "schema");
  if (schema !== REPORT_MARKDOWN_SCHEMA) {
    throw new Error(`schema must be ${REPORT_MARKDOWN_SCHEMA}`);
  }
  return {
    schema: REPORT_MARKDOWN_SCHEMA,
    markdown: requireStringAllowEmpty(raw.markdown, "markdown"),
  };
}

/** Parse pentest-report/v2 semantic projection (includes Blackboard Keys). */
export function parsePentestReport(raw: unknown): PentestReportProjection {
  if (!isPlainObject(raw)) throw new Error("pentest report must be an object");
  assertAllowlist(raw, PENTEST_REPORT_FIELDS, "pentest-report");
  const schema = requireString(raw.schema, "schema");
  if (schema !== PENTEST_REPORT_SCHEMA) {
    throw new Error(`schema must be ${PENTEST_REPORT_SCHEMA}`);
  }
  for (const field of [
    "confirmed_findings",
    "unconfirmed_findings",
    "confirmed_facts",
    "tentative_facts",
  ] as const) {
    if (!Array.isArray(raw[field])) {
      throw new Error(`${field} must be an array`);
    }
  }
  return {
    schema: PENTEST_REPORT_SCHEMA,
    project: parseReportProject(raw.project, "project"),
    confirmed_findings: (raw.confirmed_findings as unknown[]).map((item, index) =>
      parseReportFinding(item, `confirmed_findings[${index}]`),
    ),
    unconfirmed_findings: (raw.unconfirmed_findings as unknown[]).map((item, index) =>
      parseReportFinding(item, `unconfirmed_findings[${index}]`),
    ),
    confirmed_facts: (raw.confirmed_facts as unknown[]).map((item, index) =>
      parseReportFact(item, `confirmed_facts[${index}]`),
    ),
    tentative_facts: (raw.tentative_facts as unknown[]).map((item, index) =>
      parseReportFact(item, `tentative_facts[${index}]`),
    ),
  };
}

/** Parse ctf-solution/v2 semantic projection (includes Blackboard Keys). */
export function parseCTFSolution(raw: unknown): CTFSolutionProjection {
  if (!isPlainObject(raw)) throw new Error("ctf solution must be an object");
  assertAllowlist(raw, CTF_SOLUTION_FIELDS, "ctf-solution");
  const schema = requireString(raw.schema, "schema");
  if (schema !== CTF_SOLUTION_SCHEMA) {
    throw new Error(`schema must be ${CTF_SOLUTION_SCHEMA}`);
  }
  for (const field of [
    "verified_flags",
    "candidate_flags",
    "answers",
    "procedures",
    "confirmed_facts",
    "tentative_facts",
    "evidence",
  ] as const) {
    if (!Array.isArray(raw[field])) {
      throw new Error(`${field} must be an array`);
    }
  }
  return {
    schema: CTF_SOLUTION_SCHEMA,
    project: parseReportProject(raw.project, "project"),
    solved: requireBoolean(raw.solved, "solved"),
    verified_flags: (raw.verified_flags as unknown[]).map((item, index) =>
      parseReportSolution(item, `verified_flags[${index}]`),
    ),
    candidate_flags: (raw.candidate_flags as unknown[]).map((item, index) =>
      parseReportSolution(item, `candidate_flags[${index}]`),
    ),
    answers: (raw.answers as unknown[]).map((item, index) =>
      parseReportSolution(item, `answers[${index}]`),
    ),
    procedures: (raw.procedures as unknown[]).map((item, index) =>
      parseReportSolution(item, `procedures[${index}]`),
    ),
    confirmed_facts: (raw.confirmed_facts as unknown[]).map((item, index) =>
      parseReportFact(item, `confirmed_facts[${index}]`),
    ),
    tentative_facts: (raw.tentative_facts as unknown[]).map((item, index) =>
      parseReportFact(item, `tentative_facts[${index}]`),
    ),
    evidence: (raw.evidence as unknown[]).map((item, index) =>
      parseReportEvidence(item, `evidence[${index}]`),
    ),
  };
}

export async function readPentestReportMarkdown(projectId: string): Promise<ReportMarkdown> {
  const raw = await apiGet<unknown>(
    `/api/v2/projects/${encodeURIComponent(projectId)}/reports/pentest${qs({ format: "markdown" })}`,
  );
  return parseReportMarkdown(raw);
}

export async function readPentestReport(projectId: string): Promise<PentestReportProjection> {
  const raw = await apiGet<unknown>(
    `/api/v2/projects/${encodeURIComponent(projectId)}/reports/pentest${qs({ format: "json" })}`,
  );
  return parsePentestReport(raw);
}

export async function readCTFSolutionMarkdown(projectId: string): Promise<ReportMarkdown> {
  const raw = await apiGet<unknown>(
    `/api/v2/projects/${encodeURIComponent(projectId)}/reports/ctf-solution${qs({ format: "markdown" })}`,
  );
  return parseReportMarkdown(raw);
}

export async function readCTFSolution(projectId: string): Promise<CTFSolutionProjection> {
  const raw = await apiGet<unknown>(
    `/api/v2/projects/${encodeURIComponent(projectId)}/reports/ctf-solution${qs({ format: "json" })}`,
  );
  return parseCTFSolution(raw);
}

/**
 * Presentation-only Finding rows from the current Snapshot. Grouping never
 * merges identities or overwrites individual severity — each key is one row.
 */
export function listFindingEntries(snapshot: RuntimeSnapshot): SnapshotListEntry[] {
  return listSnapshotEntries(snapshot)
    .filter((entry) => entry.type === "finding")
    .sort((a, b) => {
      const severityRank = (value: string | number | boolean | undefined) => {
        switch (String(value ?? "")) {
          case "critical":
            return 5;
          case "high":
            return 4;
          case "medium":
            return 3;
          case "low":
            return 2;
          case "none":
            return 1;
          default:
            return 0;
        }
      };
      const bySeverity = severityRank(b.fields.severity) - severityRank(a.fields.severity);
      if (bySeverity !== 0) return bySeverity;
      const targetCmp = String(a.secondary ?? "").localeCompare(String(b.secondary ?? ""));
      if (targetCmp !== 0) return targetCmp;
      const titleCmp = a.primary.localeCompare(b.primary);
      if (titleCmp !== 0) return titleCmp;
      return a.key.localeCompare(b.key);
    });
}

/** Presentation-only Evidence rows from the current Snapshot. */
export function listEvidenceEntries(snapshot: RuntimeSnapshot): SnapshotListEntry[] {
  return listSnapshotEntries(snapshot)
    .filter((entry) => entry.type === "evidence")
    .sort((a, b) => a.key.localeCompare(b.key));
}

export function primaryLabelForDetail(detail: CurrentDetail): string {
  const r = detail.record;
  return (
    r.title ??
    r.name ??
    r.objective ??
    r.summary ??
    detail.key
  );
}
