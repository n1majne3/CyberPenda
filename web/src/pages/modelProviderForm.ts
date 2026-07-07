import type { CredentialBinding } from "@/lib/api";

export type ModelProviderForm = {
  name: string;
  base_url: string;
  protocols: string[];
  manual_models: string;
  default_model: string;
  api_key: string;
};

export function canSubmitModelProvider(
  form: Pick<ModelProviderForm, "name" | "base_url" | "api_key">,
  creating: boolean,
): boolean {
  if (!form.name.trim() || !form.base_url.trim()) {
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
