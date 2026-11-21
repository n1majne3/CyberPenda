import { apiGet } from "@/lib/api";
import { qs, isPlainObject, requireString, requireBoolean, requireBlackboardKey, optionalString, assertAllowlist, requireStringAllowEmpty } from "./blackboardValidation";

export const REPORT_MARKDOWN_SCHEMA = "report-markdown/v2" as const;

export const PENTEST_REPORT_SCHEMA = "pentest-report/v2" as const;

export const CTF_SOLUTION_SCHEMA = "ctf-solution/v2" as const;

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

export const REPORT_MARKDOWN_FIELDS = ["schema", "markdown", "sync"] as const;

export const PENTEST_REPORT_FIELDS = [
  "schema",
  "project",
  "confirmed_findings",
  "unconfirmed_findings",
  "confirmed_facts",
  "tentative_facts",
  "sync",
] as const;

export const CTF_SOLUTION_FIELDS = [
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

export const REPORT_PROJECT_FIELDS = ["name", "description"] as const;

export const REPORT_FACT_FIELDS = ["key", "category", "summary", "body", "confidence", "scope_status"] as const;

export const REPORT_EVIDENCE_FIELDS = [
  "key",
  "status",
  "artifact_type",
  "summary",
  "media_type",
  "captured_at",
] as const;

export const REPORT_FINDING_FIELDS = [
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

export const REPORT_SOLUTION_FIELDS = [
  "key",
  "kind",
  "status",
  "summary",
  "value",
  "verification_summary",
] as const;

export function parseReportProject(raw: unknown, path: string): { name: string; description?: string } {
  if (!isPlainObject(raw)) throw new Error(`${path} must be an object`);
  assertAllowlist(raw, REPORT_PROJECT_FIELDS, path);
  return {
    name: requireString(raw.name, `${path}.name`),
    description: optionalString(raw.description),
  };
}

export function parseReportFact(raw: unknown, path: string): ReportFactDTO {
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

export function parseReportEvidence(raw: unknown, path: string): ReportEvidenceDTO {
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

export function parseReportFinding(raw: unknown, path: string): ReportFindingDTO {
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

export function parseReportSolution(raw: unknown, path: string): ReportSolutionDTO {
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
