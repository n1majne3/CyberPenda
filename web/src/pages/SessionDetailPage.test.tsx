import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";

describe("SessionDetailPage", () => {
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
    expect(screen.getByText(/this is non-project mode/i)).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Session Events" })).toBeInTheDocument();
    expect(screen.getAllByText("Review the service")).toHaveLength(2);
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
    });
    const user = userEvent.setup();

    render(
      <MemoryRouter initialEntries={["/sessions/session-2"]}>
        <Routes>
          <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByRole("heading", { name: "Conversation" })).toBeInTheDocument();
    expect(screen.getByText("Runtime ready")).toBeInTheDocument();
    const composer = screen.getByRole("textbox", { name: /continue the conversation/i });
    await user.type(composer, "Continue with the auth flow");
    await user.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions/session-2/messages",
        expect.objectContaining({ method: "POST", body: JSON.stringify({ message: "Continue with the auth flow" }) }),
      );
    });
  });
});
