import { describe, expect, it } from "vitest";
import {
  FORBIDDEN_ORDINARY_UI_TERMS,
  attentionLabel,
  buildGraphExplorer,
  knowledgeGroupsForProjectKind,
  listEvidenceEntries,
  listFindingEntries,
  listSnapshotEntries,
  missingEvidenceEntries,
  parseCTFSolution,
  parseCurrentDetail,
  parsePentestReport,
  parseRelationship,
  parseReportMarkdown,
  parseRuntimeSnapshot,
  parseSemanticHealth,
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

  it("parses semantic health with attention thresholds and closed anomalies", () => {
    const health = parseSemanticHealth({
      schema: "blackboard-health/v2",
      revision: 12,
      status: "critical",
      attention: {
        bytes: 260000,
        estimated_tokens: 65000,
        state: "required",
        complete: true,
        launchable: true,
        consolidation_offered: true,
        consolidation_required: true,
      },
      anomalies: [
        {
          code: "attention_required",
          severity: "critical",
          message:
            "Runtime Snapshot reached the 64K consolidation-required threshold (65000 estimated tokens). Start an approval-required Reason Task for consolidation; complete Snapshot remains launchable.",
        },
        {
          code: "stranded_objective",
          severity: "warning",
          message: "Open Objective has no open Attempt currently testing it.",
          subject_key: "objective:admin",
        },
      ],
      proposals: [
        {
          code: "consolidation_reason_task",
          action: "start_reason_task",
          approval_required: true,
          required: true,
        },
      ],
    });
    expect(health.status).toBe("critical");
    expect(health.attention.consolidation_required).toBe(true);
    expect(health.attention.launchable).toBe(true);
    expect(attentionLabel(health.attention.state)).toMatch(/64K consolidation required/i);
    expect(health.anomalies[0].code).toBe("attention_required");
    expect(health.anomalies[1].subject_key).toBe("objective:admin");
    expect(health.proposals).toEqual([
      {
        code: "consolidation_reason_task",
        action: "start_reason_task",
        approval_required: true,
        required: true,
      },
    ]);
  });

  it("rejects semantic health payloads that violate the closed schema", () => {
    const valid = {
      schema: "blackboard-health/v2",
      revision: 1,
      status: "healthy",
      attention: {
        bytes: 10,
        estimated_tokens: 3,
        state: "within_target",
        complete: true,
        launchable: true,
        consolidation_offered: false,
        consolidation_required: false,
      },
      anomalies: [],
      proposals: [],
    };

    expect(() =>
      parseSemanticHealth({
        ...valid,
        attention: { ...valid.attention, complete: "true" },
      }),
    ).toThrow(/attention\.complete must be a boolean/i);

    expect(() =>
      parseSemanticHealth({
        ...valid,
        attention: { ...valid.attention, launchable: undefined },
      }),
    ).toThrow(/attention\.launchable must be a boolean/i);

    expect(() =>
      parseSemanticHealth({
        ...valid,
        attention: { ...valid.attention, bytes: -1 },
      }),
    ).toThrow(/attention\.bytes must be a non-negative integer/i);

    expect(() =>
      parseSemanticHealth({
        ...valid,
        attention: { ...valid.attention, estimated_tokens: 1.5 },
      }),
    ).toThrow(/attention\.estimated_tokens must be a non-negative integer/i);

    expect(() =>
      parseSemanticHealth({
        ...valid,
        revision: -3,
      }),
    ).toThrow(/revision must be a non-negative integer/i);

    expect(() =>
      parseSemanticHealth({
        ...valid,
        extra: true,
      }),
    ).toThrow(/health has non-allowlisted field extra/i);

    expect(() =>
      parseSemanticHealth({
        ...valid,
        attention: { ...valid.attention, checker_version: 1 },
      }),
    ).toThrow(/attention has non-allowlisted field checker_version/i);

    expect(() =>
      parseSemanticHealth({
        ...valid,
        anomalies: [
          {
            code: "stranded_objective",
            severity: "warning",
            message: "Open Objective has no open Attempt currently testing it.",
            subject_key: "objective:bad\nkey",
          },
        ],
      }),
    ).toThrow(/subject_key is not a printable ASCII blackboardKey/i);

    expect(() =>
      parseSemanticHealth({
        ...valid,
        proposals: [
          {
            code: "consolidation_reason_task",
            action: "start_reason_task",
            approval_required: false,
            required: false,
          },
        ],
      }),
    ).toThrow(/approval_required must be true/i);

    expect(() => parseSemanticHealth({ ...valid, proposals: undefined })).toThrow(
      /proposals must be an array/i,
    );
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

  it("lists Finding and Evidence rows from Snapshot without merging identities", () => {
    const snapshot = parseRuntimeSnapshot({
      ...pentestSnapshot,
      knowledge: {
        ...pentestSnapshot.knowledge,
        findings: {
          "finding:b": {
            version: 1,
            status: "confirmed",
            title: "Same title",
            target: "https://b.example",
            severity: "high",
            cvss_pending: false,
          },
          "finding:a": {
            version: 1,
            status: "confirmed",
            title: "Same title",
            target: "https://a.example",
            severity: "high",
            cvss_pending: false,
          },
        },
      },
    });
    const findings = listFindingEntries(snapshot);
    expect(findings.map((row) => row.key)).toEqual(["finding:a", "finding:b"]);
    expect(findings.every((row) => row.fields.severity === "high")).toBe(true);
    expect(listEvidenceEntries(snapshot).map((row) => row.key)).toEqual([
      "evidence:admin",
      "evidence:missing",
    ]);
  });

  it("parses report-markdown/v2 deliverables with closed shape", () => {
    const report = parseReportMarkdown({
      schema: "report-markdown/v2",
      markdown: "# Report\n\n## Confirmed Findings\n",
    });
    expect(report.markdown).toContain("Confirmed Findings");
    // Schema permits empty markdown string.
    expect(
      parseReportMarkdown({
        schema: "report-markdown/v2",
        markdown: "",
      }).markdown,
    ).toBe("");
    expect(() =>
      parseReportMarkdown({
        schema: "pentest_report_v1",
        markdown: "legacy",
      }),
    ).toThrow(/report-markdown\/v2/);
    expect(() =>
      parseReportMarkdown({
        schema: "report-markdown/v2",
        markdown: "ok",
        extra: true,
      }),
    ).toThrow(/non-allowlisted field extra/);
    expect(() =>
      parseReportMarkdown({
        schema: "report-markdown/v2",
        markdown: 12,
      }),
    ).toThrow(/markdown must be a string/);
  });

  it("parses pentest-report/v2 and ctf-solution/v2 with Blackboard Keys", () => {
    const pentest = parsePentestReport({
      schema: "pentest-report/v2",
      project: { name: "Acme" },
      confirmed_findings: [
        {
          key: "finding:admin",
          title: "Admin exposed",
          status: "confirmed",
          severity: "high",
          cvss_pending: false,
          supporting_facts: [],
          contradictions: [],
          evidence: [
            {
              key: "evidence:http",
              status: "available",
              artifact_type: "http-response",
              summary: "Admin response",
            },
          ],
        },
      ],
      unconfirmed_findings: [],
      confirmed_facts: [
        {
          key: "fact:admin-exposed",
          category: "exposure",
          summary: "Admin is internet-facing",
          confidence: "confirmed",
          scope_status: "in_scope",
        },
      ],
      tentative_facts: [
        {
          key: "fact:maybe-related",
          category: "recon",
          summary: "A second host may share the panel",
          confidence: "tentative",
          scope_status: "unknown",
        },
      ],
    });
    expect(pentest.confirmed_findings[0]?.key).toBe("finding:admin");
    expect(pentest.confirmed_findings[0]?.evidence[0]?.key).toBe("evidence:http");
    expect(pentest.confirmed_facts[0]?.key).toBe("fact:admin-exposed");
    expect(pentest.tentative_facts[0]?.key).toBe("fact:maybe-related");
    expect(() =>
      parsePentestReport({
        schema: "pentest-report/v2",
        project: { name: "Acme" },
        confirmed_findings: [
          {
            title: "missing key",
            status: "confirmed",
            cvss_pending: false,
            supporting_facts: [],
            contradictions: [],
            evidence: [],
          },
        ],
        unconfirmed_findings: [],
        confirmed_facts: [],
        tentative_facts: [],
      }),
    ).toThrow(/key/);
    expect(() =>
      parsePentestReport({
        schema: "pentest-report/v2",
        project: { name: "Acme" },
        confirmed_findings: [],
        unconfirmed_findings: [],
        confirmed_facts: [
          {
            category: "exposure",
            summary: "missing fact key",
            confidence: "confirmed",
            scope_status: "in_scope",
          },
        ],
        tentative_facts: [],
      }),
    ).toThrow(/key/);

    const ctf = parseCTFSolution({
      schema: "ctf-solution/v2",
      project: { name: "Flag CTF" },
      solved: true,
      verified_flags: [
        {
          key: "solution:flag",
          kind: "flag",
          status: "verified",
          summary: "Recovered flag",
          value: "FLAG{ok}",
        },
      ],
      candidate_flags: [],
      answers: [],
      procedures: [],
      confirmed_facts: [
        {
          key: "fact:parser-clue",
          category: "challenge",
          summary: "Parser accepts reversed hex",
          confidence: "confirmed",
          scope_status: "in_scope",
        },
      ],
      tentative_facts: [],
      evidence: [],
    });
    expect(ctf.verified_flags[0]?.key).toBe("solution:flag");
    expect(ctf.confirmed_facts[0]?.key).toBe("fact:parser-clue");
    expect(() =>
      parseCTFSolution({
        schema: "ctf-solution/v2",
        project: { name: "Flag CTF" },
        solved: true,
        verified_flags: [],
        candidate_flags: [],
        answers: [],
        procedures: [],
        confirmed_facts: [],
        tentative_facts: [],
        evidence: [],
        provenance: {},
      }),
    ).toThrow(/non-allowlisted field provenance/);
  });
});
