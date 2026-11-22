import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage Runtime Owner History Window (#202)", () => {

  const taskRecord = (id: string, goal: string, status = "completed") => ({
    id,
    project_id: "project-1",
    goal,
    status,
    runner: "sandbox",
    runtime_profile_id: "profile-1",
    run_controls: {},
    scope_snapshot: {},
    runtime_controls: {
      native_resume_available: true,
      resume_available: true,
      queue_steer_available: true,
      interrupt_steer_available: false,
      native_session_captured: true,
      same_runtime_provider_only: true,
      runtime_provider: "codex",
    },
    latest_continuation: {
      id: `cont-${id}`,
      task_id: id,
      number: 1,
      runtime_profile_id: "profile-1",
      runtime_provider: "codex",
      runner: "sandbox",
      status: "completed",
      started_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:05Z",
      ended_at: "2026-01-01T00:00:05Z",
    },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:05Z",
  });

  const json = (body: unknown) => new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });


  it("pages older transcript rows with a before cursor and preserves the reading anchor", async () => {
    const { fetchMock } = stubTaskDetailApi(
      {},
      undefined,
      { task_id: "task-1", items: [], cursor: 0 },
      {
        task_id: "task-1",
        entries: [
          { id: "entry-51", seq: 51, continuation: 1, kind: "message", role: "assistant", text: "Recent message", created_at: "2026-01-01T00:00:00Z" },
          { id: "entry-52", seq: 52, continuation: 1, kind: "message", role: "assistant", text: "Newest message", created_at: "2026-01-01T00:00:00Z" },
        ],
        cursor: 52,
        has_older: true,
      },
      {
        "/api/projects/project-1/tasks/task-1/transcript?before=": {
          task_id: "task-1",
          entries: [
            { id: "entry-1", seq: 1, continuation: 1, kind: "message", role: "user", text: "Oldest message", created_at: "2026-01-01T00:00:00Z" },
            { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "user", text: "Second message", created_at: "2026-01-01T00:00:00Z" },
          ],
          cursor: 52,
          has_older: false,
        },
      },
    );
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("Recent message")).toBeInTheDocument();

    const viewport = screen.getByTestId("conversation-workspace");
    Object.defineProperty(viewport, "scrollTop", { value: 720, writable: true, configurable: true });

    await user.click(screen.getByTestId("load-older-transcript"));
    expect(await screen.findByText("Oldest message")).toBeInTheDocument();
    const calls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(calls.some((url) => url.includes("/transcript?before=51"))).toBe(true);
    
    
    
    
    expect(viewport.scrollTop).toBe(720);
    
    expect(screen.queryByTestId("load-older-transcript")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("transcript-row")).toHaveLength(4);
  });


  it("merges a backward page with a concurrent live delta without duplicates", async () => {
    vi.useFakeTimers();
    try {
      const transcriptBody: Record<string, unknown> = {
        task_id: "task-1",
        entries: [
          { id: "entry-51", seq: 51, continuation: 1, kind: "message", role: "assistant", text: "Recent message", created_at: "2026-01-01T00:00:00Z" },
          { id: "entry-52", seq: 52, continuation: 1, kind: "message", role: "assistant", text: "Newest message", created_at: "2026-01-01T00:00:00Z" },
        ],
        cursor: 52,
        has_older: true,
      };
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        
        if (url.includes("/transcript?before=")) {
          await new Promise((resolve) => setTimeout(resolve, 2000));
          return json({
            task_id: "task-1",
            entries: [
              { id: "entry-1", seq: 1, continuation: 1, kind: "message", role: "user", text: "Oldest message", created_at: "2026-01-01T00:00:00Z" },
              { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "user", text: "Second message", created_at: "2026-01-01T00:00:00Z" },
            ],
            cursor: 52,
            has_older: false,
          });
        }
        if (url.includes("/transcript")) return json(transcriptBody);
        if (url.includes("/timeline")) return json({ task_id: "task-1", items: [], cursor: 0 });
        if (url.includes("/api/runtime-profiles")) return json({ profiles: [] });
        if (url.includes("/api/model-providers")) return json({ providers: [] });
        if (url.includes("/api/runtime-plugins")) return json({ plugins: [] });
        return json(taskRecord("task-1", "Inspect task view", "running"));
      });
      vi.stubGlobal("fetch", fetchMock);
      Object.defineProperty(Element.prototype, "scrollIntoView", { value: vi.fn(), configurable: true });
      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByText("Newest message")).toBeInTheDocument();
      await act(async () => {
        await screen.getByTestId("load-older-transcript").click();
      });

      
      transcriptBody.entries = [
        ...(transcriptBody.entries as Record<string, unknown>[]),
        { id: "entry-53", seq: 53, continuation: 1, kind: "message", role: "assistant", text: "Fresh live row", created_at: "2026-01-01T00:00:02Z" },
      ];
      transcriptBody.cursor = 53;
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      expect(screen.getByText("Fresh live row")).toBeInTheDocument();

      
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });
      expect(screen.getByText("Oldest message")).toBeInTheDocument();
      const rows = screen.getAllByTestId("transcript-row");
      expect(rows).toHaveLength(5);
      expect(screen.getAllByText("Newest message")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });


  it("preserves a committed backward page when a live poll resolves after it", async () => {
    vi.useFakeTimers();
    try {
      const transcriptBody: Record<string, unknown> = {
        task_id: "task-1",
        entries: [
          { id: "entry-51", seq: 51, continuation: 1, kind: "message", role: "assistant", text: "Recent message", created_at: "2026-01-01T00:00:00Z" },
          { id: "entry-52", seq: 52, continuation: 1, kind: "message", role: "assistant", text: "Newest message", created_at: "2026-01-01T00:00:00Z" },
        ],
        cursor: 52,
        has_older: true,
      };
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        
        
        
        if (url.includes("/transcript?before=")) {
          await new Promise((resolve) => setTimeout(resolve, 300));
          return json({
            task_id: "task-1",
            entries: [
              { id: "entry-1", seq: 1, continuation: 1, kind: "message", role: "user", text: "Oldest message", created_at: "2026-01-01T00:00:00Z" },
              { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "user", text: "Second message", created_at: "2026-01-01T00:00:00Z" },
            ],
            cursor: 52,
            has_older: false,
          });
        }
        if (url.includes("/transcript") && url.includes("after=")) {
          await new Promise((resolve) => setTimeout(resolve, 500));
        }
        if (url.includes("/transcript")) return json(transcriptBody);
        if (url.includes("/timeline")) return json({ task_id: "task-1", items: [], cursor: 0 });
        if (url.includes("/api/runtime-profiles")) return json({ profiles: [] });
        if (url.includes("/api/model-providers")) return json({ providers: [] });
        if (url.includes("/api/runtime-plugins")) return json({ plugins: [] });
        return json(taskRecord("task-1", "Inspect task view", "running"));
      });
      vi.stubGlobal("fetch", fetchMock);
      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByText("Newest message")).toBeInTheDocument();
      await act(async () => {
        await screen.getByTestId("load-older-transcript").click();
      });

      
      await act(async () => {
        vi.advanceTimersByTimeAsync(400);
      });
      expect(screen.getByText("Oldest message")).toBeInTheDocument();

      
      
      transcriptBody.entries = [
        ...(transcriptBody.entries as Record<string, unknown>[]),
        { id: "entry-53", seq: 53, continuation: 1, kind: "message", role: "assistant", text: "Fresh live row", created_at: "2026-01-01T00:00:02Z" },
      ];
      transcriptBody.cursor = 53;
      
      
      await act(async () => {
        vi.advanceTimersByTimeAsync(600);
      });
      await act(async () => {
        vi.advanceTimersByTimeAsync(900);
      });
      expect(screen.getByText("Fresh live row")).toBeInTheDocument();
      expect(screen.getByText("Oldest message")).toBeInTheDocument();
      const rows = screen.getAllByTestId("transcript-row");
      expect(rows).toHaveLength(5);
    } finally {
      vi.useRealTimers();
    }
  });


  it("pages older timeline rows through the newest-first footer", async () => {
    const { fetchMock } = stubTaskDetailApi(
      {},
      undefined,
      {
        task_id: "task-1",
        items: [{ seq: 51, type: "text", content: "Timeline recent", created_at: "2026-01-01T00:00:00Z" }],
        cursor: 51,
        has_older: true,
      },
      { task_id: "task-1", entries: [], cursor: 0 },
      {
        "/api/projects/project-1/tasks/task-1/timeline?before=": {
          task_id: "task-1",
          items: [{ seq: 1, type: "text", content: "Timeline oldest", created_at: "2026-01-01T00:00:00Z" }],
          cursor: 51,
          has_older: false,
        },
      },
    );
    const user = userEvent.setup();
    renderPage("/projects/project-1/tasks/task-1?view=timeline");
    expect(await screen.findByText("Timeline recent")).toBeInTheDocument();

    await user.click(screen.getByTestId("load-older-timeline"));
    expect(await screen.findByText("Timeline oldest")).toBeInTheDocument();
    const calls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(calls.some((url) => url.includes("/timeline?before=51"))).toBe(true);
    expect(screen.queryByTestId("load-older-timeline")).not.toBeInTheDocument();
  });
});
