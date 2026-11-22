import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetOperatorCredentialStateForTests } from "@/lib/api";
import { WorkspaceSidebar } from "./WorkspaceSidebar";
import { ThemeProvider } from "./ThemeProvider";
import { findRecentSessions, navigationSummary, response, session, task } from "./WorkspaceSidebar.testSupport";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  resetOperatorCredentialStateForTests();
});

describe("WorkspaceSidebar", () => {

  it("shows durable Runtime failures instead of collapsing them into offline", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") {
          return response({
            projects: [
              navigationSummary("project-active", "Project", "2026-08-01T00:00:00Z", [
                { ...task("task-failed", "Failed task", "2026-08-01T00:00:00Z", { liveness: "offline" }), status: "failed" },
              ]),
            ],
          });
        }
        if (url === "/api/projects/project-active/tasks") {
          return response({
            tasks: [{ ...task("task-failed", "Failed task", "2026-08-01T00:00:00Z", { liveness: "offline" }), status: "failed" }],
          });
        }
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [{
              ...session("session-failed", "Failed session", "2026-08-01T00:00:00Z"),
              runtime_activity: { liveness: "offline" },
              latest_continuation: { status: "failed" },
            }],
          });
        }
        return response({});
      }),
    );

    const user = userEvent.setup();
    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await findRecentSessions();
    expect(await within(nonProject).findByRole("link", { name: /failed session.*runtime failure/i })).toBeInTheDocument();
    const projectRegion = await screen.findByRole("region", { name: /open project project dashboard/i });
    await user.click(within(projectRegion).getByRole("button", { name: /expand project/i }));
    expect(await within(projectRegion).findByRole("link", { name: /failed task.*runtime failure/i })).toBeInTheDocument();
  });


  it("shows a relative time · state second line on every session row, disambiguating shared titles", async () => {
    const twoDaysAgo = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString();
    const fiveDaysAgo = new Date(Date.now() - 5 * 24 * 60 * 60 * 1000).toISOString();
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [
              session("session-1", "Duplicate", twoDaysAgo),
              session("session-2", "Duplicate", fiveDaysAgo),
              session("session-3", "Unique", twoDaysAgo),
            ],
          });
        }
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await findRecentSessions();
    const duplicateLinks = await within(nonProject).findAllByRole("link", { name: /duplicate session conversation/i });
    expect(duplicateLinks).toHaveLength(2);
    
    
    
    expect(duplicateLinks[0]).toHaveTextContent("2 days ago · Stopped");
    expect(duplicateLinks[1]).toHaveTextContent("5 days ago · Stopped");
    
    const uniqueLink = within(nonProject).getByRole("link", { name: /unique session conversation/i });
    expect(uniqueLink).toHaveTextContent("2 days ago · Stopped");
  });


  
  
  
  
  it("shows the durable status word for active continuations while the dot keeps encoding activity", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [
              {
                ...session("session-running-idle", "Quiet session", "2026-08-01T00:00:00Z"),
                runtime_activity: { liveness: "live", turn_activity: "idle" },
                latest_continuation: { status: "running" },
              },
              {
                ...session("session-running-busy", "Working session", "2026-07-31T00:00:00Z"),
                runtime_activity: { liveness: "live", turn_activity: "busy" },
                latest_continuation: { status: "running" },
              },
              {
                ...session("session-paused", "Paused session", "2026-07-30T00:00:00Z"),
                runtime_activity: { liveness: "live", turn_activity: "idle" },
                latest_continuation: { status: "paused" },
              },
              {
                ...session("session-orphaned", "Orphaned session", "2026-07-29T00:00:00Z"),
                runtime_activity: { liveness: "orphaned" },
              },
              {
                ...session("session-unknown", "Undetermined session", "2026-07-28T00:00:00Z"),
                runtime_activity: { liveness: "unknown" },
              },
            ],
          });
        }
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await findRecentSessions();

    
    
    const runningIdle = await within(nonProject).findByRole("link", { name: /quiet session session conversation\. running\./i });
    expect(runningIdle).toHaveTextContent("· Running");
    expect(runningIdle).not.toHaveTextContent("Idle");
    expect(within(runningIdle).getByRole("img", { name: "Runtime live idle" }).firstElementChild).toHaveClass("bg-info");

    
    const runningBusy = within(nonProject).getByRole("link", { name: /working session session conversation\. running\./i });
    expect(runningBusy).toHaveTextContent("· Running");
    expect(within(runningBusy).getByRole("img", { name: "Runtime busy" }).firstElementChild).toHaveClass("bg-success", "animate-pulse");

    
    const paused = within(nonProject).getByRole("link", { name: /paused session session conversation\. paused\./i });
    expect(paused).toHaveTextContent("· Paused");
    expect(within(paused).getByRole("img", { name: "Runtime live idle" }).firstElementChild).toHaveClass("bg-info");

    
    
    expect(within(nonProject).getByRole("link", { name: /orphaned session session conversation\./i })).toHaveTextContent("· Unknown");
    expect(within(nonProject).getByRole("link", { name: /undetermined session session conversation\./i })).toHaveTextContent("· Unknown");
  });


  it("labels task rows with the durable status word while their continuation is active", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/workspace/navigation")) {
          return response({
            projects: [
              navigationSummary("project-active", "Active project", "2026-08-01T00:00:00Z", [
                { ...task("task-running", "Probe task", "2026-08-01T00:00:00Z", { liveness: "live", turn_activity: "idle" }), status: "running" },
              ]),
            ],
          });
        }
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/projects/project-active/tasks/task-running"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const projectRegion = await screen.findByRole("region", { name: /active project/i });
    expect(
      await within(projectRegion).findByRole("link", { name: /probe task task conversation\. running\./i }),
    ).toBeInTheDocument();
  });


  it("renders runtime activity as colored status dots with mockup state text", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [
              {
                ...session("session-busy", "Busy session", "2026-08-01T00:00:00Z"),
                runtime_activity: { liveness: "live", turn_activity: "busy" },
              },
              {
                ...session("session-idle", "Idle session", "2026-07-31T00:00:00Z"),
                runtime_activity: { liveness: "live", turn_activity: "idle" },
              },
              {
                ...session("session-offline", "Offline session", "2026-07-30T00:00:00Z"),
                runtime_activity: { liveness: "offline" },
              },
              {
                ...session("session-orphaned", "Orphaned session", "2026-07-29T00:00:00Z"),
                runtime_activity: { liveness: "orphaned" },
              },
              {
                ...session("session-failed", "Failed session", "2026-07-28T00:00:00Z"),
                runtime_activity: { liveness: "offline" },
                latest_continuation: { status: "failed" },
              },
            ],
          });
        }
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await findRecentSessions();
    await within(nonProject).findByRole("link", { name: /busy session/i });

    
    const busyDot = within(nonProject).getByRole("img", { name: "Runtime busy" }).firstElementChild;
    expect(busyDot).toHaveClass("bg-success", "animate-pulse");
    const idleDot = within(nonProject).getByRole("img", { name: "Runtime live idle" }).firstElementChild;
    expect(idleDot).toHaveClass("bg-info");
    const offlineDot = within(nonProject).getByRole("img", { name: "Runtime offline" }).firstElementChild;
    expect(offlineDot).toHaveClass("bg-muted-foreground/40");
    
    const orphanedDot = within(nonProject).getByRole("img", { name: "Runtime failure (orphaned)" }).firstElementChild;
    expect(orphanedDot).toHaveClass("bg-warning");
    const failedDot = within(nonProject).getByRole("img", { name: "Runtime failure" }).firstElementChild;
    expect(failedDot).toHaveClass("bg-destructive");

    
    expect(within(nonProject).getByRole("link", { name: /busy session/i })).toHaveTextContent("· Running");
    expect(within(nonProject).getByRole("link", { name: /idle session/i })).toHaveTextContent("· Idle");
    expect(within(nonProject).getByRole("link", { name: /offline session/i })).toHaveTextContent("· Stopped");
    expect(within(nonProject).getByRole("link", { name: /orphaned session/i })).toHaveTextContent("· Unknown");
    expect(within(nonProject).getByRole("link", { name: /failed session/i })).toHaveTextContent("· Failed");
  });
});
