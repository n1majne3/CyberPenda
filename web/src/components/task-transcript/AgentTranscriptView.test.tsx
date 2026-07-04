import { render, screen } from "@testing-library/react";
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
});
