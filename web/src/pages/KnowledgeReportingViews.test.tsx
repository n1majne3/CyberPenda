import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { EvidencePage } from "./EvidencePage";
import { FactsPage } from "./FactsPage";
import { FindingsPage } from "./FindingsPage";
import { ReportPage } from "./ReportPage";
import { ScopeEditorPage } from "./ScopeEditorPage";

function renderRoute(path: string, element: React.ReactElement) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/projects/:projectId/scope" element={element} />
        <Route path="/projects/:projectId/facts" element={element} />
        <Route path="/projects/:projectId/findings" element={element} />
        <Route path="/projects/:projectId/evidence" element={element} />
        <Route path="/projects/:projectId/report" element={element} />
      </Routes>
    </MemoryRouter>,
  );
}

const project = {
  id: "project-1",
  name: "Acme External",
  description: "External web assessment",
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

const task = {
  id: "task-1",
  project_id: "project-1",
  goal: "Validate admin exposure and report evidence",
  status: "completed",
  runner: "sandbox",
  runtime_profile_id: "profile-1",
  run_controls: {},
  scope_snapshot: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-02T00:00:00Z",
};

describe("knowledge and reporting views", () => {
  it("renders the Scope editor with Geist hierarchy and explicit safety states", async () => {
    mockApi({
      "/api/projects/project-1": project,
      "/api/runtime-profiles": { profiles: [] },
    });

    renderRoute("/projects/project-1/scope", <ScopeEditorPage />);

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

  it("renders Project Facts as dense Geist rows with non-color scope status", async () => {
    mockApi({
      "/api/projects/project-1/facts/index": {
        facts: [
          {
            fact_key: "asset:mail",
            category: "asset",
            summary: "mail.acme.test responds but is outside current Scope",
            confidence: "tentative",
            scope_status: "out_of_scope",
          },
        ],
      },
      "/api/projects/project-1/tasks": { tasks: [] },
    });

    renderRoute("/projects/project-1/facts", <FactsPage />);

    const row = await screen.findByRole("button", {
      name: /mail\.acme\.test responds but is outside current Scope/i,
    });
    expect(row).toHaveClass("rounded-lg", "border", "bg-card", "shadow-sm");
    expect(screen.getByText("out-of-scope")).toHaveClass("border-warning/25", "bg-warning/10");
    expect(screen.getByText("non-actionable")).toBeInTheDocument();
  });

  it("renders Findings with readable sections and explicit pending states", async () => {
    mockApi({
      "/api/projects/project-1/findings": {
        findings: [
          {
            id: "finding-1",
            project_id: "project-1",
            finding_key: "finding:admin-exposed",
            version: 1,
            title: "Admin panel exposed",
            description: "Admin panel is reachable from the internet.",
            status: "confirmed",
            target: "https://acme.test/admin",
            proof: "HTTP 200 from unauthenticated request.",
            impact: "Account takeover path may exist.",
            recommendation: "Restrict access and require MFA.",
            cvss_version: "4.0",
            cvss_vector: "",
            cvss_pending: true,
            severity: "high",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-02T00:00:00Z",
          },
        ],
      },
    });

    renderRoute("/projects/project-1/findings", <FindingsPage />);

    expect(await screen.findByText("Confirmed (1)")).toHaveClass("tracking-tight");
    expect(screen.getByText("Admin panel exposed").closest("article")).toHaveClass(
      "rounded-lg",
      "border",
      "bg-card",
      "shadow-sm",
    );
    expect(screen.getByText("CVSS pending")).toHaveClass("border-warning/25", "bg-warning/10");
  });

  it("renders Evidence artifacts in responsive rows with scannable hashes", async () => {
    mockApi({
      "/api/projects/project-1/evidence": {
        evidence: [
          {
            id: "evidence-1",
            project_id: "project-1",
            evidence_key: "evidence:http-admin",
            attach_to_type: "finding",
            attach_to_key: "finding:admin-exposed",
            artifact_type: "http-response",
            source_path: "/tmp/response.txt",
            managed_path: "/artifacts/task-1/response.txt",
            sha256: "0123456789abcdef",
            summary: "Admin response capture",
            created_at: "2026-01-02T00:00:00Z",
            updated_at: "2026-01-02T00:00:00Z",
          },
        ],
      },
    });

    renderRoute("/projects/project-1/evidence", <EvidencePage />);

    const artifact = await screen.findByText("Admin response capture");
    expect(artifact.closest("article")).toHaveClass("flex-col", "sm:flex-row", "bg-card");
    expect(screen.getByText("sha256: 01234567")).toHaveClass("max-w-full", "truncate");
  });

  it("renders Report controls and preview as responsive Geist surfaces", async () => {
    mockApi({
      "/api/projects/project-1/tasks": { tasks: [task] },
    });

    renderRoute("/projects/project-1/report", <ReportPage />);

    expect((await screen.findByText("Report source")).closest("section")).toHaveClass(
      "rounded-lg",
      "border",
      "bg-card",
      "shadow-sm",
    );
    expect(screen.getByText("No report generated yet.")).toHaveClass("border-dashed", "bg-muted/30");
  });
});
