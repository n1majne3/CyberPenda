import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage } from "./RuntimeProfilesPage.testSupport";

describe("RuntimeProfilesPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });


  it("loads the saved native config only when requested and keeps long lines bounded", async () => {
    const longEndpoint = `https://${"very-long-host-segment-".repeat(12)}example.test/v1`;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.endsWith("/projected-config")) {
          return Promise.resolve(new Response(JSON.stringify({ provider: "codex", format: "toml", text: `base_url = "${longEndpoint}"` })));
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Codex with long config",
                    provider: "codex",
                    fields: {
                      model: "gpt-5",
                      endpoint: longEndpoint,
                      mcp_servers: [
                        {
                          name: "external",
                          mode: "external",
                          url: `${longEndpoint}/mcp/${"deep-path-".repeat(12)}`,
                        },
                      ],
                    },
                    created_at: "",
                    updated_at: "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(
            new Response(JSON.stringify({ plugins: [] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-extensions")) {
          return Promise.resolve(
            new Response(JSON.stringify({ extensions: [] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-extension-catalog")) {
          return Promise.resolve(
            new Response(JSON.stringify({ items: [] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
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

    expect(await screen.findByText("Codex with long config")).toBeInTheDocument();
    expect(screen.queryByText("Generated config preview")).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Actual runtime config" })).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    const previewSection = await screen.findByRole("region", { name: "Actual runtime config" });
    await waitFor(() => expect(previewSection.querySelector("pre")).toHaveTextContent(longEndpoint));
    expect(previewSection).toHaveClass("min-w-0");
    expect(previewSection.querySelector("pre")).toHaveClass("w-full", "max-w-full", "overflow-x-auto");
  });


  it("opens the config edit window on the projected config and imports the edit", async () => {
    const importCalls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/import-config")) {
          importCalls.push(String(init?.body ?? ""));
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profile: {
                  id: "profile-1",
                  name: "Claude Edit",
                  provider: "claude_code",
                  fields: {
                    env: { MY_TOOL_TAG: "abc" },
                    custom_config_file: '{\n  "enabledPlugins": {"warp@claude-code-warp": true}\n}',
                  },
                  created_at: "",
                  updated_at: "",
                },
                mapped_keys: ["env"],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.endsWith("/projected-config")) {
          return Promise.resolve(new Response(JSON.stringify({ provider: "claude_code", format: "json", text: importCalls.length ? '{"env":{"MY_TOOL_TAG":"abc"},"enabledPlugins":{"warp@claude-code-warp":true}}' : "{}" })));
        }
        if (url.includes("/api/runtime-profiles")) {
          
          
          const imported = importCalls.length > 0;
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude Edit",
                    provider: "claude_code",
                    fields: imported
                      ? { env: { MY_TOOL_TAG: "abc" }, custom_config_file: '{\n  "enabledPlugins": {"warp@claude-code-warp": true}\n}' }
                      : {},
                    created_at: "",
                    updated_at: "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify(url.includes("model-providers") ? { providers: [] } : { plugins: [], extensions: [], items: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Claude Edit/i }));
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));

    const editor = await screen.findByLabelText(/config editor/i);
    await userEvent.type(editor, "MY_TOOL_TAG");
    await userEvent.click(await screen.findByRole("button", { name: /Import config/i }));

    await waitFor(() => expect(screen.queryByLabelText(/config editor/i)).not.toBeInTheDocument());
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    expect(await screen.findByText(/warp@claude-code-warp/)).toBeInTheDocument();
    expect(importCalls).toHaveLength(1);
    expect(importCalls[0]).toContain("MY_TOOL_TAG");
  });


  it("renders per-key import errors in the config edit window", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/import-config")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                error: "profile config import refused: env.MY_API_TOKEN: key looks like a secret",
                keys: [{ key: "env.MY_API_TOKEN", message: "key looks like a secret; use the API keys structured field instead" }],
              }),
              { status: 400, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.endsWith("/projected-config")) {
          return Promise.resolve(new Response(JSON.stringify({ provider: "claude_code", format: "json", text: "{}" })));
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude Reject",
                    provider: "claude_code",
                    fields: {},
                    created_at: "",
                    updated_at: "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify(url.includes("model-providers") ? { providers: [] } : { plugins: [], extensions: [], items: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Claude Reject/i }));
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Import config/i }));

    expect(await screen.findAllByText(/env\.MY_API_TOKEN/)).not.toHaveLength(0);
    expect(screen.getAllByText(/looks like a secret/i).length).toBeGreaterThan(0);
  });


  it("confirms before a provider switch clears the Custom Config File", async () => {
    const patchCalls: Record<string, unknown>[] = [];
    let first = true;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/runtime-profiles/") && init?.method === "PATCH") {
          patchCalls.push(JSON.parse(String(init.body)));
          if (first) {
            first = false;
            return Promise.resolve(
              new Response(
                JSON.stringify({
                  error: "switching provider from claude_code to codex requires clearing the custom config file; confirm to discard it",
                  code: "provider_switch_needs_overlay_clear",
                }),
                { status: 409, headers: { "Content-Type": "application/json" } },
              ),
            );
          }
          return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude With Overlay",
                    provider: "claude_code",
                    fields: { custom_config_file: '{\n  "enabledPlugins": {"warp@claude-code-warp": true}\n}' },
                    created_at: "",
                    updated_at: "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(new Response(JSON.stringify({ plugins: [] }), { status: 200 }));
        }
        if (url.includes("/api/runtime-extensions")) {
          return Promise.resolve(new Response(JSON.stringify({ extensions: [] }), { status: 200 }));
        }
        if (url.includes("/api/runtime-extension-catalog")) {
          return Promise.resolve(new Response(JSON.stringify({ items: [] }), { status: 200 }));
        }
        return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Claude With Overlay/i }));

    
    const providerSelect = await screen.findByLabelText("Runtime", { selector: "#profile-provider" });
    await userEvent.selectOptions(providerSelect, "codex");
    await userEvent.click(await screen.findByRole("button", { name: /Save/i }));

    
    expect(await screen.findByText(/Switch runtime provider\?/i)).toBeInTheDocument();
    expect(patchCalls).toHaveLength(1);
    expect(patchCalls[0]).not.toHaveProperty("confirm_provider_switch_clears_overlay");

    
    await userEvent.click(await screen.findByRole("button", { name: /Switch and clear/i }));
    await waitFor(() => expect(patchCalls).toHaveLength(2));
    expect(patchCalls[1]).toMatchObject({ confirm_provider_switch_clears_overlay: true });
    expect(patchCalls[1].fields.custom_config_file ?? "").toBe("");
  });

  it("keeps config loading on demand and exposes a read error without an editable fallback", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/projected-config")) return Promise.resolve(new Response(JSON.stringify({ error: "Projection unavailable" }), { status: 503 }));
      const body = url.endsWith("/api/runtime-profiles")
        ? { profiles: [{ id: "saved", name: "Saved profile", provider: "codex", fields: {}, updated_at: "1" }] }
        : url.endsWith("/model-provider-migration-preview") ? { eligible: false }
        : { plugins: [], extensions: [], providers: [] };
      return Promise.resolve(new Response(JSON.stringify(body)));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPage();
    const view = await screen.findByRole("button", { name: "View actual config" });
    expect(fetchMock.mock.calls.some(([input]) => /projected-config|merged-config-preview/.test(String(input)))).toBe(false);
    expect(screen.queryByRole("region", { name: "Model provider migration" })).not.toBeInTheDocument();
    await userEvent.click(view);
    expect(await screen.findByRole("alert")).toHaveTextContent("Projection unavailable");
    expect(screen.queryByRole("button", { name: "Edit config" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Runtime config editor")).not.toBeInTheDocument();
  });


  it("keeps unsaved form changes out of the saved config and prevents import over them", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      const body = url.endsWith("/api/runtime-profiles")
        ? { profiles: [{ id: "saved", name: "Saved profile", provider: "codex", fields: {}, updated_at: "1" }] }
        : url.endsWith("/projected-config") ? { provider: "codex", format: "toml", text: 'model = "saved-model"' }
        : url.endsWith("/model-provider-migration-preview") ? { eligible: false }
        : { plugins: [], extensions: [], providers: [] };
      return Promise.resolve(new Response(JSON.stringify(body)));
    }));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    await userEvent.click(await screen.findByRole("button", { name: "Edit config" }));
    await userEvent.type(screen.getByLabelText("Name"), " changed");
    expect(screen.getByRole("button", { name: "Import config" })).toBeDisabled();
    expect(screen.getByText("Save profile changes before editing the runtime config.")).toBeInTheDocument();
    expect(screen.getByLabelText("Runtime config editor")).toHaveValue('model = "saved-model"');
  });
});
