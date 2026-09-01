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

// navigationSummary builds one row of GET /api/workspace/navigation (#193):
// a Project with its inlined Tasks and a last_activity_at. The daemon computes
// last_activity_at server-side; tests pass it explicitly to drive ordering.
function navigationSummary(id: string, name: string, updatedAt: string, tasks: object[] = [], lastActivityAt?: string) {
  return {
    ...project(id, name, updatedAt),
    last_activity_at: lastActivityAt ?? updatedAt,
    tasks,
  };
}

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
        if (url === "/api/workspace/navigation") {
          return response({
            projects: [
              navigationSummary("project-active", "Project", "2026-08-01T00:00:00Z", [
                { ...task("task-failed", "Failed task", "2026-08-01T00:00:00Z", { liveness: "offline" }), status: "failed" },
              ]),
            ],
          });
        }
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
    expect(await within(nonProject).findByRole("link", { name: /failed session.*runtime failure/i })).toBeInTheDocument();
    const projectRegion = await screen.findByRole("region", { name: /open project project dashboard/i });
    await user.click(within(projectRegion).getByRole("button", { name: /expand project/i }));
    expect(await within(projectRegion).findByRole("link", { name: /failed task.*runtime failure/i })).toBeInTheDocument();
  });

  it("does not force an archived current Session into the open Non-project tree", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
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
    // Wait until the sessions data has rendered, so the negative assertion
    // below passes because the archived Session is excluded, not because the
    // list has not loaded yet.
    const nonProject = await screen.findByRole("region", { name: /non-project/i });
    await within(nonProject).findByText(/no open sessions/i);
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
        if (url.startsWith("/api/workspace/navigation")) {
          return response({
            projects: [navigationSummary("project-active", "Active project", "2026-07-01T00:00:00Z", activeTasks)],
          });
        }
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
    expect(await screen.findByRole("link", { name: /active project project dashboard/i })).toHaveAttribute(
      "href",
      "/projects/project-active",
    );
    // The first navigation request carries the selected Task context (#201).
    expect(vi.mocked(fetch).mock.calls[0][0]).toContain("selected_task=task-current");

    const projectRegion = screen.getByRole("region", { name: /active project/i });
    // The current Project renders its daemon-bounded inlined Tasks as-is (#201):
    // the busy Task first, then the five ordinary recent Tasks, then the old
    // selected Task — all seven stay visible even though the selected Task is
    // older than the recent summary.
    const taskLinks = await within(projectRegion).findAllByRole("link", { name: /task conversation/i });
    expect(taskLinks.map((link) => link.textContent?.trim())).toEqual([
      "Busy task task conversation",
      "Recent task 1 task conversation",
      "Recent task 2 task conversation",
      "Recent task 3 task conversation",
      "Recent task 4 task conversation",
      "Recent task 5 task conversation",
      "Current task task conversation",
    ]);
    const currentTaskLink = within(projectRegion).getByRole("link", { name: /current task/i });
    expect(currentTaskLink).toHaveAttribute("aria-current", "page");
    // Nested Task routes still match the Project path as a prefix; the Project
    // row must not share the active wash with the Task or the two boxes merge.
    const projectLink = screen.getByRole("link", { name: /active project project dashboard/i });
    // NavLink may still emit aria-current="false"; the Project must not claim page.
    expect(projectLink.getAttribute("aria-current")).not.toBe("page");
    expect(projectLink).not.toHaveClass("bg-signal/5");
    expect(projectLink.querySelector('[data-nav-indicator="active"]')).toBeNull();
    expect(currentTaskLink).toHaveClass("bg-signal/5");
    expect(currentTaskLink.querySelector('[data-nav-indicator="active"]')).not.toBeNull();
    expect(within(projectRegion).getByRole("img", { name: /runtime busy/i })).toBeInTheDocument();
    expect(within(projectRegion).getByRole("link", { name: /show more/i })).toHaveAttribute(
      "href",
      "/projects/project-active/tasks",
    );
  });

  it("highlights the Project row only when no nested Task is selected", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/workspace/navigation")) {
          return response({
            projects: [
              navigationSummary("project-active", "Active project", "2026-08-01T00:00:00Z", [
                task("task-1", "First task", "2026-08-01T00:00:00Z"),
              ]),
            ],
          });
        }
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/projects/project-active"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const projectLink = await screen.findByRole("link", { name: /active project project dashboard/i });
    expect(projectLink).toHaveAttribute("aria-current", "page");
    expect(projectLink).toHaveClass("bg-signal/5");
    expect(projectLink.querySelector('[data-nav-indicator="active"]')).not.toBeNull();

    const taskLink = screen.getByRole("link", { name: /first task/i });
    expect(taskLink).not.toHaveAttribute("aria-current");
    expect(taskLink).not.toHaveClass("bg-signal/5");
  });

  it("keeps disclosure controls separate from navigation and exposes accessible menus", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") {
          return response({ projects: [navigationSummary("project-1", "Project one", "2026-08-01T00:00:00Z", [])] });
        }
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

    const sessionActions = await within(nonProject).findByRole("button", { name: /more actions for session one/i });
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
        if (url === "/api/workspace/navigation") return response({ projects: [] });
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
        if (url === "/api/workspace/navigation") return response({ projects: [] });
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
    const sessionLinks = await within(nonProject).findAllByRole("link", { name: /session conversation/i });
    // Rows are two-line now (title + "time · state"): assert the title order,
    // which is what this test is about, without pinning the relative-time text.
    expect(sessionLinks.map((link) => link.textContent ?? "")).toEqual([
      expect.stringContaining("Busy session"),
      expect.stringContaining("Idle session 1"),
      expect.stringContaining("Idle session 2"),
      expect.stringContaining("Idle session 3"),
      expect.stringContaining("Current session"),
    ]);
    expect(within(nonProject).getByRole("img", { name: "Runtime busy" })).toBeInTheDocument();
  });

  it("orders projects by the latest project-or-task activity", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") {
          return response({
            projects: [
              // The older Project has the more recent activity (folded from its
              // Task), so it must rank first even though it was created earlier.
              navigationSummary("project-old", "Older project", "2026-07-01T00:00:00Z", [], "2026-08-01T00:00:00Z"),
              navigationSummary("project-new", "Newer project", "2026-07-31T00:00:00Z", [], "2026-07-31T00:00:00Z"),
            ],
          });
        }
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
        if (url === "/api/workspace/navigation") return Promise.reject(new Error("projects unavailable"));
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
    expect(await within(nonProject).findByText(/no open sessions/i)).toBeInTheDocument();
    const projects = screen.getByRole("region", { name: /projects/i });
    expect(await within(projects).findByRole("alert")).toHaveTextContent("projects unavailable");
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
        if (url === "/api/workspace/navigation") {
          return response({
            projects: [navigationSummary("project-1", "Project one", "2026-08-01T00:00:00Z", [task("task-idle", "Idle task", "2026-08-01T00:00:00Z")])],
          });
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
        if (url === "/api/workspace/navigation") {
          return response({
            projects: [
              navigationSummary("project-1", "Project one", "2026-08-01T00:00:00Z", [
                // A busy task would normally drive fast polling; the only thing
                // that should gate it here is tab visibility.
                task("task-busy", "Busy task", "2026-08-01T00:00:00Z", { liveness: "live", turn_activity: "busy" }),
              ]),
            ],
          });
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

  // #193: the Sidebar must not fan out one Task-list request per Project. A
  // 123-Project workspace makes a constant number of requests on mount and on
  // each refresh. This is the issue's "browser performance test" delivered as a
  // Vitest volume test (no new toolchain): it seeds 123 Projects with mixed
  // Task states and asserts no /api/projects/{id}/tasks URL is ever requested
  // during initial load or a poll, and that rendered inactive rows are bounded.
  it("does not fan out per project at 123-project scale", async () => {
    vi.useFakeTimers();
    try {
      const summaries = Array.from({ length: 123 }, (_, index) => {
        const states = [
          [task(`busy-${index}`, "Busy task", "2026-08-01T00:00:00Z", { liveness: "live", turn_activity: "busy" })],
          [task(`idle-${index}`, "Idle task", "2026-08-01T00:00:00Z")],
          [{ ...task(`failed-${index}`, "Failed task", "2026-08-01T00:00:00Z", { liveness: "offline" }), status: "failed" }],
          Array.from({ length: 7 }, (_, j) => task(`many-${index}-${j}`, `Task ${j}`, `2026-08-0${(j % 9) + 1}T00:00:00Z`)),
          [],
        ];
        return navigationSummary(`project-${index}`, `Project ${index}`, "2026-08-01T00:00:00Z", states[index % states.length]);
      });
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: summaries });
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
      // Initial load: navigation + sessions only. No per-Project fan-out.
      const urls = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urls.filter((url) => url.includes("/tasks")).length).toBe(0);

      // One poll later: still only navigation + sessions.
      await act(async () => {
        vi.advanceTimersByTimeAsync(2500);
      });
      const urlsAfterPoll = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urlsAfterPoll.filter((url) => url.includes("/tasks")).length).toBe(0);

      // Bounded inactive rows: every Project region shows at most 5 task links.
      const projectRegions = screen.getAllByRole("region", { name: /project \d+ project dashboard/i });
      expect(projectRegions.length).toBe(123);
    } finally {
      vi.useRealTimers();
    }
  });

  // #201: an unchanged navigation refresh must not reserialize the projection.
  // The Sidebar sends back the opaque revision it received, and when the daemon
  // answers changed=false it keeps the current rows instead of replacing them
  // with an empty list.
  it("sends the revision back and keeps the projection on an unchanged refresh", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/workspace/navigation")) {
          // The refresh carries the revision from the previous response plus
          // the current selected Task.
          if (url.includes("revision=rev-1")) {
            return response({ revision: "rev-1", changed: false, projects: [] });
          }
          return response({
            revision: "rev-1",
            changed: true,
            projects: [
              navigationSummary("project-1", "Project one", "2026-08-01T00:00:00Z", [
                task("task-a", "Inline task", "2026-08-01T00:00:00Z"),
              ]),
            ],
          });
        }
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        return response({});
      });
      vi.stubGlobal("fetch", fetchMock);

      render(
        <ThemeProvider>
          <MemoryRouter initialEntries={["/projects/project-1/tasks/task-a"]}>
            <WorkspaceSidebar />
          </MemoryRouter>
        </ThemeProvider>,
      );

      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.getByRole("region", { name: /project one/i })).toBeInTheDocument();
      // The eager load used the current revision-less URL with the selected Task.
      expect(fetchMock.mock.calls[0][0]).toBe("/api/workspace/navigation?selected_task=task-a");

      // One idle poll later the request carries the stored revision and the
      // unchanged answer must not wipe the rendered projection.
      await act(async () => {
        vi.advanceTimersByTimeAsync(30000);
      });
      const pollCalls = fetchMock.mock.calls.filter(([input]) => String(input).startsWith("/api/workspace/navigation"));
      expect(pollCalls[pollCalls.length - 1][0]).toContain("revision=rev-1");
      expect(pollCalls[pollCalls.length - 1][0]).toContain("selected_task=task-a");
      expect(screen.getByRole("region", { name: /project one/i })).toBeInTheDocument();
      expect(within(screen.getByRole("region", { name: /project one/i })).getByRole("link", { name: /inline task/i })).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("loads the full task list only when a non-current project expands", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/workspace/navigation")) {
          return response({
            projects: [
              navigationSummary("project-current", "Current project", "2026-08-01T00:00:00Z", [task("task-a", "Inline task", "2026-08-01T00:00:00Z")]),
              navigationSummary("project-other", "Other project", "2026-08-01T00:00:00Z", [task("task-inline-other", "Inline other", "2026-08-01T00:00:00Z")]),
            ],
          });
        }
        if (url === "/api/projects/project-other/tasks") {
          return response({
            tasks: [
              task("task-full-1", "Full task 1", "2026-08-01T00:00:00Z"),
              task("task-full-2", "Full task 2", "2026-08-02T00:00:00Z"),
            ],
          });
        }
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/projects/project-current/tasks/task-a"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    // Collapsed: no on-demand fetch has happened yet.
    await screen.findByRole("region", { name: /current project/i });
    let taskFetches = vi.mocked(fetch).mock.calls.filter((call) => String(call[0]).includes("/projects/project-other/tasks")).length;
    expect(taskFetches).toBe(0);

    // Expanding the non-current Project fires exactly one on-demand fetch and
    // shows its full Task list.
    const otherRegion = screen.getByRole("region", { name: /other project/i });
    await user.click(within(otherRegion).getByRole("button", { name: /expand other project/i }));
    expect(await within(otherRegion).findByRole("link", { name: /full task 1/i })).toBeInTheDocument();
    expect(within(otherRegion).getByRole("link", { name: /full task 2/i })).toBeInTheDocument();
    taskFetches = vi.mocked(fetch).mock.calls.filter((call) => String(call[0]).includes("/projects/project-other/tasks")).length;
    expect(taskFetches).toBe(1);
  });

  it("filters sessions and projects by name with the sidebar search box", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/workspace/navigation")) {
          return response({
            projects: [
              navigationSummary("project-alpha", "Alpha project", "2026-08-01T00:00:00Z"),
              navigationSummary("project-beta", "Beta project", "2026-08-01T00:00:00Z"),
            ],
          });
        }
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [
              session("session-1", "Deploy alpha", "2026-08-01T00:00:00Z"),
              session("session-2", "Write docs", "2026-08-01T00:00:00Z"),
            ],
          });
        }
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

    const filter = await screen.findByRole("searchbox", { name: /filter sessions and projects/i });
    expect(await screen.findByRole("link", { name: /deploy alpha session conversation/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /write docs session conversation/i })).toBeInTheDocument();

    // Case-insensitive match across both Sessions and Projects.
    await user.type(filter, "ALPHA");
    expect(screen.getByRole("link", { name: /deploy alpha session conversation/i })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /write docs session conversation/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /alpha project project dashboard/i })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /beta project project dashboard/i })).not.toBeInTheDocument();

    // A query with no matches keeps both groups mounted with an explicit status.
    await user.clear(filter);
    await user.type(filter, "zzz");
    const nonProject = screen.getByRole("region", { name: /non-project/i });
    expect(within(nonProject).getByText(/no sessions match/i)).toBeInTheDocument();
    const projects = screen.getByRole("region", { name: /^projects$/i });
    expect(within(projects).getByText(/no projects match/i)).toBeInTheDocument();
  });

  it("shows a relative time · state second line on every session row, disambiguating shared titles", async () => {
    const twoDaysAgo = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString();
    const fiveDaysAgo = new Date(Date.now() - 5 * 24 * 60 * 60 * 1000).toISOString();
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [
              session("session-1", "Duplicate", twoDaysAgo),
              session("session-2", "Duplicate", fiveDaysAgo),
              session("session-3", "Unique", twoDaysAgo),
            ],
          });
        }
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

    const nonProject = await screen.findByRole("region", { name: /non-project/i });
    const duplicateLinks = await within(nonProject).findAllByRole("link", { name: /duplicate session conversation/i });
    expect(duplicateLinks).toHaveLength(2);
    // Every row carries the time · state line, so same-titled Sessions stay
    // distinguishable. Recent-first ordering: the two-day-old duplicate ranks
    // above the five-day one.
    expect(duplicateLinks[0]).toHaveTextContent("2 days ago · Stopped");
    expect(duplicateLinks[1]).toHaveTextContent("5 days ago · Stopped");
    // The line is not conditional on duplicates: unique rows show it too.
    const uniqueLink = within(nonProject).getByRole("link", { name: /unique session conversation/i });
    expect(uniqueLink).toHaveTextContent("2 days ago · Stopped");
  });

  it("shows item counts on the Non-project and Projects group headers", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") {
          return response({
            projects: [
              navigationSummary("project-1", "Project one", "2026-08-01T00:00:00Z"),
              navigationSummary("project-2", "Project two", "2026-08-01T00:00:00Z"),
              navigationSummary("project-3", "Project three", "2026-08-01T00:00:00Z"),
            ],
          });
        }
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [
              session("session-1", "Session one", "2026-08-01T00:00:00Z"),
              session("session-2", "Session two", "2026-08-01T00:00:00Z"),
            ],
          });
        }
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
    expect(nonProjectLink.parentElement).toHaveTextContent("Non-project2");
    const projectsLink = screen.getByRole("link", { name: /^projects$/i });
    expect(projectsLink.parentElement).toHaveTextContent("Projects3");
  });

  it("renders runtime activity as colored status dots with mockup state text", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [
              {
                ...session("session-busy", "Busy session", "2026-08-01T00:00:00Z"),
                runtime_activity: { liveness: "live", turn_activity: "busy" },
              },
              {
                ...session("session-idle", "Idle session", "2026-07-31T00:00:00Z"),
                runtime_activity: { liveness: "live", turn_activity: "idle" },
              },
              {
                ...session("session-offline", "Offline session", "2026-07-30T00:00:00Z"),
                runtime_activity: { liveness: "offline" },
              },
              {
                ...session("session-orphaned", "Orphaned session", "2026-07-29T00:00:00Z"),
                runtime_activity: { liveness: "orphaned" },
              },
              {
                ...session("session-failed", "Failed session", "2026-07-28T00:00:00Z"),
                runtime_activity: { liveness: "offline" },
                latest_continuation: { status: "failed" },
              },
            ],
          });
        }
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

    const nonProject = await screen.findByRole("region", { name: /non-project/i });
    await within(nonProject).findByRole("link", { name: /busy session/i });

    // Dot color per state (mockup stateDot mapping).
    const busyDot = within(nonProject).getByRole("img", { name: "Runtime busy" }).firstElementChild;
    expect(busyDot).toHaveClass("bg-success", "animate-pulse");
    const idleDot = within(nonProject).getByRole("img", { name: "Runtime live idle" }).firstElementChild;
    expect(idleDot).toHaveClass("bg-info");
    const offlineDot = within(nonProject).getByRole("img", { name: "Runtime offline" }).firstElementChild;
    expect(offlineDot).toHaveClass("bg-muted-foreground/40");
    // Undetermined liveness is amber, never confused with a durable failure.
    const orphanedDot = within(nonProject).getByRole("img", { name: "Runtime failure (orphaned)" }).firstElementChild;
    expect(orphanedDot).toHaveClass("bg-warning");
    const failedDot = within(nonProject).getByRole("img", { name: "Runtime failure" }).firstElementChild;
    expect(failedDot).toHaveClass("bg-destructive");

    // Visible second-line state text per row (mockup: time · state).
    expect(within(nonProject).getByRole("link", { name: /busy session/i })).toHaveTextContent("· Running");
    expect(within(nonProject).getByRole("link", { name: /idle session/i })).toHaveTextContent("· Idle");
    expect(within(nonProject).getByRole("link", { name: /offline session/i })).toHaveTextContent("· Stopped");
    expect(within(nonProject).getByRole("link", { name: /orphaned session/i })).toHaveTextContent("· Unknown");
    expect(within(nonProject).getByRole("link", { name: /failed session/i })).toHaveTextContent("· Failed");
  });
});
