import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { ChallengeWorkflowPage } from "./ChallengeWorkflowPage";

afterEach(() => vi.unstubAllGlobals());

it("shows retained challenge history without offering platform operations", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    const body = url.endsWith("/challenges")
      ? { platforms: ["arena"], retired: true, attempts: [{ platform: "arena", external_attempt_id: "old-attempt", challenge_id: "42", status: "open", wrong_submissions: 0 }], operations: [{ operation_id: "pending-submit", platform: "arena", external_attempt_id: "old-attempt", kind: "submit", state: "pending" }] }
      : url.endsWith("/finish-readiness") ? { ready_to_finish: true, blockers: [] }
        : { id: "project-1", name: "Project", kind: "ctf_challenge" };
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<MemoryRouter initialEntries={["/projects/project-1/tasks/task-1/challenges"]}><Routes><Route path="/projects/:projectId/tasks/:taskId/challenges" element={<ChallengeWorkflowPage />} /></Routes></MemoryRouter>);
  expect(await screen.findByText("old-attempt")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Claim" })).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Candidate")).not.toBeInTheDocument();
  expect(await screen.findByText("pending-submit")).toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent(/original Platform/i);
  expect(fetchMock.mock.calls.every(([input]) => !String(input).endsWith("/finish-readiness"))).toBe(true);
});
