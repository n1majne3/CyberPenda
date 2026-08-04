import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkspaceSidebar } from "./WorkspaceSidebar";
import { ThemeProvider } from "./ThemeProvider";

// jsdom's document.visibilityState is "visible" by default and has no setter;
// tests that exercise the Page Visibility API override it via defineProperty and
// dispatch the real event the hook listens to.
function setDocumentVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

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
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
});

describe("WorkspaceSidebar", () => {
  it("shows durable Runtime failures instead of collapsing them into offline", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [project("project-active", "Project", "2026-08-01T00:00:00Z")] });
        if (url === "/api/projects/project-active/tasks") {
          return response({
            tasks: [{ ...task("task-failed", "Failed task", "2026-08-01T00:00:00Z", { liveness: "offline" }), status: "failed" }],
          });
        }
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [{
              ...session("session-failed", "Failed session", "2026-08-01T00:00:00Z"),
              runtime_activity: { liveness: "offline" },
              latest_continuation: { status: "failed" },
            }],
          });
        }
        return response({});
      }),
    );

    const user = userEvent.setup();
    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await screen.findByRole("region", { name: /non-project/i });
    expect(within(nonProject).getByRole("link", { name: /failed session.*runtime failure/i })).toBeInTheDocument();
    const projectRegion = screen.getByRole("region", { name: /open project project dashboard/i });
    await user.click(within(projectRegion).getByRole("button", { name: /expand project/i }));
    expect(await within(projectRegion).findByRole("link", { name: /failed task.*runtime failure/i })).toBeInTheDocument();
  });

  it("does not force an archived current Session into the open Non-project tree", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        if (url === "/api/sessions/session-archived") {
          return response({
            ...session("session-archived", "Archived session", "2026-08-01T00:00:00Z"),
            lifecycle: "archived",
          });
        }
        return response({});
      }),
    );
    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions/session-archived"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );
    await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/sessions/session-archived", expect.anything()));
    expect(screen.queryByRole("link", { name: /archived session/i })).not.toBeInTheDocument();
  });

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
        if (url === "/api/sessions?limit=5") return response({ sessions });
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
        if (url === "/api/sessions?limit=5") return response({ sessions: [session("session-1", "Session one", "2026-08-01T00:00:00Z")] });
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
    // The Non-project header mirrors Projects: a nav link plus an inline
    // "+ New session" action, with no section-level collapse control.
    expect(within(nonProject).queryByRole("button", { name: /collapse non-project/i })).toBeNull();
    expect(within(nonProject).getByRole("link", { name: /non-project/i })).toHaveAttribute("href", "/sessions");
    expect(within(nonProject).getByRole("link", { name: /new session/i })).toHaveAttribute(
      "href",
      "/sessions#new-session",
    );

    const projectRegion = await screen.findByRole("region", { name: /project one/i });
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

  it("keeps the Non-project and Projects section headers visually aligned", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProjectLink = await screen.findByRole("link", { name: /non-project/i });
    const projectsLink = screen.getByRole("link", { name: /projects/i });
    // Both section headers share the same idle color tokens so the sidebar
    // reads as one system instead of two differently-toned regions.
    for (const className of [
      "text-muted-foreground",
      "hover:text-sidebar-accent-foreground",
      "hover:bg-sidebar-accent/70",
    ]) {
      expect(nonProjectLink).toHaveClass(className);
      expect(projectsLink).toHaveClass(className);
    }
  });

  it("promotes busy sessions and force-includes the current session within the five-item cap", async () => {
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
        if (url === "/api/sessions?limit=5") return response({ sessions });
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
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
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
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
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

  // Polling-gated behavior. These are the only tests in this file that use fake
  // timers: the gated poll is the feature under test, and advancing the clock is
  // the clearest way to assert "no fetch happens while idle/hidden". They are
  // isolated to the `it` scope so the rest of the suite keeps using real timers.
  // We avoid waitFor here because it polls on real timers, which under fake
  // timers lets wall-clock time leak and fire the very intervals under test.
  it("slowly backs off when no work is active and resumes fast polling when work starts", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [project("project-1", "Project one", "2026-08-01T00:00:00Z")] });
        if (url === "/api/projects/project-1/tasks") return response({ tasks: [task("task-idle", "Idle task", "2026-08-01T00:00:00Z")] });
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        return response({});
      });
      vi.stubGlobal("fetch", fetchMock);

      render(
        <ThemeProvider>
          <MemoryRouter>
            <WorkspaceSidebar />
          </MemoryRouter>
        </ThemeProvider>,
      );

      // Let the eager mount load resolve (microtasks only, no clock advance).
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.getByRole("region", { name: /project one/i })).toBeInTheDocument();
      const callsAfterMount = fetchMock.mock.calls.length;

      // Everything is idle (completed task, no busy runtime) → no poll should
      // land inside the fast 2s window.
      await act(async () => {
        vi.advanceTimersByTimeAsync(2500);
      });
      expect(fetchMock.mock.calls.length).toBe(callsAfterMount);

      // A poll only fires once the slow backoff interval (~30s) elapses.
      await act(async () => {
        vi.advanceTimersByTimeAsync(30000);
      });
      expect(fetchMock.mock.calls.length).toBeGreaterThan(callsAfterMount);
    } finally {
      vi.useRealTimers();
    }
  });

  it("suspends polling entirely while the tab is hidden and resumes when it returns", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/projects") return response({ projects: [project("project-1", "Project one", "2026-08-01T00:00:00Z")] });
        if (url === "/api/projects/project-1/tasks") {
          // A busy task would normally drive fast polling; the only thing that
          // should gate it here is tab visibility.
          return response({ tasks: [task("task-busy", "Busy task", "2026-08-01T00:00:00Z", { liveness: "live", turn_activity: "busy" })] });
        }
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        return response({});
      });
      vi.stubGlobal("fetch", fetchMock);

      render(
        <ThemeProvider>
          <MemoryRouter>
            <WorkspaceSidebar />
          </MemoryRouter>
        </ThemeProvider>,
      );

      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.getByRole("region", { name: /project one/i })).toBeInTheDocument();
      const callsAfterMount = fetchMock.mock.calls.length;

      await act(async () => {
        setDocumentVisibility("hidden");
        await Promise.resolve();
      });

      // No matter how much time passes while hidden, no poll fires — even though
      // work is active (which would otherwise poll every 2s).
      await act(async () => {
        vi.advanceTimersByTimeAsync(60000);
      });
      expect(fetchMock.mock.calls.length).toBe(callsAfterMount);

      act(() => setDocumentVisibility("visible"));
      await act(async () => {
        vi.advanceTimersByTimeAsync(2500);
      });
      expect(fetchMock.mock.calls.length).toBeGreaterThan(callsAfterMount);
    } finally {
      vi.useRealTimers();
    }
  });
});
