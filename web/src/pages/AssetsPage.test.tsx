import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AssetsPage } from "./AssetsPage";
import { mockApi } from "@/test/mockApi";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const project = {
  id: "project-1", name: "Acme External", description: "External assessment", kind: "pentest",
  blackboard_protocol: "fgs", scope: {}, defaults: {}, created_at: "", updated_at: "",
};
const root = {
  id: "asset-root", project_id: "project-1", type: "root_domain", value: "example.com", normalized_value: "example.com",
  host: "example.com", first_seen_at: "2026-01-01T00:00:00Z", last_seen_at: "2026-01-01T00:00:00Z", created_at: "", updated_at: "",
  scope_status: "in_scope", covered: false, goal_count: 0, step_count: 0, fact_count: 0, anchors: [],
};
const endpoint = {
  ...root,
  id: "asset-endpoint", type: "endpoint", value: "https://example.com/login", normalized_value: "https://example.com/login",
  scheme: "https", host: "example.com", port: 443, path: "/login", parent_id: "asset-root", covered: true, fact_count: 1,
  anchors: [{ project_id: "project-1", asset_id: "asset-endpoint", node_key: "fact:login", node_type: "fact", actor_id: "runtime", created_at: "2026-01-01T00:00:00Z" }],
};
const coverage = {
  schema: "project-asset-coverage/v1", project_id: "project-1", total_assets: 2, in_scope_assets: 2,
  covered_in_scope_assets: 1, coverage_percent: 50, out_of_scope_assets: 0, excluded_assets: 0,
  uncovered_in_scope_asset_ids: ["asset-root"],
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/projects/project-1/assets"]}>
      <Routes><Route path="/projects/:projectId/assets" element={<AssetsPage />} /></Routes>
    </MemoryRouter>,
  );
}

function mockAssetsPage() {
  return mockApi({
    "/api/projects/project-1/dashboard": { counts: {} },
    "/api/projects/project-1/assets/coverage": coverage,
    "/api/projects/project-1/assets": { assets: [root, endpoint] },
    "/api/projects/project-1": project,
  });
}

describe("AssetsPage", () => {
  it("shows inventory, coverage, hierarchy, and durable anchors", async () => {
    mockAssetsPage();
    renderPage();
    expect(await screen.findByRole("heading", { name: "Acme External" })).toBeInTheDocument();
    expect(screen.getByText("In-scope coverage")).toBeInTheDocument();
    expect(screen.getByText("50%")).toBeInTheDocument();
    expect(screen.getByText("https://example.com/login")).toBeInTheDocument();
    expect(screen.getByText("1/2 assets have Fact anchors")).toBeInTheDocument();
    await userEvent.click(screen.getByText("https://example.com/login"));
    const detail = await screen.findByText("Durable FGS anchors");
    expect(detail).toBeInTheDocument();
    expect(screen.getByText("fact:login")).toBeInTheDocument();
    expect(screen.getByText("Covered by Fact evidence")).toBeInTheDocument();
  });

  it("filters inventory by search text", async () => {
    mockAssetsPage();
    renderPage();
    await screen.findByText("https://example.com/login");
    await userEvent.type(screen.getByPlaceholderText("Host, service, or path"), "login");
    expect(screen.getByText("https://example.com/login")).toBeInTheDocument();
    expect(screen.getAllByText("example.com", { exact: true })).toHaveLength(1);
  });

  it("imports all assets from a selected source Project", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/api/projects")) return new Response(JSON.stringify({ projects: [project, { ...project, id: "project-2", name: "Red Team" }] }));
      if (url.endsWith("/api/projects/project-1/assets/import")) {
        expect(JSON.parse(String(init?.body))).toEqual({ source_project_id: "project-2" });
        return new Response(JSON.stringify({ assets: [], created: 2, updated: 1 }));
      }
      if (url.includes("/assets/coverage")) return new Response(JSON.stringify(coverage));
      if (url.includes("/assets")) return new Response(JSON.stringify({ assets: [root, endpoint] }));
      if (url.includes("/dashboard")) return new Response(JSON.stringify({ counts: {} }));
      return new Response(JSON.stringify(project));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPage();
    await user.click(await screen.findByRole("button", { name: /import assets/i }));
    const source = await screen.findByLabelText(/source project/i);
    expect(source).toHaveValue("project-2");
    await user.click(screen.getByRole("button", { name: "Import all assets" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Imported 2 new assets; 1 already existed.");
    expect(within(screen.getByRole("status")).queryByText(/failed/i)).not.toBeInTheDocument();
  });
});
