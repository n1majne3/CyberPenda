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

function stubTaskDetailApi(
  taskOverrides: Record<string, unknown> = {},
  transcriptEntries: Record<string, unknown>[] = [
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
) {
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
      entries: transcriptEntries,
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
        resume_available: true,
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

  it("opens on the interactive conversation by default", async () => {
    stubTaskDetailApi();

    renderPage();

    const tabs = await screen.findAllByRole("button", { name: /^(Timeline|Conversation)$/ });
    expect(tabs.map((tab) => tab.textContent?.trim())).toEqual(["Conversation", "Timeline"]);
    expect(tabs[0]).toHaveAttribute("aria-pressed", "true");
    expect(tabs[1]).toHaveAttribute("aria-pressed", "false");
    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    expect(screen.getByTestId("conversation-workspace")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Task message" })).toBeInTheDocument();
    expect(screen.getByTestId("task-workspace")).toHaveClass("overflow-visible", "md:overflow-hidden");
    expect(screen.getByTestId("task-composer")).toHaveClass("fixed", "inset-x-0", "bottom-0", "md:static");
    expect(screen.getByTestId("conversation-workspace")).toHaveClass("pb-44", "md:pb-5");
    expect(screen.queryByText("Timeline opened first")).not.toBeInTheDocument();
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

  it("renders safe Claude runtime text as a visible assistant message", async () => {
    stubTaskDetailApi({}, [
      {
        id: "runtime-entry",
        seq: 2,
        continuation: 1,
        kind: "runtime_output",
        role: "runtime",
        text: JSON.stringify({
          type: "assistant",
          message: { role: "assistant", content: [{ type: "text", text: "Inspecting the scoreboard now." }] },
        }),
        stream: "assistant",
        created_at: "2026-01-01T00:00:01Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("Inspecting the scoreboard now.")).toBeInTheDocument();
    const assistantMessage = screen.getByTestId("transcript-message-bubble");
    expect(assistantMessage).toBeInTheDocument();
    expect(assistantMessage.previousElementSibling).toBeNull();
    expect(screen.queryByText(/"type":"assistant"/)).not.toBeInTheDocument();
  });

  it("projects Claude tool calls and results into readable transcript rows", async () => {
    stubTaskDetailApi({}, [
      {
        id: "assistant-runtime-entry",
        seq: 7,
        continuation: 1,
        kind: "runtime_output",
        role: "runtime",
        text: JSON.stringify({
          type: "assistant",
          message: {
            role: "assistant",
            content: [
              { type: "text", text: "I will inspect the target now." },
              { type: "tool_use", id: "call-1", name: "Bash", input: { command: "curl http://localhost:3000" } },
            ],
          },
        }),
        stream: "assistant",
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "user-runtime-entry",
        seq: 8,
        continuation: 1,
        kind: "runtime_output",
        role: "runtime",
        text: JSON.stringify({
          type: "user",
          message: {
            role: "user",
            content: [{ type: "tool_result", tool_use_id: "call-1", content: "HTTP/1.1 200 OK\\nbody" }],
          },
        }),
        stream: "user",
        created_at: "2026-01-01T00:00:02Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("I will inspect the target now.")).toBeInTheDocument();
    expect(screen.getByText("Bash · curl http://localhost:3000")).toBeInTheDocument();
    expect(screen.getByText(/Result · HTTP\/1\.1 200 OK/)).toBeInTheDocument();
    expect(screen.getAllByText(/HTTP\/1\.1 200 OK/)).toHaveLength(2);
    expect(screen.queryByText(/"type":"assistant"/)).not.toBeInTheDocument();
    expect(screen.queryByText(/"type":"user"/)).not.toBeInTheDocument();
    const toolRows = screen.getAllByTestId("transcript-tool-row");
    expect(toolRows).toHaveLength(2);
    expect(toolRows[0]).toHaveClass("border-b");
    expect(toolRows[0]).not.toHaveClass("rounded-md");
    expect(toolRows[0]).not.toHaveClass("bg-card/60");
    const resultBody = screen.getAllByText(/HTTP\/1\.1 200 OK/).find((element) => element.tagName === "PRE");
    expect(resultBody).toBeDefined();
    expect(resultBody?.textContent).not.toContain("tool_call_id: call-1");
  });

  it("switches into a compact focus view without project chrome", async () => {
    const searches: string[] = [];
    const user = userEvent.setup();
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1", (search) => searches.push(search));

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Project sections" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Enter focus view" }));

    expect(searches.at(-1)).toBe("?focus=1");
    expect(screen.queryByRole("navigation", { name: "Project sections" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Exit focus view" })).toBeInTheDocument();
    expect(screen.getByTestId("task-session-header")).toHaveClass("h-12");
    expect(screen.getByTestId("task-detail-shell")).toHaveClass("h-[calc(100dvh-3.5rem)]", "md:h-dvh");
  });

  it("does not auto-scroll the default timeline view to the bottom", async () => {
    const { scrollIntoView } = stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=timeline");

    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("gives task tabs focus rings and names the auto-follow state", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
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

  it("shows native resume and queue steering controls", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByRole("button", { name: /Resume$/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Resume with handoff/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Resume and send/ })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("option", { name: "MiMo" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Anthropic" })).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Use Codex/ })).not.toBeInTheDocument();
  });

  it("keeps Resume enabled when a stopped task has stale runtime controls", async () => {
    stubTaskDetailApi({
      status: "stopped",
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "claude_code",
      },
    });

    renderPage();

    const resume = await screen.findByRole("button", { name: /Resume$/ });
    expect(resume).toBeEnabled();
  });

  it("shows pending provider permissions and answers on the Task session route", async () => {
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_steer_available: false,
        native_session_captured: true,
        same_runtime_provider: true,
        runtime_provider: "claude_code",
        provider_permissions: [{ permission_request_id: "perm-1", provider: "claude_code" }],
      },
    });
    const user = userEvent.setup();

    renderPage();

    expect(await screen.findByText("perm-1")).toBeInTheDocument();
    expect(screen.getByTestId("conversation-workspace")).toContainElement(
      screen.getByRole("region", { name: "Provider permission requests" }),
    );
    await user.click(screen.getByRole("button", { name: "Allow provider permission perm-1" }));

    const permissionCall = fetchMock.mock.calls.find(([input]) =>
      String(input).includes("/permissions/perm-1/respond"),
    );
    expect(permissionCall?.[1]?.method).toBe("POST");
    expect(JSON.parse(String(permissionCall?.[1]?.body))).toMatchObject({ decision: "allow" });
  });

  it("queues steering with a continuation model selection", async () => {
    const { fetchMock } = stubTaskDetailApi();
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("Conversation should be hidden by default");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model" }), "mimo-v2-pro");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation reasoning effort" }), "xhigh");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "continue with mimo");
    await user.click(screen.getByRole("button", { name: /Resume and send/ }));

    const steerCall = fetchMock.mock.calls.find(([input]) =>
      String(input).includes("/api/projects/project-1/tasks/task-1/steer/queue"),
    );
    expect(steerCall?.[1]?.method).toBe("POST");
    expect(JSON.parse(String(steerCall?.[1]?.body))).toMatchObject({
      directive: "continue with mimo",
      model_provider_id: "mimo",
      model: "mimo-v2-pro",
      model_override: "mimo-v2-pro",
      reasoning_effort: "xhigh",
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).endsWith("/api/projects/project-1/tasks/task-1/resume") && init?.method === "POST",
    )).toBe(true);
  });

  it("sends an active Task message through the native conversation route", async () => {
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_steer_available: true,
        native_steer_mode: "in_turn_steer",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
        turn_selection: {
          model_provider_id: "mimo",
          model: "mimo-v2-flash",
          reasoning_effort: "medium",
        },
      },
    });
    const user = userEvent.setup();

    renderPage();

    const input = await screen.findByRole("textbox", { name: "Task message" });
    await user.type(input, "check the admin route");
    await user.keyboard("{Enter}");

    const steerCall = fetchMock.mock.calls.find(([input]) =>
      String(input).endsWith("/api/projects/project-1/tasks/task-1/steer"),
    );
    expect(steerCall?.[1]?.method).toBe("POST");
    expect(JSON.parse(String(steerCall?.[1]?.body))).toMatchObject({
      message: "check the admin route",
      model_provider_id: "mimo",
      model: "mimo-v2-flash",
      reasoning_effort: "medium",
    });
  });

  it("keeps same-provider model and effort changes on the native steer route", async () => {
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_steer_available: true,
        native_steer_mode: "interrupt_then_replace",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
        turn_selection: {
          model_provider_id: "mimo",
          model: "mimo-v2-flash",
          reasoning_effort: "medium",
        },
      },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("Conversation should be hidden by default");
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("mimo");
    expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveValue("mimo-v2-flash");
    expect(screen.getByRole("combobox", { name: "Continuation reasoning effort" })).toHaveValue("medium");

    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model" }), "mimo-v2-pro");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation reasoning effort" }), "xhigh");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "stronger turn");
    await user.click(screen.getByRole("button", { name: /Native interrupt & send/ }));

    const postPaths = fetchMock.mock.calls
      .filter(([, init]) => init?.method === "POST")
      .map(([input]) => String(input));
    expect(postPaths.some((path) => path.endsWith("/steer"))).toBe(true);
    expect(postPaths.some((path) => path.endsWith("/steer/queue"))).toBe(false);
    expect(postPaths.some((path) => path.endsWith("/stop"))).toBe(false);

    const steerCall = fetchMock.mock.calls.find(([input]) =>
      String(input).endsWith("/api/projects/project-1/tasks/task-1/steer"),
    );
    expect(JSON.parse(String(steerCall?.[1]?.body))).toMatchObject({
      message: "stronger turn",
      model_provider_id: "mimo",
      model: "mimo-v2-pro",
      reasoning_effort: "xhigh",
    });
    // Composer retains the submitted selection for the next turn.
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("mimo");
    expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveValue("mimo-v2-pro");
    expect(screen.getByRole("combobox", { name: "Continuation reasoning effort" })).toHaveValue("xhigh");
  });

  it("keeps Shift+Enter as a newline and sends the composed message on Enter", async () => {
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_steer_available: true,
        native_steer_mode: "in_turn_steer",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
    });
    const user = userEvent.setup();

    renderPage();

    const input = await screen.findByRole("textbox", { name: "Task message" });
    await user.type(input, "line one");
    await user.keyboard("{Shift>}{Enter}{/Shift}line two");

    expect(input).toHaveValue("line one\nline two");
    expect(fetchMock.mock.calls.some(([request]) =>
      String(request).endsWith("/api/projects/project-1/tasks/task-1/steer"),
    )).toBe(false);

    await user.keyboard("{Enter}");

    const steerCall = fetchMock.mock.calls.find(([request]) =>
      String(request).endsWith("/api/projects/project-1/tasks/task-1/steer"),
    );
    expect(JSON.parse(String(steerCall?.[1]?.body))).toMatchObject({ message: "line one\nline two" });
  });

  it("sends native steer as one idempotent Task Conversation message", async () => {
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_steer_available: true,
        native_steer_mode: "interrupt_then_replace",
        native_steer_state: "idle",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("Conversation should be hidden by default");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "focus on admin");
    await user.click(screen.getByRole("button", { name: /Native interrupt & send/ }));

    const steerCall = fetchMock.mock.calls.find(([input]) =>
      String(input).endsWith("/api/projects/project-1/tasks/task-1/steer"),
    );
    expect(steerCall?.[1]?.method).toBe("POST");
    const body = JSON.parse(String(steerCall?.[1]?.body));
    expect(body.message).toBe("focus on admin");
    expect(typeof body.request_id).toBe("string");
    expect(body.request_id.length).toBeGreaterThan(8);
  });

  it("restarts native steer when switching the model provider", async () => {
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_steer_available: true,
        native_steer_mode: "in_turn_steer",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
        turn_selection: {
          model_provider_id: "anthropic",
          model: "claude-sonnet",
          reasoning_effort: "high",
        },
      },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("Conversation should be hidden by default");
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("anthropic");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "continue with mimo");
    expect(screen.getByRole("button", { name: "Switch provider and resume" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Switch provider and resume" }));

    const postPaths = fetchMock.mock.calls
      .filter(([, init]) => init?.method === "POST")
      .map(([input]) => String(input));
    expect(postPaths).toEqual([
      "/api/projects/project-1/tasks/task-1/steer/queue",
      "/api/projects/project-1/tasks/task-1/stop",
      "/api/projects/project-1/tasks/task-1/resume",
    ]);
    const queueCall = fetchMock.mock.calls.find(([input]) => String(input).endsWith("/steer/queue"));
    expect(JSON.parse(String(queueCall?.[1]?.body))).toMatchObject({
      directive: "continue with mimo",
      model_provider_id: "mimo",
      model: "mimo-v2-flash",
      reasoning_effort: "high",
    });
    expect(postPaths.some((path) => path.endsWith("/steer"))).toBe(false);
  });

  it("restarts when introducing a Model Provider from an empty preceding selection", async () => {
    // Empty preceding provider + any selected provider must use Config Projection
    // restart, not native steer that would 409 after the fact.
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_resume_available: false,
        resume_available: false,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_steer_available: true,
        native_steer_mode: "in_turn_steer",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
        turn_selection: {
          model: "gpt-test",
          reasoning_effort: "high",
        },
      },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("Conversation should be hidden by default");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "bind a provider");
    expect(screen.getByRole("button", { name: "Switch provider and resume" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Switch provider and resume" }));

    const postPaths = fetchMock.mock.calls
      .filter(([, init]) => init?.method === "POST")
      .map(([input]) => String(input));
    expect(postPaths).toEqual([
      "/api/projects/project-1/tasks/task-1/steer/queue",
      "/api/projects/project-1/tasks/task-1/stop",
      "/api/projects/project-1/tasks/task-1/resume",
    ]);
    expect(postPaths.some((path) => path.endsWith("/steer"))).toBe(false);
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
