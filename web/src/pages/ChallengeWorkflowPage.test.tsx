import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChallengeWorkflowPage } from "./ChallengeWorkflowPage";

afterEach(() => vi.unstubAllGlobals());

describe("ChallengeWorkflowPage", () => {
  it("shows durable Attempts and sends an idempotent claim operation", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/api/projects/project-1")) return Promise.resolve(new Response(JSON.stringify({ id:"project-1",name:"Arena",description:"",kind:"ctf_challenge",scope:{},defaults:{},created_at:"",updated_at:"" }),{status:200,headers:{"Content-Type":"application/json"}}));
      if (url.endsWith("/challenges") && (init?.method ?? "GET") === "GET") return Promise.resolve(new Response(JSON.stringify({ platforms:["arena"], attempts:[{project_id:"project-1",task_id:"task-1",platform:"arena",external_attempt_id:"attempt-42",challenge_id:"3121",attempt_key:"attempt/arena/attempt-42",objective_key:"objective/arena/attempt-42",status:"open",wrong_submissions:1,consecutive_failures:1,initial_rating:2100,peak_rating:2100,current_rating:2080,last_progress_at:"",created_at:"",updated_at:""}] }),{status:200,headers:{"Content-Type":"application/json"}}));
      if (url.endsWith("/finish-readiness")) return Promise.resolve(new Response(JSON.stringify({ ready_to_finish:false,blockers:[{code:"unfinalized_challenge_attempts",count:1,message:"Challenge Attempts are not finalized."}] }),{status:200,headers:{"Content-Type":"application/json"}}));
      if (url.endsWith("/challenges/claim") && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        expect(body.platform).toBe("arena");
        expect(body.challenge_id).toBe("3180");
        expect(body.operation_id).toMatch(/^claim-/);
        return Promise.resolve(new Response(JSON.stringify({external_attempt_id:"attempt-43",challenge_id:"3180",attempt_key:"attempt/arena/attempt-43"}),{status:200,headers:{"Content-Type":"application/json"}}));
      }
      return Promise.resolve(new Response("{}",{status:200,headers:{"Content-Type":"application/json"}}));
    });
    vi.stubGlobal("fetch",fetchMock);
    render(<MemoryRouter initialEntries={["/projects/project-1/tasks/task-1/challenges"]}><Routes><Route path="/projects/:projectId/tasks/:taskId/challenges" element={<ChallengeWorkflowPage />} /></Routes></MemoryRouter>);

    expect(await screen.findByText("attempt-42")).toBeInTheDocument();
    expect(screen.getByText("Challenge Attempts are not finalized.")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Challenge ID"),"3180");
    await userEvent.click(screen.getByRole("button",{name:"Claim"}));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("/challenges/claim"),expect.objectContaining({method:"POST"})));
  });
});


it("keeps history readable without offering unavailable operations", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    const body = url.endsWith("/challenges")
      ? { platforms: [], attempts: [{ platform: "retired", external_attempt_id: "old-attempt", challenge_id: "42", status: "finalized", wrong_submissions: 0 }] }
      : url.endsWith("/finish-readiness")
        ? { ready_to_finish: true, blockers: [] }
        : { id: "project-1", name: "Project", kind: "ctf_challenge" };
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  }));
  render(<MemoryRouter initialEntries={["/projects/project-1/tasks/task-1/challenges"]}><Routes><Route path="/projects/:projectId/tasks/:taskId/challenges" element={<ChallengeWorkflowPage />} /></Routes></MemoryRouter>);
  expect(await screen.findByText("old-attempt")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Claim" })).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Candidate")).not.toBeInTheDocument();
});
