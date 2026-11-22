import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { renderPage } from "./RuntimeProfilesPage.testSupport";

it("shows the final merged config after import", async () => {
    let imported = false;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/import-config")) {
          imported = true;
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profile: {
                  id: "profile-1",
                  name: "Claude Merge",
                  provider: "claude_code",
                  fields: { custom_config_file: '{"enabledPlugins":{"warp@claude-code-warp":true}}' },
                  created_at: "",
                  updated_at: "",
                },
                mapped_keys: [],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.endsWith("/projected-config")) {
          return Promise.resolve(new Response(JSON.stringify({ provider: "claude_code", format: "json", text: imported ? '{"enabledPlugins":{"warp@claude-code-warp":true}}' : '{}' })));
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude Merge",
                    provider: "claude_code",
                    fields: imported
                      ? { custom_config_file: '{"enabledPlugins":{"warp@claude-code-warp":true}}' }
                      : {},
                    created_at: "",
                    updated_at: imported ? "2026-06-20T00:00:00Z" : "2026-06-19T00:00:00Z",
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
    await userEvent.click(await screen.findByRole("button", { name: /Claude Merge/i }));
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Import config/i }));

    await waitFor(() => expect(screen.queryByLabelText(/config editor/i)).not.toBeInTheDocument());
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    expect(await screen.findByText(/warp@claude-code-warp/)).toBeInTheDocument();
  });

it("seeds the config editor with the provider-native projected config", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/projected-config")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                provider: "codex",
                format: "toml",
                text: 'approval_policy = "never"\nsandbox_mode = "danger-full-access"\n',
                custom_config_file: "",
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/merged-config-preview")) {
          return Promise.resolve(
            new Response(JSON.stringify({ provider: "codex", merged: {} }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Codex Seed",
                    provider: "codex",
                    fields: { model: "gpt-test" },
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
    await userEvent.click(await screen.findByRole("button", { name: /Codex Seed/i }));
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));

    const editor = await screen.findByLabelText(/config editor/i);
    await waitFor(() => {
      const value = (editor as HTMLTextAreaElement).value;
      expect(value).toContain("approval_policy");
      
      expect(value).not.toContain('"launch_preview"');
    });
  });

it("reopens the config editor on the merged projected file including the Custom Config File", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/projected-config")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                provider: "claude_code",
                format: "json",
                text: JSON.stringify(
                  {
                    env: { ANTHROPIC_MODEL: "claude-opus-4-6" },
                    enabledPlugins: { "warp@claude-code-warp": true },
                  },
                  null,
                  2,
                ),
                custom_config_file: '{\n  "enabledPlugins": {\n    "warp@claude-code-warp": true\n  }\n}',
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/merged-config-preview")) {
          return Promise.resolve(
            new Response(JSON.stringify({ provider: "claude_code", merged: { enabledPlugins: { "warp@claude-code-warp": true } } }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude Warp",
                    provider: "claude_code",
                    fields: { custom_config_file: '{\n  "enabledPlugins": {\n    "warp@claude-code-warp": true\n  }\n}' },
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
    await userEvent.click(await screen.findByRole("button", { name: /Claude Warp/i }));
    await userEvent.click(await screen.findByRole("button", { name: "View actual config" }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));

    const editor = await screen.findByLabelText(/config editor/i);
    await waitFor(() => {
      expect((editor as HTMLTextAreaElement).value).toContain("warp@claude-code-warp");
    });
  });
