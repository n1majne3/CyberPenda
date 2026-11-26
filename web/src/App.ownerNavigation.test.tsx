import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { ThemeProvider } from "@/components/ThemeProvider";
import { mockApi } from "@/test/mockApi";
import App from "./App";

const nextOwner = vi.hoisted(() => {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => { release = resolve; });
  return { ready: false, promise, release };
});

vi.mock("@/components/WorkspaceTopbar", async () => {
  const { Link } = await import("react-router-dom");
  return { WorkspaceTopbar: () => <Link to="/sessions/session-b">Open B</Link> };
});

vi.mock("@/pages/SessionDetailPage", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/pages/SessionDetailPage")>();
  const { useParams } = await import("react-router-dom");
  return {
    SessionDetailPage: function PendingSessionPage() {
      const { sessionId } = useParams();
      if (sessionId === "session-b" && !nextOwner.ready) throw nextOwner.promise;
      return <original.SessionDetailPage />;
    },
  };
});

it("removes the old interactive composer when the URL changes, before the next owner can render", async () => {
  window.history.replaceState({}, "", "/sessions/session-a");
  const routes: Record<string, unknown> = {};
  for (const id of ["session-a", "session-b"]) {
    routes[`/api/sessions/${id}/transcript`] = { entries: [] };
    routes[`/api/sessions/${id}/timeline`] = { items: [] };
    routes[`/api/sessions/${id}`] = {
      id, title: id, lifecycle: "open", runtime_activity: { liveness: "offline" },
      latest_continuation: { id: `continuation-${id}`, session_id: id, number: 1, runtime_provider: "pi", runner: "sandbox", status: "stopped" },
    };
  }
  mockApi(routes);
  const user = userEvent.setup();
  render(<ThemeProvider><App /></ThemeProvider>);
  const oldComposer = await screen.findByRole("textbox", { name: "Session message" });
  await user.type(oldComposer, "A existing draft");
  await user.click(screen.getByRole("link", { name: "Open B" }));
  expect(window.location.pathname).toBe("/sessions/session-b");
  expect(screen.queryByRole("textbox", { name: "Session message" })).not.toBeInTheDocument();
  await act(async () => { nextOwner.ready = true; nextOwner.release(); });
  const composer = await screen.findByRole("textbox", { name: "Session message" });
  expect(screen.getByTestId("task-session-header")).toHaveTextContent("session-b");
  expect(composer).not.toBe(oldComposer);
  await user.type(composer, "B OWN DRAFT");
  expect(composer).toHaveValue("B OWN DRAFT");
});
