import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SubagentHistory } from "./SubagentHistory";

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });

it("reads child history without loading main history and replaces bounded pages", async () => {
  const fetch = mockApi({
    "/children/a?before=20": { entries: [{ id: "old", seq: 2, kind: "message", text: "older child work" }], cursor: 40, before: 2, has_older: false },
    "/children/a": { entries: [{ id: "new", seq: 40, kind: "message", text: "recent child work" }], cursor: 40, before: 20, has_older: true },
  });
  render(<SubagentHistory history="/api/sessions/one/transcript/children/a" renderItems={(items) => items.map((item) => <p key={item.id}>{item.text}</p>)} />);
  expect(await screen.findByText("recent child work")).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "Older child activity" }));
  expect(await screen.findByText("older child work")).toBeVisible();
  expect(screen.queryByText("recent child work")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Latest child activity" }));
  expect(await screen.findByText("recent child work")).toBeVisible();
  await waitFor(() => expect(fetch).toHaveBeenCalled());
  expect(fetch.mock.calls.every(([url]) => String(url).includes("/children/a"))).toBe(true);
});

it("keeps an older page visible during live updates and rejects a stale owner response", async () => {
  vi.useFakeTimers();
  let resolveOld: ((response: Response) => void) | undefined;
  const fetch = vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path.includes("owner-two")) return new Response(JSON.stringify({ entries: [{ id: "two", seq: 1, text: "second owner" }], cursor: 1, before: 1 }));
    if (path.includes("before=")) return new Response(JSON.stringify({ entries: [{ id: "old", seq: 2, text: "reading old content" }], cursor: 40, before: 2 }));
    if (path.includes("after=")) return new Promise<Response>((resolve) => { resolveOld = resolve; });
    return new Response(JSON.stringify({ entries: [{ id: "recent", seq: 40, text: "recent content" }], cursor: 40, before: 20, has_older: true }));
  });
  vi.stubGlobal("fetch", fetch);
  const renderItems = (items: Array<{ id: string; text?: string }>) => items.map((item) => <p key={item.id}>{item.text}</p>);
  const view = render(<SubagentHistory history="/api/owner-one/children/a" renderItems={renderItems} />);
  await act(async () => {});
  fireEvent.click(screen.getByRole("button", { name: "Older child activity" }));
  await act(async () => {});
  expect(screen.getByText("reading old content")).toBeVisible();
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  await act(async () => { resolveOld?.(new Response(JSON.stringify({ entries: [{ id: "new", seq: 41, text: "new activity" }], cursor: 41 }))); });
  expect(screen.getByText("reading old content")).toBeVisible();
  expect(screen.queryByText("new activity")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: /Latest child activity · new activity/ })).toBeVisible();
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  view.rerender(<SubagentHistory history="/api/owner-two/children/a" renderItems={renderItems} />);
  await act(async () => {});
  await act(async () => { resolveOld?.(new Response(JSON.stringify({ entries: [{ id: "leak", seq: 42, text: "wrong owner" }], cursor: 42 }))); });
  expect(screen.getByText("second owner")).toBeVisible();
  expect(screen.queryByText("wrong owner")).not.toBeInTheDocument();
});
