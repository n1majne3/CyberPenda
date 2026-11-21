import type { ModelProvider, RuntimeExtension, RuntimePlugin, RuntimeProfile } from "@/lib/api";
import { displayReasoningEffort } from "@/pages/runtimeProfileForm";

export const FALLBACK_PROVIDER_IDS = ["codex", "claude_code", "pi", "fake"] as const;

export const HIDDEN_PROVIDER_IDS = new Set(["fake"]);

export const RUNNERS = ["sandbox", "host"] as const;

export const PROVIDER_LABELS: Record<string, string> = {
  codex: "Codex",
  claude_code: "Claude Code",
  pi: "Pi",
  fake: "Fake harness",
};

export const DEFAULT_API_KEY_ENV: Record<string, string> = {
  codex: "OPENAI_API_KEY",
  claude_code: "ANTHROPIC_AUTH_TOKEN",
  pi: "ANTHROPIC_API_KEY",
};

// The daemon redacts stored API keys to this sentinel in profile payloads;
export const API_KEY_CONFIGURED = "[configured]";

export type RuntimeProfileFields = RuntimeProfile["fields"];

export type RuntimeExtensionFormRef = {
  id: string;
  enabled: boolean;
  config: string;
};

export type ProfileForm = {
  name: string;
  provider: string;
  binary_path: string;
  model: string;
  endpoint: string;
  model_provider_id: string;
  model_provider_protocol: string;
  model_override: string;
  reasoning_effort: string;
  custom_args: string;
  env: string;
  api_key_env: string;
  api_key: string;
  runtime_extensions: RuntimeExtensionFormRef[];
  mcp_servers: string;
  default_runner: string;
  sandbox_image: string;
  credential_refs: string;
  codex_multi_agent_state: "inherit" | "on" | "off";
  codex_multi_agent_max_threads: string;
  codex_multi_agent_max_depth: string;
};

export const emptyForm: ProfileForm = {
  name: "",
  provider: "codex",
  binary_path: "",
  model: "",
  endpoint: "",
  model_provider_id: "",
  model_provider_protocol: "",
  model_override: "",
  reasoning_effort: "high",
  custom_args: "",
  env: "",
  api_key_env: "",
  api_key: "",
  runtime_extensions: [],
  mcp_servers: "",
  default_runner: "sandbox",
  sandbox_image: "",
  credential_refs: "",
  codex_multi_agent_state: "inherit",
  codex_multi_agent_max_threads: "",
  codex_multi_agent_max_depth: "",
};

export function fallbackRuntimePlugins(): RuntimePlugin[] {
  return FALLBACK_PROVIDER_IDS.map((id) => ({
    schema_version: 1,
    id,
    name: PROVIDER_LABELS[id] ?? id,
    binary: { default: id === "claude_code" ? "claude" : id === "fake" ? "fake" : id },
    capabilities: {
      sandbox: true,
      host: true,
      mcp_config: id !== "fake",
      streaming_transcript: id !== "fake",
      resume: true,
    },
    profile_schema: {
      fields: [
        "binary_path",
        "model",
        "endpoint",
        "custom_args",
        "env",
        "api_keys",
        "credential_refs",
        "runtime_extensions",
        "mcp_servers",
        "default_runner",
        "sandbox_image",
        ...(id === "codex" ? ["codex_multi_agent"] : []),
      ].map((name) => ({
        name,
        type: name === "codex_multi_agent" ? "codex_multi_agent" : "string",
        label: name,
      })),
    },
    config_projection:
      id === "claude_code"
        ? { primitive: "claude_settings", config_path: "runtime-home/claude/settings.json", mcp_config_path: "workdir/.mcp.json" }
        : id === "codex"
          ? { primitive: "codex_home", config_path: "runtime-home/codex/config.toml" }
          : id === "pi"
            ? { primitive: "pi_agent", config_path: "runtime-home/pi/agent/models.json", mcp_config_path: "runtime-home/pi/agent/mcp.json" }
            : { primitive: "none" },
    launch: { args: [] },
    process_env: fallbackProcessEnv(id),
    credential_env: DEFAULT_API_KEY_ENV[id] ? [DEFAULT_API_KEY_ENV[id]] : [],
    transcript: { parser: fallbackTranscriptParser(id) },
  }));
}

export function fallbackProcessEnv(provider: string): Record<string, string> | undefined {
  if (provider === "claude_code") return { CLAUDE_HOME: "{{runtime_home}}/claude" };
  if (provider === "codex") return { CODEX_HOME: "{{runtime_home}}/codex" };
  if (provider === "pi") return { PI_CODING_AGENT_DIR: "{{runtime_home}}/pi/agent" };
  return undefined;
}

export function fallbackTranscriptParser(provider: string): string {
  if (provider === "claude_code") return "claude_stream_json";
  if (provider === "codex") return "codex_json";
  if (provider === "pi") return "pi_json_session";
  return "plain_runtime_output";
}

export function pluginIDs(plugins: RuntimePlugin[]): string[] {
  const ids = plugins.map((plugin) => plugin.id);
  return ids.length > 0 ? ids : [...FALLBACK_PROVIDER_IDS];
}

export function pluginFor(plugins: RuntimePlugin[], provider: string): RuntimePlugin | undefined {
  return plugins.find((plugin) => plugin.id === provider);
}

export function modelProviderModels(provider: ModelProvider) {
  return Array.from(new Set([...(provider.catalog?.manual ?? []), ...(provider.catalog?.refreshed ?? [])])).sort();
}

// defaultProvider returns the first selectable (non-hidden) plugin id, so that
// creating a new profile never defaults to a hidden provider like the fake
// harness. Falls back to the first plugin or "codex" when none qualify.
export function defaultProvider(plugins: RuntimePlugin[]): string {
  const first = plugins.find((plugin) => !HIDDEN_PROVIDER_IDS.has(plugin.id));
  return first?.id ?? plugins[0]?.id ?? "codex";
}

export function pluginLabel(plugins: RuntimePlugin[], provider: string): string {
  return pluginFor(plugins, provider)?.name || PROVIDER_LABELS[provider] || provider;
}

export function pluginHasField(plugin: RuntimePlugin | undefined, field: string): boolean {
  if (!plugin) return true;
  return plugin.profile_schema.fields.some((item) => item.name === field);
}

export function defaultAPIKeyEnv(provider: string, plugins: RuntimePlugin[]): string | undefined {
  return pluginFor(plugins, provider)?.credential_env?.[0] || DEFAULT_API_KEY_ENV[provider];
}

export function profileToForm(profile: RuntimeProfile, plugins: RuntimePlugin[]): ProfileForm {
  const apiKeyEntries = Object.entries(profile.fields.api_keys ?? {});
  const [apiKeyEnv = "", apiKeyValue = ""] = apiKeyEntries[0] ?? [];
  return {
    name: profile.name,
    provider: profile.provider,
    binary_path: profile.fields.binary_path ?? "",
    model: profile.fields.model_provider_id ? "" : (profile.fields.model ?? ""),
    endpoint: profile.fields.model_provider_id ? "" : (profile.fields.endpoint ?? ""),
    model_provider_id: profile.fields.model_provider_id ?? "",
    model_provider_protocol: profile.fields.model_provider_protocol ?? "",
    model_override: profile.fields.model_override ?? "",
    reasoning_effort: displayReasoningEffort(profile.fields.reasoning_effort),
    custom_args: (profile.fields.custom_args ?? []).join("\n"),
    env: formatEnv(profile.fields.env),
    api_key_env: profile.fields.model_provider_id ? "" : (apiKeyEnv || defaultAPIKeyEnv(profile.provider, plugins) || ""),
    api_key: profile.fields.model_provider_id ? "" : apiKeyValue,
    runtime_extensions: (profile.fields.runtime_extensions ?? []).map((ref) => ({
      id: ref.id,
      enabled: ref.enabled ?? true,
      config: formatEnv(ref.config),
    })),
    mcp_servers: formatMCPServers(profile.fields.mcp_servers),
    default_runner: profile.fields.default_runner ?? "sandbox",
    sandbox_image: profile.fields.sandbox_image ?? "",
    credential_refs: (profile.fields.credential_refs ?? []).join("\n"),
    codex_multi_agent_state: profile.fields.codex_multi_agent
      ? profile.fields.codex_multi_agent.enabled
        ? "on"
        : "off"
      : "inherit",
    codex_multi_agent_max_threads:
      profile.fields.codex_multi_agent?.max_concurrent_threads_per_session?.toString() ?? "",
    codex_multi_agent_max_depth: profile.fields.codex_multi_agent?.max_depth?.toString() ?? "",
  };
}

export function compatibleRuntimeExtensionRefs(
  refs: RuntimeExtensionFormRef[],
  provider: string,
  extensions: RuntimeExtension[]
): RuntimeExtensionFormRef[] {
  return refs.filter((ref) => {
    const extension = extensions.find((item) => item.id === ref.id);
    if (!extension) return true;
    return extension.compatible_runtime_plugins.includes(provider);
  });
}

export function formatEnv(env?: Record<string, string>): string {
  if (!env) return "";
  return Object.entries(env)
    .map(([key, value]) => `${key}=${value}`)
    .join("\n");
}

export function formatMCPServers(servers?: RuntimeProfileFields["mcp_servers"]): string {
  if (!servers || servers.length === 0) return "";
  return JSON.stringify(servers, null, 2);
}
