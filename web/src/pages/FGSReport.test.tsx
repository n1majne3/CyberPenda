import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { ReportPage } from "./ReportPage";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("uses accepted FGS results for a new Project report", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: string) => {
    let body: unknown;
    if (input.endsWith("/fgs/report")) body = { revision: 3, markdown: "# Access review\n\nSuccess criteria: result known\n\nFact: access denied" };
    else if (input.endsWith("/dashboard")) body = { counts: { tasks: 1 } };
    else body = { id: "project-1", name: "Access review", blackboard_protocol: "fgs", kind: "pentest" };
    return new Response(JSON.stringify(body), { status: 200 });
  }));
  render(<MemoryRouter initialEntries={["/projects/project-1/report"]}><Routes><Route path="/projects/:projectId/report" element={<ReportPage />} /></Routes></MemoryRouter>);
  expect(await screen.findByText(/Fact: access denied/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Download Markdown" })).toBeInTheDocument();
  expect(screen.queryByText("Confirmed Findings")).not.toBeInTheDocument();
});

it("clears a Project load error after navigation to another Project", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: string) => {
    if (input.endsWith("/api/projects/broken")) return new Response(JSON.stringify({ error: "Project missing" }), { status: 404 });
    let body: unknown;
    if (input.endsWith("/fgs/report")) body = { revision: 1, markdown: "# Second report" };
    else if (input.endsWith("/dashboard")) body = { counts: {} };
    else body = { id: "project-2", name: "Second", blackboard_protocol: "fgs", kind: "pentest" };
    return new Response(JSON.stringify(body), { status: 200 });
  }));
  render(<MemoryRouter initialEntries={["/projects/broken/report"]}><Link to="/projects/project-2/report">Open second Project</Link><Routes><Route path="/projects/:projectId/report" element={<ReportPage />} /></Routes></MemoryRouter>);
  expect(await screen.findByRole("alert")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("link", { name: "Open second Project" }));
  expect(await screen.findByText(/Second report/)).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});
