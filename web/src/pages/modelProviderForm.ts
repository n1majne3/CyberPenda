import type { CredentialBinding, ModelProvider } from "@/lib/api";

export type ModelProviderForm = {
  name: string;
  base_url: string;
  protocols: string[];
  endpoint_base_urls: Record<string, string>;
  manual_models: string;
  default_model: string;
  api_key: string;
};

export function canSubmitModelProvider(
  form: Pick<ModelProviderForm, "name" | "base_url" | "api_key" | "protocols" | "endpoint_base_urls">,
  creating: boolean,
): boolean {
  if (!form.name.trim() || !form.base_url.trim()) {
    return false;
  }
  if (Object.keys(endpointValidationErrors(form)).length > 0) {
    return false;
  }
  if (creating) {
    return !!form.api_key.trim();
  }
  return true;
}

export function providerApiKeyPlaceholder(binding?: CredentialBinding): string {
  if (binding?.source.kind === "literal" && binding.source.value === "[configured]") {
    return "[configured]";
  }
  return "Enter API key…";
}

export function providerToModelProviderForm(provider: ModelProvider, binding?: CredentialBinding): ModelProviderForm {
  const endpointBaseURLs = endpointBaseURLsByProtocol(provider);
  const protocols = modelProviderProtocols(provider);
  return {
    name: provider.name,
    base_url: normalizedBaseURL(provider.base_url || firstEndpointBaseURL(provider)),
    protocols,
    endpoint_base_urls: endpointBaseURLs,
    manual_models: (provider.catalog?.manual ?? []).join("\n"),
    default_model: provider.catalog?.default_model ?? "",
    api_key: binding?.source.kind === "literal" && binding.source.value === "[configured]" ? "[configured]" : "",
  };
}

export function buildModelProviderPayload(form: ModelProviderForm) {
  return {
    name: form.name,
    base_url: normalizedBaseURL(form.base_url),
    endpoints: form.protocols.map((protocol) => ({
      protocol,
      base_url: endpointBaseURLForProtocol(form, protocol),
    })),
    catalog: {
      manual: splitLines(form.manual_models),
      default_model: form.default_model || undefined,
    },
  };
}

export function endpointValidationErrors(
  form: Pick<ModelProviderForm, "base_url" | "protocols" | "endpoint_base_urls">,
): Record<string, string> {
  const errors: Record<string, string> = {};
  const seen = new Set<string>();
  for (const protocol of form.protocols) {
    if (seen.has(protocol)) {
      errors[protocol] = `Duplicate endpoint protocol ${protocol}.`;
      continue;
    }
    seen.add(protocol);
    const baseURL = endpointBaseURLForProtocol(form, protocol);
    if (!baseURL) {
      errors[protocol] = `${protocol} endpoint base URL is required.`;
      continue;
    }
    if (!isAbsoluteURL(baseURL)) {
      errors[protocol] = `${protocol} endpoint base URL must include scheme and host.`;
      continue;
    }
    if (hasOperationSuffix(baseURL)) {
      errors[protocol] = `${protocol} endpoint base URL must not include messages, responses, or chat/completions operation suffixes.`;
    }
  }
  return errors;
}

export function endpointBaseURLForProtocol(
  form: Pick<ModelProviderForm, "base_url" | "endpoint_base_urls">,
  protocol: string,
): string {
  return normalizedBaseURL(form.endpoint_base_urls[protocol] ?? deriveEndpointBaseURL(form.base_url, protocol));
}

export function modelProviderProtocols(provider: ModelProvider): string[] {
  if (provider.endpoints?.length) {
    return provider.endpoints.map((endpoint) => endpoint.protocol);
  }
  return provider.protocols ?? [];
}

function endpointBaseURLsByProtocol(provider: ModelProvider): Record<string, string> {
  const out: Record<string, string> = {};
  for (const endpoint of provider.endpoints ?? []) {
    out[endpoint.protocol] = normalizedBaseURL(endpoint.base_url);
  }
  return out;
}

function firstEndpointBaseURL(provider: ModelProvider): string {
  return provider.endpoints?.[0]?.base_url ?? "";
}

function deriveEndpointBaseURL(baseURL: string, protocol: string): string {
  const normalized = normalizedBaseURL(baseURL);
  if (protocol !== "anthropic_messages") return normalized;
  try {
    const url = new URL(normalized);
    const segments = url.pathname.split("/").filter(Boolean);
    segments.pop();
    url.pathname = segments.length ? `/${segments.join("/")}` : "";
    url.search = "";
    url.hash = "";
    return normalizedBaseURL(url.toString());
  } catch {
    return normalized;
  }
}

function normalizedBaseURL(value: string): string {
  return value.trim().replace(/\/+$/, "");
}

function isAbsoluteURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    return Boolean(parsed.protocol && parsed.host);
  } catch {
    return false;
  }
}

function hasOperationSuffix(value: string): boolean {
  try {
    const segments = new URL(value).pathname.split("/").filter(Boolean).map((segment) => segment.toLowerCase());
    const last = segments.at(-1);
    if (last === "messages" || last === "responses") return true;
    return segments.length >= 2 && segments.at(-2) === "chat" && last === "completions";
  } catch {
    return false;
  }
}

function splitLines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}
