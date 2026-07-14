import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { ProjectDashboardPage } from "./ProjectDashboardPage";
import { mockApi } from "@/test/mockApi";

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/projects/project-1"]}>
      <Routes>
        <Route path="/projects/:projectId" element={<ProjectDashboardPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

const project = {
  id: "project-1",
  name: "Acme External",
  description: "External web and API assessment",
  scope: {},
  defaults: {},
  created_at: "",
  updated_at: "",
};

const dashboard = {
  project_id: "project-1",
  name: "Acme External",
  scope: {
    domains: 2,
    ips: 1,
    cidrs: 0,
    urls: 3,
    ports: 4,
    excluded: 1,
    has_testing_limits: true,
    has_notes: true,
    ready: false,
  },
  counts: {
    tasks: 3,
    facts: 8,
    findings: 1,
    evidence: 5,
  },
};

describe("ProjectDashboardPage", () => {
  it("shows a concise loading state before dashboard data resolves", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>(() => {
            // Keep the request pending so the initial page state is observable.
          }),
      ),
    );

    renderPage();

    const status = screen.getByRole("status", { name: /loading dashboard/i });
    expect(status).toHaveClass("rounded-lg", "border", "bg-card", "text-muted-foreground");
  });

  it("uses an alert state when dashboard data cannot load", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ error: "dashboard unavailable" }), {
            status: 503,
            statusText: "Service Unavailable",
            headers: { "Content-Type": "application/json" },
          }),
        ),
      ),
    );

    renderPage();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load dashboard");
    expect(alert).toHaveTextContent("dashboard unavailable");
    expect(alert).toHaveClass("rounded-lg", "border-destructive/25", "bg-card");
  });

  it("renders dashboard hierarchy, scope readiness, counts, and primary actions", async () => {
    mockApi({
      "/api/projects/project-1/dashboard": dashboard,
      "/api/projects/project-1": project,
    });

    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "Acme External" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /launch task/i })).toHaveClass("rounded-md", "bg-primary");
    expect(screen.getByRole("link", { name: /edit scope/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /open report/i })).toBeInTheDocument();

    const scope = screen.getByRole("region", { name: /scope readiness/i });
    expect(scope).toHaveClass("rounded-lg", "border", "bg-card");
    expect(scope).toHaveTextContent("Scope needs attention");
    expect(scope).toHaveTextContent("Testing limits set");
    expect(scope).toHaveTextContent("Scope notes");
    expect(scope).toHaveTextContent("2 domains");
    expect(scope).toHaveTextContent("1 IP");
    expect(scope).toHaveTextContent("3 URLs");
    expect(scope).toHaveTextContent("1 excluded");

    expect(screen.getByRole("link", { name: /view 3 tasks/i })).toHaveClass("rounded-lg", "border", "bg-card");
    expect(screen.getByRole("link", { name: /view 8 facts/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /view 1 finding/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /view 5 evidence items/i })).toBeInTheDocument();
  });
});
