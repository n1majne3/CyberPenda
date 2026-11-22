import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { ActionMenu } from "./ActionMenu";

it("hides secondary actions, closes after selection, and restores focus on Escape", async () => {
  const action = vi.fn();
  const user = userEvent.setup();
  render(<ActionMenu><button onClick={action}>Rename</button></ActionMenu>);
  const trigger = screen.getByText("More actions");
  expect(screen.getByText("Rename")).not.toBeVisible();
  await user.click(trigger);
  await user.click(screen.getByRole("button", { name: "Rename" }));
  expect(action).toHaveBeenCalledOnce();
  expect(screen.getByText("Rename")).not.toBeVisible();
  await user.click(trigger);
  await user.tab();
  await user.keyboard("{Escape}");
  expect(screen.getByText("Rename")).not.toBeVisible();
  expect(trigger).toHaveFocus();
});
