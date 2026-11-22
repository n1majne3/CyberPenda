import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mockConversationViewport, renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });


  it("does not auto-scroll the default timeline view to the bottom", async () => {
    const { scrollIntoView } = stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=timeline");

    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    expect(scrollIntoView).not.toHaveBeenCalled();
  });


  it("exposes Timeline top and bottom controls that move its scroll viewport", async () => {
    const user = userEvent.setup();
    const scrollTo = vi.fn();
    Object.defineProperty(Element.prototype, "scrollTo", { value: scrollTo, configurable: true });
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=timeline");

    expect(await screen.findByText("Timeline opened first")).toBeInTheDocument();
    const viewport = screen.getByTestId("timeline-workspace");
    Object.defineProperty(viewport, "scrollHeight", { value: 9000, configurable: true });

    await user.click(screen.getByRole("button", { name: "Scroll Timeline to top" }));
    expect(scrollTo).toHaveBeenLastCalledWith(expect.objectContaining({ top: 0 }));

    await user.click(screen.getByRole("button", { name: "Scroll Timeline to bottom" }));
    expect(scrollTo).toHaveBeenLastCalledWith(expect.objectContaining({ top: 9000 }));
  });


  it("re-settles the conversation bottom after scroll-to-latest re-anchors the window", async () => {
    const user = userEvent.setup();
    stubTaskDetailApi();
    const scrollTo = vi.fn();
    const restoreViewport = mockConversationViewport({ scrollHeight: () => 20000, scrollTo });

    try {
      renderPage();

      const workspace = await screen.findByTestId("conversation-workspace");
      
      fireEvent.wheel(workspace, { deltaY: -120 });
      await waitFor(() =>
        screen.getByRole("button", { name: /scroll to latest \(auto-follow off\)/i }),
      );
      const callsBeforeClick = scrollTo.mock.calls.length;

      await user.click(screen.getByRole("button", { name: /scroll to latest/i }));

      
      await waitFor(() => {
        expect(scrollTo.mock.calls.length).toBeGreaterThan(callsBeforeClick + 1);
      });
      expect(scrollTo).toHaveBeenLastCalledWith(expect.objectContaining({ top: 20000 }));
    } finally {
      restoreViewport();
    }
  });
});

describe("TaskDetailPage Runtime Owner History Window (#202)", () => {


  it("shows an unseen indicator for live deltas while reading older history", async () => {
    vi.useFakeTimers();
    try {
      const transcriptBody = {
        task_id: "task-1",
        entries: [{
          id: "entry-1",
          seq: 1,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "Visible row",
          created_at: "2026-01-01T00:00:00Z",
        }],
        cursor: 1,
      };
      stubTaskDetailApi(
        { status: "running" },
        undefined,
        { task_id: "task-1", items: [], cursor: 0 },
        transcriptBody,
      );
      const scrollTo = vi.fn();
      Object.defineProperty(Element.prototype, "scrollTo", { value: scrollTo, configurable: true });
      renderPage();
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByText("Visible row")).toBeInTheDocument();

      
      
      
      const viewport = screen.getByTestId("conversation-workspace");
      Object.defineProperty(viewport, "scrollTop", { value: 5000, writable: true, configurable: true });
      Object.defineProperty(viewport, "scrollHeight", { value: 100000, writable: true, configurable: true });
      await act(async () => {
        viewport.dispatchEvent(new Event("wheel"));
        viewport.dispatchEvent(new Event("scroll"));
      });
      transcriptBody.entries = [
        ...transcriptBody.entries,
        { id: "entry-2", seq: 2, continuation: 1, kind: "message", role: "assistant", text: "Fresh unseen row", created_at: "2026-01-01T00:00:01Z" },
      ];
      transcriptBody.cursor = 2;
      await act(async () => {
        vi.advanceTimersByTimeAsync(1500);
      });

      const pill = screen.getByTestId("unseen-transcript-indicator");
      expect(pill).toHaveTextContent("1 new message");
      expect(screen.getByText("Fresh unseen row")).toBeInTheDocument();

      
      await act(async () => {
        pill.click();
      });
      expect(scrollTo).toHaveBeenCalled();
      expect(screen.queryByTestId("unseen-transcript-indicator")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });


  it("keeps the Conversation reading position and auto-follow state across a Timeline round trip", async () => {
    const user = userEvent.setup();
    stubTaskDetailApi();
    renderPage();

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    const viewport = screen.getByTestId("conversation-workspace");
    Object.defineProperty(viewport, "scrollTop", { value: 1200, writable: true, configurable: true });
    Object.defineProperty(viewport, "scrollHeight", { value: 100000, configurable: true });
    Object.defineProperty(viewport, "clientHeight", { value: 600, configurable: true });
    await act(async () => {
      viewport.dispatchEvent(new Event("wheel"));
      viewport.dispatchEvent(new Event("scroll"));
    });

    expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Timeline" }));
    await user.click(screen.getByRole("button", { name: "Conversation" }));

    expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeInTheDocument();
    expect(screen.getByTestId("conversation-workspace").scrollTop).toBe(1200);
  });


  it("pins the initial Conversation to the real scroll-container bottom", async () => {
    const scrollHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
    try {
      Object.defineProperty(HTMLElement.prototype, "scrollHeight", { get: () => 10000, configurable: true });
      const scrollTo = vi.fn();
      Object.defineProperty(Element.prototype, "scrollTo", { value: scrollTo, configurable: true });
      stubTaskDetailApi();
      renderPage();

      expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
      expect(scrollTo).toHaveBeenLastCalledWith({ top: 10000, behavior: "auto" });
      expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow on)" })).toBeInTheDocument();
    } finally {
      if (scrollHeight) {
        Object.defineProperty(HTMLElement.prototype, "scrollHeight", scrollHeight);
      } else {
        delete (HTMLElement.prototype as unknown as { scrollHeight?: number }).scrollHeight;
      }
    }
  });


  it("does not pull the operator back after they start scrolling toward the beginning of a page-long last message", async () => {
    vi.useFakeTimers();
    const restoreViewport = mockConversationViewport({ scrollHeight: () => 2400 });
    try {
      const transcriptBody = {
        task_id: "task-1",
        entries: [{
          id: "entry-1",
          seq: 1,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "A page-long final message",
          created_at: "2026-01-01T00:00:00Z",
        }],
        cursor: 1,
      };
      stubTaskDetailApi(
        { status: "running" },
        undefined,
        { task_id: "task-1", items: [], cursor: 0 },
        transcriptBody,
      );
      renderPage();

      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      const viewport = screen.getByTestId("conversation-workspace");
      expect(viewport.scrollTop).toBe(1800);

      await act(async () => {
        viewport.dispatchEvent(new WheelEvent("wheel", { deltaY: -80 }));
        viewport.scrollTop = 1720;
        viewport.dispatchEvent(new Event("scroll"));
      });
      expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeInTheDocument();

      transcriptBody.entries = [
        ...transcriptBody.entries,
        {
          id: "entry-2",
          seq: 2,
          continuation: 1,
          kind: "message",
          role: "assistant",
          text: "A live delta after the operator started reading upward",
          created_at: "2026-01-01T00:00:01Z",
        },
      ];
      transcriptBody.cursor = 2;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500);
      });

      expect(viewport.scrollTop).toBe(1720);
    } finally {
      vi.useRealTimers();
      restoreViewport();
    }
  });


  it("keeps initial auto-follow on when virtual rendering changes the scroll height", async () => {
    let scrollHeight = 14400;
    let grewAfterFirstPin = false;
    const restoreViewport = mockConversationViewport({
      scrollHeight: () => scrollHeight,
      scrollTo(this: HTMLElement, options: ScrollToOptions) {
        this.scrollTop = Math.max(0, Math.min(options.top ?? 0, scrollHeight - this.clientHeight));
        if (!grewAfterFirstPin) {
          grewAfterFirstPin = true;
          scrollHeight += 452;
        }
        this.dispatchEvent(new Event("scroll"));
      },
    });
    try {
      const entries = Array.from({ length: 200 }, (_, index) => ({
        id: `entry-${index + 1}`,
        seq: index + 1,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: `Conversation row ${index + 1}`,
        created_at: "2026-01-01T00:00:00Z",
      }));
      stubTaskDetailApi({}, entries);
      renderPage();

      const viewport = await screen.findByTestId("conversation-workspace");
      await waitFor(() => {
        expect(scrollHeight - viewport.scrollTop - viewport.clientHeight).toBe(0);
      });
      expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow on)" })).toBeInTheDocument();
    } finally {
      restoreViewport();
    }
  });


  it("keeps the latest Conversation rows mounted when variable-height content moves the bottom", async () => {
    let scrollHeight = 14400;
    const restoreViewport = mockConversationViewport({ scrollHeight: () => scrollHeight });
    try {
      const entries = Array.from({ length: 200 }, (_, index) => ({
        id: `entry-${index + 1}`,
        seq: index + 1,
        continuation: 1,
        kind: "message" as const,
        role: "assistant" as const,
        text: `Variable-height Conversation row ${index + 1}`,
        created_at: "2026-01-01T00:00:00Z",
      }));
      stubTaskDetailApi({}, entries);
      renderPage();

      const viewport = await screen.findByTestId("conversation-workspace");
      await act(async () => {
        viewport.scrollTo({ top: scrollHeight, behavior: "auto" });
        await Promise.resolve();
      });
      const latestRow = await screen.findByText("Variable-height Conversation row 200");

      
      
      scrollHeight = 22000;
      await act(async () => {
        viewport.scrollTo({ top: scrollHeight, behavior: "auto" });
        await Promise.resolve();
      });

      await waitFor(() => {
        expect(screen.getByText("Variable-height Conversation row 200")).toBe(latestRow);
      });
      expect(screen.getByRole("button", { name: "Scroll to latest (auto-follow on)" })).toBeInTheDocument();
    } finally {
      restoreViewport();
    }
  });


  it("keeps the rendered conversation DOM bounded on long histories", async () => {
    const entries = Array.from({ length: 400 }, (_, index) => ({
      id: `entry-${index + 1}`,
      seq: index + 1,
      continuation: 1,
      kind: "message" as const,
      role: "assistant" as const,
      text: `Row ${index + 1}`,
      created_at: "2026-01-01T00:00:00Z",
    }));
    stubTaskDetailApi(
      {},
      undefined,
      { task_id: "task-1", items: [], cursor: 0 },
      { task_id: "task-1", entries, cursor: 400 },
    );
    renderPage();
    expect(await screen.findByText("Row 400")).toBeInTheDocument();

    
    
    const viewport = screen.getByTestId("conversation-workspace");
    Object.defineProperty(viewport, "clientHeight", { value: 600, configurable: true });
    viewport.dispatchEvent(new Event("scroll"));

    await waitFor(() => {
      const rows = screen.getAllByTestId("transcript-row");
      expect(rows.length).toBeLessThan(400);
      expect(rows.length).toBeGreaterThan(0);
    });
    
    
    expect(screen.queryByText("Row 250")).not.toBeInTheDocument();
  });
});
