import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TaskDetailPage } from "./TaskDetailPage";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function renderTaskPage() {
  return render(
    <MemoryRouter initialEntries={["/projects/project-1/tasks/task-1"]}>
      <Routes>
        <Route path="/projects/:projectId/tasks/:taskId" element={<TaskDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("TaskDetailPage poll errors", () => {
  it("keeps the workspace visible on a failed poll and clears the notice on recovery", async () => {
    vi.useFakeTimers();
    let failPoll = false;
    let pollSucceeded = false;

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/api/projects/project-1/tasks/task-1")) {
        if (failPoll) return jsonResponse({ error: "daemon busy" }, 503);
        return jsonResponse({
          id: "task-1",
          project_id: "project-1",
          goal: "Keep workspace visible",
          status: "running",
          runner: "sandbox",
          run_controls: {},
          scope_snapshot: {},
          updated_at: "2026-01-01T00:00:00Z",
          active_continuation: {
            id: "cont-1",
            number: 1,
            status: "running",
            runtime_provider: "codex",
          },
        });
      }
      if (url.includes("/timeline")) {
        return jsonResponse({ task_id: "task-1", items: [], cursor: 1 });
      }
      if (url.includes("/transcript")) {
        const entries = pollSucceeded
          ? [
              { id: "entry-1", seq: 1, continuation: 1, kind: "message", role: "assistant", text: "Initial reply", created_at: "2026-01-01T00:00:00Z" },
              { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "assistant", text: "Recovered reply", created_at: "2026-01-01T00:00:02Z" },
            ]
          : [
              { id: "entry-1", seq: 1, continuation: 1, kind: "message", role: "assistant", text: "Initial reply", created_at: "2026-01-01T00:00:00Z" },
            ];
        return jsonResponse({ task_id: "task-1", entries, cursor: pollSucceeded ? 2 : 1 });
      }
      if (url.includes("/finish-readiness")) {
        return jsonResponse({ ready_to_finish: true, blockers: [] });
      }
      if (url.endsWith("/api/runtime-profiles")) return jsonResponse({ profiles: [] });
      if (url.endsWith("/api/model-providers")) return jsonResponse({ providers: [] });
      if (url.endsWith("/api/runtime-plugins")) return jsonResponse({ plugins: [] });
      return jsonResponse({});
    }));

    renderTaskPage();
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(screen.getByTestId("task-workspace")).toBeInTheDocument();
    expect(screen.getByText("Initial reply")).toBeInTheDocument();

    failPoll = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });

    expect(screen.getByTestId("task-workspace")).toBeInTheDocument();
    expect(screen.getByText("Initial reply")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load this conversation")).not.toBeInTheDocument();
    expect(screen.getByTestId("poll-error-notice")).toHaveTextContent("daemon busy");

    failPoll = false;
    pollSucceeded = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });

    expect(screen.queryByTestId("poll-error-notice")).not.toBeInTheDocument();
    expect(screen.getByText("Recovered reply")).toBeInTheDocument();
  });

  it("retries immediately when the operator clicks Retry on the poll error notice", async () => {
    vi.useFakeTimers();
    let failPoll = false;

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/api/projects/project-1/tasks/task-1")) {
        if (failPoll) return jsonResponse({ error: "temporary timeout" }, 504);
        return jsonResponse({
          id: "task-1",
          project_id: "project-1",
          goal: "Keep workspace visible",
          status: "running",
          runner: "sandbox",
          run_controls: {},
          scope_snapshot: {},
          updated_at: "2026-01-01T00:00:00Z",
        });
      }
      if (url.includes("/timeline")) return jsonResponse({ task_id: "task-1", items: [], cursor: 1 });
      if (url.includes("/transcript")) return jsonResponse({ task_id: "task-1", entries: [], cursor: 1 });
      if (url.includes("/finish-readiness")) return jsonResponse({ ready_to_finish: true, blockers: [] });
      if (url.endsWith("/api/runtime-profiles")) return jsonResponse({ profiles: [] });
      if (url.endsWith("/api/model-providers")) return jsonResponse({ providers: [] });
      if (url.endsWith("/api/runtime-plugins")) return jsonResponse({ plugins: [] });
      return jsonResponse({});
    }));

    renderTaskPage();
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    failPoll = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(screen.getByTestId("poll-error-notice")).toHaveTextContent("temporary timeout");

    failPoll = false;
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Retry" }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(screen.queryByTestId("poll-error-notice")).not.toBeInTheDocument();
    expect(screen.getByTestId("task-workspace")).toBeInTheDocument();
  });

  it("still shows the full-page error state when the initial load fails", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/api/projects/project-1/tasks/task-1")) {
        return jsonResponse({ error: "task not found" }, 404);
      }
      return jsonResponse({});
    }));

    renderTaskPage();
    expect(await screen.findByText("Couldn't load this conversation")).toBeInTheDocument();
    expect(screen.getByText("task not found")).toBeInTheDocument();
    expect(screen.queryByTestId("task-workspace")).not.toBeInTheDocument();
  });
});
