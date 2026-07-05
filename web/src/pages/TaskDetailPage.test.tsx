import { render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { TaskDetailPage } from "./TaskDetailPage";

function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={["/projects/project-1/tasks/task-1"]}>
        <Routes>
          <Route path="/projects/:projectId/tasks/:taskId" element={<TaskDetailPage />} />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
}

function stubTaskDetailApi() {
  const scrollIntoView = vi.fn();
  Object.defineProperty(Element.prototype, "scrollIntoView", {
    value: scrollIntoView,
    configurable: true,
  });

  mockApi({
    "/api/projects/project-1/tasks/task-1/timeline": {
      task_id: "task-1",
      items: [{ seq: 1, type: "text", content: "Timeline opened first", created_at: "2026-01-01T00:00:00Z" }],
    },
    "/api/projects/project-1/tasks/task-1/transcript": {
      task_id: "task-1",
      entries: [
        {
          id: "entry-1",
          seq: 1,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "Conversation should be hidden by default",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
    },
    "/api/projects/project-1/tasks/task-1": {
      id: "task-1",
      project_id: "project-1",
      goal: "Inspect task view",
      status: "completed",
      runner: "sandbox",
      runtime_profile_id: "profile-1",
      run_controls: {},
      scope_snapshot: {},
      runtime_controls: {
        native_resume_available: true,
        handoff_resume_available: true,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
      latest_continuation: {
        id: "cont-1",
        task_id: "task-1",
        number: 1,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "completed",
        native_session_id: "sess-1",
        started_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:05Z",
        ended_at: "2026-01-01T00:00:05Z",
      },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:05Z",
    },
    "/api/runtime-profiles": {
      profiles: [
        { id: "profile-1", name: "Codex", provider: "codex" },
        { id: "profile-2", name: "Fake", provider: "fake" },
      ],
    },
  });

  return { scrollIntoView };
}

describe("TaskDetailPage", () => {
  it("opens on the timeline tab before conversation", async () => {
    stubTaskDetailApi();

    renderPage();

    const tabs = await screen.findAllByRole("button", { name: /^(Timeline|Conversation)$/ });
    expect(tabs.map((tab) => tab.textContent?.trim())).toEqual(["Timeline", "Conversation"]);
    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    expect(screen.queryByText("Conversation should be hidden by default")).not.toBeInTheDocument();
  });

  it("does not auto-scroll the default timeline view to the bottom", async () => {
    const { scrollIntoView } = stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("shows the latest continuation summary when present", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("continuation #1")).toBeInTheDocument();
    expect(screen.getByText("runtime: codex")).toBeInTheDocument();
    expect(screen.getByText("continuation status: completed")).toBeInTheDocument();
    expect(screen.getByText("native session: captured")).toBeInTheDocument();
    expect(screen.getByText("same runtime only")).toBeInTheDocument();
  });

  it("separates native resume, handoff resume, and queue steering controls", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByRole("button", { name: /Resume$/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Resume with handoff/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Queue steer/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Use Codex/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Use Fake/ })).not.toBeInTheDocument();
  });
});
