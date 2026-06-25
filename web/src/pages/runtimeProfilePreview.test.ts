import { describe, expect, it } from "vitest";
import type { ModelProvider, RuntimePlugin } from "@/lib/api";
import { buildModelProviderSnapshot, enrichPreviewWithModelProvider } from "./runtimeProfilePreview";

const codexPlugin: RuntimePlugin = {
  schema_version: 1,
  id: "codex",
  name: "Codex",
  binary: { default: "codex" },
  capabilities: { sandbox: true, host: true, mcp_config: true, streaming_transcript: true, resume: true },
  model_provider: {
    requirement: "required",
    supported_protocols: ["openai_responses", "openai_chat_completions"],
    protocol_preference: ["openai_responses"],
  },
  profile_schema: { fields: [] },
  config_projection: { primitive: "codex_home", config_path: "runtime-home/codex/config.toml" },
  launch: { args: ["codex"] },
  transcript: { parser: "codex_json" },
};

const mimoProvider: ModelProvider = {
  id: "mimo",
  name: "MiMo",
  base_url: "https://api.example.test/v1",
  protocols: ["openai_responses"],
  api_key_env: "MIMO_API_KEY",
  catalog: { manual: ["mimo-v2.5-pro"], default_model: "mimo-v2.5-pro" },
  created_at: "",
  updated_at: "",
};

describe("runtimeProfilePreview", () => {
  it("builds snapshot from model provider and runtime protocol preference", () => {
    const snapshot = buildModelProviderSnapshot(
      { model_provider_id: "mimo" },
      mimoProvider,
      codexPlugin,
    );

    expect(snapshot).toEqual({
      model_provider_id: "mimo",
      model_provider_name: "MiMo",
      base_url: "https://api.example.test/v1",
      protocol: "openai_responses",
      model: "mimo-v2.5-pro",
      api_key_env: "MIMO_API_KEY",
      api_key_source: "generated_env",
      projection_target: "codex_home",
    });
  });

  it("uses model override when pinned on the profile", () => {
    const snapshot = buildModelProviderSnapshot(
      { model_provider_id: "mimo", model_override: "mimo-v2-flash" },
      { ...mimoProvider, catalog: { manual: ["mimo-v2-flash", "mimo-v2.5-pro"], default_model: "mimo-v2.5-pro" } },
      codexPlugin,
    );

    expect(snapshot?.model).toBe("mimo-v2-flash");
  });

  it("adds snapshot and resolved codex config when model provider is set", () => {
    const preview = enrichPreviewWithModelProvider(
      {
        provider: "codex",
        config_path: "runtime-home/codex/config.toml",
        config_toml: "",
      },
      { model_provider_id: "mimo" },
      [mimoProvider],
      codexPlugin,
    );

    expect(preview.model_provider_snapshot).toMatchObject({
      model_provider_id: "mimo",
      model: "mimo-v2.5-pro",
      base_url: "https://api.example.test/v1",
    });
    expect(preview.config_toml).toContain('model = "mimo-v2.5-pro"');
    expect(preview.config_toml).toContain('model_provider = "mimo"');
    expect(preview.config_toml).toContain('base_url = "https://api.example.test/v1"');
  });
});