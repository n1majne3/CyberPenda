import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";
import { codexPlugin, mimoProvider } from "./SessionDetailPage.testSupport";

describe("SessionDetailPage", () => {

  it.each(["success", "stop", "messages"])("preserves attachments when switching Model Provider: %s", async (outcome) => {
    const fetchMock = mockApi({
      "/api/sessions/session-switch/transcript": { entries: [] },
      "/api/sessions/session-switch/timeline": { items: [] },
      "/api/sessions/session-switch": {
        id: "session-switch", title: "Review evidence", lifecycle: "open",
        runtime_controls: {
          runtime_provider: "codex", native_steer_available: true, queue_steer_available: true,
          turn_selection: { model_provider_id: "original", model: "original-model" },
        },
        active_continuation: {
          id: "continuation-1", session_id: "session-switch", number: 1,
          runtime_provider: "codex", runner: "host", status: "running",
        },
      },
      "/api/model-providers": { providers: [
        { ...mimoProvider, id: "original", name: "Original", catalog: { manual: ["original-model"], default_model: "original-model" } },
        mimoProvider,
      ] },
      "/api/runtime-plugins": { plugins: [codexPlugin] },
    });
    const defaultFetch = fetchMock.getMockImplementation()!;
    const requests: { url: string; body: BodyInit | null | undefined }[] = [];
    fetchMock.mockImplementation(async (input, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "POST") {
        requests.push({ url, body: init.body });
        if (outcome !== "success" && url.endsWith(`/${outcome}`)) {
          return new Response(JSON.stringify({ error: "Provider switch failed" }), { status: 500 });
        }
      }
      return defaultFetch(input);
    });
    const user = userEvent.setup();
    render(<MemoryRouter initialEntries={["/sessions/session-switch"]}><Routes>
      <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
    </Routes></MemoryRouter>);
    await screen.findByRole("option", { name: "MiMo" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Continuation model provider" }), "mimo");
    const file = new File(["evidence for the new provider"], "evidence.txt", { type: "text/plain" });
    const upload = screen.getByLabelText<HTMLInputElement>("Attach files to Session message");
    await user.upload(upload, file);
    const composer = screen.getByRole("textbox", { name: "Session message" });
    await user.type(composer, "Review this evidence");
    await user.click(screen.getByRole("button", { name: "Switch provider and resume" }));

    if (outcome === "success") {
      await waitFor(() => expect(composer).toHaveValue(""));
      expect(upload.files).toHaveLength(0);
    } else {
      expect(await screen.findByText("Provider switch failed")).toBeInTheDocument();
      expect(composer).toHaveValue("Review this evidence");
      expect(upload.files?.[0]).toBe(file);
      expect(screen.getByText("1 attached")).toBeInTheDocument();
    }
    expect(requests.map(({ url }) => url)).toEqual(outcome === "stop"
      ? ["/api/sessions/session-switch/stop"]
      : ["/api/sessions/session-switch/stop", "/api/sessions/session-switch/messages"]);
    if (outcome !== "stop") {
      const form = requests[1]!.body;
      expect(form).toBeInstanceOf(FormData);
      if (!(form instanceof FormData)) throw new Error("Expected a multipart Session message");
      expect(JSON.parse(String(form.get("payload")))).toMatchObject({
        message: "Review this evidence", model_provider_id: "mimo", model: "mimo-v2",
      });
      expect(form.getAll("attachments")).toEqual([file]);
    }
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
    await user.click(screen.getByRole("button", { name: "Interrupt and send" }));

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
      expect(new Headers(call?.[1]?.headers).get("Idempotency-Key")).toBeTruthy();
    });
  });


  it("shows Session action-required recovery and sends explicit replacement", async () => {
    const fetchMock = mockApi({
      "/api/sessions/session-action/transcript": { session_id: "session-action", entries: [] },
      "/api/sessions/session-action/timeline": { session_id: "session-action", items: [] },
      "/api/sessions/session-action": {
        id: "session-action", title: "Recover steering", lifecycle: "open",
        runtime_controls: {
          native_steer_available: true, interrupt_steer_available: true, queue_steer_available: true,
          native_steer_mode: "in_turn_steer", native_steer_state: "action_required",
          native_steer_error_code: "target_turn_changed", native_steer_error: "target provider Runtime Turn changed",
          native_session_captured: true, runtime_provider: "codex",
        },
        active_continuation: {
          id: "continuation-action", session_id: "session-action", number: 1, runtime_profile_id: "profile-1",
          runtime_provider: "codex", runner: "host", status: "running",
          started_at: "2026-08-01T01:00:00Z", updated_at: "2026-08-01T01:00:00Z",
        },
        created_at: "2026-08-01T01:00:00Z", updated_at: "2026-08-01T01:00:00Z", last_activity_at: "2026-08-01T01:00:00Z",
      },
    });
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/sessions/session-action"]}>
        <Routes><Route path="/sessions/:sessionId" element={<SessionDetailPage />} /></Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("target provider Runtime Turn changed");
    await user.click(screen.getByRole("button", { name: "Use interrupt and replace" }));
    await user.type(screen.getByRole("textbox", { name: "Session message" }), "replace it");
    await user.click(screen.getByRole("button", { name: "Interrupt and replace" }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([input]) => String(input).endsWith("/api/sessions/session-action/steer"));
      expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({ message: "replace it", force_replace: true });
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
});
