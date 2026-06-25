import { describe, expect, it } from "vitest";
import { canSubmitModelProvider } from "./modelProviderForm";

describe("canSubmitModelProvider", () => {
  it("requires an API key when creating a provider", () => {
    expect(
      canSubmitModelProvider(
        { name: "MiMo", base_url: "https://api.example.test/v1", api_key: "" },
        true,
      ),
    ).toBe(false);
    expect(
      canSubmitModelProvider(
        { name: "MiMo", base_url: "https://api.example.test/v1", api_key: "sk-test" },
        true,
      ),
    ).toBe(true);
  });

  it("allows saving an existing provider without re-entering an API key", () => {
    expect(
      canSubmitModelProvider(
        { name: "MiMo", base_url: "https://api.example.test/v1", api_key: "" },
        false,
      ),
    ).toBe(true);
  });
});