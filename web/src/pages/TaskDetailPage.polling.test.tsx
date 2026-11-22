import { act, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage, setDocumentVisibility, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });


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

    renderPage();
    await waitFor(() => expect(parked.length).toBeGreaterThanOrEqual(1));

    const stale = parked[0];
    const latest = parked[parked.length - 1];

    
    latest.resolve(taskPayload("live", "idle"));
    expect(await screen.findByTestId("runtime-activity")).toHaveTextContent(/Runtime idle/i);

    if (stale !== latest && !stale.signal?.aborted) {
      stale.resolve(taskPayload("live", "busy"));
      await waitFor(() =>
        expect(screen.getByTestId("runtime-activity")).toHaveTextContent(/Runtime idle/i),
      );
    } else if (stale !== latest) {
      
      expect(screen.getByTestId("runtime-activity")).toHaveTextContent(/Runtime idle/i);
    }
  });


  it("suspends polling while the tab is hidden and resumes when it returns", async () => {
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
      
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByTestId("runtime-activity")).toBeInTheDocument();
      const callsAfterMount = fetchMock.mock.calls.length;

      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      expect(fetchMock.mock.calls.length).toBeGreaterThan(callsAfterMount);
      const callsAfterVisiblePoll = fetchMock.mock.calls.length;

      
      act(() => setDocumentVisibility("hidden"));
      await act(async () => {
        vi.advanceTimersByTimeAsync(10000);
      });
      expect(fetchMock.mock.calls.length).toBe(callsAfterVisiblePoll);

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

      
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      const pollURLs = fetchMock.mock.calls.map(([input]) => String(input));
      expect(pollURLs.some((url) => url.includes("/timeline?after=1"))).toBe(true);
      expect(pollURLs.some((url) => url.includes("/transcript?after=1"))).toBe(true);

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

      timelineBody.items = [];
      transcriptBody.entries = [];

      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      const urls = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urls.some((url) => url.includes("/timeline?after=1"))).toBe(true);
      expect(urls.some((url) => url.includes("/transcript?after=1"))).toBe(true);
      
      expect(urls.some((url) => url.includes("after=2"))).toBe(false);
      expect(screen.getAllByText("Conversation should be hidden by default")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });
});
