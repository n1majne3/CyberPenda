import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { BlackboardPage } from "./BlackboardPage";
import { EvidencePage } from "./EvidencePage";
import { FindingsPage } from "./FindingsPage";
import { ProjectDashboardPage } from "./ProjectDashboardPage";
import { ReportPage } from "./ReportPage";
import { SolutionPage } from "./SolutionPage";

/**
 * U06 first red test (blackboard-tdd-acceptance-and-slices.md).
 *
 * Focused Project surfaces must consume shared canonical graph projections and
 * MUST NOT issue page-specific frozen-table legacy fallback reads once the UI
 * is graph-backed. Bookmark routes remain addressable.
 */

const LEGACY_FROZEN_READ_PATTERNS = [
  /\/api\/projects\/[^/]+\/facts(?:\/|$|\?)/,
  /\/api\/projects\/[^/]+\/findings(?:\/|$|\?)/,
  /\/api\/projects\/[^/]+\/evidence(?:\/|$|\?)/,
  /\/api\/projects\/[^/]+\/report(?:\/|$|\?)/,
];

function isLegacyFrozenRead(url: string): boolean {
  // Graph-native dashboard remains allowed; only page-specific frozen Blackboard
  // Fact/Finding/Evidence/report list/detail fallbacks are forbidden.
  if (url.includes("/dashboard")) return false;
  return LEGACY_FROZEN_READ_PATTERNS.some((pattern) => pattern.test(url));
}

function nodeRow(overrides: {
  id: string;
  node_type: string;
  stable_key: string;
  label: string;
  secondary?: string;
  scope_status?: string;
  severity?: string;
  lifecycle?: string;
}) {
  return {
    ref: {
      id: overrides.id,
      node_type: overrides.node_type,
      stable_key: overrides.stable_key,
      label: overrides.label,
    },
    version: 1,
    disposition: "main",
    lifecycle: overrides.lifecycle
      ? { field: "status", value: overrides.lifecycle }
      : { field: "confidence", value: "confirmed" },
    scope_status: overrides.scope_status ?? "in_scope",
    severity: overrides.severity ?? null,
    secondary: overrides.secondary ?? overrides.stable_key,
    updated_at: "2026-01-02T00:00:00Z",
    about_entities: [],
    relationship_counts: {
      about_entities: 0,
      incoming: 0,
      outgoing: 0,
      evidence: 1,
      contradictions: 0,
    },
    updated_provenance: {
      actor_type: "operator",
      actor_id: "tester",
      task_id: null,
      continuation_id: null,
      runtime_profile_id: null,
      runner: null,
      source_event_count: 0,
      migration_source: null,
      recorded_at: "2026-01-02T00:00:00Z",
    },
  };
}

const workViewEnvelope = {
  protocol_version: 1,
  projection: "blackboard_work_v1",
  project_id: "project-1",
  project_kind: "pentest",
  observed_graph_revision: 3,
  observed_state_hash: "hash",
  source_pins: {},
  projection_hash: "work-hash",
  result: {
    summary: {
      graph_revision: 3,
      node_counts: { project_fact: 1, finding: 1, evidence_artifact: 1 },
      edge_counts: { evidences: 1 },
      current_truth: 1,
      frontier: 0,
      open_attempts: 0,
      confirmed_findings: 1,
      unconfirmed_findings: 0,
      verified_solutions: 0,
      evidence_missing: 0,
      budget: {
        state: "within_target",
        projection_bytes: 100,
        estimated_tokens: 25,
        target_tokens: 12000,
        warning_tokens: 16000,
        required_tokens: 20000,
      },
      health: {
        status: "healthy",
        stale: false,
        critical: 0,
        warning: 0,
        info: 0,
        latest_run_id: "",
      },
    },
    attention: { items: [], facets: {}, page: { limit: 20, total_items: 0 } },
    frontier: { items: [], facets: {}, page: { limit: 20, total_items: 0 } },
    active_attempts: { items: [], facets: {}, page: { limit: 20, total_items: 0 } },
    recent_changes: {
      items: [
        {
          kind: "node",
          node: nodeRow({
            id: "node-fact-1",
            node_type: "project_fact",
            stable_key: "fact:admin",
            label: "Admin panel exposed",
            secondary: "service",
          }),
          edge: null,
          updated_at: "2026-01-02T00:00:00Z",
        },
      ],
      page: { limit: 20, total_items: 1 },
    },
    facets: {
      node_type: { project_fact: 1, finding: 1, evidence_artifact: 1 },
    },
  },
};

const entityCollection = {
  protocol_version: 1,
  projection: "entity_collection_v1",
  project_id: "project-1",
  project_kind: "pentest",
  observed_graph_revision: 3,
  observed_state_hash: "hash",
  source_pins: {},
  projection_hash: "entities-hash",
  result: {
    items: [
      {
        entity: {
          id: "node-entity-1",
          node_type: "entity",
          stable_key: "entity:host:acme.test",
          label: "acme.test",
        },
        kind: "host",
        name: "acme.test",
        locator: "acme.test",
        scope_status: "in_scope",
        status: "active",
        parent_entities: [],
        child_count: 0,
        record_counts: { project_fact: 1, finding: 1 },
      },
    ],
    page: { limit: 50, total_items: 1 },
  },
};

const graphExplorer = {
  protocol_version: 1,
  projection: "graph_explorer_v1",
  project_id: "project-1",
  project_kind: "pentest",
  observed_graph_revision: 3,
  observed_state_hash: "hash",
  source_pins: {},
  projection_hash: "explorer-hash",
  result: {
    graph: {
      nodes: [
        {
          row: nodeRow({
            id: "node-finding-1",
            node_type: "finding",
            stable_key: "finding:admin-exposed",
            label: "Admin panel exposed",
            severity: "high",
            lifecycle: "confirmed",
          }),
          x_group: "finding",
          is_seed: true,
        },
      ],
      edges: [],
    },
    table: {
      nodes: [
        nodeRow({
          id: "node-finding-1",
          node_type: "finding",
          stable_key: "finding:admin-exposed",
          label: "Admin panel exposed",
          severity: "high",
          lifecycle: "confirmed",
        }),
      ],
      edges: [],
    },
    legend: {
      node_types: { finding: 1 },
      edge_types: {},
      lifecycle_values: { confirmed: 1 },
    },
    limits: { max_nodes: 200, max_edges: 500, node_count: 1, edge_count: 0 },
    equivalent_record_query: { node_type: ["finding"] },
  },
};

const healthSummary = {
  protocol_version: 1,
  projection: "blackboard_health_v1",
  project_id: "project-1",
  project_kind: "pentest",
  observed_graph_revision: 3,
  observed_state_hash: "hash",
  source_pins: {},
  projection_hash: "health-hash",
  result: {
    current_graph: { revision: 3, state_hash: "hash", main_projection_hash: "main" },
    latest_run: null,
    overall: "unknown",
  },
};

const recordDetail = {
  protocol_version: 1,
  projection: "record_detail_v1",
  project_id: "project-1",
  project_kind: "pentest",
  observed_graph_revision: 3,
  observed_state_hash: "hash",
  source_pins: {},
  projection_hash: "detail-hash",
  result: {
    node: {
      id: "node-finding-1",
      node_type: "finding",
      stable_key: "finding:admin-exposed",
      version: 1,
      disposition: "main",
      properties: {
        title: "Admin panel exposed",
        status: "confirmed",
        severity: "high",
        target: "https://acme.test/admin",
      },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-02T00:00:00Z",
      merge_target: null,
    },
    resolved_from_merged_id: null,
    derived: {
      current_truth: false,
      frontier: false,
      health_subject: false,
      solved_contributor: false,
    },
    about_entities: { items: [], total_items: 0, records_href: "" },
    relationships: {
      incoming: { items: [], total_items: 0, traversal_href: "" },
      outgoing: { items: [], total_items: 0, traversal_href: "" },
    },
    evidence: { items: [], total_items: 0, records_href: "" },
    support: {
      supporting: { items: [], total_items: 0, traversal_href: "" },
      contradicting: { items: [], total_items: 0, traversal_href: "" },
      satisfies: { items: [], total_items: 0, traversal_href: "" },
    },
    capabilities: {},
  },
};

const dashboard = {
  project_id: "project-1",
  name: "Acme External",
  project_kind: "pentest",
  scope: {
    domains: 1,
    ips: 0,
    cidrs: 0,
    urls: 1,
    ports: 1,
    excluded: 0,
    has_testing_limits: false,
    has_notes: false,
    ready: true,
  },
  tasks: { total: 1, running: 0, paused: 0, needs_attention: 0 },
  blackboard: {
    observed_graph_revision: 3,
    nodes_by_type: { project_fact: 1, finding: 1 },
    current_truth: 1,
    frontier: 0,
    open_attempts: 0,
    confirmed_findings: 1,
    unconfirmed_findings: 0,
    available_evidence: 1,
    missing_evidence: 0,
    budget_state: "within_target",
    estimated_tokens: 25,
  },
  health: {
    status: "healthy",
    stale: false,
    critical: 0,
    warning: 0,
    info: 0,
    latest_run_id: "",
  },
  ctf: null,
  counts: { tasks: 1, facts: 1, findings: 1, evidence: 1 },
  next_actions: [],
  _read: {
    protocol_version: 1,
    projection: "project_blackboard_summary_v1",
    observed_graph_revision: 3,
    observed_state_hash: "hash",
    source_pins: {},
    projection_hash: "dash-hash",
  },
};

const project = {
  id: "project-1",
  name: "Acme External",
  description: "External assessment",
  kind: "pentest",
  scope: {},
  defaults: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-02T00:00:00Z",
};

const ctfProject = {
  ...project,
  id: "ctf-1",
  name: "Flag CTF",
  kind: "ctf_challenge",
};

const runtimeSnapshotV2 = {
  schema: "runtime-blackboard/v2",
  semantics: "work is active; knowledge is current; history and details are available by key",
  revision: 3,
  work: {},
  knowledge: {
    entities: {
      "entity:host:acme.test": {
        version: 1,
        status: "active",
        kind: "host",
        name: "acme.test",
        locator: "acme.test",
        scope_status: "in_scope",
      },
    },
    facts: {
      "fact:admin": {
        version: 1,
        category: "service",
        summary: "Admin panel exposed",
        confidence: "confirmed",
        scope_status: "in_scope",
      },
    },
    findings: {
      "finding:admin-exposed": {
        version: 1,
        status: "confirmed",
        title: "Admin panel exposed",
        target: "https://acme.test/admin",
        severity: "high",
        cvss_pending: false,
      },
    },
    evidence: {
      "evidence:resp": {
        version: 1,
        status: "available",
        artifact_type: "http_exchange",
        summary: "Captured HTTP exchange",
      },
    },
  },
  relations: [
    ["finding:admin-exposed", "about", "entity:host:acme.test"],
    ["evidence:resp", "evidences", "finding:admin-exposed"],
  ],
};

const recordDetailV2 = {
  schema: "blackboard-record/v2",
  revision: 3,
  key: "finding:admin-exposed",
  type: "finding",
  version: 1,
  record: {
    status: "confirmed",
    title: "Admin panel exposed",
    severity: "high",
    target: "https://acme.test/admin",
  },
  relationships: [["finding:admin-exposed", "about", "entity:host:acme.test"]],
};

const historyV2 = {
  schema: "semantic-history/v2",
  revision: 3,
  key: "finding:admin-exposed",
  items: [
    {
      kind: "record",
      key: "finding:admin-exposed",
      version: 1,
      type: "finding",
      record: { status: "confirmed", title: "Admin panel exposed" },
    },
  ],
};

function routeBody(url: string, routes: Record<string, unknown>): unknown {
  // Loose graph-route matching first so short keys like /api/projects/{id} never
  // shadow blackboard/report projections (URLSearchParams reorders query args).
  if (url.includes("/api/v2/") && url.includes("/blackboard/health")) {
    return (
      routes["__health_v2__"] ??
      routes["/api/v2/projects/project-1/blackboard/health"] ?? {
        schema: "blackboard-health/v2",
        revision: 3,
        status: "healthy",
        attention: {
          bytes: 1024,
          estimated_tokens: 256,
          state: "within_target",
          complete: true,
          launchable: true,
          consolidation_offered: false,
          consolidation_required: false,
        },
        anomalies: [],
        proposals: [],
      }
    );
  }
  if (url.includes("/api/v2/") && url.includes("/blackboard/snapshot")) {
    return routes["__snapshot__"] ?? runtimeSnapshotV2;
  }
  if (url.includes("/api/v2/") && url.includes("/history")) {
    return routes["__history_v2__"] ?? historyV2;
  }
  if (url.includes("/api/v2/") && /\/blackboard\/records\//.test(url)) {
    return routes["__detail_v2__"] ?? recordDetailV2;
  }
  // Retired v1 record-collection reads must fail for Finding/Evidence/Fact consumers.
  if (
    url.includes("/blackboard/records") &&
    (url.includes("node_type=finding") ||
      url.includes("node_type=evidence_artifact") ||
      url.includes("node_type=project_fact")) &&
    !url.includes("/api/v2/") &&
    !/\/records\/[^?]+/.test(url)
  ) {
    return { error: { code: "not_found", message: "retired v1 record collection route" } };
  }
  if (url.includes("/history")) {
    for (const [key, body] of Object.entries(routes)) {
      if (key.includes("/history") && url.includes(key.replace(/^.*records/, "/blackboard/records"))) {
        return body;
      }
      if (url.includes(key) && key.includes("/history")) return body;
    }
  }
  if (url.includes("/provenance")) {
    for (const [key, body] of Object.entries(routes)) {
      if (url.includes(key) && key.includes("/provenance")) return body;
    }
  }
  if (/\/blackboard\/records\/[^?/]+/.test(url) && !url.includes("/history") && !url.includes("/provenance") && !url.includes("/api/v2/")) {
    return routes["__detail__"] ?? recordDetail;
  }
  if (url.includes("/blackboard/work-view")) return routes["__work__"] ?? workViewEnvelope;
  if (url.includes("/blackboard/entities")) return routes["__entities__"] ?? entityCollection;
  if (url.includes("/blackboard/graph-explorer")) return routes["__explorer__"] ?? graphExplorer;
  // v1 audit health only — v2 semantic health is handled above.
  if (url.includes("/blackboard/health") && !url.includes("/api/v2/")) {
    return routes["__health__"] ?? healthSummary;
  }
  if (url.includes("/api/v2/") && url.includes("/reports/pentest")) {
    if (url.includes("format=json")) {
      return (
        routes["__pentest_report_json__"] ??
        routes["/api/v2/projects/project-1/reports/pentest?format=json"] ?? {
          schema: "pentest-report/v2",
          project: { name: "Acme External" },
          confirmed_findings: [
            {
              key: "finding:admin-exposed",
              title: "Admin panel exposed",
              status: "confirmed",
              severity: "high",
              cvss_pending: false,
              supporting_facts: [],
              contradictions: [],
              evidence: [],
            },
          ],
          unconfirmed_findings: [],
          confirmed_facts: [],
          tentative_facts: [],
        }
      );
    }
    return (
      routes["__pentest_report__"] ??
      routes["/api/v2/projects/project-1/reports/pentest?format=markdown"] ?? {
        schema: "report-markdown/v2",
        markdown: "# Acme External Pentest Report\n\n## Confirmed Findings\n\n- Admin panel exposed\n",
      }
    );
  }
  if (url.includes("/api/v2/") && url.includes("/reports/ctf-solution")) {
    if (url.includes("format=json")) {
      return (
        routes["__ctf_solution_json__"] ??
        routes["/api/v2/projects/ctf-1/reports/ctf-solution?format=json"] ?? {
          schema: "ctf-solution/v2",
          project: { name: "Flag CTF" },
          solved: true,
          verified_flags: [
            {
              key: "solution:flag",
              kind: "flag",
              status: "verified",
              summary: "Recovered flag",
              value: "FLAG{accepted}",
            },
          ],
          candidate_flags: [],
          answers: [],
          procedures: [],
          confirmed_facts: [],
          tentative_facts: [],
          evidence: [],
        }
      );
    }
    return (
      routes["__ctf_solution__"] ??
      routes["/api/v2/projects/ctf-1/reports/ctf-solution?format=markdown"] ?? {
        schema: "report-markdown/v2",
        markdown: "# Flag CTF CTF Solution\n\n## Solved Status\n\nSolved: yes\n",
      }
    );
  }
  // Retired v1 report routes must not be served to production consumers.
  if (url.includes("/reports/pentest") || url.includes("/reports/ctf-solution")) {
    return { error: { code: "not_found", message: "retired v1 report route" } };
  }
  if (url.includes("/dashboard")) return routes["__dashboard__"] ?? dashboard;

  // Prefer the most specific registered key for remaining project/meta routes.
  const ranked = Object.entries(routes).sort((a, b) => b[0].length - a[0].length);
  for (const [key, body] of ranked) {
    if (url.includes(key)) return body;
  }
  return {};
}

function trackFetch(routes: Record<string, unknown>) {
  const requests: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input.toString();
    requests.push(url);
    const body = routeBody(url, routes);
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, requests };
}

function renderAt(path: string, element: React.ReactElement, routePath: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path={routePath} element={element} />
        {/* Allow NavLink targets under /blackboard/* to remount the same page. */}
        {routePath.includes("blackboard") && (
          <>
            <Route path="/projects/:projectId/blackboard" element={element} />
            <Route path="/projects/:projectId/blackboard/*" element={element} />
          </>
        )}
      </Routes>
    </MemoryRouter>,
  );
}

function renderBlackboard(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/projects/:projectId/blackboard" element={<BlackboardPage />} />
        <Route path="/projects/:projectId/blackboard/*" element={<BlackboardPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("U06 bundled UI graph-backed focused views", () => {
  afterEach(() => {
    cleanup();
  });

  it("TestBundledUIRendersGraphBackedFocusedViewsWithoutLegacyFallbackRequests", async () => {
    const user = userEvent.setup();
    const { requests } = trackFetch({
      "/api/projects/project-1/dashboard": dashboard,
      "/api/v2/projects/project-1/blackboard/snapshot": runtimeSnapshotV2,
      "/api/v2/projects/project-1/blackboard/records/finding%3Aadmin-exposed": recordDetailV2,
      "/api/v2/projects/project-1/blackboard/records/finding:admin-exposed": recordDetailV2,
      "/api/v2/projects/project-1/reports/pentest?format=json": {
        schema: "pentest-report/v2",
        project: { name: "Acme External" },
        confirmed_findings: [
          {
            key: "finding:admin-exposed",
            title: "Admin panel exposed",
            status: "confirmed",
            severity: "high",
            cvss_pending: false,
            supporting_facts: [],
            contradictions: [],
            evidence: [],
          },
        ],
        unconfirmed_findings: [],
        confirmed_facts: [],
        tentative_facts: [],
      },
      "/api/v2/projects/project-1/reports/pentest?format=markdown": {
        schema: "report-markdown/v2",
        markdown: "# Acme External Pentest Report\n\n## Confirmed Findings\n\n- Admin panel exposed\n",
      },
      "/api/projects/project-1": project,
      "/api/v2/projects/ctf-1/reports/ctf-solution?format=json": {
        schema: "ctf-solution/v2",
        project: { name: "Flag CTF" },
        solved: true,
        verified_flags: [
          {
            key: "solution:flag",
            kind: "flag",
            status: "verified",
            summary: "Recovered flag",
            value: "FLAG{accepted}",
          },
        ],
        candidate_flags: [],
        answers: [],
        procedures: [],
        confirmed_facts: [],
        tentative_facts: [],
        evidence: [],
      },
      "/api/v2/projects/ctf-1/reports/ctf-solution?format=markdown": {
        schema: "report-markdown/v2",
        markdown: "# Flag CTF CTF Solution\n\n## Solved Status\n\nSolved: yes\n",
      },
      "/api/projects/ctf-1": ctfProject,
      "/api/projects": { projects: [project] },
    });

    // Dashboard consumes ProjectBlackboardSummary (graph projection via /dashboard).
    renderAt("/projects/project-1", <ProjectDashboardPage />, "/projects/:projectId");
    expect(await screen.findByRole("heading", { level: 1, name: "Acme External" })).toBeInTheDocument();
    cleanup();

    // Ordinary Blackboard is Snapshot-backed Current Work + Project Knowledge.
    renderBlackboard("/projects/project-1/blackboard");
    expect(await screen.findByRole("heading", { name: /Blackboard/i })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: /Current Work/i })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: /Project Knowledge/i })).toBeInTheDocument();
    expect(screen.getAllByText("Admin panel exposed").length).toBeGreaterThan(0);
    expect(screen.getByRole("navigation", { name: /Blackboard views/i })).toBeInTheDocument();

    // Knowledge + Explorer tabs stay on v2 Snapshot projections.
    await user.click(screen.getByRole("link", { name: /^Knowledge$/i }));
    expect(await screen.findByText("acme.test")).toBeInTheDocument();
    await user.click(screen.getByRole("link", { name: /^Explorer$/i }));
    expect(await screen.findByRole("table", { name: /Graph Explorer records/i })).toBeInTheDocument();
    expect(screen.getByRole("table", { name: /Graph Explorer records/i })).toHaveTextContent(
      "Admin panel exposed",
    );
    cleanup();

    // Focused Finding/Evidence bookmarks read the v2 Snapshot and key-based detail.
    renderAt(
      "/projects/project-1/findings",
      <FindingsPage />,
      "/projects/:projectId/findings",
    );
    expect(await screen.findByText("Admin panel exposed")).toBeInTheDocument();
    expect(screen.getByText("high")).toBeInTheDocument();
    cleanup();

    renderAt(
      "/projects/project-1/evidence",
      <EvidencePage />,
      "/projects/:projectId/evidence",
    );
    expect(await screen.findByText("Captured HTTP exchange")).toBeInTheDocument();
    cleanup();

    // Report uses structured v2 JSON (keys) plus markdown deliverable.
    renderAt("/projects/project-1/report", <ReportPage />, "/projects/:projectId/report");
    expect(await screen.findByText(/Confirmed Findings \(1\)/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Admin panel exposed/i })).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/finding%3Aadmin-exposed",
    );
    cleanup();

    // CTF Solution is only for CTF Projects and uses verified-flag solved state.
    renderAt(
      "/projects/ctf-1/solution",
      <SolutionPage />,
      "/projects/:projectId/solution",
    );
    expect(
      await screen.findByRole("heading", { name: /Flag CTF — Solved: yes/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /Recovered flag/i }),
    ).toHaveAttribute("href", "/projects/ctf-1/blackboard/records/solution%3Aflag");
    cleanup();

    // Legacy Facts bookmark redirects into Blackboard Knowledge.
    renderBlackboard("/projects/project-1/blackboard/knowledge");
    expect(await screen.findByRole("region", { name: /Project Knowledge/i })).toBeInTheDocument();
    expect(screen.getAllByText("Admin panel exposed").length).toBeGreaterThan(0);
    cleanup();

    // Record detail bookmark uses Blackboard Keys over v2.
    renderBlackboard("/projects/project-1/blackboard/records/finding%3Aadmin-exposed");
    expect((await screen.findAllByText("finding:admin-exposed")).length).toBeGreaterThan(0);
    cleanup();

    const legacyHits = requests.filter((url) => isLegacyFrozenRead(url));
    expect(legacyHits).toEqual([]);

    const blackboardHits = requests.filter(
      (url) =>
        (url.includes("/api/v2/") && url.includes("/blackboard/")) ||
        (url.includes("/api/v2/") && url.includes("/reports/")) ||
        url.includes("/dashboard"),
    );
    expect(blackboardHits.length).toBeGreaterThan(0);
    expect(requests.some((url) => url.includes("node_type="))).toBe(false);
    expect(requests.some((url) => url.includes("/api/projects/") && url.includes("/reports/"))).toBe(
      false,
    );

    // Dense ledger rows remain keyboard reachable (link/row semantics).
    renderAt(
      "/projects/project-1/findings",
      <FindingsPage />,
      "/projects/:projectId/findings",
    );
    const findingRow = await screen.findByRole("link", {
      name: /Admin panel exposed/i,
    });
    expect(findingRow).toHaveAttribute("href", expect.stringContaining("/blackboard/records/"));
  });

  it("keeps ProjectNav bookmarks valid and CTF Solution exclusive", async () => {
    mockApi({
      "/api/projects/project-1/dashboard": dashboard,
      "/api/projects/project-1": project,
      "/api/projects/ctf-1": ctfProject,
      "/api/projects/ctf-1/dashboard": {
        ...dashboard,
        project_id: "ctf-1",
        name: "Flag CTF",
        project_kind: "ctf_challenge",
        ctf: { solved: true, verified_flag_count: 1, candidate_solution_count: 0, primary_solution: null },
      },
    });

    render(
      <MemoryRouter initialEntries={["/projects/project-1"]}>
        <Routes>
          <Route path="/projects/:projectId" element={<ProjectDashboardPage />} />
        </Routes>
      </MemoryRouter>,
    );

    // Pentest dashboard still links into Findings/Report rather than Solution.
    expect(await screen.findByRole("link", { name: /open report/i })).toHaveAttribute(
      "href",
      "/projects/project-1/report",
    );
  });

  it("Graph Explorer table matches graph node labels for accessibility parity", async () => {
    trackFetch({
      "/api/v2/projects/project-1/blackboard/snapshot": runtimeSnapshotV2,
      "/api/projects/project-1": project,
    });

    renderBlackboard("/projects/project-1/blackboard/explorer");

    const table = await screen.findByRole("table", { name: /Graph Explorer records/i });
    expect(within(table).getAllByText("Admin panel exposed").length).toBeGreaterThan(0);
    expect(within(table).getByText("finding:admin-exposed")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: /Graph Explorer relationships/i })).toBeInTheDocument();
  });
});
