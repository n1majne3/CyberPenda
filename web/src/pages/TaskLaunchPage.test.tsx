import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { TaskLaunchPage } from "./TaskLaunchPage";

function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={["/projects/project-1/tasks/new"]}>
        <Routes>
          <Route path="/projects/:projectId/tasks/new" element={<TaskLaunchPage />} />
          <Route path="/projects/:projectId/tasks/:taskId" element={<div>Task detail</div>} />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
}

describe("TaskLaunchPage", () => {
  it("shows enabled skills after preflight without skill-owned credential refs", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        const method = init?.method ?? "GET";
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Codex Default",
                    provider: "codex",
                    fields: {},
                    created_at: "",
                    updated_at: "",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/projects/project-1/preflight")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                pass: true,
                checks: [
                  { name: "runtime_profile", status: "pass" },
                  { name: "skills", status: "pass", detail: "1 enabled skill(s)" },
                  { name: "credentials", status: "pass", detail: "no credential references" },
                ],
                skills: [{ id: "recon-helper", name: "Recon Helper" }],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
          return new Promise<Response>(() => {});
        }
        if (url.includes("/api/projects/project-1")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: "project-1",
                name: "Acme",
                description: "",
                scope: {},
                defaults: { runtime_profile: "profile-1", runner: "sandbox" },
                created_at: "",
                updated_at: "",
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({}), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();

    await userEvent.type(await screen.findByLabelText("Task goal"), "Run recon");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Recon Helper")).toBeInTheDocument();
    expect(screen.queryByText("recon-api-key")).not.toBeInTheDocument();
    expect(screen.queryByText(/credential "recon-api-key" has no binding/)).not.toBeInTheDocument();
  });
});
