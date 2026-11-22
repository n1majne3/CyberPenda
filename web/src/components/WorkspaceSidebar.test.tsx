import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { operatorCredentialIsNeeded, resetOperatorCredentialStateForTests } from "@/lib/api";
import { WorkspaceSidebar } from "./WorkspaceSidebar";
import { ThemeProvider } from "./ThemeProvider";
import { findRecentSessions, navigationSummary, openRecentSessions, response, session, task } from "./WorkspaceSidebar.testSupport";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  resetOperatorCredentialStateForTests();
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

    openRecentSessions();
    expect(screen.getByRole("link", { name: /^sessions$/i })).toHaveAttribute("href", "/sessions");
    expect(screen.getByRole("link", { name: /projects/i })).toHaveAttribute("href", "/");
    expect(screen.getByRole("link", { name: /new project/i })).toHaveAttribute("href", "/?new=1");
    expect(await screen.findByRole("link", { name: /active project project dashboard/i })).toHaveAttribute(
      "href",
      "/projects/project-active",
    );
    expect(vi.mocked(fetch).mock.calls[0][0]).toContain("selected_task=task-current");

    const projectRegion = screen.getByRole("region", { name: /active project/i });
    // the busy Task first, then the five ordinary recent Tasks, then the old
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
    // row must not share the active wash with the Task or the two boxes merge.
    const projectLink = screen.getByRole("link", { name: /active project project dashboard/i });
    // NavLink may still emit aria-current="false"; the Project must not claim page.
    expect(projectLink.getAttribute("aria-current")).not.toBe("page");
    expect(projectLink).not.toHaveClass("bg-card");
    expect(projectLink.querySelector('[data-nav-indicator="active"]')).toBeNull();
    expect(currentTaskLink).toHaveClass("bg-card");
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
    expect(projectLink).toHaveClass("bg-card");
    expect(projectLink.querySelector('[data-nav-indicator="active"]')).not.toBeNull();

    expect(screen.queryByRole("link", { name: /first task/i })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /expand active project/i }));
    const taskLink = screen.getByRole("link", { name: /first task/i });
    expect(taskLink).not.toHaveAttribute("aria-current");
    expect(taskLink).not.toHaveClass("bg-card");
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

    const nonProject = openRecentSessions();
    expect(within(nonProject).queryByRole("button", { name: /collapse non-project/i })).toBeNull();
    expect(screen.getByRole("link", { name: /^sessions$/i })).toHaveAttribute("href", "/sessions");
    expect(within(nonProject).getByText(/Recent sessions/).closest("details")).toHaveAttribute("open");

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


  it("keeps primary Projects and Sessions links at the same quiet hierarchy", async () => {
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

    const nonProjectLink = await screen.findByRole("link", { name: /^sessions$/i });
    const projectsLink = screen.getByRole("link", { name: /projects/i });
    // Both section headers share the same idle color tokens so the sidebar
    // reads as one system instead of two differently-toned regions.
    for (const className of [
      "h-[37px]",
      "text-xs",
      "focus-visible:ring-2",
    ]) {
      expect(nonProjectLink).toHaveClass(className);
      expect(projectsLink).toHaveClass(className);
    }
  });


  it("orders projects by the latest project-or-task activity", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") {
          return response({
            projects: [
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
      expect(screen.getAllByRole("link", { name: /project dashboard/i }).map((link) => link.getAttribute("aria-label"))).toEqual([
        "Open Older project project dashboard",
        "Open Newer project project dashboard",
      ]);
    });
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

    await user.click(screen.getByRole("button", { name: "Search workspace" }));
    openRecentSessions();
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
    const nonProject = openRecentSessions();
    expect(within(nonProject).getByText(/no sessions match/i)).toBeInTheDocument();
    const projects = screen.getByRole("region", { name: /^your projects$/i });
    expect(within(projects).getByText(/no projects match/i)).toBeInTheDocument();
  });


  it("signs out by clearing the tab token and expiring the daemon cookie", async () => {
    const user = userEvent.setup();
    window.sessionStorage.setItem("pentest.authToken", "tab-token");
    const fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url === "/api/operator-session" && method === "DELETE") {
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      if (url === "/api/operator-session" && method === "POST") {
        // A fixed-token daemon behind a tunnel cannot re-establish the session.
        return Promise.resolve(new Response(null, { status: 401 }));
      }
      if (url.startsWith("/api/workspace/navigation")) return response({ projects: [] });
      if (url.startsWith("/api/sessions")) return response({ sessions: [] });
      return response({});
    });
    vi.stubGlobal("fetch", fetch);

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );
    await findRecentSessions();

    await act(async () => {
      await user.click(screen.getByRole("button", { name: "Sign out" }));
    });

    expect(fetch).toHaveBeenCalledWith(
      "/api/operator-session",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(window.sessionStorage.getItem("pentest.authToken")).toBeNull();
    expect(operatorCredentialIsNeeded()).toBe(true);
  });
});
