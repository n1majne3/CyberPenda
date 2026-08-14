import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";

function InAppNavigationButton({ to, label }: { to: string; label: string }) {
  const navigate = useNavigate();
  return <button type="button" onClick={() => navigate(to)}>{label}</button>;
}

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
      "/api/sessions/session-shared-ui/transcript": {
        session_id: "session-shared-ui",
        entries: [
          {
            id: "event-1-message",
            seq: 1,
            continuation: 1,
            kind: "message",
            role: "user",
            text: "Reuse the task workspace",
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
    expect(await screen.findByRole("option", { name: "MiMo" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Conversation" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Timeline" })).toHaveAttribute("aria-pressed", "false");
  });

  it("renders owner-local Events without Project Scope semantics", async () => {
    mockApi({
      "/api/sessions/session-1/transcript": {
        session_id: "session-1",
        entries: [
          {
            id: "event-1-message",
            seq: 1,
            continuation: 1,
            kind: "message",
            role: "user",
            text: "Review the service",
            created_at: "2026-08-01T01:00:00Z",
          },
          {
            id: "event-2-attachment",
            seq: 2,
            continuation: 1,
            kind: "attachment",
            role: "system",
            text: "Attached notes.txt",
            details: { filename: "notes.txt", size: 12 },
            created_at: "2026-08-01T01:01:00Z",
          },
        ],
      },
      "/api/sessions/session-1/timeline": {
        session_id: "session-1",
        items: [
          {
            seq: 1,
            type: "lifecycle",
            content: "Attached notes.txt (12 bytes)",
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
      "/api/sessions/session-2/transcript": {
        session_id: "session-2",
        entries: [
          {
            id: "conversation-1-message",
            seq: 1,
            continuation: 1,
            kind: "message",
            role: "user",
            text: "Start the review",
            created_at: "2026-08-01T01:00:00Z",
          },
          {
            id: "runtime-1-message",
            seq: 2,
            continuation: 1,
            kind: "message",
            role: "assistant",
            text: "Runtime ready",
            created_at: "2026-08-01T01:01:00Z",
          },
        ],
      },
      "/api/sessions/session-2/timeline": {
        session_id: "session-2",
        items: [
          {
            seq: 1,
            type: "text",
            content: "Runtime ready",
            created_at: "2026-08-01T01:01:00Z",
          },
        ],
      },
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
    await screen.findByRole("option", { name: "MiMo" });
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
      "/api/sessions/session-interrupt/transcript": { session_id: "session-interrupt", entries: [] },
      "/api/sessions/session-interrupt/timeline": { session_id: "session-interrupt", items: [] },
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
      "/api/sessions/session-queue/transcript": { session_id: "session-queue", entries: [] },
      "/api/sessions/session-queue/timeline": { session_id: "session-queue", items: [] },
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
      "/api/sessions/session-attachments/transcript": { session_id: "session-attachments", entries: [] },
      "/api/sessions/session-attachments/timeline": { session_id: "session-attachments", items: [] },
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
      "/api/sessions/session-retry/transcript": { session_id: "session-retry", entries: [] },
      "/api/sessions/session-retry/timeline": { session_id: "session-retry", items: [] },
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

describe("SessionDetailPage Runtime Owner History Window (#202)", () => {
  const sessionRecord = (id: string, title: string) => ({
    id,
    title,
    lifecycle: "open",
    runtime_activity: { liveness: "live", turn_activity: "idle" },
    runtime_controls: {
      native_resume_available: false,
      native_steer_available: true,
      queue_steer_available: true,
      interrupt_steer_available: true,
      native_session_captured: true,
      runtime_provider: "codex",
    },
    active_continuation: {
      id: `cont-${id}`,
      session_id: id,
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
  });

  it("replaces Session history after in-app navigation between owners", async () => {
    const user = userEvent.setup();
    const json = (body: unknown) => new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
    // The second Session's history stays pending until the test releases it,
    // so the ordering assertion below is deterministic instead of racing a timer.
    let releaseSessionBHistory!: () => void;
    const sessionBHistory = new Promise<void>((resolve) => {
      releaseSessionBHistory = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("sessions/session-b/") && (url.includes("/timeline") || url.includes("/transcript"))) {
        await sessionBHistory;
      }
      if (url.includes("sessions/session-a/transcript")) {
        return json({ session_id: "session-a", entries: [{ id: "a-1", seq: 1, continuation: 1, kind: "message", role: "assistant", text: "Session A content", created_at: "2026-08-01T01:00:00Z" }], cursor: 1 });
      }
      if (url.includes("sessions/session-a/timeline")) {
        return json({ session_id: "session-a", items: [], cursor: 0 });
      }
      if (url.includes("sessions/session-b/transcript")) {
        return json({ session_id: "session-b", entries: [{ id: "b-1", seq: 1, continuation: 1, kind: "message", role: "assistant", text: "Session B content", created_at: "2026-08-01T01:00:00Z" }], cursor: 1 });
      }
      if (url.includes("sessions/session-b/timeline")) {
        return json({ session_id: "session-b", items: [], cursor: 0 });
      }
      if (url.includes("sessions/session-a")) return json(sessionRecord("session-a", "Session A"));
      if (url.includes("sessions/session-b")) return json(sessionRecord("session-b", "Session B"));
      if (url.includes("/api/runtime-profiles")) return json({ profiles: [{ id: "profile-1", name: "Codex", provider: "codex", fields: {} }] });
      if (url.includes("/api/model-providers")) return json({ providers: [mimoProvider] });
      if (url.includes("/api/runtime-plugins")) return json({ plugins: [codexPlugin] });
      return json({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(
      <MemoryRouter initialEntries={["/sessions/session-a"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
        <InAppNavigationButton to="/sessions/session-b" label="Open session B" />
      </MemoryRouter>,
    );

    expect(await screen.findByText("Session A content")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open session B" }));

    // The old owner's rows disappear before the new owner's history loads.
    await waitFor(() => {
      expect(screen.queryByText("Session A content")).not.toBeInTheDocument();
    });
    expect(screen.queryByText("Session B content")).not.toBeInTheDocument();
    releaseSessionBHistory();
    expect(await screen.findByText("Session B content")).toBeInTheDocument();
    expect(screen.queryByText("Session A content")).not.toBeInTheDocument();
  });

  it("sends an explicit after=0 on idle Session polls after an empty initial read", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = mockApi({
        "/api/sessions/session-zero/transcript": { session_id: "session-zero", entries: [], cursor: 0 },
        "/api/sessions/session-zero/timeline": { session_id: "session-zero", items: [], cursor: 0 },
        "/api/sessions/session-zero": sessionRecord("session-zero", "Empty session"),
        "/api/runtime-profiles": { profiles: [{ id: "profile-1", name: "Codex", provider: "codex", fields: {} }] },
        "/api/model-providers": { providers: [mimoProvider] },
        "/api/runtime-plugins": { plugins: [codexPlugin] },
      });
      render(
        <MemoryRouter initialEntries={["/sessions/session-zero"]}>
          <Routes>
            <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
          </Routes>
        </MemoryRouter>,
      );
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

  it("pages older Session transcript rows with a before cursor", async () => {
    const fetchMock = mockApi({
      "/api/sessions/session-old/transcript?before=": {
        session_id: "session-old",
        entries: [
          { id: "old-1", seq: 1, continuation: 1, kind: "message", role: "user", text: "Oldest session row", created_at: "2026-08-01T01:00:00Z" },
          { id: "old-2", seq: 2, continuation: 1, kind: "message", role: "user", text: "Second session row", created_at: "2026-08-01T01:00:00Z" },
        ],
        cursor: 52,
        has_older: false,
      },
      "/api/sessions/session-old/transcript": {
        session_id: "session-old",
        entries: [
          { id: "new-51", seq: 51, continuation: 1, kind: "message", role: "assistant", text: "Recent session row", created_at: "2026-08-01T01:00:00Z" },
          { id: "new-52", seq: 52, continuation: 1, kind: "message", role: "assistant", text: "Newest session row", created_at: "2026-08-01T01:00:00Z" },
        ],
        cursor: 52,
        has_older: true,
      },
      "/api/sessions/session-old/timeline": { session_id: "session-old", items: [], cursor: 0 },
      "/api/sessions/session-old": sessionRecord("session-old", "Old session"),
      "/api/runtime-profiles": { profiles: [{ id: "profile-1", name: "Codex", provider: "codex", fields: {} }] },
      "/api/model-providers": { providers: [mimoProvider] },
      "/api/runtime-plugins": { plugins: [codexPlugin] },
    });
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/sessions/session-old"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("Recent session row")).toBeInTheDocument();
    await user.click(screen.getByTestId("load-older-transcript"));
    expect(await screen.findByText("Oldest session row")).toBeInTheDocument();
    const calls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(calls.some((url) => url.includes("/transcript?before=51"))).toBe(true);
    expect(screen.queryByTestId("load-older-transcript")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("transcript-row")).toHaveLength(4);
  });
});
