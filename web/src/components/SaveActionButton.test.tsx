import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SaveActionButton } from "./SaveActionButton";

describe("SaveActionButton", () => {
  it("shows a spinner and saving label while pending", () => {
    render(<SaveActionButton pending label="Save" />);
    expect(screen.getByRole("button", { name: /Saving…/ })).toBeDisabled();
    expect(screen.queryByText(/please wait/i)).not.toBeInTheDocument();
    expect(document.querySelector(".animate-spin")).not.toBeNull();
  });

  it("shows saved feedback with a check icon", () => {
    render(<SaveActionButton saved label="Save provider" />);
    expect(screen.getByRole("button", { name: /Saved/ })).toBeEnabled();
    expect(screen.queryByText(/successfully/i)).not.toBeInTheDocument();
    expect(document.querySelector(".save-check-pop")).not.toBeNull();
    expect(document.querySelector('[aria-live="polite"]')?.className).not.toContain("transition-all");
  });

  it("uses the idle label by default", () => {
    render(<SaveActionButton label="Create provider" />);
    expect(screen.getByRole("button", { name: "Create provider" })).toBeEnabled();
  });
});
