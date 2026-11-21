import { apiGet, ApiError } from "@/lib/api";
import { RUNTIME_SNAPSHOT_SCHEMA, RECORD_DETAIL_SCHEMA, SEMANTIC_HISTORY_SCHEMA, RELATIONSHIP_TYPES, type RelationshipType, SNAPSHOT_FIELD_ALLOWLIST, type SnapshotObjective, type SnapshotAttempt, type SnapshotEntity, type SnapshotFact, type SnapshotFinding, type SnapshotSolution, type SnapshotEvidence, type SnapshotWork, type SnapshotKnowledge, type RelationshipEdge, type RuntimeSnapshot, type SemanticRecord, type CurrentDetail, type HistoryItem, type SemanticHistory, type BlackboardV2Error } from "./blackboardTypes";
import { projectBlackboardV2Base, qs, isPlainObject, requireString, requireNumber, optionalString, assertAllowlist } from "./blackboardValidation";

export { projectBlackboardV2Base, qs, recordHref, blackboardHref, requireBlackboardKey } from "./blackboardValidation";
export * from "./blackboardTypes";
export * from "./blackboardHealth";
export * from "./blackboardReports";
export * from "./blackboardEntries";

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
