import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { BlackboardPage } from "./BlackboardPage";
import { EvidencePage } from "./EvidencePage";
import { FactsPage } from "./FactsPage";
import { FindingsPage } from "./FindingsPage";
import { ReportPage } from "./ReportPage";
import { ScopeEditorPage } from "./ScopeEditorPage";

function renderRoute(path: string, element: React.ReactElement, routePath: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path={routePath} element={element} />
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

const project = {
  id: "project-1",
  name: "Acme External",
  description: "External web assessment",
  kind: "pentest",
  scope: {
    domains: ["acme.test"],
    ips: ["203.0.113.5"],
    cidrs: [],
    urls: ["https://acme.test/admin"],
    ports: ["443"],
    excluded: ["mail.acme.test"],
    testing_limits: ["Business hours only"],
    notes: "Coordinate with ops.",
  },
  defaults: { runner: "sandbox" },
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-02T00:00:00Z",
};

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
      evidence: 0,
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

describe("knowledge and reporting views", () => {
  it("renders the Scope editor with Geist hierarchy and explicit safety states", async () => {
    mockApi({
      "/api/projects/project-1": project,
      "/api/runtime-profiles": { profiles: [] },
    });

    renderRoute("/projects/project-1/scope", <ScopeEditorPage />, "/projects/:projectId/scope");

    expect(await screen.findByRole("heading", { name: /Scope & defaults/i })).toHaveClass(
      "tracking-tight",
    );
    expect(screen.getByText("Project defaults").closest("section")).toHaveClass(
      "rounded-lg",
      "border",
      "bg-card",
      "shadow-sm",
    );
    expect(screen.getByText("Out-of-scope assets")).toBeInTheDocument();
    expect(screen.getByText("non-actionable")).toHaveClass("border-warning/25", "bg-warning/10");
    expect(screen.getByLabelText("Default runner").closest("div")?.parentElement).toHaveClass(
      "grid-cols-1",
      "sm:grid-cols-2",
    );
  });

  it("redirects legacy Facts bookmarks to Blackboard Work filtered by project_fact", async () => {
    mockApi({
      "/api/projects/project-1/blackboard/records": {
        protocol_version: 1,
        projection: "record_collection_v1",
        project_id: "project-1",
        project_kind: "pentest",
        observed_graph_revision: 1,
        observed_state_hash: "hash",
        projection_hash: "facts",
        result: {
          items: [
            nodeRow({
              id: "node-fact-1",
              node_type: "project_fact",
              stable_key: "asset:mail",
              label: "mail.acme.test responds but is outside current Scope",
              secondary: "asset",
              scope_status: "out_of_scope",
              lifecycle: "tentative",
            }),
          ],
          facets: {},
          page: { limit: 50, total_items: 1 },
        },
      },
      "/api/projects/project-1": project,
    });

    render(
      <MemoryRouter initialEntries={["/projects/project-1/facts"]}>
        <Routes>
          <Route path="/projects/:projectId/facts" element={<FactsPage />} />
          <Route path="/projects/:projectId/blackboard" element={<BlackboardPage />} />
          <Route path="/projects/:projectId/blackboard/*" element={<BlackboardPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByRole("heading", { name: /Blackboard/i })).toBeInTheDocument();
    expect(await screen.findByText(/Filtered ledger/i)).toBeInTheDocument();
    expect(screen.getByText(/node_type=project_fact/i)).toBeInTheDocument();
    expect(
      screen.getByRole("link", {
        name: /mail\.acme\.test responds but is outside current Scope/i,
      }),
    ).toBeInTheDocument();
    expect(screen.getByText("out-of-scope")).toHaveClass("border-warning/25", "bg-warning/10");
    expect(screen.getByText("non-actionable")).toBeInTheDocument();
  });

  it("renders Findings as graph-backed ledger rows with severity labels", async () => {
    mockApi({
      "/api/projects/project-1/blackboard/records": {
        protocol_version: 1,
        projection: "record_collection_v1",
        project_id: "project-1",
        project_kind: "pentest",
        observed_graph_revision: 1,
        observed_state_hash: "hash",
        projection_hash: "findings",
        result: {
          items: [
            nodeRow({
              id: "node-finding-1",
              node_type: "finding",
              stable_key: "finding:admin-exposed",
              label: "Admin panel exposed",
              secondary: "https://acme.test/admin",
              severity: "high",
              lifecycle: "confirmed",
            }),
          ],
          facets: {},
          page: { limit: 100, total_items: 1 },
        },
      },
      "/api/projects/project-1": project,
    });

    renderRoute("/projects/project-1/findings", <FindingsPage />, "/projects/:projectId/findings");

    expect(await screen.findByText("Confirmed (1)")).toHaveClass("tracking-tight");
    expect(screen.getByRole("link", { name: /Admin panel exposed/i })).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/node-finding-1",
    );
    expect(screen.getByText("high")).toBeInTheDocument();
  });

  it("renders Evidence artifacts as graph-backed ledger rows", async () => {
    mockApi({
      "/api/projects/project-1/blackboard/records": {
        protocol_version: 1,
        projection: "record_collection_v1",
        project_id: "project-1",
        project_kind: "pentest",
        observed_graph_revision: 1,
        observed_state_hash: "hash",
        projection_hash: "evidence",
        result: {
          items: [
            nodeRow({
              id: "node-evidence-1",
              node_type: "evidence_artifact",
              stable_key: "evidence:http-admin",
              label: "Admin response capture",
              secondary: "http-response",
              lifecycle: "available",
            }),
          ],
          facets: {},
          page: { limit: 100, total_items: 1 },
        },
      },
      "/api/projects/project-1": project,
    });

    renderRoute("/projects/project-1/evidence", <EvidencePage />, "/projects/:projectId/evidence");

    const artifact = await screen.findByText("Admin response capture");
    expect(artifact.closest("a")).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/node-evidence-1",
    );
    expect(screen.getByText("available")).toBeInTheDocument();
  });

  it("renders Report as a deterministic graph deliverable preview", async () => {
    mockApi({
      "/api/projects/project-1/reports/pentest": {
        protocol_version: 1,
        projection: "pentest_report_v1",
        project_id: "project-1",
        project_kind: "pentest",
        observed_graph_revision: 1,
        observed_state_hash: "hash",
        projection_hash: "report",
        result: {
          source: {
            project_id: "project-1",
            project_name: "Acme External",
            graph_revision: 1,
            state_hash: "hash",
            source_hash: "source",
            renderer_version: "pentest_markdown_v1",
          },
          markdown: "# Acme External\n\n## Confirmed findings\n",
        },
      },
      "/api/projects/project-1": project,
    });

    renderRoute("/projects/project-1/report", <ReportPage />, "/projects/:projectId/report");

    expect(await screen.findByText("Deterministic Pentest report")).toBeInTheDocument();
    expect(screen.getByText(/Confirmed findings/i)).toBeInTheDocument();
  });
});
