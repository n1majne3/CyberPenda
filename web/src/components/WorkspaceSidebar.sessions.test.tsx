import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetOperatorCredentialStateForTests } from "@/lib/api";
import { WorkspaceSidebar } from "./WorkspaceSidebar";
import { ThemeProvider } from "./ThemeProvider";
import { findRecentSessions, navigationSummary, response, session } from "./WorkspaceSidebar.testSupport";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  resetOperatorCredentialStateForTests();
});

describe("WorkspaceSidebar", () => {


  it("does not force an archived current Session into the open Non-project tree", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        if (url === "/api/sessions/session-archived") {
          return response({
            ...session("session-archived", "Archived session", "2026-08-01T00:00:00Z"),
            lifecycle: "archived",
            
            latest_continuation: { status: "running" },
          });
        }
        return response({});
      }),
    );
    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions/session-archived"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );
    await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/sessions/session-archived", expect.anything()));
    
    
    const nonProject = await findRecentSessions();
    await within(nonProject).findByText(/no open sessions/i);
    expect(screen.queryByRole("link", { name: /archived session/i })).not.toBeInTheDocument();
  });


  it("promotes busy sessions and force-includes the current session within the five-item cap", async () => {
    const sessions = [
      session("session-idle-1", "Idle session 1", "2026-07-31T00:00:00Z"),
      session("session-idle-2", "Idle session 2", "2026-07-30T00:00:00Z"),
      session("session-idle-3", "Idle session 3", "2026-07-29T00:00:00Z"),
      session("session-idle-4", "Idle session 4", "2026-07-28T00:00:00Z"),
      session("session-idle-5", "Idle session 5", "2026-07-27T00:00:00Z"),
      {
        ...session("session-busy", "Busy session", "2026-07-01T00:00:00Z"),
        runtime_activity: { liveness: "live", turn_activity: "busy" },
      },
      session("session-current", "Current session", "2026-06-01T00:00:00Z"),
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return response({ projects: [] });
        if (url === "/api/sessions?limit=5") return response({ sessions });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/sessions/session-current"]}>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await findRecentSessions();
    const sessionLinks = await within(nonProject).findAllByRole("link", { name: /session conversation/i });
    expect(sessionLinks.map((link) => link.textContent ?? "")).toEqual([
      expect.stringContaining("Busy session"),
      expect.stringContaining("Idle session 1"),
      expect.stringContaining("Idle session 2"),
      expect.stringContaining("Idle session 3"),
      expect.stringContaining("Current session"),
    ]);
    expect(within(nonProject).getByRole("img", { name: "Runtime busy" })).toBeInTheDocument();
  });


  it("scopes loading and error states to their affected work groups", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") return Promise.reject(new Error("projects unavailable"));
        if (url === "/api/sessions?limit=5") return response({ sessions: [] });
        return response({});
      }),
    );

    render(
      <ThemeProvider>
        <MemoryRouter>
          <WorkspaceSidebar />
        </MemoryRouter>
      </ThemeProvider>,
    );

    const nonProject = await findRecentSessions();
    expect(await within(nonProject).findByText(/no open sessions/i)).toBeInTheDocument();
    const projects = screen.getByRole("region", { name: /projects/i });
    expect(await within(projects).findByRole("alert")).toHaveTextContent("projects unavailable");
    expect(within(nonProject).queryByRole("alert")).not.toBeInTheDocument();
  });


  it("shows the recent Session count without duplicating Project navigation", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/workspace/navigation") {
          return response({
            projects: [
              navigationSummary("project-1", "Project one", "2026-08-01T00:00:00Z"),
              navigationSummary("project-2", "Project two", "2026-08-01T00:00:00Z"),
              navigationSummary("project-3", "Project three", "2026-08-01T00:00:00Z"),
            ],
          });
        }
        if (url === "/api/sessions?limit=5") {
          return response({
            sessions: [
              session("session-1", "Session one", "2026-08-01T00:00:00Z"),
              session("session-2", "Session two", "2026-08-01T00:00:00Z"),
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

    const recent = await findRecentSessions();
    await waitFor(() => {
      expect(within(recent).getByText(/^Recent sessions/)).toHaveTextContent("2");
      expect(screen.getAllByRole("link", { name: /project dashboard/i })).toHaveLength(3);
    });
  });
});
