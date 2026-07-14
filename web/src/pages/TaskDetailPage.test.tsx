import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useEffect } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { TaskDetailPage } from "./TaskDetailPage";

function renderPage(initialEntry = "/projects/project-1/tasks/task-1", onSearch?: (search: string) => void) {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={[initialEntry]}>
        {onSearch && <LocationProbe onChange={onSearch} />}
        <Routes>
          <Route path="/projects/:projectId/tasks/:taskId" element={<TaskDetailPage />} />
          <Route path="/projects/:projectId/tasks" element={<div>Task list</div>} />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
}

function LocationProbe({ onChange }: { onChange: (search: string) => void }) {
  const location = useLocation();
  useEffect(() => {
    onChange(location.search);
  }, [location.search, onChange]);
  return null;
}

function stubTaskDetailApi(taskOverrides: Record<string, unknown> = {}) {
  const scrollIntoView = vi.fn();
  Object.defineProperty(Element.prototype, "scrollIntoView", {
    value: scrollIntoView,
    configurable: true,
  });

  const fetchMock = mockApi({
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
      ...taskOverrides,
    },
    "/api/runtime-profiles": {
      profiles: [
        { id: "profile-1", name: "Codex", provider: "codex", fields: {} },
        { id: "profile-2", name: "Fake", provider: "fake", fields: {} },
      ],
    },
    "/api/model-providers": {
      providers: [
        {
          id: "mimo",
          name: "MiMo",
          base_url: "https://api.example.test/v1",
          protocols: ["openai_responses"],
          api_key_env: "MIMO_API_KEY",
          catalog: { manual: ["mimo-v2-flash", "mimo-v2-pro"], default_model: "mimo-v2-flash" },
        },
        {
          id: "anthropic",
          name: "Anthropic",
          base_url: "https://api.anthropic.test/v1",
          protocols: ["anthropic_messages"],
          api_key_env: "ANTHROPIC_API_KEY",
          catalog: { manual: ["claude-sonnet"], default_model: "claude-sonnet" },
        },
      ],
    },
    "/api/runtime-plugins": {
      plugins: [
        {
          schema_version: 1,
          id: "codex",
          name: "Codex",
          binary: { default: "codex" },
          capabilities: { sandbox: true, host: true, mcp_config: true, streaming_transcript: true, resume: true },
          model_provider: {
            requirement: "required",
            supported_protocols: ["openai_responses"],
            protocol_preference: ["openai_responses"],
          },
          profile_schema: { fields: [] },
          config_projection: { primitive: "codex" },
          launch: { args: [] },
          transcript: { parser: "codex" },
        },
      ],
    },
  });

  return { fetchMock, scrollIntoView };
}

describe("TaskDetailPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("opens on the timeline tab before conversation", async () => {
    stubTaskDetailApi();

    renderPage();

    const tabs = await screen.findAllByRole("button", { name: /^(Timeline|Conversation)$/ });
    expect(tabs.map((tab) => tab.textContent?.trim())).toEqual(["Timeline", "Conversation"]);
    expect(tabs[0]).toHaveAttribute("aria-pressed", "true");
    expect(tabs[1]).toHaveAttribute("aria-pressed", "false");
    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    expect(screen.queryByText("Conversation should be hidden by default")).not.toBeInTheDocument();
  });

  it("deep-links and updates the task view tab", async () => {
    const searches: string[] = [];
    const user = userEvent.setup();
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=conversation", (search) => searches.push(search));

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Conversation" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByTestId("transcript-row")).toHaveClass("[content-visibility:auto]");

    await user.click(screen.getByRole("button", { name: "Timeline" }));
    expect(searches.at(-1)).toBe("?view=timeline");
  });

  it("uses shared Geist radii for conversation message surfaces", async () => {
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=conversation");

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    const message = screen.getByTestId("transcript-message-bubble");
    expect(message).toHaveClass("rounded-lg");
  });

  it("does not auto-scroll the default timeline view to the bottom", async () => {
    const { scrollIntoView } = stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("gives task tabs focus rings and names the auto-follow state", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Timeline" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("button", { name: "Conversation" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("button", { name: "Scroll to top" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("button", { name: /Scroll to latest \(auto-follow on\)/i })).toHaveClass(
      "h-9",
      "w-9",
    );
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
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("option", { name: "MiMo" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Anthropic" })).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Use Codex/ })).not.toBeInTheDocument();
  });

  it("queues steering with a continuation model selection", async () => {
    const { fetchMock } = stubTaskDetailApi();
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("Timeline opened first");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model" }), "mimo-v2-pro");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "continue with mimo");
    await user.click(screen.getByRole("button", { name: /Queue steer/ }));

    const steerCall = fetchMock.mock.calls.find(([input]) =>
      String(input).includes("/api/projects/project-1/tasks/task-1/steer/queue"),
    );
    expect(steerCall?.[1]).toMatchObject({
      method: "POST",
      body: JSON.stringify({
        directive: "continue with mimo",
        model_provider_id: "mimo",
        model_override: "mimo-v2-pro",
      }),
    });
  });

  it("requires confirmation before stopping a running task", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { fetchMock } = stubTaskDetailApi({ status: "running" });

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Stop/i }));

    expect(confirm).toHaveBeenCalledWith("Stop task Inspect task view?");
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/projects/project-1/tasks/task-1/stop") && init?.method === "POST",
      ),
    ).toBe(false);
  });

  it("deletes a terminal task after confirmation and returns to the task list", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const { fetchMock } = stubTaskDetailApi();

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Delete/i }));

    expect(confirm).toHaveBeenCalledWith("Delete task Inspect task view?");
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/projects/project-1/tasks/task-1") && init?.method === "DELETE",
      ),
    ).toBe(true);
    expect(await screen.findByText("Task list")).toBeInTheDocument();
  });
});
