import { render, screen } from "@testing-library/react";
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
});
