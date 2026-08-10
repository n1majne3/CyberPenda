import { describe, it, expect, vi } from "vitest";
import { render, waitFor, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { ProjectListPage } from "./ProjectListPage";
import { mockApi } from "@/test/mockApi";

// Smoke tests: mount the page with a mocked daemon and confirm key elements
// render without throwing. The pages are presentational; these guard against
// the restyle breaking data flow.
function renderPage() {
  return render(
    <MemoryRouter>
      <ProjectListPage />
    </MemoryRouter>,
  );
}

describe("ProjectListPage", () => {
  it("shows a concise loading state before projects resolve", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>(() => {
            // Keep the request pending so the initial page state is observable.
          }),
      ),
    );

    renderPage();

    const status = screen.getByRole("status", { name: /loading projects/i });
    expect(status).toHaveClass("rounded-lg", "border", "bg-card", "text-muted-foreground");
  });

  it("renders the heading and new-project button", async () => {
    mockApi({ "/api/projects": { projects: [] } });
    const { findByText } = renderPage();
    expect(screen.getByRole("heading", { level: 1, name: "Projects" })).toBeInTheDocument();
    // Empty state renders after fetch resolves.
    expect(await findByText("No projects")).toBeInTheDocument();
  });

  it("opens project creation when the sidebar new-project route is selected", async () => {
    mockApi({ "/api/projects": { projects: [] } });

    render(
      <MemoryRouter initialEntries={["/?new=1"]}>
        <ProjectListPage />
      </MemoryRouter>,
    );

    expect(await screen.findByLabelText("Project name")).toBeInTheDocument();
	expect(screen.getByLabelText("Project kind")).toHaveValue("");
	expect(screen.getByRole("button", { name: /^create$/i })).toBeDisabled();
  });

  it("creates a CTF Challenge Project with an explicit Project kind", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; body: unknown }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        if (init?.method === "POST") {
          requests.push({ url, body: JSON.parse(String(init.body)) as unknown });
          return new Response(JSON.stringify({ id: "ctf-1", name: "Arena", kind: "ctf_challenge", scope: {} }), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(JSON.stringify({ projects: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );

    render(
      <MemoryRouter initialEntries={["/?new=1"]}>
        <ProjectListPage />
      </MemoryRouter>,
    );

    await user.type(await screen.findByLabelText("Project name"), "Arena");
    await user.selectOptions(screen.getByLabelText("Project kind"), "ctf_challenge");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toEqual({
      url: "/api/projects",
      body: { name: "Arena", kind: "ctf_challenge", scope: {} },
    });
  });

  it("renders Geist project cards with scan metadata that does not rely on color alone", async () => {
    mockApi({
      "/api/projects": {
        projects: [
          {
            id: "p1",
            name: "Acme External",
            kind: "ctf_challenge",
            description: "web app test",
            scope: {
              domains: ["acme.test"],
              ips: ["203.0.113.10"],
              testing_limits: ["business hours", "no destructive payloads"],
              notes: "Coordinate with the blue team.",
            },
            defaults: {},
            created_at: "",
            updated_at: "",
          },
        ],
      },
    });

    renderPage();

    const link = await screen.findByRole("link", { name: /open acme external project dashboard/i });
    expect(link).toHaveClass("rounded-lg", "focus-visible:ring-2");
    expect(link.firstElementChild).toHaveClass("rounded-lg", "border", "bg-card", "shadow-sm");
    expect(screen.getByText("Scope ready")).toBeInTheDocument();
    expect(screen.getByText("CTF Challenge")).toBeInTheDocument();
    expect(screen.getByText("1 domain")).toBeInTheDocument();
    expect(screen.getByText("1 IP")).toBeInTheDocument();
    expect(screen.getByText("2 testing limits")).toBeInTheDocument();
    expect(screen.getByText("Scope notes")).toBeInTheDocument();
  });

  it("shows newest projects first", async () => {
    mockApi({
      "/api/projects": {
        projects: [
          {
            id: "older",
            name: "Older Project",
            description: "",
            scope: {},
            defaults: {},
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          {
            id: "newer",
            name: "Newer Project",
            description: "",
            scope: {},
            defaults: {},
            created_at: "2026-01-02T00:00:00Z",
            updated_at: "2026-01-02T00:00:00Z",
          },
        ],
      },
    });

    renderPage();

    const links = await screen.findAllByRole("link", { name: /open .* project dashboard/i });
    expect(links.map((link) => link.getAttribute("aria-label"))).toEqual([
      "Open Newer Project project dashboard",
      "Open Older Project project dashboard",
    ]);
  });

  it("uses an empty state with neutral treatment and a visible create action", async () => {
    mockApi({ "/api/projects": { projects: [] } });

    renderPage();

    const status = await screen.findByRole("status", { name: /no projects/i });
    expect(status).toHaveClass("rounded-lg", "border-dashed", "bg-card");
    expect(within(status).getByRole("button", { name: /new project/i })).toBeInTheDocument();
  });

  it("uses an alert state when project discovery fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ error: "daemon unavailable" }), {
            status: 503,
            statusText: "Service Unavailable",
            headers: { "Content-Type": "application/json" },
          }),
        ),
      ),
    );

    renderPage();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load projects");
    expect(alert).toHaveTextContent("daemon unavailable");
    expect(alert).toHaveClass("rounded-lg", "border-destructive/25", "bg-card");
  });

  it("exposes a launch affordance (new project button)", async () => {
    mockApi({ "/api/projects": { projects: [] } });
    const { getAllByRole } = renderPage();
    await waitFor(() => {
      expect(getAllByRole("button", { name: /new project/i }).length).toBeGreaterThan(0);
    });
  });
});
