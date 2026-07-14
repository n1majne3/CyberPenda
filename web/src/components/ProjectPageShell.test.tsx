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
  it("pins project chrome with All projects and section tabs in a fixed order", () => {
    renderShell(
      <ProjectPageShell title="Tasks">
        <p>body</p>
      </ProjectPageShell>,
    );

    const chrome = screen.getByTestId("project-page-shell-chrome");
    expect(chrome).toHaveClass("sticky", "top-0");
    expect(screen.getByRole("link", { name: /All projects/i })).toHaveAttribute("href", "/");
    const nav = screen.getByRole("navigation", { name: "Project sections" });
    expect(nav).toHaveClass("w-full");
    expect(screen.getByRole("heading", { name: "Tasks" })).toBeInTheDocument();
    expect(screen.getByText("body")).toBeInTheDocument();

    // IA order from read contract §19.1: Overview → Tasks → Blackboard → Findings →
    // Evidence → Report → Scope. CTF Projects swap Findings/Report for Solution.
    for (const label of [
      "Overview",
      "Tasks",
      "Blackboard",
      "Findings",
      "Evidence",
      "Report",
      "Scope",
    ]) {
      expect(screen.getByRole("link", { name: label })).toHaveClass("flex-1", "text-center");
    }

    const chromeText = chrome.textContent ?? "";
    expect(chromeText.indexOf("All projects")).toBeLessThan(chromeText.indexOf("Overview"));
    expect(chromeText.indexOf("Overview")).toBeLessThan(chromeText.indexOf("Tasks"));
    expect(chromeText.indexOf("Tasks")).toBeLessThan(chromeText.indexOf("Blackboard"));
    expect(chromeText.indexOf("Blackboard")).toBeLessThan(chromeText.indexOf("Findings"));
    expect(chromeText.indexOf("Findings")).toBeLessThan(chromeText.indexOf("Evidence"));
    expect(chromeText.indexOf("Evidence")).toBeLessThan(chromeText.indexOf("Report"));
    expect(chromeText.indexOf("Report")).toBeLessThan(chromeText.indexOf("Scope"));
  });
});
