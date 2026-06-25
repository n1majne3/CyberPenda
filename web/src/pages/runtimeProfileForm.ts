import type { ModelProvider, RuntimePlugin, RuntimeProfile } from "@/lib/api";

type RuntimeProfileFields = RuntimeProfile["fields"];

export type RuntimeProfileFormInput = {
  name: string;
  provider: string;
  binary_path: string;
  model: string;
  endpoint: string;
  model_provider_id: string;
  model_provider_protocol: string;
  model_override: string;
  custom_args: string;
  env: string;
  api_key_env: string;
  api_key: string;
  runtime_extensions: { id: string; enabled: boolean; config: string }[];
  mcp_servers: string;
  default_runner: string;
  sandbox_image: string;
  credential_refs: string;
};

const API_KEY_CONFIGURED = "[configured]";

const DEFAULT_API_KEY_ENV: Record<string, string> = {
  codex: "OPENAI_API_KEY",
  claude_code: "ANTHROPIC_AUTH_TOKEN",
  pi: "ANTHROPIC_API_KEY",
};

function intersection(left: string[], right: string[]): string[] {
  const allowed = new Set(right);
  return left.filter((item) => allowed.has(item));
}

export function compatibleProtocolsForRuntime(
  plugin: RuntimePlugin | undefined,
  provider: ModelProvider,
): string[] {
  return intersection(plugin?.model_provider?.supported_protocols ?? [], provider.protocols ?? []);
}

export function isModelProviderCompatibleWithRuntime(
  provider: ModelProvider,
  plugin: RuntimePlugin | undefined,
): boolean {
  return compatibleProtocolsForRuntime(plugin, provider).length > 0;
}

export function selectableModelProviders(
  providers: ModelProvider[],
  plugin: RuntimePlugin | undefined,
  selectedProviderID?: string,
): ModelProvider[] {
  const compatible = providers.filter((provider) => isModelProviderCompatibleWithRuntime(provider, plugin));
  const selectedID = selectedProviderID?.trim();
  if (selectedID && !compatible.some((provider) => provider.id === selectedID)) {
    const selected = providers.find((provider) => provider.id === selectedID);
    if (selected) return [...compatible, selected];
  }
  return compatible;
}

export function usesModelProvider(modelProviderID: string): boolean {
  return modelProviderID.trim() !== "";
}

export function showLegacyModelFields(form: Pick<RuntimeProfileFormInput, "model_provider_id">): boolean {
  return !usesModelProvider(form.model_provider_id);
}

export function applyModelProviderSelection<
  T extends Pick<
    RuntimeProfileFormInput,
    "model_provider_id" | "model" | "endpoint" | "api_key_env" | "api_key" | "model_provider_protocol" | "model_override"
  >,
>(form: T, providerID: string): T {
  if (!providerID.trim()) {
    return { ...form, model_provider_id: "", model_provider_protocol: "", model_override: "" };
  }
  return {
    ...form,
    model_provider_id: providerID,
    model_provider_protocol: "",
    model_override: "",
    model: "",
    endpoint: "",
    api_key_env: "",
    api_key: "",
  };
}

export function profileListModelHint(
  fields: {
    model_provider_id?: string;
    model_override?: string;
    model?: string;
  },
  modelProviders: ModelProvider[],
): string | undefined {
  const providerID = fields.model_provider_id?.trim();
  if (providerID) {
    const provider = modelProviders.find((candidate) => candidate.id === providerID);
    const model = fields.model_override?.trim() || provider?.catalog?.default_model?.trim();
    if (model && provider?.name) return `${model} via ${provider.name}`;
    if (provider?.name) return provider.name;
    return providerID;
  }
  const legacyModel = fields.model?.trim();
  return legacyModel || undefined;
}

export function buildProfileFields(form: RuntimeProfileFormInput, plugins: RuntimePlugin[]): RuntimeProfileFields {
  const fields: RuntimeProfileFields = {};
  const binaryPath = emptyToUndefined(form.binary_path);
  const modelProviderID = emptyToUndefined(form.model_provider_id);
  const modelProviderProtocol = emptyToUndefined(form.model_provider_protocol);
  const modelOverride = emptyToUndefined(form.model_override);
  const customArgs = splitLines(form.custom_args);
  const env = parseEnv(form.env);
  const runtimeExtensions = buildRuntimeExtensionRefs(form.runtime_extensions);
  const mcpServers = parseMCPServers(form.mcp_servers);
  const defaultRunner = emptyToUndefined(form.default_runner);
  const sandboxImage = emptyToUndefined(form.sandbox_image);
  const apiKeyEnv = emptyToUndefined(form.api_key_env) ?? defaultAPIKeyEnv(form.provider, plugins);
  const apiKey = form.api_key.trim();
  const usingModelProvider = usesModelProvider(modelProviderID ?? "");

  if (binaryPath) fields.binary_path = binaryPath;
  if (!usingModelProvider) {
    const model = emptyToUndefined(form.model);
    const endpoint = emptyToUndefined(form.endpoint);
    if (model) fields.model = model;
    if (endpoint) fields.endpoint = endpoint;
    if (apiKeyEnv && apiKey) {
      fields.api_keys = { [apiKeyEnv]: apiKey };
    } else if (apiKeyEnv && apiKey === API_KEY_CONFIGURED) {
      fields.api_keys = { [apiKeyEnv]: API_KEY_CONFIGURED };
    }
  }
  if (modelProviderID) fields.model_provider_id = modelProviderID;
  if (modelProviderProtocol) fields.model_provider_protocol = modelProviderProtocol;
  if (modelOverride) fields.model_override = modelOverride;
  if (customArgs.length > 0) fields.custom_args = customArgs;
  if (Object.keys(env).length > 0) fields.env = env;
  if (mcpServers && mcpServers.length > 0) fields.mcp_servers = mcpServers;
  if (runtimeExtensions && runtimeExtensions.length > 0) fields.runtime_extensions = runtimeExtensions;
  if (defaultRunner) fields.default_runner = defaultRunner;
  if (sandboxImage) fields.sandbox_image = sandboxImage;
  const credentialRefs = splitLines(form.credential_refs);
  if (credentialRefs.length > 0) fields.credential_refs = credentialRefs;
  return fields;
}

function defaultAPIKeyEnv(provider: string, plugins: RuntimePlugin[]): string | undefined {
  const plugin = plugins.find((item) => item.id === provider);
  if (plugin?.credential_env?.[0]) return plugin.credential_env[0];
  return DEFAULT_API_KEY_ENV[provider];
}

function buildRuntimeExtensionRefs(
  refs: RuntimeProfileFormInput["runtime_extensions"],
): RuntimeProfileFields["runtime_extensions"] {
  return refs
    .map((ref) => {
      const id = ref.id.trim();
      if (!id) return null;
      const config = parseEnv(ref.config);
      return {
        id,
        enabled: ref.enabled,
        ...(Object.keys(config).length > 0 ? { config } : {}),
      };
    })
    .filter((ref): ref is NonNullable<typeof ref> => ref != null);
}

function splitLines(value: string): string[] {
  return value
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
}

function parseEnv(value: string): Record<string, string> {
  const trimmed = value.trim();
  if (!trimmed) return {};

  if (trimmed.startsWith("{")) {
    try {
      const parsed: unknown = JSON.parse(trimmed);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return Object.fromEntries(
          Object.entries(parsed as Record<string, unknown>)
            .filter(([key]) => key.trim())
            .map(([key, raw]) => [key.trim(), stringifyEnvValue(raw)]),
        );
      }
    } catch {
      // Fall through to line-based parsing.
    }
  }

  const out: Record<string, string> = {};
  for (const line of splitLines(value)) {
    const eq = line.indexOf("=");
    if (eq !== -1) {
      const key = line.slice(0, eq).trim();
      const envValue = line.slice(eq + 1).trim();
      if (key) out[key] = envValue;
    }
  }
  return out;
}

function stringifyEnvValue(raw: unknown): string {
  if (raw == null) return "";
  return String(raw);
}

function parseMCPServers(value: string): RuntimeProfileFields["mcp_servers"] {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  try {
    const parsed: unknown = JSON.parse(trimmed);
    return Array.isArray(parsed) ? (parsed as RuntimeProfileFields["mcp_servers"]) : undefined;
  } catch {
    return undefined;
  }
}

function emptyToUndefined(value: string) {
  const trimmed = value.trim();
  return trimmed || undefined;
}