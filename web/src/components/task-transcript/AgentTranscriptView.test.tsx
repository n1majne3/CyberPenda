import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AgentTranscriptView } from "./AgentTranscriptView";

const owner = {
  status: "completed",
  runner: "sandbox",
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:05Z",
};

describe("AgentTranscriptView", () => {
  it("distinguishes assisted Work, Conclude, retry, and applied revision events", () => {
    render(
      <AgentTranscriptView
        owner={owner}
        items={[
          { seq: 1, type: "harness", content: "Blackboard conclusion pending for work Turn work-7" },
          { seq: 2, type: "harness", content: "Blackboard Conclude Turn started" },
          { seq: 3, type: "harness", content: "Blackboard conclusion repair requested" },
          { seq: 4, type: "harness", content: "Blackboard conclusion retry requested" },
          { seq: 5, type: "harness", content: "Blackboard conclusion applied at revision 12" },
        ]}
      />,
    );

    expect(screen.getAllByText("Work Turn").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Harness Conclude Turn").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Harness repair/retry").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Blackboard revision 12 applied").length).toBeGreaterThan(0);
  });

  it("renders pending Blackboard conclusion markers as Harness activity", () => {
    render(
      <AgentTranscriptView
        owner={owner}
        items={[{
          seq: 1,
          type: "harness",
          content: "Blackboard conclusion pending · source Turn turn-7",
        }]}
      />,
    );

    expect(screen.getAllByText("Harness")).not.toHaveLength(0);
    expect(screen.getByRole("button", { name: /Blackboard conclusion pending/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Jump to Harness event/i })).toBeInTheDocument();
  });

  it("defaults to newest events first", () => {
    render(
      <AgentTranscriptView
        owner={owner}
        items={[
          { seq: 1, type: "text", content: "Older timeline event" },
          { seq: 2, type: "text", content: "Newer timeline event" },
        ]}
      />,
    );

    expect(screen.getByRole("button", { name: /Newest/i })).toHaveAttribute("aria-pressed", "true");
    const eventRows = screen.getAllByRole("button", { name: /timeline event/i });
    expect(eventRows.map((row) => row.textContent)).toEqual(["Newer timeline event", "Older timeline event"]);
  });

  it("labels timeline segment buttons and gives rows a content-visibility boundary", () => {
    render(
      <AgentTranscriptView
        owner={owner}
        items={[
          { seq: 1, type: "tool_use", tool: "shell", input: { command: "ls" } },
          { seq: 2, type: "error", content: "Command failed" },
        ]}
      />,
    );

    expect(screen.getByRole("button", { name: /Jump to shell event/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Jump to Error event/i })).toBeInTheDocument();
    expect(screen.getAllByTestId("transcript-event-row")[0]).toHaveClass("[content-visibility:auto]");
  });

  it("exposes disclosure state and visible focus styles on transcript controls", () => {
    render(
      <AgentTranscriptView
        owner={owner}
        items={[
          { seq: 1, type: "tool_use", tool: "shell", input: { command: "ls" } },
          { seq: 2, type: "tool_result", tool: "shell", output: "ok" },
        ]}
      />,
    );

    const filter = screen.getByRole("button", { name: /Filter/i });
    expect(filter).toHaveAttribute("aria-expanded", "false");
    expect(filter).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("button", { name: /Copy all/i })).toHaveClass("focus-visible:ring-2");
    for (const segment of screen.getAllByRole("button", { name: /Jump to shell event/i })) {
      expect(segment).toHaveClass("focus-visible:ring-2");
    }
    expect(screen.getByRole("button", { name: /Newest/i })).toHaveClass("focus-visible:ring-2");
  });

  it("uses shared Geist radii for transcript chrome and status badges", () => {
    const { container } = render(
      <AgentTranscriptView
        owner={owner}
        items={[{ seq: 1, type: "text", content: "Timeline opened" }]}
      />,
    );

    expect(container.firstChild).toHaveClass("rounded-lg");
    expect(screen.getByText("Completed")).toHaveClass("rounded-md");
    expect(screen.getByText("Completed")).not.toHaveClass("rounded-full");
  });

  it("uses semantic Geist tokens for active transcript filters", async () => {
    const user = userEvent.setup();
    render(
      <AgentTranscriptView
        owner={owner}
        items={[
          { seq: 1, type: "tool_use", tool: "shell", input: { command: "ls" } },
          { seq: 2, type: "tool_result", tool: "shell", output: "ok" },
        ]}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Filter/i }));
    await user.click(screen.getByLabelText("shell"));

    const filterButton = screen.getByRole("button", { name: /^Filter/i });
    expect(filterButton).toHaveClass("bg-info/10", "text-info");
  });

  it("honors reduced motion classes and typographic ellipses in dynamic states", async () => {
    render(
      <AgentTranscriptView
        owner={{ ...owner, status: "running" }}
        isLive
        items={[
          { seq: 1, type: "tool_result", tool: "shell", output: "x".repeat(4100) },
        ]}
      />,
    );

    expect(document.querySelector(".animate-spin")).toHaveClass("motion-reduce:animate-none");
    await screen.getByRole("button", { name: /x+/i }).click();
    expect(screen.getByText(/… \(truncated\)$/)).toBeInTheDocument();
    expect(screen.queryByText(/\.\.\. \(truncated\)$/)).not.toBeInTheDocument();
  });
});

describe("AgentTranscriptView history window (#202)", () => {
  it("shows an unseen pill with the live-event count and jumps to the tail on click", async () => {
    const onShowLatest = vi.fn();
    const user = userEvent.setup();
    render(
      <AgentTranscriptView
        owner={owner}
        items={[
          { seq: 1, type: "text", content: "Older event" },
          { seq: 2, type: "text", content: "Newer event" },
        ]}
        unseenCount={3}
        onShowLatest={onShowLatest}
      />,
    );

    const pill = screen.getByTestId("unseen-timeline-indicator");
    expect(pill).toHaveTextContent("3 new events");
    await user.click(pill);
    expect(onShowLatest).toHaveBeenCalledTimes(1);
  });

  it("renders the paging footer at the older end of the list", () => {
    render(
      <AgentTranscriptView
        owner={owner}
        items={[
          { seq: 1, type: "text", content: "Older event" },
          { seq: 2, type: "text", content: "Newer event" },
        ]}
        footer={<button type="button">Load older events</button>}
      />,
    );

    // Default newest-first sort puts older rows at the bottom, so the footer
    // renders after the rows inside the scroll container.
    const rows = screen.getAllByTestId("transcript-event-row");
    const footer = screen.getByRole("button", { name: "Load older events" });
    expect(rows[0].compareDocumentPosition(footer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(rows[1].compareDocumentPosition(footer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("keeps the rendered DOM bounded while a long history stays available", () => {
    const items = Array.from({ length: 500 }, (_, index) => ({
      seq: index + 1,
      type: "text" as const,
      content: `event-${index + 1}`,
    }));
    Object.defineProperty(HTMLElement.prototype, "clientHeight", { get: () => 600, configurable: true });
    try {
      render(<AgentTranscriptView owner={owner} items={items} />);
      const rendered = screen.getAllByTestId("transcript-event-row");
      expect(rendered.length).toBeLessThan(500);
      expect(rendered.length).toBeGreaterThan(0);
      // Rows outside the window are not in the DOM while the loaded history
      // stays available in state.
      expect(screen.queryByText("event-250")).not.toBeInTheDocument();
    } finally {
      delete (HTMLElement.prototype as unknown as { clientHeight?: number }).clientHeight;
    }
  });
});
