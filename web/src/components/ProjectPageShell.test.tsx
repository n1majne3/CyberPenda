import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { ProjectPageShell } from "./ProjectPageShell";

function renderShell(ui: React.ReactElement) {
  return render(
    <MemoryRouter initialEntries={["/projects/project-1/tasks"]}>
      <Routes>
        <Route path="/projects/:projectId/tasks" element={ui} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("ProjectPageShell", () => {
  it("renders compact project tabs in domain order without the old back-link band", () => {
    renderShell(
      <ProjectPageShell title="Tasks">
        <p>body</p>
      </ProjectPageShell>,
    );

    const chrome = screen.getByTestId("project-page-shell-chrome");
    expect(chrome).not.toHaveClass("sticky");
    expect(chrome).toHaveClass("w-full", "border-b");
    expect(chrome.parentElement).toHaveClass("min-w-0", "w-full");
    expect(screen.getByTestId("project-page-shell-header").parentElement).toHaveClass("min-w-0", "w-full");
    expect(screen.queryByRole("link", { name: /All projects/i })).not.toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Project sections" });
    expect(nav).toHaveClass("w-full");
    expect(screen.getByRole("heading", { name: "Tasks" })).toBeInTheDocument();
    expect(screen.getByText("body")).toBeInTheDocument();

    for (const label of [
      "Overview",
      "Tasks",
      "Blackboard",
      "Findings",
      "Evidence",
      "Report",
      "Scope",
    ]) {
      expect(screen.getByRole("link", { name: label })).toHaveClass("border-b-2", "pb-[13px]");
    }

    const chromeText = chrome.textContent ?? "";
    expect(chromeText.indexOf("Overview")).toBeLessThan(chromeText.indexOf("Tasks"));
    expect(chromeText.indexOf("Tasks")).toBeLessThan(chromeText.indexOf("Blackboard"));
    expect(chromeText.indexOf("Blackboard")).toBeLessThan(chromeText.indexOf("Findings"));
    expect(chromeText.indexOf("Findings")).toBeLessThan(chromeText.indexOf("Evidence"));
    expect(chromeText.indexOf("Evidence")).toBeLessThan(chromeText.indexOf("Report"));
    expect(chromeText.indexOf("Report")).toBeLessThan(chromeText.indexOf("Scope"));
  });
});
