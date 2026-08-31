import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionHomePage } from "./SessionHomePage";

const codexPlugin = {
  schema_version: 1,
  id: "codex",
  name: "Codex",
  binary: { default: "codex" },
  capabilities: {
    sandbox: true,
    host: true,
    mcp_config: true,
    streaming_transcript: true,
    resume: true,
    assisted_conclusion: true,
  },
  model_provider: {
    requirement: "required",
    supported_protocols: ["openai_responses"],
    protocol_preference: ["openai_responses"],
  },
  profile_schema: { fields: [] },
  config_projection: { primitive: "codex_home" },
  launch: { args: ["codex"] },
  transcript: { parser: "codex_json" },
};

const mimoProvider = {
  id: "mimo",
  name: "MiMo",
  base_url: "https://api.example.test/v1",
  protocols: ["openai_responses"],
  api_key_env: "MIMO_API_KEY",
  catalog: { manual: ["mimo-v2.5-pro"], default_model: "mimo-v2.5-pro" },
};

const sessionLaunchRoutes = {
  "/api/runtime-profiles/resolve-launch": {
    profile_id: "resolved-profile",
    created: true,
    profile: {
      id: "resolved-profile",
      name: "Codex · MiMo",
      provider: "codex",
      kind: "launch_resolve",
      fields: { model_provider_id: "mimo", model_override: "mimo-v2.5-pro" },
    },
  },
  "/api/sessions/preflight": { pass: true, checks: [] },
  "/api/runtime-plugins": { plugins: [codexPlugin] },
  "/api/model-providers": { providers: [mimoProvider] },
  "/api/runtime-profiles": { profiles: [] },
  "/api/skills?": { skills: [] },
  "/api/health": {
    version: "test",
    database: { status: "ok" },
    runner: { container_cli: "docker", engine_kind: "docker", engine_name: "Docker" },
  },
};

function renderPage(initialEntries = ["/sessions"], view: "open" | "archived" = "open") {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <SessionHomePage view={view} />
    </MemoryRouter>,
  );
}

describe("SessionHomePage", () => {
  it("launches a Non-Project Session with Disabled Blackboard Mode", async () => {
    const fetchMock = mockApi({
      ...sessionLaunchRoutes,
      "/api/sessions?lifecycle=archived": { sessions: [] },
      "/api/sessions": { sessions: [] },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByRole("option", { name: "MiMo" });
    await user.type(screen.getByLabelText("Initial input"), "Inspect the standalone target");
    const mode = screen.getByLabelText("Blackboard Mode");
    expect(screen.getByRole("option", { name: "Disabled" })).toBeEnabled();
    await user.selectOptions(mode, "disabled");
    expect(screen.getByText(/does not receive Blackboard state or Blackboard access/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /create session/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            input: "Inspect the standalone target",
            runtime_plugin_id: "codex",
            model_provider_id: "mimo",
            model: "mimo-v2.5-pro",
            reasoning_effort: "high",
            runner: "sandbox",
            run_controls: { container_cli: "docker", blackboard_conclusion_mode: "disabled" },
          }),
        }),
      );
    });
  });

  it("reuses the Task launch selection and preflight flow", async () => {
    const fetchMock = mockApi({
      ...sessionLaunchRoutes,
      "/api/sessions?lifecycle=archived": { sessions: [] },
      "/api/sessions": { sessions: [] },
    });
    const user = userEvent.setup();

    renderPage();

    // Launch-controls data lands after first paint; anchor on a fetched option.
    await screen.findByRole("option", { name: "MiMo" });
    expect(await screen.findByLabelText("Runtime")).toBeInTheDocument();
    expect(screen.getByLabelText("Model provider")).toBeInTheDocument();
    expect(screen.getByLabelText("Model")).toBeInTheDocument();
    expect(screen.getByLabelText("Reasoning effort")).toBeInTheDocument();
    expect(screen.queryByLabelText("Runtime profile")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("Initial input"), "Inspect the standalone target");
    await user.selectOptions(screen.getByLabelText("Reasoning effort"), "xhigh");
    await user.selectOptions(screen.getByLabelText("Blackboard Mode"), "assisted");
    await user.click(screen.getByRole("button", { name: /create session/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions/preflight",
        expect.objectContaining({ method: "POST" }),
      );
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            input: "Inspect the standalone target",
            runtime_plugin_id: "codex",
            model_provider_id: "mimo",
            model: "mimo-v2.5-pro",
            reasoning_effort: "xhigh",
            runner: "sandbox",
            run_controls: { container_cli: "docker", blackboard_conclusion_mode: "assisted" },
          }),
        }),
      );
    });
  });

  it("labels Non-Project Mode and keeps archived Sessions on their own page", async () => {
    mockApi({
      "/api/sessions?lifecycle=archived": {
        sessions: [
          {
            id: "session-archived",
            title: "Archived notes",
            lifecycle: "archived",
            created_at: "2026-08-01T02:00:00Z",
            updated_at: "2026-08-01T02:00:00Z",
            last_activity_at: "2026-08-01T02:00:00Z",
          },
        ],
      },
      "/api/sessions": {
        sessions: [
          {
            id: "session-open",
            title: "Investigate a host",
            lifecycle: "open",
            created_at: "2026-08-01T01:00:00Z",
            updated_at: "2026-08-01T01:00:00Z",
            last_activity_at: "2026-08-01T01:00:00Z",
          },
        ],
      },
    });

    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "Non-Project Sessions" })).toBeInTheDocument();
    expect(screen.getByText(/this is non-project mode/i)).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: /open investigate a host session/i })).toHaveAttribute(
      "href",
      "/sessions/session-open",
    );
    expect(screen.queryByRole("link", { name: /open archived notes session/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /archived sessions/i })).toHaveAttribute("href", "/sessions/archived");
    expect(screen.getByRole("button", { name: /archive investigate a host/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /restore archived notes/i })).not.toBeInTheDocument();
  });

  it("renders archived Sessions on the dedicated archive page", async () => {
    mockApi({
      "/api/sessions?lifecycle=archived": {
        sessions: [{
          id: "session-archived",
          title: "Archived notes",
          lifecycle: "archived",
          created_at: "2026-08-01T02:00:00Z",
          updated_at: "2026-08-01T02:00:00Z",
          last_activity_at: "2026-08-01T02:00:00Z",
        }],
      },
    });

    renderPage(["/sessions/archived"], "archived");

    expect(await screen.findByRole("heading", { level: 1, name: "Archived Sessions" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: /open archived notes session/i })).toHaveAttribute("href", "/sessions/session-archived");
    expect(screen.getByRole("button", { name: /restore archived notes/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /delete archived notes/i })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /new session/i })).not.toBeInTheDocument();
  });

  it("creates a session from the accessible initial-input form", async () => {
    const fetchMock = mockApi({
      ...sessionLaunchRoutes,
      "/api/sessions?lifecycle=archived": { sessions: [] },
      "/api/sessions": { sessions: [] },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByRole("option", { name: "MiMo" });
    const input = await screen.findByRole("textbox", { name: /initial input/i });
    await user.type(input, "Check the exposed service");
    await user.click(screen.getByRole("button", { name: /create session/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            input: "Check the exposed service",
            runtime_plugin_id: "codex",
            model_provider_id: "mimo",
            model: "mimo-v2.5-pro",
            reasoning_effort: "high",
            runner: "sandbox",
            run_controls: { container_cli: "docker", blackboard_conclusion_mode: "interactive" },
          }),
        }),
      );
    });
  });

  it("reuses the shared attachment picker with validation, preview, and removal", async () => {
    mockApi({
      ...sessionLaunchRoutes,
      "/api/sessions?lifecycle=archived": { sessions: [] },
      "/api/sessions": { sessions: [] },
    });
    const user = userEvent.setup();

    renderPage();

    const dropzone = await screen.findByTestId("attachment-dropzone");
    expect(dropzone).toHaveTextContent("Drag & drop files here, or click to browse");
    expect(dropzone).toHaveTextContent("Up to 25 files, 100 MB each");

    await user.upload(screen.getByLabelText("Attachments"), new File(["notes"], "notes.txt", { type: "text/plain" }));
    expect(screen.getByText("notes.txt")).toBeInTheDocument();
    expect(screen.getByText("5 B")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove notes.txt" }));
    expect(screen.queryByText("notes.txt")).not.toBeInTheDocument();
  });

  it("persists assisted Blackboard conclusions from the Session launch controls", async () => {
    const fetchMock = mockApi({
      ...sessionLaunchRoutes,
      "/api/sessions?lifecycle=archived": { sessions: [] },
      "/api/sessions": { sessions: [] },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByRole("option", { name: "MiMo" });
    await user.type(await screen.findByRole("textbox", { name: /initial input/i }), "Inspect the standalone target");
    await user.selectOptions(screen.getByRole("combobox", { name: /blackboard mode/i }), "assisted");
    await user.click(screen.getByRole("button", { name: /create session/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            input: "Inspect the standalone target",
            runtime_plugin_id: "codex",
            model_provider_id: "mimo",
            model: "mimo-v2.5-pro",
            reasoning_effort: "high",
            runner: "sandbox",
            run_controls: { container_cli: "docker", blackboard_conclusion_mode: "assisted" },
          }),
        }),
      );
    });
  });

  it("focuses the creation form when opened from the New session sidebar route", async () => {
    mockApi({
      "/api/sessions?lifecycle=archived": { sessions: [] },
      "/api/sessions": { sessions: [] },
    });

    renderPage(["/sessions#new-session"]);

    expect(await screen.findByRole("textbox", { name: /initial input/i })).toHaveFocus();
  });

  it("navigates to the created session page after creation", async () => {
    // Method-aware mock: GET /api/sessions lists nothing, POST /api/sessions
    // returns the created Session so the page can navigate to its detail route.
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-profiles/resolve-launch")) {
        return json(sessionLaunchRoutes["/api/runtime-profiles/resolve-launch"]);
      }
      if (url.includes("/api/sessions/preflight")) return json({ pass: true, checks: [] });
      if (url.includes("/api/runtime-plugins")) return json({ plugins: [codexPlugin] });
      if (url.includes("/api/model-providers")) return json({ providers: [mimoProvider] });
      if (url.includes("/api/runtime-profiles")) return json({ profiles: [] });
      if (url.includes("/api/skills")) return json({ skills: [] });
      if (url.includes("/api/sessions") && method === "POST") {
        return json({
          id: "session-new",
          title: "Check the exposed service",
          lifecycle: "open",
          created_at: "2026-08-04T00:00:00Z",
          updated_at: "2026-08-04T00:00:00Z",
          last_activity_at: "2026-08-04T00:00:00Z",
        });
      }
      // GET /api/sessions (list) and archived list default to empty.
      return json({ sessions: [] });
    });
    vi.stubGlobal("fetch", fetchMock);

    const router = createMemoryRouter(
      [
        { path: "/sessions", element: <SessionHomePage /> },
        { path: "/sessions/:sessionId", element: <div data-testid="session-detail" /> },
      ],
      { initialEntries: ["/sessions"] },
    );
    const user = userEvent.setup();
    render(<RouterProvider router={router} />);

    await screen.findByRole("option", { name: "MiMo" });
    await user.type(await screen.findByRole("textbox", { name: /initial input/i }), "Check the exposed service");
    await user.click(screen.getByRole("button", { name: /create session/i }));

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/sessions/session-new");
    });
    expect(screen.getByTestId("session-detail")).toBeInTheDocument();
  });
});

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
