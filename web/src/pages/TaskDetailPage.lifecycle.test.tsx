import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });


  it("requires confirmation before stopping a running task", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { fetchMock } = stubTaskDetailApi({ status: "running" });

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Stop/i }));

    
    
    expect(confirm).not.toHaveBeenCalled();
    const stopDialog = screen.getByRole("alertdialog", { name: /Stop task/i });
    expect(stopDialog).toBeInTheDocument();
    expect(within(stopDialog).getByText(/Inspect task view/i)).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/projects/project-1/tasks/task-1/stop") && init?.method === "POST",
      ),
    ).toBe(false);
  });


  it("deletes a terminal task after confirmation and returns to the task list", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { fetchMock } = stubTaskDetailApi();

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Delete/i }));

    expect(confirm).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/projects/project-1/tasks/task-1") && init?.method === "DELETE",
      ),
    ).toBe(true);
    expect(await screen.findByText("Task list")).toBeInTheDocument();
  });


  it("offers Finish Task only when controls.finish_available is true", async () => {
    stubTaskDetailApi({
      status: "running",
      
      runtime_activity: { liveness: "live", turn_activity: "idle" },
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        finish_available: true,
        queue_steer_available: true,
        interrupt_steer_available: true,
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
    });

    renderPage();
    expect(await screen.findByTestId("finish-task")).toBeInTheDocument();
    expect(screen.getByTestId("finish-task-composer")).toBeInTheDocument();
  });


  it("does not offer Finish from runtime_activity alone without finish_available", async () => {
    stubTaskDetailApi({
      status: "running",
      runtime_activity: { liveness: "live", turn_activity: "idle" },
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        finish_available: false,
        queue_steer_available: true,
        interrupt_steer_available: true,
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
    });

    renderPage();
    await screen.findByTestId("runtime-activity");
    expect(screen.queryByTestId("finish-task")).not.toBeInTheDocument();
    expect(screen.queryByTestId("finish-task-composer")).not.toBeInTheDocument();
  });


  it("hides Finish Task when Runtime is live and busy", async () => {
    stubTaskDetailApi({
      status: "running",
      runtime_activity: { liveness: "live", turn_activity: "busy" },
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        finish_available: false,
        queue_steer_available: true,
        interrupt_steer_available: true,
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
    });

    renderPage();
    await screen.findByTestId("runtime-activity");
    expect(screen.queryByTestId("finish-task")).not.toBeInTheDocument();
    expect(screen.queryByTestId("finish-task-composer")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Stop/i })).toBeInTheDocument();
  });


  it("posts Finish after confirmation and surfaces clear errors", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const taskBody = {
      id: "task-1",
      project_id: "project-1",
      goal: "Inspect task view",
      status: "running",
      runner: "sandbox",
      runtime_profile_id: "profile-1",
      run_controls: {},
      scope_snapshot: {},
      runtime_activity: { liveness: "live", turn_activity: "idle" },
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        finish_available: true,
        queue_steer_available: true,
        interrupt_steer_available: true,
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:05Z",
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/finish") && init?.method === "POST") {
        return new Response(
          JSON.stringify({ error: "finish requires a live idle Runtime; Stop interrupts a busy Runtime" }),
          { status: 409, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url.includes("/timeline")) {
        return new Response(JSON.stringify({ task_id: "task-1", items: [] }), {
          status: 200, headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/transcript")) {
        return new Response(JSON.stringify({ task_id: "task-1", entries: [] }), {
          status: 200, headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/tasks/task-1")) {
        return new Response(JSON.stringify(taskBody), {
          status: 200, headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify({}), {
        status: 200, headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await userEvent.click(await screen.findByTestId("finish-task"));
    expect(confirm).not.toHaveBeenCalled();
    await userEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Finish" }));
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/finish") && init?.method === "POST",
      ),
    ).toBe(true);
    expect(await screen.findByRole("alert")).toHaveTextContent(/live idle|busy/i);
  });


  it.each(["completed", "failed", "interrupted", "stopped"] as const)(
    "queues one message and resumes a %s task conversation",
    async (status) => {
      const { fetchMock } = stubTaskDetailApi({
        status,
        runtime_activity: { liveness: "offline" },
        runtime_controls: {
          native_resume_available: true,
          resume_available: true,
          finish_available: false,
          queue_steer_available: true,
          interrupt_steer_available: false,
          native_session_captured: true,
          same_runtime_provider_only: true,
          runtime_provider: "codex",
        },
      });
      const user = userEvent.setup();
      renderPage();
      await user.type(await screen.findByPlaceholderText("Focus on admin.example.com next…"), "continue work");
      await user.click(screen.getByRole("button", { name: /Resume and send/i }));

      const postPaths = fetchMock.mock.calls
        .filter(([, init]) => init?.method === "POST")
        .map(([input]) => String(input));
      expect(postPaths).toEqual([
        "/api/projects/project-1/tasks/task-1/steer/queue",
        "/api/projects/project-1/tasks/task-1/resume",
      ]);
      expect(postPaths.filter((path) => path.endsWith("/steer/queue"))).toHaveLength(1);
    },
  );
});
