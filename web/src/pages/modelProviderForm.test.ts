import { describe, expect, it } from "vitest";
import {
  buildModelProviderPayload,
  canSubmitModelProvider,
  endpointBaseURLForProtocol,
  endpointValidationErrors,
  providerToModelProviderForm,
} from "./modelProviderForm";

// A minimal form for the quick-setup + validation cases, where only the shared
// base URL, enabled protocols, and optional per-protocol overrides matter.
const quickSetupForm = (
  baseURL: string,
  protocols: string[],
  endpointBaseURLs: Record<string, string> = {},
  name = "Hub",
) => ({ name, base_url: baseURL, protocols, endpoint_base_urls: endpointBaseURLs });

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

  describe("quick setup endpoint derivation", () => {
    // OpenAI Chat Completions and OpenAI Responses use the shared provider base
    // URL as-is; Anthropic Messages removes the final non-empty path segment so
    // Claude Code can append its own versioned messages operation path.
    it("uses the shared provider base URL as-is for OpenAI Chat Completions and Responses", () => {
      const form = quickSetupForm("https://hub.example.test/v1", ["openai_chat_completions", "openai_responses"]);
      expect(endpointBaseURLForProtocol(form, "openai_chat_completions")).toBe("https://hub.example.test/v1");
      expect(endpointBaseURLForProtocol(form, "openai_responses")).toBe("https://hub.example.test/v1");
    });

    it.each(["/v1", "/v2"])(
      "derives Anthropic Messages by removing the final path segment for %s",
      (version) => {
        const form = quickSetupForm(`https://hub.example.test${version}`, ["anthropic_messages"]);
        expect(endpointBaseURLForProtocol(form, "anthropic_messages")).toBe("https://hub.example.test");
      },
    );

    it("removes only the final non-empty path segment for deeper paths", () => {
      // A Z.ai-like coding path: Anthropic drops only the last segment, not the
      // whole prefix. This is pure segment splitting, not version detection.
      const form = quickSetupForm("https://api.example.test/api/coding/paas/v4", ["anthropic_messages"]);
      expect(endpointBaseURLForProtocol(form, "anthropic_messages")).toBe("https://api.example.test/api/coding/paas");
    });

    it("leaves a host-only shared base URL unchanged for Anthropic Messages", () => {
      const form = quickSetupForm("https://hub.example.test", ["anthropic_messages"]);
      expect(endpointBaseURLForProtocol(form, "anthropic_messages")).toBe("https://hub.example.test");
    });

    it("normalizes trailing slashes before deriving any protocol endpoint", () => {
      const form = quickSetupForm("https://hub.example.test/v1/", ["openai_responses", "anthropic_messages"]);
      expect(endpointBaseURLForProtocol(form, "openai_responses")).toBe("https://hub.example.test/v1");
      expect(endpointBaseURLForProtocol(form, "anthropic_messages")).toBe("https://hub.example.test");
    });

    it("prefers an explicit per-protocol override over the derived value", () => {
      const form = quickSetupForm(
        "https://hub.example.test/v1",
        ["anthropic_messages"],
        { anthropic_messages: "https://hub.example.test/api/anthropic" },
      );
      expect(endpointBaseURLForProtocol(form, "anthropic_messages")).toBe("https://hub.example.test/api/anthropic");
    });
  });

  describe("endpoint validation messaging", () => {
    it("reports a protocol-specific duplicate protocol error", () => {
      const errors = endpointValidationErrors(
        quickSetupForm("https://api.example.test/v1", ["openai_responses", "openai_responses"], {}, "Bad"),
      );
      expect(errors.openai_responses).toMatch(/openai_responses/);
      expect(errors.openai_responses).toMatch(/duplicate/i);
    });

    it("reports a protocol-specific missing base URL error when the shared base URL is blank", () => {
      const errors = endpointValidationErrors(
        quickSetupForm("", ["openai_responses"], {}, "Bad"),
      );
      expect(errors.openai_responses).toMatch(/openai_responses/);
      expect(errors.openai_responses).toMatch(/required/i);
    });

    it("names the affected protocol when a base URL is not absolute", () => {
      const errors = endpointValidationErrors(
        quickSetupForm("api.example.test/v1", ["openai_responses"], {}, "Bad"),
      );
      expect(errors.openai_responses).toMatch(/openai_responses/);
      expect(errors.openai_responses).toMatch(/scheme and host/i);
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
