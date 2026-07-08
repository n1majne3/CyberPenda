import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  SettingsChipFilter,
  SettingsDetailPane,
  SettingsSearchField,
  SettingsSegmentedFilter,
  SettingsStatSummary,
} from "./settingsLibrary";

describe("settingsLibrary primitives", () => {
  it("search field reports value changes", async () => {
    const onChange = vi.fn();
    render(
      <SettingsSearchField
        aria-label="Search skills"
        value=""
        onChange={onChange}
        placeholder="Search…"
      />,
    );
    await userEvent.type(screen.getByLabelText("Search skills"), "recon");
    expect(onChange).toHaveBeenCalled();
    expect(onChange.mock.calls.some(([value]) => value === "r")).toBe(true);
  });

  it("segmented filter toggles the selected option", async () => {
    const onChange = vi.fn();
    render(
      <SettingsSegmentedFilter
        aria-label="Filter by status"
        value="all"
        onChange={onChange}
        options={[
          { id: "all", label: "All", count: 3 },
          { id: "enabled", label: "Enabled", count: 2 },
          { id: "opted_out", label: "Opted out", count: 1 },
        ]}
      />,
    );
    expect(screen.getByRole("button", { name: /enabled/i })).toHaveAttribute("aria-pressed", "false");
    await userEvent.click(screen.getByRole("button", { name: /enabled/i }));
    expect(onChange).toHaveBeenCalledWith("enabled");
  });

  it("chip filter marks the active chip", () => {
    render(
      <SettingsChipFilter
        aria-label="Filter by source kind"
        value="env"
        onChange={() => {}}
        options={[
          { id: "all", label: "Any source" },
          { id: "env", label: "env" },
        ]}
      />,
    );
    expect(screen.getByRole("button", { name: "env" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Any source" })).toHaveAttribute("aria-pressed", "false");
  });

  it("stat summary renders value and total", () => {
    render(<SettingsStatSummary value={12} unit="enabled" total={39} />);
    expect(screen.getByText("12")).toBeInTheDocument();
    expect(screen.getByText(/enabled/)).toBeInTheDocument();
    expect(screen.getByText(/39/)).toBeInTheDocument();
  });

  it("detail pane keeps header footer and body structure", () => {
    render(
      <SettingsDetailPane
        data-testid="detail"
        header={<h3>Edit</h3>}
        footer={<button type="button">Save</button>}
      >
        <p>Body content</p>
      </SettingsDetailPane>,
    );
    const detail = screen.getByTestId("detail");
    expect(detail).toHaveClass("lg:min-h-0", "lg:overflow-hidden");
    expect(screen.getByRole("heading", { name: "Edit" })).toBeInTheDocument();
    expect(screen.getByText("Body content")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeInTheDocument();
  });
});
