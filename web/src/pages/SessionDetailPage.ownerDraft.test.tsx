import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";
import { expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionDetailPage } from "./SessionDetailPage";
import { codexPlugin, mimoProvider } from "./SessionDetailPage.testSupport";

it("does not carry a pending Session message's files into another Session", async () => {
  const routes: Record<string, unknown> = {
    "/api/model-providers": { providers: [mimoProvider] },
    "/api/runtime-plugins": { plugins: [codexPlugin] },
  };
  for (const id of ["session-a", "session-b"]) {
    routes[`/api/sessions/${id}/transcript`] = { entries: [] };
    routes[`/api/sessions/${id}/timeline`] = { items: [] };
    routes[`/api/sessions/${id}`] = {
      id, title: id, lifecycle: "open",
      runtime_controls: {
        runtime_provider: "codex", resume_available: true, native_resume_available: true,
        turn_selection: { model_provider_id: "mimo", model: "mimo-v2" },
      },
      latest_continuation: {
        id: `continuation-${id}`, session_id: id, number: 1,
        runtime_provider: "codex", runner: "host", status: "stopped",
      },
    };
  }
  const fetchMock = mockApi(routes);
  const defaultFetch = fetchMock.getMockImplementation()!;
  let admitFirst!: (response: Response) => void;
  const firstAdmission = new Promise<Response>((resolve) => { admitFirst = resolve; });
  const submitted: { url: string; body: BodyInit | null | undefined }[] = [];
  fetchMock.mockImplementation(async (input, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === "POST" && url.endsWith("/messages")) {
      submitted.push({ url, body: init.body });
      if (url.includes("session-a")) return firstAdmission;
    }
    return defaultFetch(input);
  });
  const user = userEvent.setup();
  render(<MemoryRouter initialEntries={["/sessions/session-a"]}>
    <Link to="/sessions/session-b">Open second Session</Link>
    <Routes><Route path="/sessions/:sessionId" element={<SessionDetailPage />} /></Routes>
  </MemoryRouter>);
  await screen.findByRole("option", { name: "MiMo" });
  const firstFile = new File(["private to A"], "first-session.txt", { type: "text/plain" });
  await user.upload(screen.getByLabelText("Attach files to Session message"), firstFile);
  await user.type(screen.getByRole("textbox", { name: "Session message" }), "Message for first Session");
  await user.click(screen.getByRole("button", { name: "Resume and send" }));
  await waitFor(() => expect(submitted).toHaveLength(1));
  await user.click(screen.getByRole("link", { name: "Open second Session" }));
  await screen.findByText("session-b");
  const composer = screen.getByRole("textbox", { name: "Session message" });
  await user.clear(composer);
  await user.type(composer, "Message for second Session");
  await act(async () => { admitFirst(new Response(JSON.stringify({}), { status: 202 })); });
  expect(screen.getByLabelText<HTMLInputElement>("Attach files to Session message").files).toHaveLength(0);
  expect(composer).toHaveValue("Message for second Session");
  await user.click(screen.getByRole("button", { name: "Resume and send" }));
  await waitFor(() => expect(submitted).toHaveLength(2));
  expect(submitted[0]?.body).toBeInstanceOf(FormData);
  expect(submitted[1]?.url).toBe("/api/sessions/session-b/messages");
  expect(submitted[1]?.body).not.toBeInstanceOf(FormData);
});
