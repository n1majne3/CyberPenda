import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";

const mimoProvider = {
  id: "mimo",
  name: "MiMo",
  base_url: "https://api.example.test/v1",
  protocols: ["openai_responses"],
  api_key_env: "MIMO_API_KEY",
  catalog: { manual: ["mimo-v2"], default_model: "mimo-v2" },
};

const codexPlugin = {
  schema_version: 1,
  id: "codex",
  name: "Codex",
  binary: { default: "codex" },
  capabilities: { sandbox: true, host: true, mcp_config: true, streaming_transcript: true, resume: true },
  model_provider: { requirement: "required", supported_protocols: ["openai_responses"] },
  profile_schema: { fields: [] },
  config_projection: { primitive: "codex" },
  launch: { args: [] },
  transcript: { parser: "codex" },
};

describe("SessionDetailPage", () => {
  it("uses the same runtime workspace surface as Project Tasks", async () => {
    mockApi({
      "/api/sessions/session-shared-ui/events": {
        events: [
          {
            id: "event-1",
            session_id: "session-shared-ui",
            seq: 1,
            kind: "conversation",
            payload: { role: "user", text: "Reuse the task workspace" },
            created_at: "2026-08-01T01:00:00Z",
          },
        ],
      },
      "/api/sessions/session-shared-ui": {
        id: "session-shared-ui",
        title: "Reuse the task workspace",
        lifecycle: "open",
        runtime_controls: {
          native_resume_available: false,
          native_steer_available: true,
          queue_steer_available: true,
          interrupt_steer_available: true,
          native_session_captured: true,
          runtime_provider: "codex",
          turn_selection: {
            model_provider_id: "mimo",
            model: "mimo-v2",
            reasoning_effort: "high",
          },
        },
        active_continuation: {
          id: "continuation-1",
          session_id: "session-shared-ui",
          number: 1,
          runtime_profile_id: "profile-1",
          runtime_provider: "codex",
          runner: "host",
          status: "running",
          started_at: "2026-08-01T01:00:00Z",
          updated_at: "2026-08-01T01:00:00Z",
        },
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:00:00Z",
        last_activity_at: "2026-08-01T01:00:00Z",
      },
      "/api/runtime-profiles": { profiles: [{ id: "profile-1", name: "Codex", provider: "codex", fields: {} }] },
      "/api/model-providers": {
        providers: [mimoProvider],
      },
      "/api/runtime-plugins": {
        plugins: [codexPlugin],
      },
    });

    render(
      <MemoryRouter initialEntries={["/sessions/session-shared-ui"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByTestId("task-session-header")).toHaveTextContent("Reuse the task workspace");
    expect(screen.getByTestId("task-workspace")).toBeInTheDocument();
    expect(screen.getByTestId("task-composer")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Session message" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "MiMo" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Conversation" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Timeline" })).toHaveAttribute("aria-pressed", "false");
  });

  it("renders owner-local Events without Project Scope semantics", async () => {
    mockApi({
      "/api/sessions/session-1/events": {
        events: [
          {
            id: "event-1",
            session_id: "session-1",
            seq: 1,
            kind: "conversation",
            payload: { role: "user", text: "Review the service" },
            created_at: "2026-08-01T01:00:00Z",
          },
          {
            id: "event-2",
            session_id: "session-1",
            seq: 2,
            kind: "attachment",
            payload: { filename: "notes.txt", size: 12 },
            created_at: "2026-08-01T01:01:00Z",
          },
        ],
      },
      "/api/sessions/session-1": {
        id: "session-1",
        title: "Review the service",
        lifecycle: "open",
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:00:00Z",
        last_activity_at: "2026-08-01T01:01:00Z",
      },
    });

    render(
      <MemoryRouter initialEntries={["/sessions/session-1"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByRole("heading", { level: 1, name: "Review the service" })).toBeInTheDocument();
    expect(screen.getByText(/non-project mode/i)).toBeInTheDocument();
    expect(screen.getByText(/attached notes\.txt/i)).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Timeline" }));
    expect(screen.getByText(/attached notes\.txt/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Archive" })).toBeInTheDocument();
  });

  it("keeps the transcript separate and sends follow-up input through the Session Runtime route", async () => {
    const fetchMock = mockApi({
      "/api/sessions/session-2/conversation": {
        events: [
          {
            id: "conversation-1",
            session_id: "session-2",
            seq: 1,
            kind: "conversation",
            payload: { role: "user", text: "Start the review" },
            created_at: "2026-08-01T01:00:00Z",
          },
        ],
      },
      "/api/sessions/session-2/timeline": {
        events: [
          {
            id: "timeline-1",
            session_id: "session-2",
            seq: 2,
            kind: "runtime_output",
            payload: { stream: "stdout", text: "Runtime ready" },
            created_at: "2026-08-01T01:01:00Z",
          },
        ],
      },
      "/api/sessions/session-2/events": { events: [] },
      "/api/sessions/session-2": {
        id: "session-2",
        title: "Start the review",
        lifecycle: "open",
        runtime_activity: { liveness: "live", turn_activity: "idle" },
        runtime_controls: { native_resume_available: false, native_steer_available: true, queue_steer_available: true, interrupt_steer_available: true, native_session_captured: true, runtime_provider: "codex" },
        active_continuation: { id: "continuation-1", session_id: "session-2", number: 1, runtime_profile_id: "profile-1", runtime_provider: "codex", runner: "host", status: "running", started_at: "2026-08-01T01:00:00Z", updated_at: "2026-08-01T01:00:00Z" },
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:01:00Z",
        last_activity_at: "2026-08-01T01:01:00Z",
      },
      "/api/model-providers": {
        providers: [mimoProvider],
      },
      "/api/runtime-plugins": {
        plugins: [codexPlugin],
      },
    });
    const user = userEvent.setup();

    render(
      <MemoryRouter initialEntries={["/sessions/session-2"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByRole("button", { name: "Conversation" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getAllByTestId("transcript-row")).toHaveLength(2);
    expect(screen.getAllByTestId("transcript-row")[1]).toHaveTextContent("Runtime ready");
    expect(screen.getByRole("button", { name: "Queue message" })).toBeInTheDocument();
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    expect(screen.getByRole("combobox", { name: "Continuation model" })).toHaveValue("mimo-v2");
    const composer = screen.getByRole("textbox", { name: "Session message" });
    await user.type(composer, "Continue with the auth flow");
    expect(screen.getByRole("button", { name: "Queue message" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Switch provider and resume" }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions/session-2/messages",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            message: "Continue with the auth flow",
            reasoning_effort: "high",
            model_provider_id: "mimo",
            model: "mimo-v2",
            model_override: "mimo-v2",
          }),
        }),
      );
    });
  });

  it("uses the Session steer route for an interrupt-only Runtime", async () => {
    const fetchMock = mockApi({
      "/api/sessions/session-interrupt/events": { events: [] },
      "/api/sessions/session-interrupt/conversation": { events: [] },
      "/api/sessions/session-interrupt/timeline": { events: [] },
      "/api/sessions/session-interrupt": {
        id: "session-interrupt",
        title: "Interrupt the current turn",
        lifecycle: "open",
        runtime_controls: {
          native_resume_available: true,
          native_steer_available: false,
          queue_steer_available: true,
          interrupt_steer_available: true,
          native_session_captured: true,
          runtime_provider: "codex",
        },
        active_continuation: {
          id: "continuation-interrupt",
          session_id: "session-interrupt",
          number: 1,
          runtime_profile_id: "profile-1",
          runtime_provider: "codex",
          runner: "host",
          status: "running",
          started_at: "2026-08-01T01:00:00Z",
          updated_at: "2026-08-01T01:00:00Z",
        },
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:00:00Z",
        last_activity_at: "2026-08-01T01:00:00Z",
      },
    });
    const user = userEvent.setup();

    render(
      <MemoryRouter initialEntries={["/sessions/session-interrupt"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    await user.upload(
      await screen.findByLabelText("Attach files to Session message"),
      new File(["interrupt evidence"], "interrupt-notes.txt", { type: "text/plain" }),
    );
    await user.type(
      screen.getByRole("textbox", { name: "Session message" }),
      "Replace the current direction",
    );
    await user.click(screen.getByRole("button", { name: "Interrupt and resume" }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([input]) => String(input).endsWith("/api/sessions/session-interrupt/steer"));
      expect(call).toBeDefined();
      const form = call?.[1]?.body as FormData;
      expect(form).toBeInstanceOf(FormData);
      expect(form.get("payload")).toBe(JSON.stringify({
        message: "Replace the current direction",
        reasoning_effort: "high",
      }));
      expect((form.get("attachments") as File).name).toBe("interrupt-notes.txt");
    });
  });

  it("queues Session input with the shared model selection and attachments", async () => {
    const fetchMock = mockApi({
      "/api/sessions/session-queue/events": { events: [] },
      "/api/sessions/session-queue/conversation": { events: [] },
      "/api/sessions/session-queue/timeline": { events: [] },
      "/api/sessions/session-queue": {
        id: "session-queue",
        title: "Queue the evidence",
        lifecycle: "open",
        runtime_controls: {
          native_resume_available: false,
          native_steer_available: true,
          queue_steer_available: true,
          interrupt_steer_available: true,
          native_session_captured: true,
          runtime_provider: "codex",
          turn_selection: {
            model_provider_id: "mimo",
            model: "mimo-v2",
            reasoning_effort: "high",
          },
        },
        active_continuation: {
          id: "continuation-queue",
          session_id: "session-queue",
          number: 1,
          runtime_profile_id: "profile-1",
          runtime_provider: "codex",
          runner: "host",
          status: "running",
          started_at: "2026-08-01T01:00:00Z",
          updated_at: "2026-08-01T01:00:00Z",
        },
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:00:00Z",
        last_activity_at: "2026-08-01T01:00:00Z",
      },
      "/api/model-providers": { providers: [mimoProvider] },
      "/api/runtime-plugins": { plugins: [codexPlugin] },
    });
    const user = userEvent.setup();

    render(
      <MemoryRouter initialEntries={["/sessions/session-queue"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toHaveValue("mimo");
    });
    await user.upload(
      screen.getByLabelText("Attach files to Session message"),
      new File(["queued evidence"], "evidence.txt", { type: "text/plain" }),
    );
    await user.type(screen.getByRole("textbox", { name: "Session message" }), "Review this evidence next");
    await user.click(screen.getByRole("button", { name: "Queue message" }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([input]) => String(input).endsWith("/api/sessions/session-queue/steer/queue"));
      expect(call).toBeDefined();
      const form = call?.[1]?.body as FormData;
      expect(form).toBeInstanceOf(FormData);
      expect(form.get("payload")).toBe(JSON.stringify({
        message: "Review this evidence next",
        reasoning_effort: "high",
        model_provider_id: "mimo",
        model: "mimo-v2",
        model_override: "mimo-v2",
      }));
      expect((form.get("attachments") as File).name).toBe("evidence.txt");
    });
  });

  it("keeps Session attachments inside the shared composer", async () => {
    const fetchMock = mockApi({
      "/api/sessions/session-attachments/events": { events: [] },
      "/api/sessions/session-attachments/conversation": { events: [] },
      "/api/sessions/session-attachments/timeline": { events: [] },
      "/api/sessions/session-attachments": {
        id: "session-attachments",
        title: "Attach notes",
        lifecycle: "open",
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:00:00Z",
        last_activity_at: "2026-08-01T01:00:00Z",
      },
    });
    const user = userEvent.setup();

    render(
      <MemoryRouter initialEntries={["/sessions/session-attachments"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    await user.upload(
      await screen.findByLabelText("Attach files to Session message"),
      new File(["owner-local notes"], "notes.txt", { type: "text/plain" }),
    );
    await user.type(screen.getByRole("textbox", { name: "Session message" }), "Use the attached notes");
    await user.click(screen.getByRole("button", { name: "Resume and send" }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([input]) => String(input).endsWith("/api/sessions/session-attachments/messages"));
      expect(call).toBeDefined();
      const form = call?.[1]?.body as FormData;
      expect(form).toBeInstanceOf(FormData);
      expect(form.get("payload")).toBe(JSON.stringify({ message: "Use the attached notes", reasoning_effort: "high" }));
      expect((form.get("attachments") as File).name).toBe("notes.txt");
    });
  });

  it("shows Session-local conclusion recovery and retries with an idempotency key", async () => {
    const fetchMock = mockApi({
      "/api/sessions/session-retry/events": { events: [] },
      "/api/sessions/session-retry/conversation": { events: [] },
      "/api/sessions/session-retry/timeline": { events: [] },
      "/api/sessions/session-retry/blackboard-conclusion/retry": {
        id: "session-retry",
        title: "Needs recovery",
        lifecycle: "open",
        run_controls: { blackboard_conclusion_mode: "assisted" },
        blackboard_conclusion: { mode: "assisted", state: "pending", retry_available: false },
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:00:00Z",
        last_activity_at: "2026-08-01T01:00:00Z",
      },
      "/api/sessions/session-retry": {
        id: "session-retry",
        title: "Needs recovery",
        lifecycle: "open",
        run_controls: { blackboard_conclusion_mode: "assisted" },
        blackboard_conclusion: {
          mode: "assisted",
          state: "action_required",
          error_code: "semantic_conclusion_repair_exhausted",
          retry_available: true,
        },
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:00:00Z",
        last_activity_at: "2026-08-01T01:00:00Z",
      },
    });
    const user = userEvent.setup();

    render(
      <MemoryRouter initialEntries={["/sessions/session-retry"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByTestId("blackboard-conclusion-state")).toHaveTextContent("assisted");
    expect(screen.getByRole("alert")).toHaveTextContent("semantic_conclusion_repair_exhausted");
    await user.click(screen.getByRole("button", { name: /retry blackboard conclusion/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions/session-retry/blackboard-conclusion/retry",
        expect.objectContaining({
          method: "POST",
          headers: expect.objectContaining({ "idempotency-key": expect.stringMatching(/^blackboard-retry-/) }),
        }),
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("blackboard-conclusion-state")).toHaveTextContent("pending");
    });
    expect(screen.getByRole("heading", { level: 1, name: "Needs recovery" })).toBeInTheDocument();
  });
});
