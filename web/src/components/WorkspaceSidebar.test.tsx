import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkspaceSidebar } from "./WorkspaceSidebar";
import { ThemeProvider } from "./ThemeProvider";

const project = (id: string, name: string, updatedAt: string) => ({
  id,
  name,
  description: "",
  scope: {},
  defaults: {},
  created_at: "2026-07-01T00:00:00Z",
  updated_at: updatedAt,
});

const session = (id: string, title: string, lastActivityAt: string) => ({
  id,
  title,
  lifecycle: "open",
  created_at: lastActivityAt,
  updated_at: lastActivityAt,
  last_activity_at: lastActivityAt,
});

const task = (id: string, goal: string, updatedAt: string, runtimeActivity?: object) => ({
  id,
  project_id: "project-active",
  goal,
  status: "completed",
  runner: "sandbox",
  runtime_profile_id: "profile-1",
  run_controls: {},
  scope_snapshot: {},
  created_at: updatedAt,
  updated_at: updatedAt,
  ...(runtimeActivity ? { runtime_activity: runtimeActivity } : {}),
});

function response(body: unknown) {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe("WorkspaceSidebar", () => {
  it("shows recent work, promotes busy tasks, and keeps current owners visible", async () => {
    const activeTasks = [
      task("task-busy", "Busy task", "2026-07-20T00:00:00Z", { liveness: "live", turn_activity: "busy" }),
      task("task-recent-1", "Recent task 1", "2026-07-31T00:00:00Z"),
      task("task-recent-2", "Recent task 2", "2026-07-30T00:00:00Z"),
      task("task-recent-3", "Recent task 3", "2026-07-29T00:00:00Z"),
      task("task-recent-4", "Recent task 4", "2026-07-28T00:00:00Z"),
      task("task-recent-5", "Recent task 5", "2026-07-27T00:00:00Z"),
      task("task-current", "Current task", "2026-07-01T00:00:00Z"),
    ];
    const sessions = [
      session("session-1", "Session 1", "2026-07-31T00:00:00Z"),
      session("session-2", "Session 2", "2026-07-30T00:00:00Z"),
      session("session-3", "Session 3", "2026-07-29T00:00:00Z"),
      session("session-4", "Session 4", "2026-07-28T00:00:00Z"),
      session("session-5", "Session 5", "2026-07-27T00:00:00Z"),
      session("session-current", "Current session", "2026-07-01T00:00:00Z"),
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [project("project-active", "Active project", "2026-07-01T00:00:00Z")] });
        if (url === "/api/projects/project-active/tasks") return response({ tasks: activeTasks });
        if (url === "/api/sessions") return response({ sessions });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/projects/project-active/tasks/task-current"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    expect(await screen.findByRole("link", { name: /new session/i })).toHaveAttribute("href", "/sessions#new-session");
    expect(screen.getByRole("link", { name: /non-project/i })).toHaveAttribute("href", "/sessions");
    expect(screen.getByRole("link", { name: /projects/i })).toHaveAttribute("href", "/");
    expect(screen.getByRole("link", { name: /new project/i })).toHaveAttribute("href", "/?new=1");
    expect(screen.getByRole("link", { name: /active project project dashboard/i })).toHaveAttribute(
      "href",
      "/projects/project-active",
    );

    const projectRegion = screen.getByRole("region", { name: /active project/i });
    const taskLinks = await within(projectRegion).findAllByRole("link", { name: /task conversation/i });
    expect(taskLinks.map((link) => link.textContent?.trim())).toEqual([
      "Busy task task conversation",
      "Recent task 1 task conversation",
      "Recent task 2 task conversation",
      "Recent task 3 task conversation",
      "Recent task 4 task conversation",
      "Current task task conversation",
    ]);
    expect(within(projectRegion).getByRole("link", { name: /current task/i })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(within(projectRegion).getByRole("img", { name: /runtime busy/i })).toBeInTheDocument();
    expect(within(projectRegion).getByRole("link", { name: /show more/i })).toHaveAttribute(
      "href",
      "/projects/project-active/tasks",
    );
  });

  it("keeps disclosure controls separate from navigation and exposes accessible menus", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [project("project-1", "Project one", "2026-08-01T00:00:00Z")] });
        if (url === "/api/projects/project-1/tasks") return response({ tasks: [] });
        if (url === "/api/sessions") return response({ sessions: [session("session-1", "Session one", "2026-08-01T00:00:00Z")] });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions/session-1"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = screen.getByRole("region", { name: /non-project/i });
    const nonProjectDisclosure = within(nonProject).getByRole("button", { name: /collapse non-project/i });
    expect(nonProjectDisclosure).toHaveAttribute("aria-expanded", "true");
    await user.click(nonProjectDisclosure);
    expect(nonProjectDisclosure).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("link", { name: /non-project/i })).toHaveAttribute("href", "/sessions");
    await user.click(screen.getByRole("button", { name: /expand non-project/i }));

    const projectRegion = screen.getByRole("region", { name: /project one/i });
    const projectDisclosure = within(projectRegion).getByRole("button", { name: /expand project one/i });
    expect(projectDisclosure).toHaveAttribute("aria-controls", "project-tasks-project-1");
    await user.click(projectDisclosure);
    expect(projectDisclosure).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: /project one project dashboard/i })).toHaveAttribute(
      "href",
      "/projects/project-1",
    );

    const sessionActions = within(nonProject).getByRole("button", { name: /more actions for session one/i });
    await user.click(sessionActions);
    const menu = within(nonProject).getByRole("menu", { name: /session one actions/i });
    expect(within(menu).getByRole("menuitem", { name: /rename/i })).toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: /archive/i })).toBeInTheDocument();
    expect(within(menu).queryByRole("menuitem", { name: /delete|restore/i })).not.toBeInTheDocument();
  });

  it("promotes busy sessions and force-includes the current session outside the recent five", async () => {
    const sessions = [
      session("session-idle-1", "Idle session 1", "2026-07-31T00:00:00Z"),
      session("session-idle-2", "Idle session 2", "2026-07-30T00:00:00Z"),
      session("session-idle-3", "Idle session 3", "2026-07-29T00:00:00Z"),
      session("session-idle-4", "Idle session 4", "2026-07-28T00:00:00Z"),
      session("session-idle-5", "Idle session 5", "2026-07-27T00:00:00Z"),
      {
        ...session("session-busy", "Busy session", "2026-07-01T00:00:00Z"),
        runtime_activity: { liveness: "live", turn_activity: "busy" },
      },
      session("session-current", "Current session", "2026-06-01T00:00:00Z"),
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [] });
        if (url === "/api/sessions") return response({ sessions });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions/session-current"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await screen.findByRole("region", { name: /non-project/i });
    const sessionLinks = within(nonProject).getAllByRole("link", { name: /session conversation/i });
    expect(sessionLinks.map((link) => link.textContent?.trim())).toEqual([
      "Busy session session conversation",
      "Idle session 1 session conversation",
      "Idle session 2 session conversation",
      "Idle session 3 session conversation",
      "Idle session 4 session conversation",
      "Current session session conversation",
    ]);
    expect(within(nonProject).getByRole("img", { name: "Runtime busy" })).toBeInTheDocument();
  });

  it("orders projects by the latest project-or-task activity", async () => {
    const olderProjectWithRecentTask = project("project-old", "Older project", "2026-07-01T00:00:00Z");
    const newerProjectWithoutRecentTask = project("project-new", "Newer project", "2026-07-31T00:00:00Z");
    const recentTask = {
      ...task("task-recent", "Recent task", "2026-08-01T00:00:00Z"),
      project_id: olderProjectWithRecentTask.id,
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [newerProjectWithoutRecentTask, olderProjectWithRecentTask] });
        if (url === "/api/projects/project-old/tasks") return response({ tasks: [recentTask] });
        if (url === "/api/projects/project-new/tasks") return response({ tasks: [] });
        if (url === "/api/sessions") return response({ sessions: [] });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    await waitFor(() => {
      expect(screen.getAllByRole("link", { name: /project dashboard/i }).map((link) => link.textContent?.trim())).toEqual([
        "Older project project dashboard",
        "Newer project project dashboard",
      ]);
    });
  });

  it("scopes loading and error states to their affected work groups", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return Promise.reject(new Error("projects unavailable"));
        if (url === "/api/sessions") return response({ sessions: [] });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await screen.findByRole("region", { name: /non-project/i });
    expect(within(nonProject).getByText(/no open sessions/i)).toBeInTheDocument();
    const projects = screen.getByRole("region", { name: /projects/i });
    expect(within(projects).getByRole("alert")).toHaveTextContent("projects unavailable");
    expect(within(nonProject).queryByRole("alert")).not.toBeInTheDocument();
  });
});
