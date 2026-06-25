import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { TaskLaunchPage } from "./TaskLaunchPage";

const codexPlugin = {
  schema_version: 1,
  id: "codex",
  name: "Codex",
  binary: { default: "codex" },
  capabilities: { sandbox: true, host: true, mcp_config: true, streaming_transcript: true, resume: true },
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

const codexPreset = {
  id: "codex-preset",
  name: "Codex MCP Preset",
  provider: "codex",
  fields: { model_provider_id: "mimo", model_override: "mimo-v2.5-pro" },
  created_at: "",
  updated_at: "",
};

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
  it("shows runtime and model provider controls instead of profile picker", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(
            new Response(JSON.stringify({ plugins: [codexPlugin] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/model-providers")) {
          return Promise.resolve(
            new Response(JSON.stringify({ providers: [mimoProvider] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(JSON.stringify({ profiles: [codexPreset] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/projects/project-1")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: "project-1",
                name: "Acme",
                description: "",
                scope: {},
                defaults: { runner: "sandbox" },
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

    expect(await screen.findByLabelText("Runtime")).toBeInTheDocument();
    expect(screen.getByLabelText("Model provider")).toBeInTheDocument();
    expect(screen.getByLabelText("Model")).toBeInTheDocument();
    expect(screen.queryByLabelText("Runtime profile")).not.toBeInTheDocument();
    expect(screen.getByRole("option", { name: "MiMo" })).toBeInTheDocument();
  });

  it("preselects project default preset and launches without resolve-launch", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(
          new Response(JSON.stringify({ plugins: [codexPlugin] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(
          new Response(JSON.stringify({ providers: [mimoProvider] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-profiles/resolve-launch") && method === "POST") {
        return Promise.reject(new Error("resolve-launch should not be called for preset launch"));
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(JSON.stringify({ profiles: [codexPreset] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { runtime_profile_id?: string };
        expect(body.runtime_profile_id).toBe("codex-preset");
        return Promise.resolve(
          new Response(
            JSON.stringify({
              pass: true,
              checks: [
                { name: "runtime_profile", status: "pass" },
                { name: "skills", status: "pass", detail: "1 enabled skill(s)" },
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
              defaults: { runtime_profile: "codex-preset", runner: "sandbox" },
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
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    expect(await screen.findByLabelText("Runtime profile preset")).toHaveValue("codex-preset");
    expect(screen.getByLabelText("Runtime")).toBeDisabled();
    expect(screen.getByLabelText("Model provider")).toBeDisabled();
    expect(screen.getByLabelText("Model")).not.toBeDisabled();

    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Recon Helper")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalledWith(
      expect.stringContaining("/api/runtime-profiles/resolve-launch"),
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("shows runtime extension preview from preflight for preset launches", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        const method = init?.method ?? "GET";
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(
            new Response(JSON.stringify({ plugins: [codexPlugin] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/model-providers")) {
          return Promise.resolve(
            new Response(JSON.stringify({ providers: [mimoProvider] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(JSON.stringify({ profiles: [codexPreset] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                pass: true,
                checks: [
                  { name: "runtime_profile", status: "pass" },
                  { name: "runtime_extensions", status: "pass", detail: "1 enabled runtime extension(s)" },
                ],
                runtime_extensions: [
                  {
                    id: "npm:pi-mcp-adapter",
                    source: "catalog",
                    install_ref: "npm:pi-mcp-adapter",
                    registry: "pi.dev/packages",
                  },
                ],
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
                defaults: { runtime_profile: "codex-preset", runner: "sandbox" },
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

    await userEvent.type(await screen.findByLabelText("Task goal"), "Run with extension");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Runtime extensions")).toBeInTheDocument();
    expect(screen.getAllByText("npm:pi-mcp-adapter").length).toBeGreaterThan(0);
    expect(screen.getByText("Install: npm:pi-mcp-adapter")).toBeInTheDocument();
  });

  it("resolves launch profile for simple path and shows model provider preview after preflight", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        const method = init?.method ?? "GET";
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(
            new Response(JSON.stringify({ plugins: [codexPlugin] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/model-providers")) {
          return Promise.resolve(
            new Response(JSON.stringify({ providers: [mimoProvider] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-profiles/resolve-launch") && method === "POST") {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profile_id: "resolved-profile",
                created: true,
                profile: {
                  id: "resolved-profile",
                  name: "Codex · MiMo",
                  provider: "codex",
                  fields: { model_provider_id: "mimo", model_override: "mimo-v2.5-pro" },
                  created_at: "",
                  updated_at: "",
                },
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(JSON.stringify({ profiles: [codexPreset] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/projects/project-1/preflight")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                pass: true,
                checks: [
                  { name: "runtime_profile", status: "pass" },
                  { name: "model_provider", status: "pass", detail: "mimo-v2.5-pro via MIMO_API_KEY" },
                ],
                model_provider: {
                  model_provider_id: "mimo",
                  model_provider_name: "MiMo",
                  base_url: "https://api.example.test/v1",
                  protocol: "openai_responses",
                  model: "mimo-v2.5-pro",
                  api_key_env: "MIMO_API_KEY",
                },
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
                defaults: { runner: "sandbox" },
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

    await userEvent.click(await screen.findByRole("button", { name: /use saved preset/i }));
    await userEvent.selectOptions(screen.getByLabelText("Runtime profile preset"), "");

    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    const preview = await screen.findByText("Model provider", { selector: "p" });
    expect(preview.parentElement).toHaveTextContent("MiMo");
    expect(preview.parentElement).toHaveTextContent(/mimo-v2\.5-pro via openai_responses/);
    expect(preview.parentElement).toHaveTextContent("API key: MIMO_API_KEY");
  });

  it("sends launch model override when preset model changes", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(
          new Response(JSON.stringify({ plugins: [codexPlugin] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              providers: [
                {
                  ...mimoProvider,
                  catalog: { manual: ["mimo-v2-flash", "mimo-v2-pro"], default_model: "mimo-v2-flash" },
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              profiles: [
                {
                  ...codexPreset,
                  fields: { model_provider_id: "mimo", model_override: "mimo-v2-flash" },
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { model_override?: string };
        expect(body.model_override).toBe("mimo-v2-pro");
        return Promise.resolve(
          new Response(
            JSON.stringify({
              pass: true,
              checks: [{ name: "model_provider", status: "pass" }],
              model_provider: {
                model_provider_id: "mimo",
                model_provider_name: "MiMo",
                model: "mimo-v2-pro",
                protocol: "openai_responses",
                api_key_env: "MIMO_API_KEY",
              },
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
              defaults: { runtime_profile: "codex-preset", runner: "sandbox" },
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
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    const modelSelect = await screen.findByLabelText("Model");
    await userEvent.selectOptions(modelSelect, "mimo-v2-pro");
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Model provider", { selector: "p" })).toBeInTheDocument();
  });

  it("clears preset selection when switching to auto-resolve", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(
            new Response(JSON.stringify({ plugins: [codexPlugin] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/model-providers")) {
          return Promise.resolve(
            new Response(JSON.stringify({ providers: [mimoProvider] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(JSON.stringify({ profiles: [codexPreset] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/projects/project-1")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: "project-1",
                name: "Acme",
                description: "",
                scope: {},
                defaults: { runtime_profile: "codex-preset", runner: "sandbox" },
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

    const presetSelect = await screen.findByLabelText("Runtime profile preset");
    expect(presetSelect).toHaveValue("codex-preset");
    expect(screen.getByLabelText("Runtime")).toBeDisabled();

    await userEvent.selectOptions(presetSelect, "");

    expect(presetSelect).toHaveValue("");
    expect(screen.getByLabelText("Runtime")).not.toBeDisabled();
    expect(screen.getByLabelText("Model provider")).not.toBeDisabled();
  });
});