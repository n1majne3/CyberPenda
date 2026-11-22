import { InAppNavigationButton } from "@/test/RouteControls";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { TaskDetailPage } from "./TaskDetailPage";
import { renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage Runtime Owner History Window (#202)", () => {

  const taskRecord = (id: string, goal: string, status = "completed") => ({
    id,
    project_id: "project-1",
    goal,
    status,
    runner: "sandbox",
    runtime_profile_id: "profile-1",
    run_controls: {},
    scope_snapshot: {},
    runtime_controls: {
      native_resume_available: true,
      resume_available: true,
      queue_steer_available: true,
      interrupt_steer_available: false,
      native_session_captured: true,
      same_runtime_provider_only: true,
      runtime_provider: "codex",
    },
    latest_continuation: {
      id: `cont-${id}`,
      task_id: id,
      number: 1,
      runtime_profile_id: "profile-1",
      runtime_provider: "codex",
      runner: "sandbox",
      status: "completed",
      started_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:05Z",
      ended_at: "2026-01-01T00:00:05Z",
    },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:05Z",
  });

  const json = (body: unknown) => new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });


  it("replaces owner-local history before rendering the new owner after in-app navigation", async () => {
    const user = userEvent.setup();
    const scrollIntoView = vi.fn();
    Object.defineProperty(Element.prototype, "scrollIntoView", { value: scrollIntoView, configurable: true });
    
    
    let releaseSecondOwnerHistory!: () => void;
    const secondOwnerHistory = new Promise<void>((resolve) => {
      releaseSecondOwnerHistory = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("tasks/task-2/") && (url.includes("/timeline") || url.includes("/transcript"))) {
        await secondOwnerHistory;
      }
      if (url.includes("tasks/task-1/timeline")) {
        return json({ task_id: "task-1", items: [{ seq: 1, type: "text", content: "First task timeline", created_at: "2026-01-01T00:00:00Z" }], cursor: 1 });
      }
      if (url.includes("tasks/task-1/transcript")) {
        return json({ task_id: "task-1", entries: [{ id: "one-1", seq: 1, continuation: 1, kind: "message", role: "assistant", text: "First task content", created_at: "2026-01-01T00:00:00Z" }], cursor: 1 });
      }
      if (url.includes("tasks/task-2/timeline")) {
        return json({ task_id: "task-2", items: [], cursor: 0 });
      }
      if (url.includes("tasks/task-2/transcript")) {
        return json({ task_id: "task-2", entries: [{ id: "two-1", seq: 1, continuation: 1, kind: "message", role: "assistant", text: "Second task content", created_at: "2026-01-01T00:00:00Z" }], cursor: 1 });
      }
      if (url.includes("tasks/task-1")) return json(taskRecord("task-1", "First task"));
      if (url.includes("tasks/task-2")) return json(taskRecord("task-2", "Second task"));
      if (url.includes("/api/runtime-profiles")) return json({ profiles: [] });
      if (url.includes("/api/model-providers")) return json({ providers: [] });
      if (url.includes("/api/runtime-plugins")) return json({ plugins: [] });
      return json({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(
      <StrictMode>
        <MemoryRouter initialEntries={["/projects/project-1/tasks/task-1"]}>
          <Routes>
            <Route path="/projects/:projectId/tasks/:taskId" element={<TaskDetailPage />} />
            <Route path="/projects/:projectId/tasks" element={<div>Task list</div>} />
          </Routes>
          <InAppNavigationButton to="/projects/project-1/tasks/task-2" label="Open second task" />
        </MemoryRouter>
      </StrictMode>,
    );

    expect(await screen.findByText("First task content")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open second task" }));

    
    await waitFor(() => {
      expect(screen.queryByText("First task content")).not.toBeInTheDocument();
    });
    expect(screen.queryByText("First task timeline")).not.toBeInTheDocument();
    expect(screen.queryByText("Second task content")).not.toBeInTheDocument();
    releaseSecondOwnerHistory();
    expect(await screen.findByText("Second task content")).toBeInTheDocument();
    
    expect(screen.queryByText("First task content")).not.toBeInTheDocument();
  });


  it("sends an explicit after=0 on idle polls after an empty initial read", async () => {
    vi.useFakeTimers();
    try {
      const { fetchMock } = stubTaskDetailApi(
        { status: "running" },
        [],
        { task_id: "task-1", items: [], cursor: 0 },
        { task_id: "task-1", entries: [], cursor: 0 },
      );
      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      const urls = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urls.some((url) => url.includes("/timeline?after=0"))).toBe(true);
      expect(urls.some((url) => url.includes("/transcript?after=0"))).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });


  it("never resends the synthetic seq-0 goal row on idle polls", async () => {
    vi.useFakeTimers();
    try {
      const { fetchMock } = stubTaskDetailApi(
        { status: "running" },
        undefined,
        { task_id: "task-1", items: [], cursor: 0 },
        {
          task_id: "task-1",
          entries: [{
            id: "goal-row",
            seq: 0,
            continuation: 0,
            kind: "message",
            role: "user",
            text: "Inspect task view",
            created_at: "2026-01-01T00:00:00Z",
          }],
          cursor: 0,
        },
      );
      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getAllByTestId("transcript-row")).toHaveLength(1);
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      const urls = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urls.some((url) => url.includes("/transcript?after=0"))).toBe(true);
      
      expect(screen.getAllByTestId("transcript-row")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });
});
