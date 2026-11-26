import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";

afterEach(() => { vi.useRealTimers(); });

it("keeps an edit across polls but starts Rename from the saved Session title after Cancel", async () => {
  vi.useFakeTimers();
  const session = { id: "session-rename", title: "Saved title", lifecycle: "open" };
  mockApi({
    "/api/sessions/session-rename/transcript": { entries: [] },
    "/api/sessions/session-rename/timeline": { items: [] },
    "/api/sessions/session-rename": session,
  });
  await act(async () => {
    render(<MemoryRouter initialEntries={["/sessions/session-rename"]}><Routes>
      <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
    </Routes></MemoryRouter>);
  });
  fireEvent.click(screen.getByText("More actions"));
  fireEvent.click(screen.getByRole("button", { name: "Rename Saved title" }));
  expect(screen.getByRole("textbox", { name: "Session title" })).toHaveValue("Saved title");
  fireEvent.change(screen.getByRole("textbox", { name: "Session title" }), { target: { value: "Unsaved title" } });
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(screen.getByRole("textbox", { name: "Session title" })).toHaveValue("Unsaved title");
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  session.title = "Remote title";
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  fireEvent.click(screen.getByText("More actions"));
  fireEvent.click(screen.getByRole("button", { name: "Rename Remote title" }));
  expect(screen.getByRole("textbox", { name: "Session title" })).toHaveValue("Remote title");
});
