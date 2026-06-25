import { describe, expect, it } from "vitest";
import type { ModelProvider, RuntimePlugin } from "@/lib/api";
import {
  applyModelProviderSelection,
  buildProfileFields,
  isModelProviderCompatibleWithRuntime,
  profileListModelHint,
  selectableModelProviders,
  showLegacyModelFields,
  usesModelProvider,
} from "./runtimeProfileForm";

const codexPlugin: RuntimePlugin = {
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
  profile_schema: {
      fields: [
        { name: "model", type: "string", label: "Model" },
        { name: "endpoint", type: "url", label: "Endpoint" },
        { name: "api_keys", type: "secret_env_map", label: "API keys" },
      ],
    },
  config_projection: { primitive: "codex_home" },
  launch: { args: ["codex"] },
  transcript: { parser: "codex_json" },
};

const plugins = [codexPlugin];

const mimoChatProvider: ModelProvider = {
  id: "mimo",
  name: "Mimo",
  base_url: "https://token-plan-cn.xiaomimimo.com/v1",
  protocols: ["openai_chat_completions"],
  api_key_env: "MIMO_API_KEY",
  catalog: { manual: ["mimo-v2-flash"], default_model: "mimo-v2-flash" },
  created_at: "",
  updated_at: "",
};

const responsesProvider: ModelProvider = {
  id: "openai-proxy",
  name: "OpenAI Proxy",
  base_url: "https://api.example.test/v1",
  protocols: ["openai_responses"],
  api_key_env: "OPENAI_PROXY_API_KEY",
  catalog: { manual: ["gpt-5"], default_model: "gpt-5" },
  created_at: "",
  updated_at: "",
};

const anthropicProvider: ModelProvider = {
  id: "anthropic",
  name: "Anthropic",
  base_url: "https://api.anthropic.com",
  protocols: ["anthropic_messages"],
  api_key_env: "ANTHROPIC_API_KEY",
  catalog: { manual: ["claude-sonnet"], default_model: "claude-sonnet" },
  created_at: "",
  updated_at: "",
};

describe("runtimeProfileForm", () => {
  it("detects model provider usage", () => {
    expect(usesModelProvider("mimo")).toBe(true);
    expect(usesModelProvider("")).toBe(false);
  });

  it("hides legacy model fields when a model provider is selected", () => {
    expect(showLegacyModelFields({ model_provider_id: "mimo" })).toBe(false);
    expect(showLegacyModelFields({ model_provider_id: "" })).toBe(true);
  });

  it("omits legacy model-service fields when saving with a model provider", () => {
    const fields = buildProfileFields(
      {
        name: "codex",
        provider: "codex",
        binary_path: "",
        model: "gpt-5",
        endpoint: "https://legacy.example.test/v1",
        model_provider_id: "mimo",
        model_provider_protocol: "",
        model_override: "",
        custom_args: "",
        env: "",
        api_key_env: "OPENAI_API_KEY",
        api_key: "sk-secret",
        runtime_extensions: [],
        mcp_servers: "",
        default_runner: "sandbox",
        sandbox_image: "",
        credential_refs: "codex-api-key",
      },
      plugins,
    );

    expect(fields.model_provider_id).toBe("mimo");
    expect(fields.model).toBeUndefined();
    expect(fields.endpoint).toBeUndefined();
    expect(fields.api_keys).toBeUndefined();
    expect(fields.credential_refs).toEqual(["codex-api-key"]);
  });

  it("clears legacy fields when selecting a model provider", () => {
    expect(
      applyModelProviderSelection(
        {
          model_provider_id: "",
          model: "gpt-5",
          endpoint: "https://legacy.example.test/v1",
          api_key_env: "OPENAI_API_KEY",
          api_key: "sk-secret",
          model_provider_protocol: "",
          model_override: "",
        },
        "mimo",
      ),
    ).toEqual({
      model_provider_id: "mimo",
      model: "",
      endpoint: "",
      api_key_env: "",
      api_key: "",
      model_provider_protocol: "",
      model_override: "",
    });
  });

  it("shows provider model hint in profile list when model provider is bound", () => {
    expect(
      profileListModelHint(
        {
          model_provider_id: "mimo",
          model_override: "mimo-v2-flash",
          model: "gpt-5",
        },
        [mimoChatProvider],
      ),
    ).toBe("mimo-v2-flash via Mimo");
    expect(profileListModelHint({ model: "gpt-5" }, [])).toBe("gpt-5");
  });

  it("filters model providers to those compatible with the selected runtime", () => {
    const providers = [mimoChatProvider, responsesProvider, anthropicProvider];

    expect(isModelProviderCompatibleWithRuntime(mimoChatProvider, codexPlugin)).toBe(false);
    expect(isModelProviderCompatibleWithRuntime(responsesProvider, codexPlugin)).toBe(true);
    expect(isModelProviderCompatibleWithRuntime(anthropicProvider, codexPlugin)).toBe(false);

    expect(selectableModelProviders(providers, codexPlugin).map((provider) => provider.id)).toEqual(["openai-proxy"]);
  });

  it("keeps a stale incompatible provider visible when already selected", () => {
    const providers = [mimoChatProvider, responsesProvider];

    expect(
      selectableModelProviders(providers, codexPlugin, "mimo").map((provider) => provider.id),
    ).toEqual(["openai-proxy", "mimo"]);
  });
});