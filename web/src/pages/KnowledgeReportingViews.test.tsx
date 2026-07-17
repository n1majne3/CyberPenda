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
import { SolutionPage } from "./SolutionPage";

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

  it("redirects legacy Facts bookmarks to Blackboard Knowledge", async () => {
    mockApi({
      "/api/v2/projects/project-1/blackboard/snapshot": {
        schema: "runtime-blackboard/v2",
        semantics: "work is active; knowledge is current; history and details are available by key",
        revision: 1,
        work: {},
        knowledge: {
          facts: {
            "fact:mail": {
              version: 1,
              category: "asset",
              summary: "mail.acme.test responds but is outside current Scope",
              confidence: "tentative",
              scope_status: "out_of_scope",
            },
          },
        },
        relations: [],
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
    expect(await screen.findByRole("region", { name: /Project Knowledge/i })).toBeInTheDocument();
    expect(
      screen.getByRole("link", {
        name: /mail\.acme\.test responds but is outside current Scope/i,
      }),
    ).toBeInTheDocument();
    expect(screen.getByText("out-of-scope")).toHaveClass("border-warning/25", "bg-warning/10");
  });

  it("renders Findings from the v2 Snapshot with severity preserved per identity", async () => {
    mockApi({
      "/api/v2/projects/project-1/blackboard/snapshot": {
        schema: "runtime-blackboard/v2",
        semantics: "work is active; knowledge is current; history and details are available by key",
        revision: 2,
        work: {},
        knowledge: {
          findings: {
            "finding:admin-exposed": {
              version: 1,
              status: "confirmed",
              title: "Admin panel exposed",
              target: "https://acme.test/admin",
              severity: "high",
              cvss_pending: false,
            },
            "finding:verbose": {
              version: 1,
              status: "unconfirmed",
              title: "Verbose errors",
              severity: "low",
              cvss_pending: true,
            },
          },
        },
        relations: [],
      },
      "/api/projects/project-1": project,
    });

    renderRoute("/projects/project-1/findings", <FindingsPage />, "/projects/:projectId/findings");

    expect(await screen.findByText("Confirmed (1)")).toHaveClass("tracking-tight");
    expect(screen.getByText("Unconfirmed (1)")).toBeInTheDocument();
    const findingLink = screen.getByRole("link", { name: /Admin panel exposed/i });
    expect(findingLink).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/finding%3Aadmin-exposed",
    );
    expect(screen.getByText("high")).toBeInTheDocument();
    expect(screen.getByText("low")).toBeInTheDocument();
  });

  it("Findings row link uses Blackboard Key only", async () => {
    mockApi({
      "/api/v2/projects/project-1/blackboard/snapshot": {
        schema: "runtime-blackboard/v2",
        semantics: "work is active; knowledge is current; history and details are available by key",
        revision: 1,
        work: {},
        knowledge: {
          findings: {
            "finding:sqli-login": {
              version: 1,
              status: "confirmed",
              title: "SQL injection on login",
              severity: "critical",
              cvss_pending: false,
            },
          },
        },
        relations: [],
      },
      "/api/projects/project-1": project,
    });

    renderRoute("/projects/project-1/findings", <FindingsPage />, "/projects/:projectId/findings");

    const link = await screen.findByRole("link", { name: /SQL injection on login/i });
    expect(link).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/finding%3Asqli-login",
    );
    expect(link.getAttribute("href")).not.toContain("uuid");
    expect(link.getAttribute("href")).not.toMatch(/node-/);
  });

  it("renders Evidence from the v2 Snapshot by Blackboard Key", async () => {
    mockApi({
      "/api/v2/projects/project-1/blackboard/snapshot": {
        schema: "runtime-blackboard/v2",
        semantics: "work is active; knowledge is current; history and details are available by key",
        revision: 1,
        work: {},
        knowledge: {
          evidence: {
            "evidence:http-admin": {
              version: 1,
              status: "available",
              artifact_type: "http-response",
              summary: "Admin response capture",
            },
          },
        },
        relations: [],
      },
      "/api/projects/project-1": project,
    });

    renderRoute("/projects/project-1/evidence", <EvidencePage />, "/projects/:projectId/evidence");

    const artifact = await screen.findByRole("link", { name: /Admin response capture/i });
    expect(artifact).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/evidence%3Ahttp-admin",
    );
    expect(screen.getByText("available")).toBeInTheDocument();
  });

  it("Evidence row link uses Blackboard Key only", async () => {
    mockApi({
      "/api/v2/projects/project-1/blackboard/snapshot": {
        schema: "runtime-blackboard/v2",
        semantics: "work is active; knowledge is current; history and details are available by key",
        revision: 1,
        work: {},
        knowledge: {
          evidence: {
            "evidence:pcap-1": {
              version: 1,
              status: "available",
              artifact_type: "pcap",
              summary: "Traffic capture",
            },
          },
        },
        relations: [],
      },
      "/api/projects/project-1": project,
    });

    renderRoute("/projects/project-1/evidence", <EvidencePage />, "/projects/:projectId/evidence");

    const link = await screen.findByRole("link", { name: /Traffic capture/i });
    expect(link).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/evidence%3Apcap-1",
    );
    expect(link.getAttribute("href")).not.toContain("uuid");
  });

  it("renders Report from v2 JSON with Blackboard Key links and markdown preview", async () => {
    mockApi({
      "/api/v2/projects/project-1/reports/pentest?format=json": {
        schema: "pentest-report/v2",
        project: { name: "Acme External", description: "External assessment" },
        confirmed_findings: [
          {
            key: "finding:admin-exposed",
            title: "Admin panel exposed",
            status: "confirmed",
            severity: "high",
            cvss_pending: false,
            supporting_facts: [
              {
                key: "fact:admin-facing",
                category: "exposure",
                summary: "Admin is internet-facing",
                confidence: "confirmed",
                scope_status: "in_scope",
              },
            ],
            contradictions: [
              {
                key: "fact:maybe-internal",
                category: "recon",
                summary: "May only be internal",
                confidence: "tentative",
                scope_status: "unknown",
              },
            ],
            evidence: [
              {
                key: "evidence:http-admin",
                status: "available",
                artifact_type: "http-response",
                summary: "Admin response capture",
              },
            ],
          },
        ],
        unconfirmed_findings: [],
        confirmed_facts: [
          {
            key: "fact:admin-facing",
            category: "exposure",
            summary: "Admin is internet-facing",
            confidence: "confirmed",
            scope_status: "in_scope",
          },
        ],
        tentative_facts: [
          {
            key: "fact:maybe-internal",
            category: "recon",
            summary: "May only be internal",
            confidence: "tentative",
            scope_status: "unknown",
          },
        ],
      },
      "/api/v2/projects/project-1/reports/pentest?format=markdown": {
        schema: "report-markdown/v2",
        markdown: "# Acme External Pentest Report\n\n## Confirmed Findings\n\n_No records._\n",
      },
      "/api/projects/project-1": project,
    });

    renderRoute("/projects/project-1/report", <ReportPage />, "/projects/:projectId/report");

    expect(await screen.findByText("Deterministic Pentest report")).toBeInTheDocument();
    const findingLink = await screen.findByRole("link", { name: /Admin panel exposed/i });
    expect(findingLink).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/finding%3Aadmin-exposed",
    );
    expect(screen.getByRole("link", { name: /evidence:http-admin/i })).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/evidence%3Ahttp-admin",
    );
    const confirmedFactLinks = await screen.findAllByRole("link", {
      name: /Admin is internet-facing/i,
    });
    expect(confirmedFactLinks.length).toBeGreaterThanOrEqual(1);
    for (const link of confirmedFactLinks) {
      expect(link).toHaveAttribute(
        "href",
        "/projects/project-1/blackboard/records/fact%3Aadmin-facing",
      );
    }
    const tentativeFactLinks = screen.getAllByRole("link", { name: /May only be internal/i });
    expect(tentativeFactLinks.length).toBeGreaterThanOrEqual(1);
    for (const link of tentativeFactLinks) {
      expect(link).toHaveAttribute(
        "href",
        "/projects/project-1/blackboard/records/fact%3Amaybe-internal",
      );
    }
    expect(screen.getByRole("heading", { name: /Report preview/i })).toBeInTheDocument();
    expect(screen.getByText(/Acme External Pentest Report/i)).toBeInTheDocument();
  });

  it("renders CTF Solution Facts with Blackboard Key links", async () => {
    mockApi({
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
        tentative_facts: [
          {
            key: "fact:maybe-token",
            category: "challenge",
            summary: "Maybe another token exists",
            confidence: "tentative",
            scope_status: "unknown",
          },
        ],
        evidence: [],
      },
      "/api/v2/projects/ctf-1/reports/ctf-solution?format=markdown": {
        schema: "report-markdown/v2",
        markdown: "# Flag CTF CTF Solution\n\n## Solved Status\n\nSolved: yes\n",
      },
      "/api/projects/ctf-1": {
        ...project,
        id: "ctf-1",
        name: "Flag CTF",
        kind: "ctf_challenge",
      },
    });

    renderRoute("/projects/ctf-1/solution", <SolutionPage />, "/projects/:projectId/solution");

    expect(
      await screen.findByRole("heading", { name: /Flag CTF — Solved: yes/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Parser accepts reversed hex/i })).toHaveAttribute(
      "href",
      "/projects/ctf-1/blackboard/records/fact%3Aparser-clue",
    );
    expect(screen.getByRole("link", { name: /Maybe another token exists/i })).toHaveAttribute(
      "href",
      "/projects/ctf-1/blackboard/records/fact%3Amaybe-token",
    );
  });
});
