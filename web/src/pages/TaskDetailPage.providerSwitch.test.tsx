import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
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
});
