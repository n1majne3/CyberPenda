import { describe, expect, it } from "vitest";
import {
  buildModelProviderPayload,
  canSubmitModelProvider,
  endpointValidationErrors,
  providerToModelProviderForm,
} from "./modelProviderForm";

describe("canSubmitModelProvider", () => {
  it("requires an API key when creating a provider", () => {
    expect(
      canSubmitModelProvider(
        { name: "MiMo", base_url: "https://api.example.test/v1", api_key: "", protocols: ["openai_responses"], endpoint_base_urls: {} },
        true,
      ),
    ).toBe(false);
    expect(
      canSubmitModelProvider(
        { name: "MiMo", base_url: "https://api.example.test/v1", api_key: "sk-test", protocols: ["openai_responses"], endpoint_base_urls: {} },
        true,
      ),
    ).toBe(true);
  });

  it("allows saving an existing provider without re-entering an API key", () => {
    expect(
      canSubmitModelProvider(
        { name: "MiMo", base_url: "https://api.example.test/v1", api_key: "", protocols: ["openai_responses"], endpoint_base_urls: {} },
        false,
      ),
    ).toBe(true);
  });

  it("builds endpoint payloads from shared quick setup and per-protocol overrides", () => {
    const payload = buildModelProviderPayload({
      name: "Split Provider",
      base_url: "https://hub.example.test/v1/",
      protocols: ["openai_responses", "anthropic_messages", "openai_chat_completions"],
      endpoint_base_urls: {
        openai_chat_completions: "https://hub.example.test/api/coding/paas/v4/",
      },
      manual_models: "gpt-5\nclaude-sonnet",
      default_model: "gpt-5",
      api_key: "sk-test",
    });

    expect(payload).toEqual({
      name: "Split Provider",
      base_url: "https://hub.example.test/v1",
      endpoints: [
        { protocol: "openai_responses", base_url: "https://hub.example.test/v1" },
        { protocol: "anthropic_messages", base_url: "https://hub.example.test" },
        { protocol: "openai_chat_completions", base_url: "https://hub.example.test/api/coding/paas/v4" },
      ],
      catalog: {
        manual: ["gpt-5", "claude-sonnet"],
        default_model: "gpt-5",
      },
    });
  });

  it("hydrates forms from endpoint-backed providers without provider-level protocols", () => {
    const form = providerToModelProviderForm({
      id: "split",
      name: "Split",
      base_url: "",
      api_key_env: "SPLIT_API_KEY",
      endpoints: [
        { protocol: "openai_responses", base_url: "https://api.example.test/v1" },
        { protocol: "anthropic_messages", base_url: "https://api.example.test/api/anthropic" },
      ],
      catalog: { manual: ["gpt"], default_model: "gpt" },
    });

    expect(form.protocols).toEqual(["openai_responses", "anthropic_messages"]);
    expect(form.base_url).toBe("https://api.example.test/v1");
    expect(form.endpoint_base_urls).toEqual({
      openai_responses: "https://api.example.test/v1",
      anthropic_messages: "https://api.example.test/api/anthropic",
    });
  });

  it("reports protocol-specific operation suffix validation errors", () => {
    const errors = endpointValidationErrors({
      name: "Bad",
      base_url: "https://api.example.test/v1/responses/",
      protocols: ["openai_responses"],
      endpoint_base_urls: {},
      manual_models: "",
      default_model: "",
      api_key: "sk-test",
    });

    expect(errors.openai_responses).toMatch(/openai_responses/);
    expect(
      canSubmitModelProvider(
        { name: "Bad", base_url: "https://api.example.test/v1/responses/", api_key: "sk-test", protocols: ["openai_responses"], endpoint_base_urls: {} },
        true,
      ),
    ).toBe(false);
  });
});
