import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";

it("resumes a stopped Session only when a new message is sent", async () => {
  const session = {
    id: "session-stopped", title: "Stopped Session", lifecycle: "open",
    latest_continuation: { id: "continuation-1", number: 1, status: "stopped", runtime_provider: "pi" },
    runtime_activity: { state: "offline" },
    runtime_controls: { native_resume_available: true, queue_steer_available: true },
  };
  mockApi({
    "/api/sessions/session-stopped/transcript": { entries: [] },
    "/api/sessions/session-stopped/timeline": { items: [] },
    "/api/sessions/session-stopped/messages": session,
    "/api/sessions/session-stopped": session,
  });
  await act(async () => {
    render(<MemoryRouter initialEntries={["/sessions/session-stopped"]}><Routes>
      <Route path="/sessions/:sessionId" element={<SessionDetailPage />} />
    </Routes></MemoryRouter>);
  });
  expect(within(screen.getByRole("banner")).queryByRole("button", { name: "Resume", exact: true })).not.toBeInTheDocument();
  const send = screen.getByRole("button", { name: "Resume and send" });
  const input = screen.getByRole("textbox", { name: "Session message" });
  expect(send).toBeDisabled();
  await act(async () => { fireEvent.keyDown(input, { key: "Enter" }); });
  fireEvent.change(input, { target: { value: "   " } });
  await act(async () => { fireEvent.keyDown(input, { key: "Enter" }); });
  expect(send).toBeDisabled();
  expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith("/messages"))).toBe(false);
  fireEvent.change(input, { target: { value: "Resume with this message" } });
  expect(send).toBeEnabled();
  await act(async () => { fireEvent.keyDown(input, { key: "Enter" }); });
  const messages = vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith("/messages"));
  expect(messages).toHaveLength(1);
  expect(JSON.parse(String(messages[0][1]?.body))).toMatchObject({ message: "Resume with this message" });
  expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith("/resume"))).toBe(false);
});
