import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useEffect } from "react";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { TaskDetailPage } from "./TaskDetailPage";

function InAppNavigationButton({ to, label }: { to: string; label: string }) {
  const navigate = useNavigate();
  return <button type="button" onClick={() => navigate(to)}>{label}</button>;
}

function setDocumentVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

function mockConversationViewport(options: {
  scrollHeight: () => number;
  clientHeight?: () => number;
  scrollTo?: (this: HTMLElement, options: ScrollToOptions) => void;
}) {
  const scrollHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  const clientHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  const scrollToDescriptor = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTo");
  const clientHeight = options.clientHeight ?? (() => 600);
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", { get: options.scrollHeight, configurable: true });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", { get: clientHeight, configurable: true });
  Object.defineProperty(Element.prototype, "scrollTo", {
    configurable: true,
    value: options.scrollTo ?? function (this: HTMLElement, scrollOptions: ScrollToOptions) {
      this.scrollTop = Math.max(0, Math.min(scrollOptions.top ?? 0, this.scrollHeight - this.clientHeight));
      this.dispatchEvent(new Event("scroll"));
    },
  });

  return () => {
    restoreProperty(HTMLElement.prototype, "scrollHeight", scrollHeightDescriptor);
    restoreProperty(HTMLElement.prototype, "clientHeight", clientHeightDescriptor);
    restoreProperty(Element.prototype, "scrollTo", scrollToDescriptor);
  };
}

function restoreProperty(target: object, property: PropertyKey, descriptor?: PropertyDescriptor) {
  if (descriptor) {
    Object.defineProperty(target, property, descriptor);
    return;
  }
  Reflect.deleteProperty(target, property);
}

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
  timelineBody: Record<string, unknown> = {
    task_id: "task-1",
    items: [{ seq: 1, type: "text", content: "Timeline opened first", created_at: "2026-01-01T00:00:00Z" }],
  },
  transcriptBody: Record<string, unknown> = {
    task_id: "task-1",
    entries: transcriptEntries,
  },
  extraRoutes: Record<string, unknown> = {},
) {
  const scrollIntoView = vi.fn();
  Object.defineProperty(Element.prototype, "scrollIntoView", {
    value: scrollIntoView,
    configurable: true,
  });

  const fetchMock = mockApi({
    // Specific query routes first: mockApi matches in insertion order.
	"/api/projects/project-1/tasks/task-1/finish-readiness": { ready_to_finish: true, blockers: [] },
    ...extraRoutes,
    "/api/projects/project-1/tasks/task-1/timeline": timelineBody,
    "/api/projects/project-1/tasks/task-1/transcript": transcriptBody,
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
          catalog: { manual: ["claude-sonnet", "claude-opus"], default_model: "claude-sonnet" },
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
        {
          schema_version: 1,
          id: "pi",
          name: "Pi",
          binary: { default: "pi" },
          capabilities: {
            sandbox: true,
            host: true,
            mcp_config: true,
            streaming_transcript: true,
            resume: true,
            persistent_session: true,
            send_turn: true,
            in_turn_steer: true,
          },
          model_provider: {
            requirement: "required",
            supported_protocols: ["openai_chat_completions", "openai_responses", "anthropic_messages"],
            protocol_preference: ["openai_chat_completions", "openai_responses", "anthropic_messages"],
          },
          profile_schema: { fields: [] },
          config_projection: { primitive: "pi_agent" },
          launch: { args: [] },
          transcript: { parser: "pi" },
        },
      ],
    },
  });

  return { fetchMock, scrollIntoView };
}

describe("TaskDetailPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });

  it("opens on the interactive conversation by default", async () => {
    stubTaskDetailApi();

    renderPage();

    const tabs = await screen.findAllByRole("button", { name: /^(Timeline|Conversation)$/ });
    expect(tabs.map((tab) => tab.textContent?.trim())).toEqual(["Conversation", "Timeline"]);
    expect(tabs[0]).toHaveAttribute("aria-pressed", "true");
    expect(tabs[1]).toHaveAttribute("aria-pressed", "false");
    expect(tabs[0]?.querySelector("svg")).toBeNull();
    expect(tabs[1]?.querySelector("svg")).toBeNull();
    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    expect(screen.getByTestId("conversation-workspace")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Task message" })).toBeInTheDocument();
    expect(screen.getByLabelText("Continuation model provider")).toHaveClass("appearance-none");
    expect(screen.getByLabelText("Continuation model")).toHaveClass("appearance-none");
    expect(screen.getByLabelText("Continuation reasoning effort")).toHaveClass("appearance-none");
    expect(screen.getByTestId("task-workspace")).toHaveClass("overflow-visible", "md:overflow-hidden");
    expect(screen.getByTestId("task-composer")).toHaveClass("fixed", "inset-x-0", "bottom-0", "md:static");
    expect(screen.getByTestId("conversation-workspace")).toHaveClass("pb-44", "md:pb-5");
    expect(screen.queryByText("Timeline opened first")).not.toBeInTheDocument();
  });

  it("does not draw an outer frame around the Runtime Owner Workspace", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByTestId("task-session-header")).not.toHaveClass("border-b");
    expect(screen.getByRole("button", { name: "Conversation" }).parentElement).not.toHaveClass("border-b");
    expect(await screen.findByTestId("task-workspace")).not.toHaveClass("border-x", "border-b");
    expect(screen.getByTestId("task-composer")).not.toHaveClass("border-t");
  });

  it("shows assisted pending Blackboard conclusion state in the Task header", async () => {
    stubTaskDetailApi({
      blackboard_conclusion: {
        mode: "assisted",
        state: "pending",
        source_turn_id: "turn-7",
      },
    });

    renderPage();

    const badge = await screen.findByTestId("blackboard-conclusion-state");
    expect(badge).toHaveTextContent("Blackboard · assisted · pending");
    expect(badge).toHaveAttribute("title", expect.stringContaining("turn-7"));
  });

  it("shows Disabled mode and hides Task Blackboard surfaces", async () => {
    stubTaskDetailApi({
      status: "running",
      run_controls: { blackboard_conclusion_mode: "disabled" },
      blackboard_conclusion: {
        mode: "disabled",
        state: "action_required",
        error_code: "semantic_conclusion_repair_exhausted",
        retry_available: true,
      },
    });

    renderPage();

    expect(await screen.findByTestId("blackboard-conclusion-state")).toHaveTextContent("Blackboard: Disabled");
    expect(screen.queryByRole("alert", { name: "Blackboard conclusion requires attention" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Conversation" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Timeline" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Task message" })).toBeInTheDocument();
  });

  it("shows every Finish Readiness blocker and its related surface on Task Detail", async () => {
	stubTaskDetailApi({}, undefined, undefined, undefined, {
	  "/api/projects/project-1/tasks/task-1/finish-readiness": {
		ready_to_finish: false,
		blockers: [
		  {
			code: "blackboard_conclusion_action_required",
			count: 1,
			message: "A Blackboard conclusion needs operator action.",
			links: ["/projects/project-1/blackboard"],
		  },
		  {
			code: "unfinalized_challenge_attempts",
			count: 2,
			message: "Challenge Attempts are not finalized.",
			links: ["/projects/project-1/tasks/task-1/challenges"],
		  },
		],
	  },
	});

	renderPage();

	const readiness = await screen.findByRole("region", { name: "Finish Readiness" });
	expect(within(readiness).getByText("A Blackboard conclusion needs operator action.")).toBeInTheDocument();
	expect(within(readiness).getByText("Challenge Attempts are not finalized.")).toBeInTheDocument();
	expect(within(readiness).getByRole("link", { name: "Open blackboard_conclusion_action_required" })).toHaveAttribute("href", "/projects/project-1/blackboard");
	expect(within(readiness).getByRole("link", { name: "Open unfinalized_challenge_attempts" })).toHaveAttribute("href", "/projects/project-1/tasks/task-1/challenges");
  });

  it("shows an assisted Conclude Turn in the Task header", async () => {
	stubTaskDetailApi({
	  blackboard_conclusion: {
		mode: "assisted",
		state: "concluding",
		source_turn_id: "turn-7",
	  },
	});

	renderPage();
	expect(await screen.findByTestId("blackboard-conclusion-state")).toHaveTextContent("Blackboard · assisted · concluding");
  });

  it("shows the applied Blackboard revision in the Task header", async () => {
	stubTaskDetailApi({
	  blackboard_conclusion: { mode: "assisted", state: "clean", source_turn_id: "turn-7", applied_revision: 9 },
	});
	renderPage();
	const badge = await screen.findByTestId("blackboard-conclusion-state");
	expect(badge).toHaveTextContent("applied revision 9");
	expect(badge).toHaveAttribute("title", expect.stringContaining("revision 9"));
  });

  it("surfaces action-required Blackboard recovery without blocking manual conversation", async () => {
    const user = userEvent.setup();
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      blackboard_conclusion: {
        mode: "assisted",
        state: "action_required",
        source_turn_id: "turn-7",
        error_code: "semantic_conclusion_invalid_result",
        retry_available: true,
      },
    });

    renderPage();

    expect(await screen.findByTestId("blackboard-conclusion-state")).toHaveTextContent(
      "Blackboard · assisted · action required",
    );
    const recovery = screen.getByRole("alert", { name: "Blackboard conclusion requires attention" });
    expect(recovery).toHaveTextContent("The runtime returned an invalid Blackboard conclusion.");
    expect(recovery).toHaveTextContent("semantic_conclusion_invalid_result");
    expect(screen.getByRole("textbox", { name: "Task message" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Retry Blackboard conclusion" }));

    await waitFor(() => {
      const retryCall = fetchMock.mock.calls.find(([input]) =>
        String(input).endsWith("/api/projects/project-1/tasks/task-1/blackboard-conclusion/retry"),
      );
      expect(retryCall).toBeDefined();
      const headers = new Headers((retryCall?.[1] as RequestInit | undefined)?.headers);
      expect(headers.get("Idempotency-Key")).toMatch(/^blackboard-retry-/);
    });
  });

  it("surfaces the bounded validation reason when the repair budget is exhausted", async () => {
    stubTaskDetailApi({
      status: "running",
      blackboard_conclusion: {
        mode: "assisted",
        state: "action_required",
        error_code: "semantic_conclusion_repair_exhausted",
        validation_reason: "invalid_key_format",
        validation_field_path: "attempt.key",
        validation_expected: "the key must use the attempt: prefix",
        retry_available: true,
      },
    });

    renderPage();

    const recovery = await screen.findByRole("alert", { name: "Blackboard conclusion requires attention" });
    expect(recovery).toHaveTextContent("semantic_conclusion_repair_exhausted");
    expect(screen.getByTestId("blackboard-conclusion-validation")).toHaveTextContent(
      "invalid_key_format · attempt.key · the key must use the attempt: prefix",
    );
  });

  it("maps forbidden Conclude tool use to bounded operator copy", async () => {
    stubTaskDetailApi({
      status: "running",
      blackboard_conclusion: {
        mode: "assisted",
        state: "action_required",
        error_code: "conclude_tool_use_forbidden",
      },
    });

    renderPage();

    expect(await screen.findByRole("alert", { name: "Blackboard conclusion requires attention" })).toHaveTextContent(
      "The runtime attempted to use a tool while concluding Blackboard state.",
    );
    expect(screen.getByRole("button", { name: "Retry Blackboard conclusion" })).toBeDisabled();
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

  it("renders assistant messages with the Direction A signal accent", async () => {
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=conversation");

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    const message = screen.getByTestId("transcript-message-bubble");
    expect(message).toHaveClass("border-l-2", "border-signal/40");
    expect(message).not.toHaveClass("rounded-lg");
  });

  it("renders safe Claude runtime text as a visible assistant message", async () => {
    stubTaskDetailApi({}, [
      {
        id: "runtime-entry",
        seq: 2,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: "Inspecting the scoreboard now.",
        created_at: "2026-01-01T00:00:01Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("Inspecting the scoreboard now.")).toBeInTheDocument();
    const assistantMessage = screen.getByTestId("transcript-message-bubble");
    expect(assistantMessage).toBeInTheDocument();
    expect(assistantMessage.previousElementSibling).toHaveAttribute("data-testid", "transcript-turn-header");
    expect(screen.queryByText(/"type":"assistant"/)).not.toBeInTheDocument();
  });

  it("projects Claude tool calls and results into readable transcript rows", async () => {
    stubTaskDetailApi({}, [
      {
        id: "assistant-message",
        seq: 7,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: "I will inspect the target now.",
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "assistant-tool-call",
        seq: 7,
        continuation: 1,
        kind: "tool_call",
        role: "assistant",
        tool_call_id: "call-1",
        tool_name: "Bash",
        details: { input: { command: "curl http://localhost:3000" } },
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "tool-result",
        seq: 8,
        continuation: 1,
        kind: "tool_result",
        role: "tool",
        text: "HTTP/1.1 200 OK\nbody",
        tool_call_id: "call-1",
        created_at: "2026-01-01T00:00:02Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("I will inspect the target now.")).toBeInTheDocument();
    expect(screen.getByText("Bash · curl http://localhost:3000")).toBeInTheDocument();
    expect(screen.getAllByText(/HTTP\/1\.1 200 OK/)).toHaveLength(2);
    expect(screen.queryByText(/"type":"assistant"/)).not.toBeInTheDocument();
    expect(screen.queryByText(/"type":"user"/)).not.toBeInTheDocument();
    const toolRows = screen.getAllByTestId("transcript-tool-row");
    expect(toolRows).toHaveLength(1);
    expect(toolRows[0]).not.toHaveAttribute("open");
    expect(toolRows[0]).not.toHaveClass("rounded-md");
    expect(toolRows[0]).not.toHaveClass("bg-card/60");
    const resultBody = screen.getAllByText(/HTTP\/1\.1 200 OK/).find((element) => element.tagName === "PRE");
    expect(resultBody).toBeDefined();
    expect(resultBody?.textContent).not.toContain("tool_call_id: call-1");
  });

  it("renders completed reasoning as collapsed activity instead of an agent message", async () => {
    stubTaskDetailApi({}, [
      {
        id: "reasoning-1",
        seq: 6,
        continuation: 1,
        kind: "reasoning",
        role: "assistant",
        text: "Checked the active challenge and prepared the next command.",
        created_at: "2026-08-26T09:00:01Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("Reasoning")).toBeInTheDocument();
    const row = screen.getByTestId("transcript-reasoning-row");
    expect(row).not.toHaveAttribute("open");
    expect(screen.getByText("Checked the active challenge and prepared the next command.")).toBeInTheDocument();
    expect(screen.queryByTestId("transcript-message-bubble")).not.toBeInTheDocument();
    expect(screen.queryByText("Agent")).not.toBeInTheDocument();
    expect(screen.queryByText("You")).not.toBeInTheDocument();
  });

  it("auto-expands the latest reasoning entry while the Runtime Turn is busy", async () => {
    stubTaskDetailApi({
      status: "running",
      runtime_activity: { liveness: "live", turn_activity: "busy" },
    }, [
      {
        id: "reasoning-old",
        seq: 4,
        continuation: 1,
        kind: "reasoning",
        role: "assistant",
        text: "Older reasoning.",
        created_at: "2026-08-26T09:00:00Z",
      },
      {
        id: "reasoning-live",
        seq: 6,
        continuation: 1,
        kind: "reasoning",
        role: "assistant",
        text: "Checking the auth flow.",
        status: "streaming",
        created_at: "2026-08-26T09:00:01Z",
      },
    ]);

    renderPage();

    const rows = await screen.findAllByTestId("transcript-reasoning-row");
    expect(rows).toHaveLength(2);
    expect(rows[0]).not.toHaveAttribute("open");
    expect(rows[1]).toHaveAttribute("open");
  });

  it("keeps agent messages and tool rows tight, only spacing out new user turns", async () => {
    stubTaskDetailApi({}, [
      {
        id: "user-msg",
        seq: 1,
        continuation: 1,
        kind: "message",
        role: "user",
        text: "Inspect the target.",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "assistant-probe",
        seq: 2,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: "Running a probe.",
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "assistant-tool-call",
        seq: 2,
        continuation: 1,
        kind: "tool_call",
        role: "assistant",
        tool_call_id: "call-1",
        tool_name: "Bash",
        details: { input: { command: "curl http://localhost:3000" } },
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "tool-result",
        seq: 3,
        continuation: 1,
        kind: "tool_result",
        role: "tool",
        text: "HTTP/1.1 200 OK",
        tool_call_id: "call-1",
        created_at: "2026-01-01T00:00:02Z",
      },
      {
        id: "assistant-summary",
        seq: 4,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: "The service is up.",
        created_at: "2026-01-01T00:00:03Z",
      },
      {
        id: "next-user-msg",
        seq: 5,
        continuation: 1,
        kind: "message",
        role: "user",
        text: "Thanks, stop there.",
        created_at: "2026-01-01T00:00:04Z",
      },
    ]);

    renderPage();

    const rows = await screen.findAllByTestId("transcript-row");
    // The call/result pair is one compressed row. New user turns get breathing
    // room; every row inside the Runtime turn stays tight.
    expect(rows).toHaveLength(5);
    expect(rows[0]).not.toHaveClass("mt-1");
    expect(rows[0]).not.toHaveClass("mt-4");
    expect(rows[1]).toHaveClass("mt-1");
    expect(rows[2]).toHaveClass("mt-1");
    expect(rows[3]).toHaveClass("mt-1");
    expect(rows[4]).toHaveClass("mt-4");
    expect(screen.getAllByTestId("transcript-turn-header").map((header) => header.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining("你"), expect.stringContaining("Codex")]),
    );
  });

  it("renders tool call arguments as labeled fields rather than a raw JSON envelope", async () => {
    stubTaskDetailApi({}, [
      {
        id: "assistant-tool-call",
        seq: 7,
        continuation: 1,
        kind: "tool_call",
        role: "assistant",
        tool_call_id: "call-1",
        tool_name: "Bash",
        details: { input: { command: "curl http://localhost:3000", timeout: 30 } },
        created_at: "2026-01-01T00:00:01Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("Command")).toBeInTheDocument();
    expect(screen.getByText("Timeout")).toBeInTheDocument();
    expect(screen.getByText("30")).toBeInTheDocument();
    expect(screen.queryByText(/"input":/)).not.toBeInTheDocument();
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
    expect(screen.getByTestId("task-session-header")).toHaveClass("py-2");
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
      "h-10",
      "w-10",
    );
  });

  it("exposes Timeline top and bottom controls that move its scroll viewport", async () => {
    const user = userEvent.setup();
    const scrollTo = vi.fn();
    Object.defineProperty(Element.prototype, "scrollTo", { value: scrollTo, configurable: true });
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=timeline");

    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    const viewport = screen.getByTestId("timeline-workspace");
    Object.defineProperty(viewport, "scrollHeight", { value: 9000, configurable: true });

    await user.click(screen.getByRole("button", { name: "Scroll Timeline to top" }));
    expect(scrollTo).toHaveBeenLastCalledWith(expect.objectContaining({ top: 0 }));

    await user.click(screen.getByRole("button", { name: "Scroll Timeline to bottom" }));
    expect(scrollTo).toHaveBeenLastCalledWith(expect.objectContaining({ top: 9000 }));
  });

  it("re-settles the conversation bottom after scroll-to-latest re-anchors the window", async () => {
    const user = userEvent.setup();
    stubTaskDetailApi();
    const scrollTo = vi.fn();
    const restoreViewport = mockConversationViewport({ scrollHeight: () => 20000, scrollTo });

    try {
      renderPage();

      const workspace = await screen.findByTestId("conversation-workspace");
      // Reading upward ends auto-follow so the button click must restore it.
      fireEvent.wheel(workspace, { deltaY: -120 });
      await waitFor(() =>
        screen.getByRole("button", { name: /scroll to latest \(auto-follow off\)/i }),
      );
      const callsBeforeClick = scrollTo.mock.calls.length;

      await user.click(screen.getByRole("button", { name: /scroll to latest/i }));

      // The click settles synchronously on the pre-render layout; flipping
      // auto-follow re-renders with the end-anchored window, and the settle
      // effect must run again so the view lands on the real bottom.
      await waitFor(() => {
        expect(scrollTo.mock.calls.length).toBeGreaterThan(callsBeforeClick + 1);
      });
      expect(scrollTo).toHaveBeenLastCalledWith(expect.objectContaining({ top: 20000 }));
    } finally {
      restoreViewport();
    }
  });

  it("shows the compact continuation summary when present", async () => {
    stubTaskDetailApi();

    renderPage();

    const summary = await screen.findByTestId("continuation-summary");
    expect(summary).toHaveTextContent("continuation #1");
    expect(summary).toHaveTextContent("runtime codex");
    expect(summary).toHaveTextContent("runner docker");
    expect(summary).not.toHaveTextContent("continuation status");
    expect(summary).not.toHaveTextContent("native session");
    expect(summary).toHaveAttribute("title", expect.stringContaining("status: completed"));
    expect(summary).toHaveAttribute("title", expect.stringContaining("native session: captured"));
  });

  it("shows the prior terminal continuation next to the current writable one", async () => {
    stubTaskDetailApi({
      status: "running",
      active_continuation: {
        id: "cont-1",
        task_id: "task-1",
        number: 1,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "running",
      },
      latest_continuation: {
        id: "cont-2",
        task_id: "task-1",
        number: 2,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "completed",
      },
    });

    renderPage();

    expect(await screen.findByText("continuation #1")).toBeInTheDocument();
    const summary = screen.getByTestId("continuation-summary");
    expect(summary).not.toHaveTextContent("continuation status");
    expect(summary).toHaveAttribute("title", expect.stringContaining("prior terminal: #2 (completed)"));
  });

  it("does not repeat the same continuation as its own prior terminal", async () => {
    stubTaskDetailApi({
      status: "running",
      active_continuation: {
        id: "cont-2",
        task_id: "task-1",
        number: 2,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "running",
      },
      latest_continuation: {
        id: "cont-2",
        task_id: "task-1",
        number: 2,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "running",
      },
    });

    renderPage();

    const summary = await screen.findByTestId("continuation-summary");
    expect(summary).toHaveTextContent("continuation #2");
    expect(summary).not.toHaveTextContent("continuation status");
    expect(summary).toHaveAttribute("title", expect.not.stringContaining("prior terminal"));
  });

  it("shows pending and failed Harness Steering states in the composer", async () => {
    stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_steer_available: true,
        native_steer_state: "pending",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
    });

    renderPage();

    expect(await screen.findByTestId("steer-pending-badge")).toHaveTextContent("steering pending…");
    expect(screen.queryByTestId("steer-failed-badge")).not.toBeInTheDocument();
  });

  it("shows a failed Harness Steering state with a stable reason surface", async () => {
    stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_steer_available: true,
        native_steer_state: "failed",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
    });

    renderPage();

    expect(await screen.findByTestId("steer-failed-badge")).toHaveTextContent("steering failed");
    expect(screen.queryByTestId("steer-pending-badge")).not.toBeInTheDocument();
  });

  it("shows action-required reason and lets the operator select replacement delivery", async () => {
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      runtime_controls: {
        native_steer_available: true,
        interrupt_steer_available: true,
        native_steer_mode: "in_turn_steer",
        native_steer_state: "action_required",
        native_steer_error_code: "active_turn_not_steerable",
        native_steer_error: "active provider Runtime Turn is not steerable",
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
    });
    const user = userEvent.setup();

    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent("active provider Runtime Turn is not steerable");
    await user.click(screen.getByRole("button", { name: "Use interrupt and replace" }));
    await user.type(screen.getByRole("textbox", { name: "Task message" }), "continue in a replacement turn");
    await user.click(screen.getByRole("button", { name: "Interrupt and replace" }));

    const steerCall = fetchMock.mock.calls.find(([input]) =>
      String(input).endsWith("/api/projects/project-1/tasks/task-1/steer"),
    );
    expect(JSON.parse(String(steerCall?.[1]?.body))).toMatchObject({
      message: "continue in a replacement turn",
      force_replace: true,
    });
  });

  it("keeps the compact continuation summary bounded below the title row", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("continuation #1")).toBeInTheDocument();
    const summary = screen.getByTestId("continuation-summary");
    expect(summary).toHaveClass("min-w-0", "overflow-hidden", "whitespace-nowrap");
    const blackboardChip = screen.getByTestId("blackboard-conclusion-state");
    expect(blackboardChip).toHaveClass("min-w-0", "shrink");
  });

  it("shows native resume and queue steering controls", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByRole("button", { name: /Resume$/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Resume with handoff/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Resume and send/ })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveClass("focus-visible:ring-2");
    expect(await screen.findByRole("option", { name: "MiMo" })).toBeInTheDocument();
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
    await screen.findByRole("option", { name: "MiMo" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    await screen.findByRole("option", { name: "mimo-v2-pro" });
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
    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("mimo");
      expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveValue("mimo-v2-flash");
      expect(screen.getByRole("combobox", { name: "Continuation reasoning effort" })).toHaveValue("medium");
    });

    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model" }), "mimo-v2-pro");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation reasoning effort" }), "xhigh");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "stronger turn");
    await user.click(screen.getByRole("button", { name: /Interrupt and send/ }));

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

  // #146: Claude Code shares the same Task Conversation turn-selection contract.
  it("keeps Claude same-provider model and effort changes on the native steer route", async () => {
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
        runtime_provider: "claude_code",
        turn_selection: {
          model_provider_id: "anthropic",
          model: "claude-sonnet",
          reasoning_effort: "medium",
        },
      },
      latest_continuation: {
        id: "cont-1",
        task_id: "task-1",
        number: 1,
        runtime_profile_id: "profile-1",
        runtime_provider: "claude_code",
        runner: "sandbox",
        status: "running",
        native_session_id: "claude-sess-1",
        started_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:05Z",
      },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("Conversation should be hidden by default");
    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("anthropic");
      expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveValue("claude-sonnet");
      expect(screen.getByRole("combobox", { name: "Continuation reasoning effort" })).toHaveValue("medium");
    });

    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model" }), "claude-opus");
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation reasoning effort" }), "xhigh");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "claude stronger turn");
    await user.click(screen.getByRole("button", { name: /Interrupt and send/ }));

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
      message: "claude stronger turn",
      model_provider_id: "anthropic",
      model: "claude-opus",
      reasoning_effort: "xhigh",
    });
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("anthropic");
    expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveValue("claude-opus");
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
    await user.click(screen.getByRole("button", { name: /Interrupt and send/ }));

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
    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("anthropic");
    });
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

  it("uses native steer for Pi when switching to a projected model provider", async () => {
    // ADR 0015: Pi already projected launch-ready providers; cross-provider
    // turns stay on /steer without stop/resume when the target is in the set.
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
        runtime_provider: "pi",
        projected_model_provider_ids: ["anthropic", "mimo"],
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
    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("anthropic");
    });
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "continue with mimo on pi");
    // Pi must not present the restart-oriented provider-switch label.
    expect(screen.queryByRole("button", { name: "Switch provider and resume" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Steer current turn" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Steer current turn" }));

    const postPaths = fetchMock.mock.calls
      .filter(([, init]) => init?.method === "POST")
      .map(([input]) => String(input));
    expect(postPaths).toEqual(["/api/projects/project-1/tasks/task-1/steer"]);
    const steerCall = fetchMock.mock.calls.find(([input]) => String(input).endsWith("/steer"));
    expect(JSON.parse(String(steerCall?.[1]?.body))).toMatchObject({
      message: "continue with mimo on pi",
      model_provider_id: "mimo",
      model: "mimo-v2-flash",
      reasoning_effort: "high",
    });
    expect(postPaths.some((path) => path.endsWith("/stop") || path.endsWith("/resume"))).toBe(false);
  });

  it("restarts Pi provider switch when projected_model_provider_ids is missing (legacy)", async () => {
    // Fail closed: legacy tasks without projected set metadata must not send
    // native cross-provider and surface 409 — use queue/stop/resume instead.
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
        runtime_provider: "pi",
        // projected_model_provider_ids intentionally omitted
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
    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("anthropic");
    });
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "legacy pi switch");
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

  it("restarts Pi provider switch when target is outside projected_model_provider_ids", async () => {
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
        runtime_provider: "pi",
        projected_model_provider_ids: ["anthropic"],
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
    await screen.findByRole("option", { name: "MiMo" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    await user.type(screen.getByPlaceholderText("Focus on admin.example.com next…"), "outside projected set");
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
    await screen.findByRole("option", { name: "MiMo" });
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

    // In-app confirm dialog replaces the native window.confirm; the action is
    // not posted until the styled confirm is accepted.
    expect(confirm).not.toHaveBeenCalled();
    const stopDialog = screen.getByRole("alertdialog", { name: /Stop task/i });
    expect(stopDialog).toBeInTheDocument();
    // Long Task Goals belong in the body, not the short dialog title.
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

  it("shows Runtime Activity separately from Task lifecycle", async () => {
    stubTaskDetailApi({
      status: "running",
      runtime_activity: { liveness: "live", turn_activity: "busy" },
    });

    renderPage();

    expect(await screen.findByTestId("runtime-activity")).toHaveTextContent(/Runtime busy/i);
    expect(screen.getByText("Running")).toBeInTheDocument();
  });

  it("offers Finish Task only when controls.finish_available is true", async () => {
    stubTaskDetailApi({
      status: "running",
      // Live idle alone must NOT enable Finish without finish_available.
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
      // Exactly one queue — no second message invent on resume.
      expect(postPaths.filter((path) => path.endsWith("/steer/queue"))).toHaveLength(1);
    },
  );

  it("ignores stale out-of-order poll responses", async () => {
    type Parked = { resolve: (value: Response) => void; signal?: AbortSignal };
    const parked: Parked[] = [];
    const taskPayload = (liveness: string, turn: string) =>
      new Response(
        JSON.stringify({
          id: "task-1",
          project_id: "project-1",
          goal: "Inspect task view",
          status: "running",
          runner: "sandbox",
          runtime_profile_id: "profile-1",
          run_controls: {},
          scope_snapshot: {},
          runtime_activity: { liveness, turn_activity: turn },
          runtime_controls: {
            native_resume_available: false,
            resume_available: false,
            queue_steer_available: true,
            interrupt_steer_available: true,
            native_session_captured: true,
            same_runtime_provider_only: true,
            runtime_provider: "codex",
          },
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:05Z",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
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
      if (url.includes("/api/runtime-profiles") || url.includes("/api/model-providers") || url.includes("/api/runtime-plugins")) {
        return new Response(JSON.stringify({ profiles: [], providers: [], plugins: [] }), {
          status: 200, headers: { "Content-Type": "application/json" },
        });
      }
      if (url.match(/\/api\/projects\/project-1\/tasks\/task-1$/)) {
        return new Promise<Response>((resolve, reject) => {
          const signal = init?.signal;
          const onAbort = () => reject(new DOMException("Aborted", "AbortError"));
          if (signal?.aborted) {
            onAbort();
            return;
          }
          signal?.addEventListener("abort", onAbort, { once: true });
          parked.push({
            signal,
            resolve: (response) => {
              signal?.removeEventListener("abort", onAbort);
              resolve(response);
            },
          });
        });
      }
      return new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    // StrictMode double-mounts: first load is aborted when the second starts.
    renderPage();
    await waitFor(() => expect(parked.length).toBeGreaterThanOrEqual(1));

    const stale = parked[0];
    const latest = parked[parked.length - 1];

    // Newer generation wins with idle; later stale busy must not overwrite.
    latest.resolve(taskPayload("live", "idle"));
    expect(await screen.findByTestId("runtime-activity")).toHaveTextContent(/Runtime idle/i);

    if (stale !== latest && !stale.signal?.aborted) {
      stale.resolve(taskPayload("live", "busy"));
      await waitFor(() =>
        expect(screen.getByTestId("runtime-activity")).toHaveTextContent(/Runtime idle/i),
      );
    } else if (stale !== latest) {
      // Aborted stale request rejects; UI must remain on the latest idle state.
      expect(screen.getByTestId("runtime-activity")).toHaveTextContent(/Runtime idle/i);
    }
  });

  it("suspends polling while the tab is hidden and resumes when it returns", async () => {
    // Pure fake timers (no shouldAdvanceTime): the running-task poll is the
    // feature under test, and real-timer leakage via waitFor could fire it
    // nondeterministically. Flush the initial multi-endpoint load via microtasks.
    vi.useFakeTimers();
    try {
      const { fetchMock } = stubTaskDetailApi({
        status: "running",
        runtime_activity: { liveness: "live", turn_activity: "busy" },
        latest_continuation: {
          id: "cont-1",
          task_id: "task-1",
          number: 1,
          runtime_profile_id: "profile-1",
          runtime_provider: "codex",
          runner: "sandbox",
          status: "running",
          native_session_id: "sess-1",
          started_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:05Z",
        },
      });

      renderPage();
      // The task detail page fires several loads on mount; flush them with
      // microtask ticks until the runtime-activity badge renders.
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByTestId("runtime-activity")).toBeInTheDocument();
      const callsAfterMount = fetchMock.mock.calls.length;

      // While visible + running, polls land on the 1s cadence.
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      expect(fetchMock.mock.calls.length).toBeGreaterThan(callsAfterMount);
      const callsAfterVisiblePoll = fetchMock.mock.calls.length;

      // Hidden: polling must suspend even though the task is still running.
      act(() => setDocumentVisibility("hidden"));
      await act(async () => {
        vi.advanceTimersByTimeAsync(10000);
      });
      expect(fetchMock.mock.calls.length).toBe(callsAfterVisiblePoll);

      // Visible again: polling resumes within the 1s cadence.
      act(() => setDocumentVisibility("visible"));
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      expect(fetchMock.mock.calls.length).toBeGreaterThan(callsAfterVisiblePoll);
    } finally {
      vi.useRealTimers();
    }
  });

  it("loads the complete Timeline and Transcript without a cursor on the first load", async () => {
    const { fetchMock } = stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    const calls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(calls.some((url) => url.includes("/timeline"))).toBe(true);
    expect(calls.some((url) => url.includes("/transcript"))).toBe(true);
    for (const url of calls) {
      expect(url).not.toContain("after=");
    }
  });

  it("polls the Timeline and Transcript with the last committed cursor", async () => {
    vi.useFakeTimers();
    try {
      const timelineBody = {
        task_id: "task-1",
        items: [{ seq: 1, type: "text", content: "Timeline opened first", created_at: "2026-01-01T00:00:00Z" }],
        cursor: 1,
      };
      const transcriptBody = {
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
        cursor: 1,
      };
      const { fetchMock } = stubTaskDetailApi({ status: "running" }, undefined, timelineBody, transcriptBody);

      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByText("Conversation should be hidden by default")).toBeInTheDocument();

      // The first poll requests only the events after the committed cursor.
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      const pollURLs = fetchMock.mock.calls.map(([input]) => String(input));
      expect(pollURLs.some((url) => url.includes("/timeline?after=1"))).toBe(true);
      expect(pollURLs.some((url) => url.includes("/transcript?after=1"))).toBe(true);

      // New events arrive; the daemon answers the next poll from cursor 1.
      timelineBody.items = [
        { seq: 1, type: "text", content: "Timeline opened first", created_at: "2026-01-01T00:00:00Z" },
        { seq: 2, type: "text", content: "Timeline item two", created_at: "2026-01-01T00:00:01Z" },
      ];
      timelineBody.cursor = 2;
      transcriptBody.entries = [
        {
          id: "entry-1",
          seq: 1,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "Conversation should be hidden by default",
          created_at: "2026-01-01T00:00:00Z",
        },
        {
          id: "entry-2",
          seq: 2,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "Fresh transcript row",
          created_at: "2026-01-01T00:00:01Z",
        },
      ];
      transcriptBody.cursor = 2;

      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      const deltaURLs = fetchMock.mock.calls.map(([input]) => String(input));
      expect(deltaURLs.some((url) => url.includes("/timeline?after=2"))).toBe(true);
      expect(deltaURLs.some((url) => url.includes("/transcript?after=2"))).toBe(true);
      expect(screen.getByText("Fresh transcript row")).toBeInTheDocument();
      // The merge kernel deduplicates by stable identity: no repeated rows.
      expect(screen.getAllByText("Conversation should be hidden by default")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the cursor stable when an idle poll returns an empty delta", async () => {
    vi.useFakeTimers();
    try {
      const timelineBody = {
        task_id: "task-1",
        items: [{ seq: 1, type: "text", content: "Timeline opened first", created_at: "2026-01-01T00:00:00Z" }],
        cursor: 1,
      };
      const transcriptBody = {
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
        cursor: 1,
      };
      const { fetchMock } = stubTaskDetailApi({ status: "running" }, undefined, timelineBody, transcriptBody);

      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });

      // Idle daemon: empty delta, cursor unchanged.
      timelineBody.items = [];
      transcriptBody.entries = [];

      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      const urls = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urls.some((url) => url.includes("/timeline?after=1"))).toBe(true);
      expect(urls.some((url) => url.includes("/transcript?after=1"))).toBe(true);
      // The cursor never advanced: no poll asked for events after 1.
      expect(urls.some((url) => url.includes("after=2"))).toBe(false);
      expect(screen.getAllByText("Conversation should be hidden by default")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });
});

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
    // The second owner's history stays pending until the test releases it, so
    // the ordering assertion below is deterministic instead of racing a timer.
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

    // The old owner's rows disappear before the new owner's history loads.
    await waitFor(() => {
      expect(screen.queryByText("First task content")).not.toBeInTheDocument();
    });
    expect(screen.queryByText("First task timeline")).not.toBeInTheDocument();
    expect(screen.queryByText("Second task content")).not.toBeInTheDocument();
    releaseSecondOwnerHistory();
    expect(await screen.findByText("Second task content")).toBeInTheDocument();
    // Nothing from the first owner leaks into the second owner's workspace.
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
      // The goal row was never re-rendered as a duplicate.
      expect(screen.getAllByTestId("transcript-row")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("pages older transcript rows with a before cursor and preserves the reading anchor", async () => {
    const { fetchMock } = stubTaskDetailApi(
      {},
      undefined,
      { task_id: "task-1", items: [], cursor: 0 },
      {
        task_id: "task-1",
        entries: [
          { id: "entry-51", seq: 51, continuation: 1, kind: "message", role: "assistant", text: "Recent message", created_at: "2026-01-01T00:00:00Z" },
          { id: "entry-52", seq: 52, continuation: 1, kind: "message", role: "assistant", text: "Newest message", created_at: "2026-01-01T00:00:00Z" },
        ],
        cursor: 52,
        has_older: true,
      },
      {
        "/api/projects/project-1/tasks/task-1/transcript?before=": {
          task_id: "task-1",
          entries: [
            { id: "entry-1", seq: 1, continuation: 1, kind: "message", role: "user", text: "Oldest message", created_at: "2026-01-01T00:00:00Z" },
            { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "user", text: "Second message", created_at: "2026-01-01T00:00:00Z" },
          ],
          cursor: 52,
          has_older: false,
        },
      },
    );
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("Recent message")).toBeInTheDocument();

    const viewport = screen.getByTestId("conversation-workspace");
    Object.defineProperty(viewport, "scrollTop", { value: 720, writable: true, configurable: true });

    await user.click(screen.getByTestId("load-older-transcript"));
    expect(await screen.findByText("Oldest message")).toBeInTheDocument();
    const calls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(calls.some((url) => url.includes("/transcript?before=51"))).toBe(true);
    // The prepended page pushed the visible rows down by 2 × 72px.
    expect(viewport.scrollTop).toBe(720 + 2 * 72);
    // The boundary page reached the beginning: the affordance disappears.
    expect(screen.queryByTestId("load-older-transcript")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("transcript-row")).toHaveLength(4);
  });

  it("merges a backward page with a concurrent live delta without duplicates", async () => {
    vi.useFakeTimers();
    try {
      const transcriptBody: Record<string, unknown> = {
        task_id: "task-1",
        entries: [
          { id: "entry-51", seq: 51, continuation: 1, kind: "message", role: "assistant", text: "Recent message", created_at: "2026-01-01T00:00:00Z" },
          { id: "entry-52", seq: 52, continuation: 1, kind: "message", role: "assistant", text: "Newest message", created_at: "2026-01-01T00:00:00Z" },
        ],
        cursor: 52,
        has_older: true,
      };
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        // The backward page resolves slowly, so a live poll lands first.
        if (url.includes("/transcript?before=")) {
          await new Promise((resolve) => setTimeout(resolve, 2000));
          return json({
            task_id: "task-1",
            entries: [
              { id: "entry-1", seq: 1, continuation: 1, kind: "message", role: "user", text: "Oldest message", created_at: "2026-01-01T00:00:00Z" },
              { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "user", text: "Second message", created_at: "2026-01-01T00:00:00Z" },
            ],
            cursor: 52,
            has_older: false,
          });
        }
        if (url.includes("/transcript")) return json(transcriptBody);
        if (url.includes("/timeline")) return json({ task_id: "task-1", items: [], cursor: 0 });
        if (url.includes("/api/runtime-profiles")) return json({ profiles: [] });
        if (url.includes("/api/model-providers")) return json({ providers: [] });
        if (url.includes("/api/runtime-plugins")) return json({ plugins: [] });
        return json(taskRecord("task-1", "Inspect task view", "running"));
      });
      vi.stubGlobal("fetch", fetchMock);
      Object.defineProperty(Element.prototype, "scrollIntoView", { value: vi.fn(), configurable: true });
      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByText("Newest message")).toBeInTheDocument();
      await act(async () => {
        await screen.getByTestId("load-older-transcript").click();
      });

      // The live poll lands first and appends a fresh row past the cursor.
      transcriptBody.entries = [
        ...(transcriptBody.entries as Record<string, unknown>[]),
        { id: "entry-53", seq: 53, continuation: 1, kind: "message", role: "assistant", text: "Fresh live row", created_at: "2026-01-01T00:00:02Z" },
      ];
      transcriptBody.cursor = 53;
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      expect(screen.getByText("Fresh live row")).toBeInTheDocument();

      // The delayed backward page then lands without duplicating anything.
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      expect(screen.getByText("Oldest message")).toBeInTheDocument();
      const rows = screen.getAllByTestId("transcript-row");
      expect(rows).toHaveLength(5);
      expect(screen.getAllByText("Newest message")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("preserves a committed backward page when a live poll resolves after it", async () => {
    vi.useFakeTimers();
    try {
      const transcriptBody: Record<string, unknown> = {
        task_id: "task-1",
        entries: [
          { id: "entry-51", seq: 51, continuation: 1, kind: "message", role: "assistant", text: "Recent message", created_at: "2026-01-01T00:00:00Z" },
          { id: "entry-52", seq: 52, continuation: 1, kind: "message", role: "assistant", text: "Newest message", created_at: "2026-01-01T00:00:00Z" },
        ],
        cursor: 52,
        has_older: true,
      };
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        // The backward page resolves first (300ms); the live poll starts at
        // 1000ms and resolves at 1500ms, landing after the page but before
        // the next poll tick would abort it.
        if (url.includes("/transcript?before=")) {
          await new Promise((resolve) => setTimeout(resolve, 300));
          return json({
            task_id: "task-1",
            entries: [
              { id: "entry-1", seq: 1, continuation: 1, kind: "message", role: "user", text: "Oldest message", created_at: "2026-01-01T00:00:00Z" },
              { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "user", text: "Second message", created_at: "2026-01-01T00:00:00Z" },
            ],
            cursor: 52,
            has_older: false,
          });
        }
        if (url.includes("/transcript") && url.includes("after=")) {
          await new Promise((resolve) => setTimeout(resolve, 500));
        }
        if (url.includes("/transcript")) return json(transcriptBody);
        if (url.includes("/timeline")) return json({ task_id: "task-1", items: [], cursor: 0 });
        if (url.includes("/api/runtime-profiles")) return json({ profiles: [] });
        if (url.includes("/api/model-providers")) return json({ providers: [] });
        if (url.includes("/api/runtime-plugins")) return json({ plugins: [] });
        return json(taskRecord("task-1", "Inspect task view", "running"));
      });
      vi.stubGlobal("fetch", fetchMock);
      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByText("Newest message")).toBeInTheDocument();
      await act(async () => {
        await screen.getByTestId("load-older-transcript").click();
      });

      // The backward page commits first (t=300ms).
      await act(async () => {
        vi.advanceTimersByTimeAsync(400);
      });
      expect(screen.getByText("Oldest message")).toBeInTheDocument();

      // A live delta arrives and the poll resolves last; the committed page
      // must survive the poll's merge instead of being wiped by it.
      transcriptBody.entries = [
        ...(transcriptBody.entries as Record<string, unknown>[]),
        { id: "entry-53", seq: 53, continuation: 1, kind: "message", role: "assistant", text: "Fresh live row", created_at: "2026-01-01T00:00:02Z" },
      ];
      transcriptBody.cursor = 53;
      // Staged advances so the poll's timer-resolved continuation and the
      // React render it schedules both flush before the next assertion.
      await act(async () => {
        vi.advanceTimersByTimeAsync(600);
      });
      await act(async () => {
        vi.advanceTimersByTimeAsync(900);
      });
      expect(screen.getByText("Fresh live row")).toBeInTheDocument();
      expect(screen.getByText("Oldest message")).toBeInTheDocument();
      const rows = screen.getAllByTestId("transcript-row");
      expect(rows).toHaveLength(5);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows an unseen indicator for live deltas while reading older history", async () => {
    vi.useFakeTimers();
    try {
      const transcriptBody = {
        task_id: "task-1",
        entries: [{
          id: "entry-1",
          seq: 1,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "Visible row",
          created_at: "2026-01-01T00:00:00Z",
        }],
        cursor: 1,
      };
      stubTaskDetailApi(
        { status: "running" },
        undefined,
        { task_id: "task-1", items: [], cursor: 0 },
        transcriptBody,
      );
      const scrollTo = vi.fn();
      Object.defineProperty(Element.prototype, "scrollTo", { value: scrollTo, configurable: true });
      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByText("Visible row")).toBeInTheDocument();

      // The operator scrolls away from the tail while a live delta arrives.
      // jsdom has no layout, so the scroll geometry is stubbed: a large
      // scrollHeight with a small scrollTop means "not near the bottom".
      const viewport = screen.getByTestId("conversation-workspace");
      Object.defineProperty(viewport, "scrollTop", { value: 5000, writable: true, configurable: true });
      Object.defineProperty(viewport, "scrollHeight", { value: 100000, writable: true, configurable: true });
      await act(async () => {
        viewport.dispatchEvent(new Event("wheel"));
        viewport.dispatchEvent(new Event("scroll"));
      });
      transcriptBody.entries = [
        ...transcriptBody.entries,
        { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "assistant", text: "Fresh unseen row", created_at: "2026-01-01T00:00:01Z" },
      ];
      transcriptBody.cursor = 2;
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });

      const pill = screen.getByTestId("unseen-transcript-indicator");
      expect(pill).toHaveTextContent("1 new message");
      expect(screen.getByText("Fresh unseen row")).toBeInTheDocument();

      // Clicking the indicator jumps to the tail and dismisses it.
      await act(async () => {
        pill.click();
      });
      expect(scrollTo).toHaveBeenCalled();
      expect(screen.queryByTestId("unseen-transcript-indicator")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the Conversation reading position and auto-follow state across a Timeline round trip", async () => {
    const user = userEvent.setup();
    stubTaskDetailApi();
    renderPage();

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    const viewport = screen.getByTestId("conversation-workspace");
    Object.defineProperty(viewport, "scrollTop", { value: 1200, writable: true, configurable: true });
    Object.defineProperty(viewport, "scrollHeight", { value: 100000, configurable: true });
    Object.defineProperty(viewport, "clientHeight", { value: 600, configurable: true });
    await act(async () => {
      viewport.dispatchEvent(new Event("wheel"));
      viewport.dispatchEvent(new Event("scroll"));
    });

    expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Timeline" }));
    await user.click(screen.getByRole("button", { name: "Conversation" }));

    expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeInTheDocument();
    expect(screen.getByTestId("conversation-workspace").scrollTop).toBe(1200);
  });

  it("pins the initial Conversation to the real scroll-container bottom", async () => {
    const scrollHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
    try {
      Object.defineProperty(HTMLElement.prototype, "scrollHeight", { get: () => 10000, configurable: true });
      const scrollTo = vi.fn();
      Object.defineProperty(Element.prototype, "scrollTo", { value: scrollTo, configurable: true });
      stubTaskDetailApi();
      renderPage();

      expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
      expect(scrollTo).toHaveBeenLastCalledWith({ top: 10000, behavior: "auto" });
      expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow on)" })).toBeInTheDocument();
    } finally {
      if (scrollHeight) {
        Object.defineProperty(HTMLElement.prototype, "scrollHeight", scrollHeight);
      } else {
        delete (HTMLElement.prototype as unknown as { scrollHeight?: number }).scrollHeight;
      }
    }
  });

  it("does not pull the operator back after they start scrolling toward the beginning of a page-long last message", async () => {
    vi.useFakeTimers();
    const restoreViewport = mockConversationViewport({ scrollHeight: () => 2400 });
    try {
      const transcriptBody = {
        task_id: "task-1",
        entries: [{
          id: "entry-1",
          seq: 1,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "A page-long final message",
          created_at: "2026-01-01T00:00:00Z",
        }],
        cursor: 1,
      };
      stubTaskDetailApi(
        { status: "running" },
        undefined,
        { task_id: "task-1", items: [], cursor: 0 },
        transcriptBody,
      );
      renderPage();

      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      const viewport = screen.getByTestId("conversation-workspace");
      expect(viewport.scrollTop).toBe(1800);

      await act(async () => {
        viewport.dispatchEvent(new WheelEvent("wheel", { deltaY: -80 }));
        viewport.scrollTop = 1720;
        viewport.dispatchEvent(new Event("scroll"));
      });
      expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeInTheDocument();

      transcriptBody.entries = [
        ...transcriptBody.entries,
        {
          id: "entry-2",
          seq: 2,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "A live delta after the operator started reading upward",
          created_at: "2026-01-01T00:00:01Z",
        },
      ];
      transcriptBody.cursor = 2;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500);
      });

      expect(viewport.scrollTop).toBe(1720);
    } finally {
      vi.useRealTimers();
      restoreViewport();
    }
  });

  it("keeps initial auto-follow on when virtual rendering changes the scroll height", async () => {
    let scrollHeight = 14400;
    let grewAfterFirstPin = false;
    const restoreViewport = mockConversationViewport({
      scrollHeight: () => scrollHeight,
      scrollTo(this: HTMLElement, options: ScrollToOptions) {
        this.scrollTop = Math.max(0, Math.min(options.top ?? 0, scrollHeight - this.clientHeight));
        if (!grewAfterFirstPin) {
          grewAfterFirstPin = true;
          scrollHeight += 452;
        }
        this.dispatchEvent(new Event("scroll"));
      },
    });
    try {
      const entries = Array.from({ length: 200 }, (_, index) => ({
        id: `entry-${index + 1}`,
        seq: index + 1,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: `Conversation row ${index + 1}`,
        created_at: "2026-01-01T00:00:00Z",
      }));
      stubTaskDetailApi({}, entries);
      renderPage();

      const viewport = await screen.findByTestId("conversation-workspace");
      await waitFor(() => {
        expect(scrollHeight - viewport.scrollTop - viewport.clientHeight).toBe(0);
      });
      expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow on)" })).toBeInTheDocument();
    } finally {
      restoreViewport();
    }
  });

  it("keeps the latest Conversation rows mounted when variable-height content moves the bottom", async () => {
    let scrollHeight = 14400;
    const restoreViewport = mockConversationViewport({ scrollHeight: () => scrollHeight });
    try {
      const entries = Array.from({ length: 200 }, (_, index) => ({
        id: `entry-${index + 1}`,
        seq: index + 1,
        continuation: 1,
        kind: "message" as const,
        role: "assistant" as const,
        text: `Variable-height Conversation row ${index + 1}`,
        created_at: "2026-01-01T00:00:00Z",
      }));
      stubTaskDetailApi({}, entries);
      renderPage();

      const viewport = await screen.findByTestId("conversation-workspace");
      await act(async () => {
        viewport.scrollTo({ top: scrollHeight, behavior: "auto" });
        await Promise.resolve();
      });
      const latestRow = await screen.findByText("Variable-height Conversation row 200");

      // Expanding tool output can make the real bottom much deeper than the
      // fixed row estimate. Auto-follow moves to that real bottom.
      scrollHeight = 22000;
      await act(async () => {
        viewport.scrollTo({ top: scrollHeight, behavior: "auto" });
        await Promise.resolve();
      });

      await waitFor(() => {
        expect(screen.getByText("Variable-height Conversation row 200")).toBe(latestRow);
      });
      expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow on)" })).toBeInTheDocument();
    } finally {
      restoreViewport();
    }
  });

  it("keeps the rendered conversation DOM bounded on long histories", async () => {
    const entries = Array.from({ length: 400 }, (_, index) => ({
      id: `entry-${index + 1}`,
      seq: index + 1,
      continuation: 1,
      kind: "message" as const,
      role: "assistant" as const,
      text: `Row ${index + 1}`,
      created_at: "2026-01-01T00:00:00Z",
    }));
    stubTaskDetailApi(
      {},
      undefined,
      { task_id: "task-1", items: [], cursor: 0 },
      { task_id: "task-1", entries, cursor: 400 },
    );
    renderPage();
    expect(await screen.findByText("Row 400")).toBeInTheDocument();

    // A measurable viewport activates the virtualized window; jsdom has no
    // layout, so the measurement is stubbed on the container itself.
    const viewport = screen.getByTestId("conversation-workspace");
    Object.defineProperty(viewport, "clientHeight", { value: 600, configurable: true });
    viewport.dispatchEvent(new Event("scroll"));

    await waitFor(() => {
      const rows = screen.getAllByTestId("transcript-row");
      expect(rows.length).toBeLessThan(400);
      expect(rows.length).toBeGreaterThan(0);
    });
    // Rows outside the window are not in the DOM while the loaded history
    // stays available in state.
    expect(screen.queryByText("Row 250")).not.toBeInTheDocument();
  });

  it("pages older timeline rows through the newest-first footer", async () => {
    const { fetchMock } = stubTaskDetailApi(
      {},
      undefined,
      {
        task_id: "task-1",
        items: [{ seq: 51, type: "text", content: "Timeline recent", created_at: "2026-01-01T00:00:00Z" }],
        cursor: 51,
        has_older: true,
      },
      { task_id: "task-1", entries: [], cursor: 0 },
      {
        "/api/projects/project-1/tasks/task-1/timeline?before=": {
          task_id: "task-1",
          items: [{ seq: 1, type: "text", content: "Timeline oldest", created_at: "2026-01-01T00:00:00Z" }],
          cursor: 51,
          has_older: false,
        },
      },
    );
    const user = userEvent.setup();
    renderPage("/projects/project-1/tasks/task-1?view=timeline");
    expect(await screen.findByText("Timeline recent")).toBeInTheDocument();

    await user.click(screen.getByTestId("load-older-timeline"));
    expect(await screen.findByText("Timeline oldest")).toBeInTheDocument();
    const calls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(calls.some((url) => url.includes("/timeline?before=51"))).toBe(true);
    expect(screen.queryByTestId("load-older-timeline")).not.toBeInTheDocument();
  });
});
