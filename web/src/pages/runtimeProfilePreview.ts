import type { ModelProvider, RuntimePlugin } from "@/lib/api";

export type RuntimeProfileFields = {
  model?: string;
  endpoint?: string;
  model_provider_id?: string;
  model_provider_protocol?: string;
  model_override?: string;
  env?: Record<string, string>;
  api_keys?: Record<string, string>;
  [key: string]: unknown;
};

export type ModelProviderSnapshot = {
  model_provider_id: string;
  model_provider_name: string;
  base_url: string;
  protocol: string;
  model: string;
  api_key_env: string;
  api_key_source: string;
  projection_target: string;
};

function intersection(left: string[], right: string[]): string[] {
  const rightSet = new Set(right);
  return left.filter((value) => rightSet.has(value));
}

function resolveProtocol(
  provider: ModelProvider,
  plugin: RuntimePlugin | undefined,
  pin?: string,
): string | undefined {
  const supported = intersection(plugin?.model_provider?.supported_protocols ?? [], provider.protocols ?? []);
  const trimmedPin = pin?.trim();
  if (trimmedPin) {
    return supported.includes(trimmedPin) ? trimmedPin : undefined;
  }
  for (const preferred of plugin?.model_provider?.protocol_preference ?? []) {
    if (supported.includes(preferred)) return preferred;
  }
  return supported[0];
}

function resolveModel(fields: RuntimeProfileFields, provider: ModelProvider): string | undefined {
  const override = fields.model_override?.trim();
  if (override) return override;
  const defaultModel = provider.catalog?.default_model?.trim();
  if (defaultModel) return defaultModel;
  return undefined;
}

export function buildModelProviderSnapshot(
  fields: RuntimeProfileFields,
  provider: ModelProvider | undefined,
  plugin: RuntimePlugin | undefined,
): ModelProviderSnapshot | undefined {
  const providerID = fields.model_provider_id?.trim();
  if (!providerID || !provider) return undefined;

  const protocol = resolveProtocol(provider, plugin, fields.model_provider_protocol);
  const model = resolveModel(fields, provider);
  if (!protocol || !model) return undefined;

  return {
    model_provider_id: provider.id,
    model_provider_name: provider.name,
    base_url: provider.base_url,
    protocol,
    model,
    api_key_env: provider.api_key_env,
    api_key_source: "generated_env",
    projection_target: plugin?.config_projection.primitive ?? "",
  };
}

function codexWireAPI(protocol: string): string {
  return protocol === "openai_responses" ? "responses" : protocol;
}

function buildCodexConfigTomlFromSnapshot(snapshot: ModelProviderSnapshot): string {
  const providerID = snapshot.model_provider_id;
  const wireApi = codexWireAPI(snapshot.protocol);
  return [
    `model = "${snapshot.model}"`,
    `model_provider = "${providerID}"`,
    `cli_auth_credentials_store = "file"`,
    "",
    `[model_providers.${providerID}]`,
    `name = "${snapshot.model_provider_name}"`,
    `base_url = "${snapshot.base_url.replace(/\/$/, "")}"`,
    `wire_api = "${wireApi}"`,
    "requires_openai_auth = true",
  ].join("\n");
}

export function enrichPreviewWithModelProvider(
  preview: Record<string, unknown>,
  fields: RuntimeProfileFields,
  modelProviders: ModelProvider[],
  plugin: RuntimePlugin | undefined,
): Record<string, unknown> {
  const provider = modelProviders.find((candidate) => candidate.id === fields.model_provider_id);
  const snapshot = buildModelProviderSnapshot(fields, provider, plugin);
  if (!snapshot) return preview;

  const next: Record<string, unknown> = {
    ...preview,
    model_provider_snapshot: snapshot,
    model: snapshot.model,
    endpoint: snapshot.base_url,
  };

  if (preview.provider === "codex") {
    next.config_toml = buildCodexConfigTomlFromSnapshot(snapshot);
  }

  return next;
}