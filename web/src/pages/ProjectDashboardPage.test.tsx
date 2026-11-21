import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";
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
      runtime_activity: { liveness: "live", turn_activity: "busy" },
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
  function conversionApi(kind = "pentest", fail?: "preview" | "confirm", blocked = false) {
    const target = kind === "pentest" ? "ctf_challenge" : "pentest";
    let failed = false;
    let checked = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const preview = url.endsWith("/kind-conversion/preview");
      const confirm = url.endsWith("/kind-conversion");
      if ((preview && fail === "preview" || confirm && fail === "confirm") && !failed) {
        failed = true;
        return new Response(JSON.stringify({ error: "Please try again" }), { status: 503 });
      }
      if (preview) {
        const blockers = blocked && !checked ? [
          { code: "active_tasks", count: 1 },
          { code: "incompatible_findings", count: 2 },
          { code: "incompatible_solutions", count: 3 },
        ] : [];
        checked = true;
        return new Response(JSON.stringify({ current_kind: kind, target_kind: target, ready: !blockers.length, blockers }));
      }
      if (confirm) {
        expect(JSON.parse(String(init?.body))).toEqual({ target_kind: target, confirm: true });
        return new Response(JSON.stringify({ ...project, kind: target }));
      }
      if (url.endsWith("/dashboard")) return new Response(JSON.stringify(dashboard));
      if (url.endsWith("/tasks")) return new Response(JSON.stringify(tasks));
      return new Response(JSON.stringify({ ...project, kind }));
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  it.each(["preview", "confirm"] as const)("keeps the Dashboard and recovers from a %s failure", async (failure) => {
    const user = userEvent.setup();
    conversionApi("pentest", failure);
    renderPage();
    await user.click(await screen.findByText("Project settings"));
    await user.click(screen.getByRole("button", { name: /change project kind/i }));
    if (failure === "confirm") await user.click(screen.getByRole("button", { name: /confirm conversion to/i }));
    const card = screen.getByRole("region", { name: "Project kind" });
    expect(within(card).getByRole("alert")).toHaveTextContent("Please try again");
    expect(screen.getByRole("heading", { name: project.name })).toBeVisible();
    expect(screen.queryByText("Couldn't load dashboard")).not.toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: /retry/i }));
    if (failure === "preview") await user.click(screen.getByRole("button", { name: /confirm conversion to/i }));
    expect(await screen.findByRole("status")).toHaveTextContent("Project Kind changed to CTF Challenge.");
  });

  it("confirms conversion from CTF Challenge to Pentest", async () => {
    const user = userEvent.setup();
    conversionApi("ctf_challenge");
    renderPage();
    await user.click(await screen.findByText("Project settings"));
    await user.click(screen.getByRole("button", { name: /change project kind/i }));
    expect(screen.getByText("CTF Challenge → Pentest")).toBeVisible();
    expect(screen.getByText(/Only Project Kind changes/)).toHaveTextContent("Historical Task Types stay unchanged. Findings and Solutions are not converted into each other.");
    await user.click(screen.getByRole("button", { name: "Confirm conversion to Pentest" }));
    expect(await screen.findByText("Pentest Project")).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent("Project Kind changed to Pentest.");
  });

  it("links each blocker and re-checks before confirmation", async () => {
    const user = userEvent.setup();
    conversionApi("pentest", undefined, true);
    renderPage();
    await user.click(await screen.findByText("Project settings"));
    await user.click(screen.getByRole("button", { name: /change project kind/i }));
    expect(screen.queryByRole("button", { name: /confirm conversion/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Review Tasks" })).toHaveAttribute("href", "/projects/project-1/tasks");
    expect(screen.getByRole("link", { name: "Review Findings" })).toHaveAttribute("href", "/projects/project-1/findings");
    expect(screen.getByRole("link", { name: "Review Solutions in Blackboard" })).toHaveAttribute("href", "/projects/project-1/blackboard");
    await user.click(screen.getByRole("button", { name: "Re-check" }));
    expect(await screen.findByRole("button", { name: "Confirm conversion to CTF Challenge" })).toBeEnabled();
  });

  it("cancels with the keyboard without a conversion request", async () => {
    const user = userEvent.setup();
    const fetchMock = conversionApi();
    renderPage();
    await user.click(await screen.findByText("Project settings"));
    await user.click(screen.getByRole("button", { name: /change project kind/i }));
    const cancel = screen.getByRole("button", { name: "Cancel" });
    cancel.focus();
    await user.keyboard("{Enter}");
    expect(screen.queryByText("Pentest → CTF Challenge")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /change project kind/i })).toHaveFocus();
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/kind-conversion"))).toHaveLength(0);
  });

  it("shows request progress and prevents double submission", async () => {
    const user = userEvent.setup();
    const normalFetch = conversionApi();
    let resolveRequest: (response: Response) => void = () => {};
    const pendingFetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
      String(input).includes("/kind-conversion")
        ? new Promise<Response>((resolve) => { resolveRequest = resolve; })
        : normalFetch(input, init));
    vi.stubGlobal("fetch", pendingFetch);
    renderPage();
    await user.click(await screen.findByText("Project settings"));
    await user.dblClick(screen.getByRole("button", { name: /change project kind/i }));
    expect(screen.getByRole("status")).toHaveTextContent("Checking…");
    expect(pendingFetch.mock.calls.filter(([url]) => String(url).includes("/kind-conversion"))).toHaveLength(1);
    resolveRequest(new Response(JSON.stringify({ current_kind: "pentest", target_kind: "ctf_challenge", ready: true, blockers: [] })));
    const confirm = await screen.findByRole("button", { name: /confirm conversion to/i });
    await user.dblClick(confirm);
    expect(screen.getByRole("status")).toHaveTextContent("Converting…");
    expect(confirm).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(pendingFetch.mock.calls.filter(([url]) => String(url).endsWith("/kind-conversion"))).toHaveLength(1);
    resolveRequest(new Response(JSON.stringify({ ...project, kind: "ctf_challenge" })));
    expect(await screen.findByText("CTF Challenge Project")).toBeVisible();
  });

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
    expect(screen.getByTestId("project-page-shell-header").parentElement).toHaveClass("mx-auto", "max-w-6xl", "px-6", "lg:px-8");
    expect(screen.getByRole("link", { name: /All projects/i })).toHaveAttribute("href", "/");
    expect(screen.getByRole("link", { name: /launch task/i })).toHaveClass("rounded-md", "bg-primary");
    expect(screen.getByRole("link", { name: /open report/i })).toHaveClass("border-border", "bg-background");
    expect(screen.getByRole("link", { name: "Blackboard" })).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard",
    );

    const nav = screen.getByRole("navigation", { name: /project sections/i });
    expect(within(nav).getByRole("link", { name: /tasks/i })).toHaveTextContent("3");
    expect(within(nav).getByRole("link", { name: /evidence/i })).toHaveTextContent("5");
    expect(within(nav).getByRole("link", { name: /findings/i })).toHaveTextContent("1");

    const scope = screen.getByRole("region", { name: /scope readiness/i });
    expect(scope).toHaveClass("rounded-lg", "border-warning/30");
    expect(scope).toHaveTextContent("2 / 3 complete");
    expect(scope).toHaveTextContent("Add at least one in-scope asset");
    expect(scope).toHaveTextContent("Scope notes");
    expect(scope).toHaveTextContent("Testing limits");
    expect(within(scope).getByRole("link", { name: /add asset/i })).toHaveAttribute(
      "href",
      "/projects/project-1/scope",
    );

    const totals = screen.getByLabelText("Project totals");
    const tasksTotal = within(totals).getByRole("link", { name: /view 3 tasks/i });
    expect(within(tasksTotal).getByText("1 running")).toBeVisible();
    expect(screen.getByRole("link", { name: /view 8 facts/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /view 1 finding/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /view 5 evidence items/i })).toBeInTheDocument();
    const activeTasks = screen.getByRole("region", { name: "Active Tasks" });
    expect(within(activeTasks).getByRole("link", { name: /Enumerate api.acme.example running/i })).toHaveAttribute("href", "/projects/project-1/tasks/task-1");
    expect(within(activeTasks).queryByText("Exploration objectives")).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Needs your attention" })).toHaveTextContent("No Tasks need attention.");
    const activity = screen.getByText("Recent activity").closest("details");
    expect(activity).not.toHaveAttribute("open");
    await userEvent.click(screen.getByText("Recent activity"));
    expect(activity).toHaveTextContent("Port scan 203.0.113.10 — Completed");
    expect(activity).toHaveTextContent("Fingerprint cdn.acme.example — Stopped");
    expect(screen.getByRole("link", { name: /open Blackboard/i })).toHaveAttribute("href", "/projects/project-1/blackboard");

    expect(screen.getByText("Pentest Project")).toHaveClass("border-signal/25", "bg-signal/10", "text-signal");
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
    await user.click(await screen.findByText("Project settings"));
    await user.click(screen.getByRole("button", { name: /change project kind/i }));
    expect(await screen.findByText(/conversion is ready/i)).toBeInTheDocument();
    expect(screen.getByText("Pentest → CTF Challenge")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /confirm conversion to CTF Challenge/i }));
    expect(await screen.findByText("CTF Challenge Project")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Project Kind changed to CTF Challenge.");
    // The report route does not exist for CTF projects, so the footer button hides.
    expect(screen.queryByRole("link", { name: /open report/i })).not.toBeInTheDocument();
  });

  it("keeps the latest Project when a previous Project read finishes late", async () => {
    let releaseFirst = () => {};
    const firstGate = new Promise<void>((resolve) => { releaseFirst = resolve; });
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    const second = { ...project, id: "project-2", name: "Second Project" };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/api/projects/project-1")) {
        await firstGate;
        if (url.endsWith("/dashboard")) return json(dashboard);
        if (url.endsWith("/tasks")) return json(tasks);
        return json(project);
      }
      if (url.endsWith("/api/projects/project-2/dashboard")) return json({ ...dashboard, project_id: "project-2", name: "Second Project" });
      if (url.endsWith("/api/projects/project-2/tasks")) return json({ tasks: [] });
      return json(second);
    }));

    render(
      <MemoryRouter initialEntries={["/projects/project-1"]}>
        <Link to="/projects/project-2">Open second Project</Link>
        <Routes>
          <Route path="/projects/:projectId" element={<ProjectDashboardPage />} />
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("link", { name: "Open second Project" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Second Project" })).toBeInTheDocument();
    releaseFirst();
    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByText("Acme External")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Second Project" })).toBeInTheDocument();
  });
});
