import { describe, it, expect } from "vitest";
import { render, waitFor, screen } from "@testing-library/react";
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
  it("renders the heading and new-project button", async () => {
    mockApi({ "/api/projects": { projects: [] } });
    const { getByText, findByText } = renderPage();
    expect(getByText("Projects")).toBeInTheDocument();
    // Empty state renders after fetch resolves.
    expect(await findByText(/No projects yet/i)).toBeInTheDocument();
  });

  it("renders project cards for returned projects", async () => {
    mockApi({
      "/api/projects": {
        projects: [
          {
            id: "p1",
            name: "Acme External",
            description: "web app test",
            scope: { domains: ["acme.test"], ips: [] },
            defaults: {},
            created_at: "",
            updated_at: "",
          },
        ],
      },
    });
    const { findByText } = renderPage();
    expect(await findByText("Acme External")).toBeInTheDocument();
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

    const links = await screen.findAllByRole("link", { name: /project/i });
    expect(links.map((link) => link.textContent)).toEqual(["Newer Project", "Older Project"]);
  });

  it("exposes a launch affordance (new project button)", async () => {
    mockApi({ "/api/projects": { projects: [] } });
    const { getAllByRole } = renderPage();
    await waitFor(() => {
      expect(getAllByRole("button", { name: /new project/i }).length).toBeGreaterThan(0);
    });
  });
});
