import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { FGSBoard } from "./FGSPage";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("shows accepted Goal, Step, and Fact relationships and node history", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: string) => new Response(JSON.stringify(
    input.endsWith("/history") ? [{ key: "goal:check", type: "goal", version: 1, title: "Check access", state: "open" }] : {
      revision: 2,
      nodes: [
        { key: "goal:check", type: "goal", version: 1, title: "Check access", state: "open", success_criteria: "Access result is known" },
        { key: "step:read", type: "step", version: 1, action: "Read health", state: "done" },
        { key: "fact:ok", type: "fact", version: 1, summary: "Health is ok" },
      ], edges: [{ from: "step:read", to: "goal:check", relation: "toward" }, { from: "step:read", to: "fact:ok", relation: "produces" }],
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
  render(<FGSBoard scope="sessions" id="session-1" />);
  expect(await screen.findByText("Health is ok")).toBeInTheDocument();
  expect(screen.getByText(/Accepted revision 2/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: /Check access/ }));
  expect(screen.getByText("Access result is known")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "History" }));
  expect(await screen.findByText("Version 1")).toBeInTheDocument();
});
