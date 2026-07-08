import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { TasksPage } from "./TasksPage";

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/projects/project-1/tasks"]}>
      <Routes>
        <Route path="/projects/:projectId/tasks" element={<TasksPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

function task(id: string, goal: string, status: string, createdAt: string) {
  return {
    id,
    project_id: "project-1",
    goal,
    status,
    runner: "sandbox",
    runtime_profile_id: "profile-1",
    run_controls: {},
    scope_snapshot: {},
    created_at: createdAt,
    updated_at: createdAt,
  };
}

describe("TasksPage", () => {
  it("shows running tasks first, then newest tasks first", async () => {
    mockApi({
      "/api/projects/project-1/tasks": {
        tasks: [
          task("older-completed", "Older completed", "completed", "2026-01-01T00:00:00Z"),
          task("newer-completed", "Newer completed", "completed", "2026-01-04T00:00:00Z"),
          task("older-running", "Older running", "running", "2026-01-02T00:00:00Z"),
          task("newer-running", "Newer running", "running", "2026-01-03T00:00:00Z"),
        ],
      },
    });

    renderPage();

    const links = await screen.findAllByRole("link", { name: /(running|completed)/i });
    const goals = ["Newer running", "Older running", "Newer completed", "Older completed"];
    expect(links.map((link) => goals.find((goal) => link.textContent?.includes(goal)))).toEqual(goals);
  });

  it("keeps long task goals inside focusable Geist task cards", async () => {
    const longGoal =
      "Investigate a-super-long-hostname-that-should-wrap-without-overlapping-status-or-metadata.example.internal";
    mockApi({
      "/api/projects/project-1/tasks": {
        tasks: [
          task("long-goal", longGoal, "running", "2026-01-04T00:00:00Z"),
        ],
      },
    });

    renderPage();

    const goal = await screen.findByText(longGoal);
    expect(goal).toHaveClass("break-words");
    expect(goal).not.toHaveClass("truncate");
    expect(screen.getByRole("link", { name: /a-super-long-hostname/i })).toHaveClass(
      "focus-visible:ring-2",
    );
  });
});
