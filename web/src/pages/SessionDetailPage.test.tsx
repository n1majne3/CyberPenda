import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";
import { codexPlugin, mimoProvider } from "./SessionDetailPage.testSupport";

describe("SessionDetailPage", () => {


  it("opens the Session Blackboard inside the Runtime workspace", async () => {
    const fetchMock = mockApi({
      "/api/sessions/session-fgs/transcript": { entries: [] },
      "/api/sessions/session-fgs/timeline": { items: [] },
      "/api/sessions/session-fgs": {
        id: "session-fgs", title: "Inspect service", lifecycle: "open",
        blackboard_protocol: "fgs", run_controls: { blackboard_mode: "working_graph" },
      },
      "/api/v2/sessions/session-fgs/fgs/status": { action_required: 0, receipts: [] },
      "/api/v2/sessions/session-fgs/fgs": {
        revision: 1, edges: [], nodes: [
          { key: "goal:inspect", type: "goal", version: 1, title: "Inspect access", state: "active" },
          { key: "step:read", type: "step", version: 1, action: "Read service response", state: "done" },
          { key: "fact:response", type: "fact", version: 1, summary: "Service responds" },
        ],
      },
    });
    const user = userEvent.setup();
    render(<MemoryRouter initialEntries={["/sessions/session-fgs"]}><Routes>
      <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
    </Routes></MemoryRouter>);
    await user.click(await screen.findByRole("button", { name: "Blackboard" }));
    expect(await screen.findByText("Service responds")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Blackboard" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("textbox", { name: "Session message" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/api/v2/projects/"))).toBe(false);
    await user.click(screen.getByRole("button", { name: "Conversation" }));
    expect(screen.queryByRole("region", { name: "FGS Blackboard" })).not.toBeInTheDocument();
  });

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
    expect(screen.getByTestId("task-detail-shell")).toHaveClass("mx-auto", "w-full", "max-w-[1280px]", "lg:px-[45px]");
    expect(screen.getByTestId("task-workspace")).toBeInTheDocument();
    expect(screen.getByTestId("task-composer")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Session message" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Continuation model provider" })).toBeInTheDocument();
    expect(await screen.findByRole("option", { name: "MiMo" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Conversation" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Timeline" })).toHaveAttribute("aria-pressed", "false");
  });


  it("shows Disabled mode and hides Session Blackboard surfaces", async () => {
    mockApi({
      "/api/sessions/session-disabled/transcript": { session_id: "session-disabled", entries: [] },
      "/api/sessions/session-disabled/timeline": { session_id: "session-disabled", items: [] },
      "/api/sessions/session-disabled": {
        id: "session-disabled",
        title: "Inspect without Blackboard",
        lifecycle: "open",
        run_controls: { blackboard_mode: "disabled" },
        blackboard_conclusion: {
          mode: "disabled",
          state: "action_required",
          error_code: "semantic_conclusion_repair_exhausted",
          retry_available: true,
        },
        created_at: "2026-08-01T01:00:00Z",
        updated_at: "2026-08-01T01:00:00Z",
        last_activity_at: "2026-08-01T01:00:00Z",
      },
    });

    render(
      <MemoryRouter initialEntries={["/sessions/session-disabled"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByTestId("blackboard-conclusion-state")).toHaveTextContent("Blackboard: Disabled");
    expect(screen.queryByRole("button", { name: "Blackboard" })).not.toBeInTheDocument();
    expect(screen.queryByRole("alert", { name: "Blackboard conclusion requires attention" })).not.toBeInTheDocument();
    expect(screen.getByText(/non-project mode/i)).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Session message" })).toBeInTheDocument();
    expect(screen.getByLabelText("Attach files to Session message")).toBeInTheDocument();
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
});
