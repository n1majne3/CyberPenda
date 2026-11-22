import { InAppNavigationButton } from "@/test/RouteControls";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";
import { codexPlugin, mimoProvider } from "./SessionDetailPage.testSupport";

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
