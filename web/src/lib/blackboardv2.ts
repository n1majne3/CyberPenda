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

export interface BlackboardV2Error {
  code: string;
  message: string;
  path?: string;
  retryable: boolean;
  details?: Record<string, unknown>;
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

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
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
