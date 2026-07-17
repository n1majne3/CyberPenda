import { describe, expect, it } from "vitest";
import {
  FORBIDDEN_ORDINARY_UI_TERMS,
  buildGraphExplorer,
  knowledgeGroupsForProjectKind,
  listSnapshotEntries,
  missingEvidenceEntries,
  parseCurrentDetail,
  parseRelationship,
  parseRuntimeSnapshot,
  parseSemanticHistory,
  RELATIONSHIP_TYPES,
  RECORD_TYPES,
  SNAPSHOT_FIELD_ALLOWLIST,
} from "./blackboardv2";

const pentestSnapshot = {
  schema: "runtime-blackboard/v2",
  semantics: "work is active; knowledge is current; history and details are available by key",
  revision: 24,
  work: {
    objectives: {
      "objective:admin": {
        version: 1,
        status: "open",
        objective: "Determine whether admin access can be bypassed",
      },
    },
    attempts: {
      "attempt:admin": {
        version: 1,
        status: "open",
        summary: "Testing the admin endpoint authorization checks",
      },
    },
  },
  knowledge: {
    entities: {
      "entity:admin": {
        version: 1,
        status: "active",
        kind: "endpoint",
        name: "Admin endpoint",
        locator: "https://example.test/admin",
        scope_status: "in_scope",
      },
    },
    facts: {
      "fact:admin": {
        version: 1,
        category: "authorization",
        summary: "The admin route responds without a privileged session",
        confidence: "tentative",
        scope_status: "in_scope",
      },
    },
    findings: {
      "finding:admin": {
        version: 1,
        status: "unconfirmed",
        title: "Admin access control bypass",
        target: "https://example.test/admin",
        severity: "critical",
        cvss_pending: false,
      },
    },
    evidence: {
      "evidence:admin": {
        version: 1,
        status: "available",
        artifact_type: "http_exchange",
        summary: "Captured unauthenticated admin response",
        media_type: "message/http",
        captured_at: "2026-07-17T12:00:00Z",
      },
      "evidence:missing": {
        version: 1,
        status: "missing",
        artifact_type: "screenshot",
        summary: "Expected capture was not retained",
      },
    },
  },
  relations: [
    ["attempt:admin", "about", "entity:admin"],
    ["fact:admin", "supports", "finding:admin", "The unauthenticated response supports the concern"],
  ],
};

const ctfSnapshot = {
  schema: "runtime-blackboard/v2",
  semantics: "work is active; knowledge is current; history and details are available by key",
  revision: 11,
  work: {
    objectives: {
      "objective:solve": {
        version: 1,
        status: "open",
        objective: "Recover and verify the challenge flag",
      },
    },
  },
  knowledge: {
    entities: {
      "entity:challenge": {
        version: 1,
        status: "active",
        kind: "service",
        name: "Challenge service",
        scope_status: "in_scope",
      },
    },
    solutions: {
      "solution:flag": {
        version: 1,
        status: "verified",
        kind: "flag",
        summary: "Recovered the challenge flag",
        value: "FLAG{deterministic}",
      },
    },
  },
  relations: [["solution:flag", "satisfies", "objective:solve"]],
};

describe("blackboard v2 data contracts", () => {
  it("parses relationship tuples structured as arrays only", () => {
    expect(parseRelationship(["a", "about", "b"])).toEqual({
      from: "a",
      relation: "about",
      to: "b",
    });
    expect(parseRelationship(["a", "supports", "b", "because"])).toEqual({
      from: "a",
      relation: "supports",
      to: "b",
      reason: "because",
    });
    expect(() => parseRelationship("a about b")).toThrow(/array tuple/i);
    expect(() => parseRelationship(["a", "blocks", "b"])).toThrow(/closed relationship/i);
  });

  it("parses Runtime Snapshot with Snapshot field allowlists", () => {
    const snapshot = parseRuntimeSnapshot(pentestSnapshot);
    expect(snapshot.schema).toBe("runtime-blackboard/v2");
    expect(snapshot.revision).toBe(24);
    expect(snapshot.work.objectives?.["objective:admin"]?.objective).toMatch(/admin access/i);
    expect(snapshot.relations[1].reason).toMatch(/unauthenticated/i);

    expect(() =>
      parseRuntimeSnapshot({
        ...pentestSnapshot,
        work: {
          objectives: {
            "objective:admin": {
              version: 1,
              status: "open",
              objective: "x",
              internal_id: "node-1",
            },
          },
        },
      }),
    ).toThrow(/non-allowlisted field internal_id/i);
  });

  it("rejects provenance/hash/audit fields in Snapshot maps", () => {
    expect(() =>
      parseRuntimeSnapshot({
        ...pentestSnapshot,
        knowledge: {
          facts: {
            "fact:admin": {
              ...pentestSnapshot.knowledge.facts["fact:admin"],
              projection_hash: "abc",
            },
          },
        },
      }),
    ).toThrow(/non-allowlisted field projection_hash/i);
  });

  it("lists Work and Knowledge with allowlisted fields only", () => {
    const snapshot = parseRuntimeSnapshot(pentestSnapshot);
    const entries = listSnapshotEntries(snapshot, "pentest");
    const work = entries.filter((e) => e.section === "work");
    const knowledge = entries.filter((e) => e.section === "knowledge");
    expect(work.map((e) => e.type).sort()).toEqual(["attempt", "objective"]);
    expect(knowledge.some((e) => e.type === "finding")).toBe(true);
    expect(knowledge.some((e) => e.type === "solution")).toBe(false);

    for (const entry of entries) {
      const allow =
        SNAPSHOT_FIELD_ALLOWLIST[
          entry.group as keyof typeof SNAPSHOT_FIELD_ALLOWLIST
        ];
      for (const field of Object.keys(entry.fields)) {
        expect(allow).toContain(field);
      }
    }
  });

  it("uses CTF knowledge groups with solutions and without findings", () => {
    expect(knowledgeGroupsForProjectKind("ctf_challenge")).toEqual([
      "entities",
      "facts",
      "solutions",
      "evidence",
    ]);
    const snapshot = parseRuntimeSnapshot(ctfSnapshot);
    const entries = listSnapshotEntries(snapshot, "ctf_challenge");
    expect(entries.some((e) => e.type === "solution")).toBe(true);
    expect(entries.some((e) => e.type === "finding")).toBe(false);
  });

  it("builds Graph Explorer for every current record type and relationship edge", () => {
    const snapshot = parseRuntimeSnapshot(pentestSnapshot);
    const graph = buildGraphExplorer(snapshot);
    const types = new Set(graph.nodes.map((n) => n.type));
    for (const type of ["objective", "attempt", "entity", "fact", "finding", "evidence"] as const) {
      expect(types.has(type)).toBe(true);
    }
    expect(graph.nodes.every((n) => n.key.includes(":"))).toBe(true);
    expect(graph.edges).toHaveLength(2);
    expect(RELATIONSHIP_TYPES).toHaveLength(11);
    expect(RECORD_TYPES).toHaveLength(7);
  });

  it("surfaces missing Evidence entries honestly", () => {
    const snapshot = parseRuntimeSnapshot(pentestSnapshot);
    const missing = missingEvidenceEntries(snapshot);
    expect(missing).toHaveLength(1);
    expect(missing[0].key).toBe("evidence:missing");
    expect(missing[0].status).toBe("missing");
  });

  it("parses current detail and paginated Semantic History", () => {
    const detail = parseCurrentDetail({
      schema: "blackboard-record/v2",
      revision: 24,
      key: "finding:admin",
      type: "finding",
      version: 2,
      record: {
        status: "unconfirmed",
        title: "Admin access control bypass",
        proof: "HTTP 200 without session",
      },
      relationships: [["finding:admin", "about", "entity:admin"]],
    });
    expect(detail.key).toBe("finding:admin");
    expect(detail.record.proof).toMatch(/HTTP 200/i);

    const history = parseSemanticHistory({
      schema: "semantic-history/v2",
      revision: 24,
      key: "finding:admin",
      items: [
        {
          kind: "record",
          key: "finding:admin",
          version: 1,
          type: "finding",
          record: { status: "unconfirmed", title: "Admin access control bypass" },
        },
      ],
      next_cursor: "cursor-2",
    });
    expect(history.items).toHaveLength(1);
    expect(history.next_cursor).toBe("cursor-2");
  });

  it("documents forbidden ordinary UI audit surfaces", () => {
    expect(FORBIDDEN_ORDINARY_UI_TERMS).toEqual(
      expect.arrayContaining([
        "Provenance",
        "Fact Index",
        "Recent changes",
        "Frontier",
      ]),
    );
  });
});
