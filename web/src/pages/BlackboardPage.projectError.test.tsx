import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { BlackboardPage } from "./BlackboardPage";

afterEach(() => { vi.unstubAllGlobals(); });

function GoToProjectB() {
  const navigate = useNavigate();
  return <button type="button" onClick={() => navigate("/projects/b/blackboard")}>Go to B</button>;
}

it("clears the Project read error of the previous Project after navigation", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/api/projects/a")) return new Response(JSON.stringify({ error: "Project a is gone" }), { status: 404, headers: { "Content-Type": "application/json" } });
    if (url.endsWith("/api/projects/b")) return new Promise<Response>(() => {});
    return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  render(
    <MemoryRouter initialEntries={["/projects/a/blackboard"]}>
      <GoToProjectB />
      <Routes>
        <Route path="/projects/:projectId/blackboard" element={<BlackboardPage />} />
      </Routes>
    </MemoryRouter>,
  );
  expect(await screen.findByRole("alert")).toHaveTextContent("Project a is gone");
  await userEvent.click(screen.getByRole("button", { name: "Go to B" }));
  expect(screen.queryByText("Project a is gone")).not.toBeInTheDocument();
  expect(screen.getByText("Loading Blackboard…")).toBeInTheDocument();
});
