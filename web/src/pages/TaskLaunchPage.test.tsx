import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
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

const legacyCodexPreset = {
  id: "legacy-preset",
  name: "Legacy Codex Preset",
  provider: "codex",
  fields: {
    model: "gpt-5",
    endpoint: "https://legacy.example.test/v1",
    default_runner: "sandbox",
  },
  created_at: "",
  updated_at: "",
};

const autoResolvedProfile = {
  id: "resolved-profile",
  name: "Codex · MiMo",
  provider: "codex",
  kind: "launch_resolve",
  fields: { model_provider_id: "mimo", model_override: "mimo-v2.5-pro" },
  created_at: "",
  updated_at: "",
};

function renderPage(path = "/projects/project-1/tasks/new") {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/projects/:projectId/tasks/new" element={<TaskLaunchPage />} />
          <Route path="/projects/:projectId/tasks/:taskId" element={<div>Task detail</div>} />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
}

async function selectPentestTaskType() {
  await userEvent.selectOptions(await screen.findByLabelText("Task type"), "pentest");
}

describe("TaskLaunchPage", () => {
  it("launches a Project Task with Disabled Blackboard Mode", async () => {
    const fetchMock = mockApi({
      "/api/projects/project-1/preflight": { pass: true, checks: [] },
      "/api/projects/project-1/tasks": { id: "task-disabled" },
      "/api/runtime-profiles/resolve-launch": {
        profile_id: "resolved-profile",
        created: true,
        profile: autoResolvedProfile,
      },
      "/api/runtime-plugins": { plugins: [codexPlugin] },
      "/api/model-providers": { providers: [mimoProvider] },
      "/api/runtime-profiles": { profiles: [] },
      "/api/skills?": { skills: [] },
      "/api/projects/project-1": {
        id: "project-1",
        name: "Acme",
        description: "",
        kind: "pentest",
        scope: {},
        defaults: { runner: "sandbox" },
        created_at: "",
        updated_at: "",
      },
      "/api/health": {
        version: "test",
        database: { status: "ok" },
        runner: { container_cli: "docker", engine_kind: "docker", engine_name: "Docker" },
      },
    });
    const user = userEvent.setup();

    renderPage();

    await screen.findByRole("option", { name: "MiMo" });
    await user.selectOptions(await screen.findByLabelText("Task type"), "pentest");
    await user.type(screen.getByLabelText("Task goal"), "Inspect the target");
    const mode = screen.getByLabelText("Blackboard Mode");
    expect(screen.getByRole("option", { name: "Disabled" })).toBeEnabled();
    await user.selectOptions(mode, "disabled");
    expect(screen.getByText(/does not receive Blackboard state or Blackboard access/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /launch/i }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([input, init]) =>
        String(input).endsWith("/api/projects/project-1/tasks") && init?.method === "POST",
      );
      expect(call).toBeDefined();
      const body = JSON.parse(String(call?.[1]?.body ?? "{}"));
      expect(body.run_controls?.blackboard_conclusion_mode).toBe("disabled");
    });
  });

  it("launches a Reason Task with the server-owned planning goal", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(new Response(JSON.stringify({ plugins: [codexPlugin] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(new Response(JSON.stringify({ providers: [mimoProvider] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(new Response(JSON.stringify({ profiles: [codexPreset] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/skills?")) {
        return Promise.resolve(new Response(JSON.stringify({ skills: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        return Promise.resolve(new Response(JSON.stringify({ pass: true, checks: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/projects/project-1/reason-tasks") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { goal?: string };
        expect(body.goal).toMatch(/^Read the complete Runtime Blackboard Snapshot/);
        expect(body.goal).toMatch(/Do not mutate Blackboard records directly\.$/);
        return Promise.resolve(new Response(JSON.stringify({ id: "reason-task-1" }), { status: 201, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/projects/project-1")) {
        return Promise.resolve(new Response(JSON.stringify({
          id: "project-1", name: "Acme", description: "", kind: "pentest", scope: {},
          defaults: { runner: "sandbox", runtime_profile: "codex-preset" }, created_at: "", updated_at: "",
        }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage("/projects/project-1/tasks/new?purpose=reason");

    expect(await screen.findByRole("heading", { name: /Launch Reason Task/i })).toBeInTheDocument();
    expect(screen.queryByLabelText("Task type")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Reason Task goal")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("Blackboard Mode")).toHaveValue("interactive");
    expect(screen.queryByRole("option", { name: "Disabled" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: /Launch Reason Task/i })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /Launch Reason Task/i }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/projects/project-1/reason-tasks"),
      expect.objectContaining({ method: "POST" }),
    ));
    expect(await screen.findByText("Task detail")).toBeInTheDocument();
  });

  it("launches with an explicit assisted Blackboard conclusion mode", async () => {
    const assistedPlugin = {
      ...codexPlugin,
      capabilities: { ...codexPlugin.capabilities, assisted_conclusion: true },
    };
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(new Response(JSON.stringify({ plugins: [assistedPlugin] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(new Response(JSON.stringify({ providers: [mimoProvider] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/runtime-profiles/resolve-launch") && method === "POST") {
        return Promise.resolve(new Response(JSON.stringify({
          profile_id: "resolved-profile",
          created: true,
          profile: autoResolvedProfile,
        }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(new Response(JSON.stringify({ profiles: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/skills?")) {
        return Promise.resolve(new Response(JSON.stringify({ skills: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          run_controls?: { blackboard_conclusion_mode?: string };
        };
        expect(body.run_controls?.blackboard_conclusion_mode).toBe("assisted");
        return Promise.resolve(new Response(JSON.stringify({ pass: true, checks: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          type?: string;
          run_controls?: { blackboard_conclusion_mode?: string; policy?: { max_wrong_submissions?: number; max_rating_drawdown?: number } };
        };
        expect(body.type).toBe("ctf_challenge");
        expect(body.run_controls?.blackboard_conclusion_mode).toBe("assisted");
        expect(body.run_controls?.policy).toMatchObject({ max_wrong_submissions: 3, max_rating_drawdown: 50 });
        return Promise.resolve(new Response(JSON.stringify({ id: "task-1" }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/projects/project-1")) {
        return Promise.resolve(new Response(JSON.stringify({
          id: "project-1",
          name: "Acme",
          description: "",
          kind: "ctf_challenge",
          scope: {},
          defaults: { runner: "sandbox" },
          created_at: "",
          updated_at: "",
        }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    // The launch form renders before its plugins/providers/profiles/project
    // batch lands; an option from that batch is the readiness anchor.
    await screen.findByRole("option", { name: "MiMo" });
    const taskType = await screen.findByLabelText("Task type");
    expect(taskType).toHaveValue("");
    await userEvent.selectOptions(taskType, "pentest");
    expect(screen.getByText(/must match this Project's kind/i)).toBeInTheDocument();
    await userEvent.selectOptions(taskType, "ctf_challenge");

    const mode = await screen.findByLabelText("Blackboard Mode");
    expect(mode).toHaveValue("interactive");
    await userEvent.selectOptions(mode, "assisted");
    expect(screen.getByText(/runs a bounded Conclude Turn and applies its validated Attempt result/i)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await userEvent.clear(screen.getByLabelText("Maximum wrong submissions"));
    await userEvent.type(screen.getByLabelText("Maximum wrong submissions"), "3");
    await userEvent.clear(screen.getByLabelText("Maximum rating drawdown"));
    await userEvent.type(screen.getByLabelText("Maximum rating drawdown"), "50");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/projects/project-1/tasks"),
      expect.objectContaining({ method: "POST" }),
    ));
  });

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
    expect(await screen.findByRole("option", { name: "MiMo" })).toBeInTheDocument();
  });

  it("keeps interactive launch available when assisted conclusions are unsupported", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(new Response(JSON.stringify({ plugins: [codexPlugin] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }));
        }
        if (url.includes("/api/model-providers")) {
          return Promise.resolve(new Response(JSON.stringify({ providers: [mimoProvider] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }));
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(new Response(JSON.stringify({ profiles: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }));
        }
        if (url.includes("/api/projects/project-1")) {
          return Promise.resolve(new Response(JSON.stringify({
            id: "project-1", name: "Acme", scope: {}, defaults: { runner: "sandbox" },
            created_at: "", updated_at: "",
          }), { status: 200, headers: { "Content-Type": "application/json" } }));
        }
        return Promise.resolve(new Response(JSON.stringify({}), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }),
    );

    renderPage();

    expect(await screen.findByLabelText("Blackboard Mode")).toHaveValue("interactive");
    expect(screen.getByRole("option", { name: "Assisted" })).toBeDisabled();
    expect(screen.getByText(/does not expose the complete persistent Turn, normalized Tool\/Turn event, and closed AttemptResult contract/i)).toBeInTheDocument();
    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await waitFor(() => expect(screen.getByRole("button", { name: /launch/i })).toBeEnabled());
  });

  it("shows enabled skills before launch for preset path", async () => {
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
        if (url.includes("/api/skills?runtime_profile_id=codex-preset")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                skills: [
                  {
                    id: "recon-helper",
                    name: "Recon Helper",
                    enabled: true,
                    created_at: "",
                    updated_at: "",
                  },
                  {
                    id: "disabled-skill",
                    name: "Disabled",
                    enabled: false,
                    created_at: "",
                    updated_at: "",
                  },
                ],
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

    await userEvent.click(await screen.findByRole("button", { name: /use runtime profile/i }));
    await userEvent.selectOptions(screen.getByLabelText("Runtime Profile"), "codex-preset");
    expect(await screen.findByText("Skills for this launch")).toBeInTheDocument();
    expect(await screen.findByText(/selected runtime profile/i)).toBeInTheDocument();
    expect(await screen.findByText("Recon Helper")).toBeInTheDocument();
    expect(within(screen.getByLabelText("1 enabled skills")).queryByText("Disabled")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /launch/i })).toBeInTheDocument();
  });

  it("shows enabled global skills for direct configuration before launch", async () => {
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
          return Promise.reject(new Error("skills preview must not create launch profiles"));
        }
        if (url.includes("/api/skills")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                skills: [
                  {
                    id: "recon-helper",
                    name: "Recon Helper",
                    enabled: true,
                    created_at: "",
                    updated_at: "",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(JSON.stringify({ profiles: [] }), {
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

    expect(await screen.findByText(/globally default-enabled/i)).toBeInTheDocument();
    expect(await screen.findByText("Recon Helper")).toBeInTheDocument();
    expect(screen.queryByText(/^Profile:/)).not.toBeInTheDocument();
  });

  it("does not resolve a launch profile just to preview skills", async () => {
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
        return Promise.reject(new Error("resolve-launch should only run during launch"));
      }
      if (url.includes("/api/skills")) {
        return Promise.resolve(new Response(JSON.stringify({ skills: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(JSON.stringify({ profiles: [] }), {
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
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    expect(await screen.findByText("Skills for this launch")).toBeInTheDocument();
    await new Promise((resolve) => window.setTimeout(resolve, 300));
    expect(fetchMock).not.toHaveBeenCalledWith(
      expect.stringContaining("/api/runtime-profiles/resolve-launch"),
      expect.objectContaining({ method: "POST" }),
    );
    expect(screen.queryByText(/^Profile:/)).not.toBeInTheDocument();
  });

  it("shows empty skills state when profile has no enabled skills", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/skills?runtime_profile_id=codex-preset")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                skills: [
                  {
                    id: "disabled-skill",
                    name: "Disabled",
                    enabled: false,
                    created_at: "",
                    updated_at: "",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
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

    await userEvent.click(await screen.findByRole("button", { name: /use runtime profile/i }));
    await userEvent.selectOptions(screen.getByLabelText("Runtime Profile"), "codex-preset");
    expect(await screen.findByText("No skills enabled for this profile.")).toBeInTheDocument();
  });

  it("ignores removed project Runtime Profile defaults and uses direct configuration", async () => {
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
        const body = JSON.parse(String(init?.body ?? "{}")) as { runtime_profile_id?: string; runtime_plugin_id?: string };
        expect(body.runtime_profile_id).toBeUndefined();
        expect(body.runtime_plugin_id).toBe("codex");
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

    await screen.findByRole("option", { name: "MiMo" });
    await userEvent.click(screen.getByRole("button", { name: /use runtime profile/i }));
    expect(screen.getByLabelText("Runtime Profile")).toHaveValue("");
    expect(screen.getByLabelText("Runtime")).not.toBeDisabled();
    expect(screen.getByLabelText("Model provider")).not.toBeDisabled();

    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Skills for this launch")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalledWith(
      expect.stringContaining("/api/runtime-profiles/resolve-launch"),
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("launches a saved legacy preset without model providers", async () => {
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
          new Response(JSON.stringify({ providers: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-profiles/resolve-launch") && method === "POST") {
        return Promise.reject(new Error("resolve-launch should not be called for preset launch"));
      }
      if (url.includes("/api/skills?runtime_profile_id=legacy-preset")) {
        return Promise.resolve(
          new Response(JSON.stringify({ skills: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(JSON.stringify({ profiles: [legacyCodexPreset] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { runtime_profile_id?: string };
        expect(body.runtime_profile_id).toBe("legacy-preset");
        return Promise.resolve(
          new Response(JSON.stringify({ pass: true, checks: [{ name: "runtime_profile", status: "pass" }] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { runtime_profile_id?: string };
        expect(body.runtime_profile_id).toBe("legacy-preset");
        return Promise.resolve(
          new Response(JSON.stringify({ id: "task-1" }), {
            status: 201,
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
              defaults: { runtime_profile: "legacy-preset", runner: "sandbox" },
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

    await userEvent.click(await screen.findByRole("button", { name: /use runtime profile/i }));
    await userEvent.selectOptions(screen.getByLabelText("Runtime Profile"), "legacy-preset");
    expect(screen.getByLabelText("Runtime Profile")).toHaveValue("legacy-preset");
    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run legacy recon");

    const launchButton = screen.getByRole("button", { name: /launch/i });
    expect(launchButton).not.toBeDisabled();
    await userEvent.click(launchButton);

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/api/projects/project-1/tasks"),
        expect.objectContaining({ method: "POST" }),
      );
    });
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

    await selectPentestTaskType();
    await userEvent.type(await screen.findByLabelText("Task goal"), "Run with extension");
    await waitFor(() => expect(screen.getByRole("button", { name: /launch/i })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Runtime extensions")).toBeInTheDocument();
    expect(screen.getAllByText("npm:pi-mcp-adapter").length).toBeGreaterThan(0);
    expect(screen.getByText("Install: npm:pi-mcp-adapter")).toBeInTheDocument();
  });

  it("shows container engine and sandbox runtime root in the Sandbox environment preflight section", async () => {
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
            new Response(JSON.stringify({ profiles: [] }), {
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
                  { name: "runner", status: "pass" },
                  {
                    name: "container_engines",
                    status: "pass",
                    detail: "docker: OrbStack via docker; podman: unavailable",
                  },
                  { name: "container_engine", status: "pass", detail: "selected docker — OrbStack via docker" },
                  {
                    name: "sandbox_runtime_root",
                    status: "pass",
                    detail: "writable; sandbox bind source /tmp/runs",
                  },
                  {
                    name: "sandbox_vpn_tun",
                    status: "pass",
                    detail: "OrbStack can grant /dev/net/tun and NET_ADMIN at container create",
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
    await selectPentestTaskType();
    await userEvent.type(await screen.findByLabelText("Task goal"), "Probe sandbox env");
    await waitFor(() => expect(screen.getByRole("button", { name: /launch/i })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Container environment")).toBeInTheDocument();
    const section = document.querySelector('[data-preflight-section="sandbox-environment"]');
    expect(section).not.toBeNull();
    expect(section).toHaveTextContent("Selected engine");
    expect(section).toHaveTextContent("OrbStack via docker");
    expect(section).toHaveTextContent("Runtime root");
    expect(section).toHaveTextContent("sandbox bind source /tmp/runs");
    expect(section).toHaveTextContent("VPN TUN");
    expect(document.querySelector('[data-preflight-check="container_engine"]')).not.toBeNull();
    expect(document.querySelector('[data-preflight-check="sandbox_runtime_root"]')).not.toBeNull();
    expect(document.querySelector('[data-preflight-check="sandbox_vpn_tun"]')).not.toBeNull();
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
                  endpoint_base_url: "https://endpoint.example.test/v1",
                  base_url: "https://alias.example.test/v1",
                  protocol: "openai_responses",
                  model: "mimo-v2.5-pro",
                  api_key_env: "MIMO_API_KEY",
                  api_key_source: "generated_env",
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

    const presetToggle = await screen.findByRole("button", { name: /use runtime profile/i });
    expect(presetToggle).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(presetToggle);
    expect(presetToggle).toHaveAttribute("aria-expanded", "true");
    await userEvent.selectOptions(screen.getByLabelText("Runtime Profile"), "");

    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    const preview = await screen.findByText("Model provider", { selector: "p" });
    expect(preview.parentElement).toHaveTextContent("MiMo");
    expect(preview.parentElement).toHaveTextContent(/mimo-v2\.5-pro via openai_responses/);
    expect(preview.parentElement).toHaveTextContent("https://endpoint.example.test/v1");
    expect(preview.parentElement).not.toHaveTextContent("https://alias.example.test/v1");
    expect(preview.parentElement).toHaveTextContent("API key: generated_env via MIMO_API_KEY");
  });

  it("sends the canonical model field when direct configuration changes", async () => {
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
        const body = JSON.parse(String(init?.body ?? "{}")) as { model?: string; model_override?: string };
        expect(body.model).toBe("mimo-v2-pro");
        expect(body.model_override).toBeUndefined();
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
    await screen.findByRole("option", { name: "mimo-v2-pro" });
    await userEvent.selectOptions(modelSelect, "mimo-v2-pro");
    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Model provider", { selector: "p" })).toBeInTheDocument();
  });

  it("previews codex multi-agent tools from preflight", async () => {
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
        return Promise.resolve(
          new Response(
            JSON.stringify({
              profile_id: "resolved-profile",
              created: false,
              profile: {
                ...autoResolvedProfile,
                fields: {
                  ...autoResolvedProfile.fields,
                  codex_multi_agent: { state: "on", max_concurrent_threads_per_session: 4, max_depth: 2 },
                },
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              pass: true,
              checks: [{ name: "model_provider", status: "pass" }],
              codex_multi_agent: { state: "on", max_concurrent_threads_per_session: 4, max_depth: 2 },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
        return Promise.resolve(new Response(() => {}));
      }
      if (url.includes("/api/projects/project-1")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              id: "project-1",
              name: "Acme",
              description: "",
              kind: "pentest",
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
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    expect(await screen.findByText("Codex multi-agent tools", { selector: "p" })).toBeInTheDocument();
    expect(await screen.findByText("Spawn tools on for this launch")).toBeInTheDocument();
    expect(await screen.findByText("max threads 4 · max depth 2")).toBeInTheDocument();
    expect(
      await screen.findByText("Spawn stays a model tool inside the runtime turn; CyberPenda schedules no subagents."),
    ).toBeInTheDocument();
  });

  it("sends host-proxy-only sandbox network in launch run controls", async () => {
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
      if (url.includes("/api/skills?runtime_profile_id=resolved-profile")) {
        return Promise.resolve(
          new Response(JSON.stringify({ skills: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(JSON.stringify({ profiles: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          run_controls?: { sandbox_network?: string };
        };
        expect(body.run_controls?.sandbox_network).toBe("host_proxy_only");
        return Promise.resolve(
          new Response(JSON.stringify({ pass: true, checks: [{ name: "runtime_profile", status: "pass" }] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          run_controls?: { sandbox_network?: string };
        };
        expect(body.run_controls?.sandbox_network).toBe("host_proxy_only");
        return Promise.resolve(
          new Response(JSON.stringify({ id: "task-1" }), {
            status: 201,
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
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await userEvent.selectOptions(await screen.findByLabelText("Docker network"), "host_proxy_only");
    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await waitFor(() => expect(screen.getByRole("button", { name: /launch/i })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/api/projects/project-1/tasks"),
        expect.objectContaining({ method: "POST" }),
      );
    });
  });

  it("includes sandbox VPN TUN in the launch payload when enabled", async () => {
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
      if (url.includes("/api/skills?runtime_profile_id=resolved-profile")) {
        return Promise.resolve(
          new Response(JSON.stringify({ skills: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(JSON.stringify({ profiles: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          run_controls?: { sandbox_vpn_tun?: boolean; sandbox_network?: string; container_cli?: string };
        };
        expect(body.run_controls?.sandbox_vpn_tun).toBe(true);
        expect(body.run_controls?.sandbox_network).toBeUndefined();
        expect(body.run_controls?.container_cli).toBe("docker");
        return Promise.resolve(
          new Response(JSON.stringify({ pass: true, checks: [{ name: "runtime_profile", status: "pass" }] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          run_controls?: { sandbox_vpn_tun?: boolean; sandbox_network?: string; container_cli?: string };
        };
        expect(body.run_controls?.sandbox_vpn_tun).toBe(true);
        expect(body.run_controls?.sandbox_network).toBeUndefined();
        expect(body.run_controls?.container_cli).toBe("docker");
        return Promise.resolve(
          new Response(JSON.stringify({ id: "task-vpn" }), {
            status: 201,
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
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await userEvent.click(await screen.findByRole("checkbox", { name: /vpn tun/i }));
    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Connect OpenVPN");
    await waitFor(() => expect(screen.getByRole("button", { name: /launch/i })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/api/projects/project-1/tasks"),
        expect.objectContaining({ method: "POST" }),
      );
    });
  });

  it("sends container_cli podman when Podman runner is selected", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(new Response(JSON.stringify({ plugins: [codexPlugin] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(new Response(JSON.stringify({ providers: [mimoProvider] }), { status: 200, headers: { "Content-Type": "application/json" } }));
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
      if (url.includes("/api/skills?")) {
        return Promise.resolve(new Response(JSON.stringify({ skills: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(new Response(JSON.stringify({ profiles: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { runner?: string; run_controls?: { container_cli?: string } };
        expect(body.runner).toBe("sandbox");
        expect(body.run_controls?.container_cli).toBe("podman");
        return Promise.resolve(new Response(JSON.stringify({ pass: true, checks: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { runner?: string; run_controls?: { container_cli?: string } };
        expect(body.runner).toBe("sandbox");
        expect(body.run_controls?.container_cli).toBe("podman");
        return Promise.resolve(new Response(JSON.stringify({ id: "task-podman" }), { status: 201, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/projects/project-1")) {
        return Promise.resolve(
          new Response(JSON.stringify({ id: "project-1", name: "Acme", description: "", scope: {}, defaults: { runner: "sandbox" }, created_at: "", updated_at: "" }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await userEvent.selectOptions(await screen.findByLabelText("Runner"), "podman");
    expect(screen.getByText(/Container engine/i)).toBeInTheDocument();
    expect(screen.getByLabelText("Podman network")).toBeInTheDocument();
    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Use podman");
    await waitFor(() => expect(screen.getByRole("button", { name: /launch/i })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("/api/projects/project-1/tasks"), expect.objectContaining({ method: "POST" }));
    });
  });

  it("omits host activation from sandbox launch after switching back from host", async () => {
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
        return Promise.resolve(
          new Response(
            JSON.stringify({
              profile_id: "resolved-profile",
              created: true,
              profile: autoResolvedProfile,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/skills?runtime_profile_id=resolved-profile")) {
        return Promise.resolve(
          new Response(JSON.stringify({ skills: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(JSON.stringify({ profiles: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          runner?: string;
          run_controls?: { host_activated?: boolean };
        };
        expect(body.runner).toBe("sandbox");
        expect(body.run_controls).not.toHaveProperty("host_activated");
        return Promise.resolve(
          new Response(JSON.stringify({ pass: true, checks: [{ name: "runtime_profile", status: "pass" }] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          runner?: string;
          run_controls?: { host_activated?: boolean };
        };
        expect(body.runner).toBe("sandbox");
        expect(body.run_controls).not.toHaveProperty("host_activated");
        return Promise.resolve(
          new Response(JSON.stringify({ id: "task-1" }), {
            status: 201,
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
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await userEvent.selectOptions(await screen.findByLabelText("Runner"), "host");
    await userEvent.click(screen.getByLabelText(/explicitly activate the host runner/i));
    await userEvent.selectOptions(screen.getByLabelText("Runner"), "docker");
    await selectPentestTaskType();
    await userEvent.type(screen.getByLabelText("Task goal"), "Run recon");
    await waitFor(() => expect(screen.getByRole("button", { name: /launch/i })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/api/projects/project-1/tasks"),
        expect.objectContaining({ method: "POST" }),
      );
    });
  });

  it("switches from a Runtime Profile back to direct configuration", async () => {
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

    await userEvent.click(await screen.findByRole("button", { name: /use runtime profile/i }));
    const presetSelect = screen.getByLabelText("Runtime Profile");
    await userEvent.selectOptions(presetSelect, "codex-preset");
    expect(presetSelect).toHaveValue("codex-preset");
    expect(screen.getByLabelText("Runtime")).toBeDisabled();
    expect(screen.getByLabelText("Model")).not.toBeDisabled();
    expect(screen.getByLabelText("Reasoning effort")).not.toBeDisabled();

    await userEvent.selectOptions(presetSelect, "");

    expect(presetSelect).toHaveValue("");
    expect(screen.getByLabelText("Runtime")).not.toBeDisabled();
    expect(screen.getByLabelText("Model provider")).not.toBeDisabled();
  });

  it("explains unavailable launch actions with visible text", async () => {
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
            new Response(JSON.stringify({ providers: [] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(JSON.stringify({ profiles: [] }), {
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

    expect(await screen.findByText("No compatible providers")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Launch/i })).toBeDisabled();
    // The provider option renders from the initial empty state, but the
    // launch-unavailable reason only settles after the runtime field loads,
    // so await the settled text instead of asserting on the first commit.
    expect(await screen.findByText(/Select a compatible model provider/i)).toBeInTheDocument();
  });

  it("uploads attachments as multipart form-data on launch", async () => {
    let createBody: unknown = null;
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(new Response(JSON.stringify({ plugins: [codexPlugin] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(new Response(JSON.stringify({ providers: [mimoProvider] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/runtime-profiles/resolve-launch") && method === "POST") {
        return Promise.resolve(new Response(JSON.stringify({
          profile_id: "resolved-profile",
          created: true,
          profile: autoResolvedProfile,
        }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(new Response(JSON.stringify({ profiles: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/skills?")) {
        return Promise.resolve(new Response(JSON.stringify({ skills: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/projects/project-1/preflight") && method === "POST") {
        return Promise.resolve(new Response(JSON.stringify({ pass: true, checks: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/projects/project-1/tasks") && method === "POST") {
        createBody = init?.body;
        return Promise.resolve(new Response(JSON.stringify({ id: "task-1" }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }));
      }
      if (url.includes("/api/projects/project-1")) {
        return Promise.resolve(new Response(JSON.stringify({
          id: "project-1",
          name: "Acme",
          description: "",
          scope: {},
          defaults: { runner: "sandbox" },
          created_at: "",
          updated_at: "",
        }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await selectPentestTaskType();
    await userEvent.type(await screen.findByLabelText("Task goal"), "Run recon");
    const file = new File(["secret-notes"], "notes.txt", { type: "text/plain" });
    await userEvent.upload(screen.getByLabelText("Attachments"), file);
    expect(screen.getByText("notes.txt")).toBeInTheDocument();

    await waitFor(() => expect(screen.getByRole("button", { name: /launch/i })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /launch/i }));

    await waitFor(() => expect(createBody).toBeInstanceOf(FormData));
    const form = createBody as FormData;
    const payload = JSON.parse(String(form.get("payload"))) as { goal?: string };
    expect(payload.goal).toBe("Run recon");
    const uploaded = form.get("attachments") as File;
    expect(uploaded.name).toBe("notes.txt");
  });
});
