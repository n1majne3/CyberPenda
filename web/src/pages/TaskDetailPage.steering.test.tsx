import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
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
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("mimo");
    expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveValue("mimo-v2-pro");
    expect(screen.getByRole("combobox", { name: "Continuation reasoning effort" })).toHaveValue("xhigh");
  });


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
});
