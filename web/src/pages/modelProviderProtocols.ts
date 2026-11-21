import type { ModelProvider } from "@/lib/api";
import type { ModelProviderForm } from "./modelProviderForm";

export const PROTOCOLS = [
  "openai_chat_completions",
  "openai_responses",
  "anthropic_messages",
] as const;

export const PROTOCOL_LABELS: Record<string, string> = {
  openai_chat_completions: "OpenAI Chat Completions",
  openai_responses: "OpenAI Responses",
  anthropic_messages: "Anthropic Messages",
};

export type Form = ModelProviderForm;

export function providerProtocols(provider: ModelProvider): string[] {
  if (provider.endpoints?.length) {
    return provider.endpoints.map((endpoint) => endpoint.protocol);
  }
  return provider.protocols ?? [];
}

export function providerDisplayURL(provider: ModelProvider): string {
  if (provider.base_url) return provider.base_url;
  return provider.endpoints?.[0]?.base_url ?? "";
}

export function shortProtocol(protocol: string): string {
  if (protocol === "openai_chat_completions") return "chat";
  if (protocol === "openai_responses") return "responses";
  if (protocol === "anthropic_messages") return "anthropic";
  return protocol;
}

export function toggle(values: string[], value: string, checked: boolean): string[] {
  const next = new Set(values);
  if (checked) next.add(value);
  else next.delete(value);
  return Array.from(next);
}

export function toggleProtocol(form: Form, protocol: string, checked: boolean): Form {
  const endpointBaseURLs = { ...form.endpoint_base_urls };
  if (!checked) delete endpointBaseURLs[protocol];
  return {
    ...form,
    protocols: toggle(form.protocols, protocol, checked),
    endpoint_base_urls: endpointBaseURLs,
  };
}

export function splitLines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}
