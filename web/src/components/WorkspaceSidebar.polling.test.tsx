import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetOperatorCredentialStateForTests } from "@/lib/api";
import { WorkspaceSidebar } from "./WorkspaceSidebar";
import { ThemeProvider } from "./ThemeProvider";
import { navigationSummary, openRecentSessions, response, session, setDocumentVisibility, task } from "./WorkspaceSidebar.testSupport";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  resetOperatorCredentialStateForTests();
});

describe("WorkspaceSidebar", () => {


  
  
  
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

      
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.getByRole("region", { name: /project one/i })).toBeInTheDocument();
      const callsAfterMount = fetchMock.mock.calls.length;

      await act(async () => {
        vi.advanceTimersByTimeAsync(2500);
      });
      expect(fetchMock.mock.calls.length).toBe(callsAfterMount);

      
      await act(async () => {
        vi.advanceTimersByTimeAsync(30000);
      });
      expect(fetchMock.mock.calls.length).toBeGreaterThan(callsAfterMount);
    } finally {
      vi.useRealTimers();
    }
  });


  
  it("polls fast while an owner has an active continuation even when no row is busy", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [{
              ...session("session-running", "Running session", "2026-08-01T00:00:00Z"),
              runtime_activity: { liveness: "live", turn_activity: "idle" },
              latest_continuation: { status: "running" },
            }],
          });
        }
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
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(within(openRecentSessions()).getByRole("link", { name: /running session.*session conversation/i })).toBeInTheDocument();
      const callsAfterMount = fetchMock.mock.calls.length;

      
      
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2500);
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
      
      const urls = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urls.filter((url) => url.includes("/tasks")).length).toBe(0);

      
      await act(async () => {
        vi.advanceTimersByTimeAsync(2500);
      });
      const urlsAfterPoll = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urlsAfterPoll.filter((url) => url.includes("/tasks")).length).toBe(0);

      
      const projectRegions = screen.getAllByRole("region", { name: /project \d+ project dashboard/i });
      expect(projectRegions.length).toBe(123);
    } finally {
      vi.useRealTimers();
    }
  });


  
  
  
  
  it("sends the revision back and keeps the projection on an unchanged refresh", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/workspace/navigation")) {
          
          
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
      
      expect(fetchMock.mock.calls[0][0]).toBe("/api/workspace/navigation?selected_task=task-a");

      
      
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

    
    await screen.findByRole("region", { name: /current project/i });
    let taskFetches = vi.mocked(fetch).mock.calls.filter((call) => String(call[0]).includes("/projects/project-other/tasks")).length;
    expect(taskFetches).toBe(0);

    
    
    const otherRegion = screen.getByRole("region", { name: /other project/i });
    await user.click(within(otherRegion).getByRole("button", { name: /expand other project/i }));
    expect(await within(otherRegion).findByRole("link", { name: /full task 1/i })).toBeInTheDocument();
    expect(within(otherRegion).getByRole("link", { name: /full task 2/i })).toBeInTheDocument();
    taskFetches = vi.mocked(fetch).mock.calls.filter((call) => String(call[0]).includes("/projects/project-other/tasks")).length;
    expect(taskFetches).toBe(1);
  });
});
