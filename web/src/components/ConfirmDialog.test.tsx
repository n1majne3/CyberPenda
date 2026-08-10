import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ConfirmDialog, PromptDialog } from "./ConfirmDialog";

describe("ConfirmDialog", () => {
  it("renders nothing while closed", () => {
    render(
      <ConfirmDialog open={false} title="Stop task" description="Really?" onConfirm={() => {}} onCancel={() => {}} />,
    );
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("renders a modal alertdialog with title and description", () => {
    render(
      <ConfirmDialog open title="Stop task" description="This closes the Runtime." onConfirm={() => {}} onCancel={() => {}} />,
    );
    const dialog = screen.getByRole("alertdialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(screen.getByText("Stop task")).toBeInTheDocument();
    expect(screen.getByText("This closes the Runtime.")).toBeInTheDocument();
  });

  it("calls onConfirm from the styled confirm button", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <ConfirmDialog open title="Stop task" description="Really?" confirmLabel="Stop" onConfirm={onConfirm} onCancel={() => {}} />,
    );
    await user.click(screen.getByRole("button", { name: "Stop" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("calls onCancel from Cancel", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog open title="Stop task" description="Really?" onConfirm={() => {}} onCancel={onCancel} />,
    );
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("calls onCancel on Escape", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog open title="Stop task" description="Really?" onConfirm={() => {}} onCancel={onCancel} />,
    );
    await user.keyboard("{Escape}");
    await waitFor(() => expect(onCancel).toHaveBeenCalledTimes(1));
  });

  it("marks destructive confirms with the destructive variant", () => {
    render(
      <ConfirmDialog open title="Delete task" description="Irreversible." destructive confirmLabel="Delete" onConfirm={() => {}} onCancel={() => {}} />,
    );
    expect(screen.getByRole("button", { name: "Delete" })).toHaveClass("bg-destructive");
  });

  it("focuses the cancel button when opened", () => {
    render(
      <ConfirmDialog open title="Stop task" description="Really?" onConfirm={() => {}} onCancel={() => {}} />,
    );
    expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus();
  });

  it("keeps confirm actions reachable when the title is extremely long", () => {
    const longTitle = `Stop task ${"x".repeat(400)} with-no-spaces-${"y".repeat(200)}?`;
    render(
      <ConfirmDialog
        open
        title={longTitle}
        description={`Stopping “${"goal ".repeat(80)}” closes its Runtime.`}
        confirmLabel="Stop"
        onConfirm={() => {}}
        onCancel={() => {}}
      />,
    );
    const dialog = screen.getByRole("alertdialog");
    expect(dialog).toHaveClass("max-h-[min(32rem,calc(100dvh-2rem))]", "overflow-hidden");
    expect(screen.getByRole("heading", { name: longTitle })).toHaveClass("break-words");
    // Actions stay outside the scroll body so they remain clickable.
    expect(screen.getByRole("button", { name: "Stop" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeVisible();
  });
});

describe("PromptDialog", () => {
  it("renders a labelled input pre-filled with the initial value", () => {
    render(
      <PromptDialog open title="Rename session" label="Session title" initialValue="Old title" onConfirm={() => {}} onCancel={() => {}} />,
    );
    expect(screen.getByRole("textbox", { name: "Session title" })).toHaveValue("Old title");
  });

  it("confirms with the edited value", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <PromptDialog open title="Rename session" label="Session title" initialValue="Old title" onConfirm={onConfirm} onCancel={() => {}} />,
    );
    const input = screen.getByRole("textbox", { name: "Session title" });
    await user.clear(input);
    await user.type(input, "New title");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onConfirm).toHaveBeenCalledWith("New title");
  });

  it("cancels without confirming", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(
      <PromptDialog open title="Rename session" label="Session title" initialValue="Old title" onConfirm={() => {}} onCancel={onCancel} />,
    );
    await user.keyboard("{Escape}");
    await waitFor(() => expect(onCancel).toHaveBeenCalledTimes(1));
  });

  it("focuses and selects the input when opened", () => {
    render(
      <PromptDialog open title="Rename session" label="Session title" initialValue="Old title" onConfirm={() => {}} onCancel={() => {}} />,
    );
    const input = screen.getByRole("textbox", { name: "Session title" });
    expect(input).toHaveFocus();
    expect(input).toHaveValue("Old title");
  });
});
