import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
  kind: "pentest",
  scope: {},
  defaults: {},
  created_at: "",
  updated_at: "",
};

// Scope is self-consistent with the backend's readiness rule (ready means at
// least one named in-scope asset): not ready because every asset count is 0.
const dashboard = {
  project_id: "project-1",
  name: "Acme External",
  scope: {
    domains: 0,
    ips: 0,
    cidrs: 0,
    urls: 0,
    ports: 0,
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

const tasks = {
  tasks: [
    {
      id: "task-1",
      project_id: "project-1",
      goal: "Enumerate api.acme.example",
      status: "running",
      runner: "pi",
      runtime_profile_id: "profile-1",
      run_controls: {},
      scope_snapshot: {},
      created_at: new Date(Date.now() - 30 * 60_000).toISOString(),
      updated_at: new Date(Date.now() - 12 * 60_000).toISOString(),
    },
    {
      id: "task-2",
      project_id: "project-1",
      goal: "Port scan 203.0.113.10",
      status: "completed",
      runner: "pi",
      runtime_profile_id: "profile-1",
      run_controls: {},
      scope_snapshot: {},
      created_at: new Date(Date.now() - 5 * 3_600_000).toISOString(),
      updated_at: new Date(Date.now() - 2 * 3_600_000).toISOString(),
    },
    {
      id: "task-3",
      project_id: "project-1",
      goal: "Fingerprint cdn.acme.example",
      status: "stopped",
      runner: "pi",
      runtime_profile_id: "profile-1",
      run_controls: {},
      scope_snapshot: {},
      created_at: new Date(Date.now() - 3 * 86_400_000).toISOString(),
      updated_at: new Date(Date.now() - 2 * 86_400_000).toISOString(),
    },
  ],
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
      "/api/projects/project-1/tasks": tasks,
      "/api/projects/project-1": project,
    });

    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "Acme External" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /launch task/i })).toHaveClass("rounded-md", "bg-primary");
    expect(screen.getByRole("link", { name: /open report/i })).toBeInTheDocument();

    // Pill-style project nav with count badges, rendered in page markup.
    const nav = screen.getByRole("navigation", { name: /project sections/i });
    expect(within(nav).getByRole("link", { name: "Overview" })).toHaveClass("bg-secondary", "font-medium");
    expect(within(nav).getByRole("link", { name: /tasks/i })).toHaveTextContent("3");
    expect(within(nav).getByRole("link", { name: /evidence/i })).toHaveTextContent("5");
    expect(within(nav).getByRole("link", { name: /findings/i })).toHaveTextContent("1");

    // Scope readiness checklist (scope is incomplete in this fixture).
    const scope = screen.getByRole("region", { name: /scope readiness/i });
    expect(scope).toHaveClass("rounded-lg", "border-warning/30");
    expect(scope).toHaveTextContent("2 / 3 项完成");
    expect(scope).toHaveTextContent("添加至少一个 in-scope 资产");
    expect(scope).toHaveTextContent("Scope notes");
    expect(scope).toHaveTextContent("Testing limits");
    expect(within(scope).getByRole("link", { name: /添加资产/ })).toHaveAttribute(
      "href",
      "/projects/project-1/scope",
    );

    // Stat cards link to their sections; the Tasks card carries a running chip.
    const tasksCard = screen.getByRole("link", { name: /view 3 tasks/i });
    expect(tasksCard).toHaveClass("rounded-lg", "border", "bg-card");
    expect(within(tasksCard).getByText("1 运行中")).toBeInTheDocument();
    expect(within(tasksCard).getByText(/最近：Enumerate api\.acme\.example · 12m ago/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /view 8 facts/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /view 1 finding/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /view 5 evidence items/i })).toBeInTheDocument();

    // Recent activity lists recent tasks with status text and relative time.
    const activity = screen.getByText("Recent activity").closest("section");
    expect(activity).not.toBeNull();
    expect(activity!).toHaveTextContent("Enumerate api.acme.example — running");
    expect(activity!).toHaveTextContent("12m ago");
    expect(activity!).toHaveTextContent("Port scan 203.0.113.10 — completed");
    expect(activity!).toHaveTextContent("2h ago");
    expect(within(activity!).getByRole("link", { name: "查看全部" })).toHaveAttribute(
      "href",
      "/projects/project-1/tasks",
    );

    // Current work summarizes open work and links into the Blackboard.
    const currentWork = screen.getByText("Current work").closest("section");
    expect(currentWork).not.toBeNull();
    expect(currentWork!).toHaveTextContent("Exploration objectives");
    expect(currentWork!).toHaveTextContent("1 开放");
    expect(currentWork!).toHaveTextContent("1 进行中");
    expect(currentWork!).toHaveTextContent("1 current");
    expect(within(currentWork!).getByRole("link", { name: /打开 Blackboard/ })).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard",
    );

    expect(screen.getByText("Pentest Project")).toBeInTheDocument();
  });

  it("previews and confirms a blocker-free Project Kind Conversion", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.endsWith("/dashboard")) return new Response(JSON.stringify(dashboard));
        if (url.endsWith("/kind-conversion/preview")) {
          return new Response(JSON.stringify({
            project_id: "project-1",
            current_kind: "pentest",
            target_kind: "ctf_challenge",
            ready: true,
            blockers: [],
          }));
        }
        if (url.endsWith("/kind-conversion") && init?.method === "POST") {
          return new Response(JSON.stringify({ ...project, kind: "ctf_challenge" }));
        }
        return new Response(JSON.stringify(project));
      }),
    );

    renderPage();
    await user.click(await screen.findByRole("button", { name: /change project kind/i }));
    expect(await screen.findByText(/conversion is ready/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /confirm conversion/i }));
    expect(await screen.findByText("CTF Challenge Project")).toBeInTheDocument();
  });
});
