/**
 * Focused Finding/Evidence/Report consumers still use v1 record collection
 * projections until #120. Ordinary Blackboard UI uses @/lib/blackboardv2 only.
 */
import {
  apiGet,
  type ReadEnvelope,
  type RecordCollection,
  type ReportMarkdown,
} from "@/lib/api";
import { qs as v2Qs, recordHref as v2RecordHref } from "@/lib/blackboardv2";

/** Build a query string, dropping empty values. */
export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  return v2Qs(params);
}

export function projectBlackboardBase(projectId: string): string {
  return `/api/projects/${projectId}/blackboard`;
}

/** @deprecated v1 projection — Finding/Evidence pages until #120. */
export function readRecords(
  projectId: string,
  params: {
    node_type?: string;
    disposition?: string;
    lifecycle?: string;
    scope_status?: string;
    severity?: string;
    query?: string;
    sort?: string;
    limit?: number;
    cursor?: string;
    about_entity_id?: string;
    frontier?: boolean;
  } = {},
) {
  return apiGet<ReadEnvelope<RecordCollection>>(
    `${projectBlackboardBase(projectId)}/records${qs(params)}`,
  );
}

export function readPentestReport(
  projectId: string,
  params: { scope_context?: string; format?: string } = {},
) {
  return apiGet<ReadEnvelope<ReportMarkdown>>(
    `/api/projects/${projectId}/reports/pentest${qs({
      format: params.format ?? "markdown",
      scope_context: params.scope_context ?? "current",
    })}`,
  );
}

export function readCTFSolution(projectId: string, params: { format?: string } = {}) {
  return apiGet<ReadEnvelope<ReportMarkdown>>(
    `/api/projects/${projectId}/reports/ctf-solution${qs({
      format: params.format ?? "markdown",
    })}`,
  );
}

/** Key- or node-id-based record path (Blackboard v2 uses Blackboard Keys). */
export function recordHref(projectId: string, keyOrNodeId: string): string {
  return v2RecordHref(projectId, keyOrNodeId);
}
