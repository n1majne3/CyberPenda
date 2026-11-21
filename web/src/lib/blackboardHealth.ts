import { apiGet } from "@/lib/api";
import { projectBlackboardV2Base, isPlainObject, requireString, requireNonNegativeInteger, requireBoolean, requireBlackboardKey, optionalBlackboardKey, assertAllowlist } from "./blackboardValidation";

export const HEALTH_SCHEMA = "blackboard-health/v2" as const;

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

export interface SemanticHealth {
  schema: typeof HEALTH_SCHEMA;
  revision: number;
  status: HealthStatus;
  attention: HealthAttention;
  anomalies: HealthAnomaly[];
  proposals: [];
}

export const ATTENTION_STATES = new Set<string>([
  "within_target",
  "above_target",
  "warning",
  "required",
]);

export const HEALTH_STATUSES = new Set<string>(["healthy", "attention", "warning", "critical"]);

export const HEALTH_SEVERITIES = new Set<string>(["info", "warning", "critical"]);

export const HEALTH_ROOT_FIELDS = [
  "schema",
  "revision",
  "status",
  "attention",
  "anomalies",
  "proposals",
  "sync",
] as const;

export const HEALTH_ATTENTION_FIELDS = [
  "bytes",
  "estimated_tokens",
  "state",
  "complete",
  "launchable",
  "consolidation_offered",
  "consolidation_required",
] as const;

export const HEALTH_ANOMALY_FIELDS = [
  "code",
  "severity",
  "message",
  "subject_key",
  "related_keys",
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
  if (raw.proposals.length !== 0) {
    throw new Error("proposals must be empty");
  }
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
    proposals: [],
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
      return "32K warning — review Working Graph";
    case "required":
      return "64K consolidation required";
  }
}

export async function readSemanticHealth(projectId: string): Promise<SemanticHealth> {
  const raw = await apiGet<unknown>(`${projectBlackboardV2Base(projectId)}/health`);
  return parseSemanticHealth(raw);
}
