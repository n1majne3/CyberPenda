import {
  apiGet,
  type BlackboardHealth,
  type BlackboardWorkView,
  type EntityCollection,
  type GraphExplorer,
  type ReadEnvelope,
  type RecordCollection,
  type RecordDetail,
  type ReportMarkdown,
} from "@/lib/api";

/** Build a query string, dropping empty values. */
export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    search.set(key, String(value));
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : "";
}

export function projectBlackboardBase(projectId: string): string {
  return `/api/projects/${projectId}/blackboard`;
}

export function readWorkView(projectId: string) {
  return apiGet<ReadEnvelope<BlackboardWorkView>>(
    `${projectBlackboardBase(projectId)}/work-view`,
  );
}

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

export function readRecordDetail(projectId: string, nodeId: string, literal = false) {
  return apiGet<ReadEnvelope<RecordDetail>>(
    `${projectBlackboardBase(projectId)}/records/${encodeURIComponent(nodeId)}${qs({
      literal: literal ? "true" : undefined,
    })}`,
  );
}

export function readRecordHistory(projectId: string, nodeId: string, literal = false) {
  return apiGet<
    ReadEnvelope<{
      record: { id: string; node_type: string; stable_key: string; label: string };
      versions: {
        version: number;
        disposition: string;
        properties: Record<string, unknown>;
        updated_at: string;
        semantic_hash: string;
      }[];
      page: { limit: number; total_items: number; next_cursor?: string };
      key_history?: unknown[];
      merge?: { id: string; node_type: string; stable_key: string; label: string } | null;
    }>
  >(
    `${projectBlackboardBase(projectId)}/records/${encodeURIComponent(nodeId)}/history${qs({
      literal: literal ? "true" : undefined,
    })}`,
  );
}

export function readRecordProvenance(projectId: string, nodeId: string) {
  return apiGet<
    ReadEnvelope<{
      record?: { id: string; node_type: string; stable_key: string; label: string };
      created?: Record<string, unknown>;
      updated?: Record<string, unknown>;
      [key: string]: unknown;
    }>
  >(`${projectBlackboardBase(projectId)}/records/${encodeURIComponent(nodeId)}/provenance`);
}

export function readEntities(
  projectId: string,
  params: { kind?: string; query?: string; limit?: number; cursor?: string } = {},
) {
  return apiGet<ReadEnvelope<EntityCollection>>(
    `${projectBlackboardBase(projectId)}/entities${qs(params)}`,
  );
}

export function readGraphExplorer(
  projectId: string,
  params: {
    seed_node_id?: string;
    node_type?: string;
    query?: string;
    max_nodes?: number;
    max_edges?: number;
  } = {},
) {
  return apiGet<ReadEnvelope<GraphExplorer>>(
    `${projectBlackboardBase(projectId)}/graph-explorer${qs(params)}`,
  );
}

export function readHealth(projectId: string) {
  return apiGet<ReadEnvelope<BlackboardHealth>>(`${projectBlackboardBase(projectId)}/health`);
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

export function recordHref(projectId: string, nodeId: string): string {
  return `/projects/${projectId}/blackboard/records/${encodeURIComponent(nodeId)}`;
}

export function blackboardHref(
  projectId: string,
  tab: "work" | "entities" | "explorer" | "health" = "work",
  params: Record<string, string | undefined> = {},
): string {
  const base =
    tab === "work"
      ? `/projects/${projectId}/blackboard`
      : `/projects/${projectId}/blackboard/${tab}`;
  const query = qs(params);
  return `${base}${query}`;
}
