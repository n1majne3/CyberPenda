import { type RecordType, type WorkGroup, type KnowledgeGroup, type SnapshotEntity, type SnapshotFact, type SnapshotFinding, type SnapshotSolution, type SnapshotEvidence, type RuntimeSnapshot, type CurrentDetail, type GraphNode, type GraphExplorerModel } from "./blackboardTypes";

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

export function sortedKeys(map: Record<string, unknown> | undefined): string[] {
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

export function knowledgeEntry(
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
