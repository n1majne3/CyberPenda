import { describe, expect, it } from "vitest";
import type { ModelProvider, RuntimePlugin, RuntimeProfile } from "@/lib/api";
import {
  canLaunch,
  defaultLaunchForm,
  formFromPreset,
  initialLaunchState,
  launchRuntimes,
  launchModelOverridePayload,
  launchReasoningEffortPayload,
  launchRuntimeProfileId,
  modelsForProvider,
  presetMatchesRuntime,
  presetsForRuntime,
  resolveLaunchPayload,
  launchSelectionFromProfile,
} from "./taskLaunchForm";

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
  profile_schema: { fields: [] },
  config_projection: { primitive: "codex_home" },
  launch: { args: ["codex"] },
  transcript: { parser: "codex_json" },
};

const piPlugin: RuntimePlugin = {
  ...codexPlugin,
  id: "pi",
  name: "Pi",
  model_provider: {
    requirement: "required",
    supported_protocols: ["anthropic_messages"],
    protocol_preference: ["anthropic_messages"],
  },
};

const fakePlugin: RuntimePlugin = {
  ...codexPlugin,
  id: "fake",
  name: "Fake",
};

const mimoProvider: ModelProvider = {
  id: "mimo",
  name: "MiMo",
  base_url: "https://api.example.test/v1",
  protocols: ["openai_responses"],
  api_key_env: "MIMO_API_KEY",
  catalog: { manual: ["mimo-v2-flash", "mimo-v2-pro"], default_model: "mimo-v2-flash" },
};

const anthropicProvider: ModelProvider = {
  id: "anthropic",
  name: "Anthropic",
  base_url: "https://api.anthropic.com",
  protocols: ["anthropic_messages"],
  api_key_env: "ANTHROPIC_API_KEY",
  catalog: { default_model: "claude-sonnet-4" },
};

describe("taskLaunchForm", () => {
  it("excludes fake runtime from launch runtimes", () => {
    const runtimes = launchRuntimes([codexPlugin, piPlugin, fakePlugin]);
    expect(runtimes.map((plugin) => plugin.id)).toEqual(["codex", "pi"]);
  });

  it("requires goal, runtime, and model provider for auto-resolved launches", () => {
    expect(canLaunch("scan target", { runtime: "codex", modelProviderId: "mimo" })).toBe(true);
    expect(canLaunch("", { runtime: "codex", modelProviderId: "mimo" })).toBe(false);
    expect(canLaunch("scan target", { runtime: "", modelProviderId: "mimo" })).toBe(false);
    expect(canLaunch("scan target", { runtime: "codex", modelProviderId: "" })).toBe(false);
  });

  it("allows saved legacy presets to launch without a model provider", () => {
    const preset: RuntimeProfile = {
      id: "legacy-preset",
      name: "Legacy Codex",
      provider: "codex",
      fields: {
        model: "gpt-5",
        endpoint: "https://legacy.example.test/v1",
        default_runner: "sandbox",
      },
      created_at: "",
      updated_at: "",
    };

    const form = formFromPreset(preset, [], "sandbox");

    expect(form.modelProviderId).toBe("");
    expect(canLaunch("scan target", form, { presetId: "legacy-preset" })).toBe(true);
  });

  it("lists provider models with default first", () => {
    expect(modelsForProvider(mimoProvider)).toEqual(["mimo-v2-flash", "mimo-v2-pro"]);
    expect(modelsForProvider(anthropicProvider)).toEqual(["claude-sonnet-4"]);
  });

  it("builds resolve-launch payload with optional model override", () => {
    expect(
      resolveLaunchPayload({
        runtime: "codex",
        modelProviderId: "mimo",
        modelOverride: "",
      }),
    ).toEqual({
      provider: "codex",
      model_provider_id: "mimo",
    });
    expect(
      resolveLaunchPayload({
        runtime: "codex",
        modelProviderId: "mimo",
        modelOverride: "mimo-v2-pro",
      }),
    ).toEqual({
      provider: "codex",
      model_provider_id: "mimo",
      model_override: "mimo-v2-pro",
    });
  });

  it("derives launch selection from an existing profile", () => {
    const profile: RuntimeProfile = {
      id: "profile-1",
      name: "Codex · MiMo",
      provider: "codex",
      fields: {
        model_provider_id: "mimo",
        model_override: "mimo-v2-pro",
        reasoning_effort: "xhigh",
        default_runner: "host",
      },
      created_at: "",
      updated_at: "",
    };
    expect(launchSelectionFromProfile(profile)).toEqual({
      runtime: "codex",
      modelProviderId: "mimo",
      modelOverride: "mimo-v2-pro",
      reasoningEffort: "xhigh",
      runner: "host",
    });
  });

  it("always sends an explicit launch reasoning effort from the five values", () => {
    // Empty inheritance resolves to high outside the select (no sixth option).
    expect(launchReasoningEffortPayload({ reasoningEffort: "" })).toEqual({
      reasoning_effort: "high",
    });
    expect(launchReasoningEffortPayload({ reasoningEffort: "max" })).toEqual({
      reasoning_effort: "max",
    });
  });

  it("inherits profile reasoning effort or high when building launch form", () => {
    const missingEffort: RuntimeProfile = {
      id: "profile-missing",
      name: "Codex",
      provider: "codex",
      fields: { model_provider_id: "mimo" },
      created_at: "",
      updated_at: "",
    };
    expect(launchSelectionFromProfile(missingEffort).reasoningEffort).toBe("high");
    expect(formFromPreset(missingEffort, [mimoProvider], "sandbox").reasoningEffort).toBe("high");
  });

  it("filters runtime profile presets by runtime", () => {
    const profiles: RuntimeProfile[] = [
      {
        id: "codex-preset",
        name: "Codex MCP",
        provider: "codex",
        fields: { model_provider_id: "mimo", mcp_servers: "[]" },
        created_at: "",
        updated_at: "",
      },
      {
        id: "pi-preset",
        name: "Pi Default",
        provider: "pi",
        fields: { model_provider_id: "anthropic" },
        created_at: "",
        updated_at: "",
      },
    ];
    expect(presetsForRuntime(profiles, "codex").map((profile) => profile.id)).toEqual(["codex-preset"]);
    expect(presetMatchesRuntime("codex-preset", profiles, "codex")).toBe(true);
    expect(presetMatchesRuntime("codex-preset", profiles, "pi")).toBe(false);
  });

  it("excludes launch-resolved profiles from preset pickers", () => {
    const profiles: RuntimeProfile[] = [
      {
        id: "codex-preset",
        name: "Codex MCP",
        provider: "codex",
        kind: "manual",
        fields: { model_provider_id: "mimo" },
        created_at: "",
        updated_at: "",
      },
      {
        id: "codex-auto",
        name: "Codex · MiMo",
        provider: "codex",
        kind: "launch_resolve",
        fields: { model_provider_id: "mimo" },
        created_at: "",
        updated_at: "",
      },
    ];
    expect(presetsForRuntime(profiles, "codex").map((profile) => profile.id)).toEqual(["codex-preset"]);
    expect(presetMatchesRuntime("codex-auto", profiles, "codex")).toBe(false);
    expect(
      initialLaunchState({
        plugins: [codexPlugin],
        modelProviders: [mimoProvider],
        profiles,
        defaultRuntimeProfileId: "codex-auto",
      }).presetId,
    ).toBe("");
  });

  it("builds launch form from a selected preset", () => {
    const preset: RuntimeProfile = {
      id: "codex-preset",
      name: "Codex MCP",
      provider: "codex",
      fields: {
        model_provider_id: "mimo",
        model_override: "mimo-v2-pro",
        default_runner: "host",
      },
      created_at: "",
      updated_at: "",
    };
    expect(formFromPreset(preset, [mimoProvider], "sandbox")).toEqual({
      runtime: "codex",
      modelProviderId: "mimo",
      modelOverride: "mimo-v2-pro",
      reasoningEffort: "high",
      runner: "sandbox",
    });
  });

  it("initializes preset mode from project default runtime profile", () => {
    const profiles: RuntimeProfile[] = [
      {
        id: "codex-preset",
        name: "Codex MCP",
        provider: "codex",
        fields: { model_provider_id: "mimo", model_override: "mimo-v2-pro" },
        created_at: "",
        updated_at: "",
      },
    ];
    const state = initialLaunchState({
      plugins: [codexPlugin, piPlugin],
      modelProviders: [mimoProvider],
      profiles,
      defaultRuntimeProfileId: "codex-preset",
      projectRunner: "sandbox",
    });
    expect(state.presetId).toBe("codex-preset");
    expect(state.presetOpen).toBe(true);
    expect(state.form.runtime).toBe("codex");
    expect(state.form.modelProviderId).toBe("mimo");
  });

  it("builds launch model override payload only for preset launches", () => {
    expect(launchModelOverridePayload("", { modelOverride: "mimo-v2-pro" })).toEqual({});
    expect(launchModelOverridePayload("codex-preset", { modelOverride: "" })).toEqual({});
    expect(launchModelOverridePayload("codex-preset", { modelOverride: "mimo-v2-pro" })).toEqual({
      model_override: "mimo-v2-pro",
    });
  });

  it("uses preset profile id directly when preset mode is active", () => {
    expect(launchRuntimeProfileId("codex-preset", "resolved-profile")).toBe("codex-preset");
    expect(launchRuntimeProfileId("", "resolved-profile")).toBe("resolved-profile");
  });

  it("picks compatible provider and default model for a runtime", () => {
    const profile: RuntimeProfile = {
      id: "legacy",
      name: "Legacy",
      provider: "codex",
      fields: { model: "gpt-5" },
      created_at: "",
      updated_at: "",
    };
    const form = defaultLaunchForm({
      plugins: [codexPlugin, piPlugin],
      modelProviders: [mimoProvider, anthropicProvider],
      defaultProfile: profile,
      projectRunner: "sandbox",
    });
    expect(form.runtime).toBe("codex");
    expect(form.modelProviderId).toBe("mimo");
    expect(form.modelOverride).toBe("mimo-v2-flash");
    expect(form.runner).toBe("sandbox");
  });
});
