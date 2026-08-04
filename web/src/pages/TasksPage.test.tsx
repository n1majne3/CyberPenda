import { act, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { TasksPage } from "./TasksPage";

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/projects/project-1/tasks"]}>
      <Routes>
        <Route path="/projects/:projectId/tasks" element={<TasksPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

function task(id: string, goal: string, status: string, createdAt: string, runtimeActivity?: object) {
  return {
    id,
    project_id: "project-1",
    goal,
    status,
    runner: "sandbox",
    runtime_profile_id: "profile-1",
    run_controls: {},
    scope_snapshot: {},
    created_at: createdAt,
    updated_at: createdAt,
    ...(runtimeActivity ? { runtime_activity: runtimeActivity } : {}),
  };
}

function setDocumentVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

afterEach(() => {
  vi.unstubAllGlobals();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
});

describe("TasksPage", () => {
  it("shows running tasks first, then newest tasks first", async () => {
    mockApi({
      "/api/projects/project-1/tasks": {
        tasks: [
          task("older-completed", "Older completed", "completed", "2026-01-01T00:00:00Z"),
          task("newer-completed", "Newer completed", "completed", "2026-01-04T00:00:00Z"),
          task("older-running", "Older running", "running", "2026-01-02T00:00:00Z"),
          task("newer-running", "Newer running", "running", "2026-01-03T00:00:00Z"),
        ],
      },
    });

    renderPage();

    const links = await screen.findAllByRole("link", { name: /(running|completed)/i });
    const goals = ["Newer running", "Older running", "Newer completed", "Older completed"];
    expect(links.map((link) => goals.find((goal) => link.textContent?.includes(goal)))).toEqual(goals);
  });

  it("keeps long task goals inside focusable Geist task cards", async () => {
    const longGoal =
      "Investigate a-super-long-hostname-that-should-wrap-without-overlapping-status-or-metadata.example.internal";
    mockApi({
      "/api/projects/project-1/tasks": {
        tasks: [
          task("long-goal", longGoal, "running", "2026-01-04T00:00:00Z"),
        ],
      },
    });

    renderPage();

    const goal = await screen.findByText(longGoal);
    expect(goal).toHaveClass("break-words");
    expect(goal).not.toHaveClass("truncate");
    expect(screen.getByRole("link", { name: /a-super-long-hostname/i })).toHaveClass(
      "focus-visible:ring-2",
    );
  });

  it("shows Runtime Activity separately from Task lifecycle", async () => {
    mockApi({
      "/api/projects/project-1/tasks": {
        tasks: [
          {
            ...task("running-live", "Live runtime task", "running", "2026-01-04T00:00:00Z"),
            runtime_activity: { liveness: "live", turn_activity: "idle" },
          },
          {
            ...task("running-orphan", "Orphaned runtime task", "running", "2026-01-03T00:00:00Z"),
            runtime_activity: { liveness: "orphaned" },
          },
        ],
      },
    });

    renderPage();

    const badges = await screen.findAllByTestId("runtime-activity");
    expect(badges.map((el) => el.textContent)).toEqual(
      expect.arrayContaining(["runtime live · idle", "runtime orphaned"]),
    );
    expect(screen.getAllByText("running").length).toBeGreaterThanOrEqual(2);
  });

  it("backs off while idle, suspends while hidden, and resumes fast polling when work is active", async () => {
    // Pure fake timers (no shouldAdvanceTime): waitFor polls on real timers, so
    // under fake timers wall-clock leakage could fire the very poll under test.
    // We flush the eager mount load via microtasks instead.
    vi.useFakeTimers();
    try {
      // All terminal → idle. The page should poll slowly (not at 2s).
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/projects/project-1/tasks")) {
          return new Response(
            JSON.stringify({ tasks: [task("t-completed", "Done", "completed", "2026-01-04T00:00:00Z")] }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        return new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } });
      });
      vi.stubGlobal("fetch", fetchMock);

      renderPage();
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.getByText("Done")).toBeInTheDocument();
      const callsAfterMount = fetchMock.mock.calls.length;

      // Idle: no poll inside the fast 2s window.
      await act(async () => {
        vi.advanceTimersByTimeAsync(2500);
      });
      expect(fetchMock.mock.calls.length).toBe(callsAfterMount);

      // Hidden: even past the slow backoff interval, no poll lands.
      act(() => setDocumentVisibility("hidden"));
      await act(async () => {
        vi.advanceTimersByTimeAsync(60000);
      });
      expect(fetchMock.mock.calls.length).toBe(callsAfterMount);

      // Visible + an active task: returning to a visible tab eagerly refreshes,
      // and polling resumes. The backing data now reports a running task; after
      // the refresh lands the page is active, so polls use the fast 2s cadence.
      fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/projects/project-1/tasks")) {
          return new Response(
            JSON.stringify({ tasks: [task("t-running", "Running", "running", "2026-01-04T00:00:00Z")] }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        return new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } });
      });
      act(() => setDocumentVisibility("visible"));
      // Let the eager refresh settle so hasActive flips and the fast interval is
      // in place before measuring the poll window. Flush microtasks (fake timers
      // are active, so waitFor's real-timer polling would hang).
      await act(async () => {
        vi.advanceTimersByTimeAsync(0);
      });
      const callsAfterRefresh = fetchMock.mock.calls.length;
      // Active work → fast polling takes over within 2s.
      await act(async () => {
        vi.advanceTimersByTimeAsync(2500);
      });
      expect(fetchMock.mock.calls.length).toBeGreaterThan(callsAfterRefresh);
    } finally {
      vi.useRealTimers();
    }
  });
});
