
export const RUNTIME_SNAPSHOT_SCHEMA = "runtime-blackboard/v2" as const;

export const RECORD_DETAIL_SCHEMA = "blackboard-record/v2" as const;

export const SEMANTIC_HISTORY_SCHEMA = "semantic-history/v2" as const;

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
  "invalidates",
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
  "health_run",
  "checker_version",
  "graph hash",
] as const;
