import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Task } from "@/lib/api";
import { AgentTranscriptView } from "./AgentTranscriptView";

const task: Task = {
  id: "task-1",
  project_id: "project-1",
  goal: "Inspect timeline",
  status: "completed",
  runner: "sandbox",
  runtime_profile_id: "profile-1",
  run_controls: {},
  scope_snapshot: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:05Z",
};

describe("AgentTranscriptView", () => {
  it("defaults to newest events first", () => {
    render(
      <AgentTranscriptView
        task={task}
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
        task={task}
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
        task={task}
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
        task={task}
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
        task={task}
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
        task={{ ...task, status: "running" }}
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
