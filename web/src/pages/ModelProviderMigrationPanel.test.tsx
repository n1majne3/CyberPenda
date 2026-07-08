import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { describe, expect, it, vi } from "vitest";
import { ModelProviderMigrationPanel } from "./ModelProviderMigrationPanel";

describe("ModelProviderMigrationPanel", () => {
  it("presents the migration preview as an accessible Geist callout", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              profile_id: "profile-1",
              profile_name: "Codex CN",
              runtime_provider: "codex",
              eligible: true,
              proposed: {
                name: "Codex CN",
                base_url: "https://api.example.test/v1",
                model: "gpt-5",
                suggested_protocol: "openai_responses",
              },
              matches: [],
              api_key_sources: [{ kind: "inline_api_key", env_var: "OPENAI_API_KEY", configured: true }],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        ),
      ),
    );

    render(
      <StrictMode>
        <ModelProviderMigrationPanel profileId="profile-1" onMigrated={() => {}} onError={() => {}} />
      </StrictMode>,
    );

    const callout = await screen.findByRole("region", { name: "Model provider migration" });
    expect(callout).toHaveClass("rounded-lg", "border-info/20", "bg-muted/30");
    expect(screen.getByRole("group", { name: "Migration target" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Migrate profile" })).toHaveClass("focus-visible:ring-2");
  });

  it("shows migration actions for eligible legacy profiles", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              profile_id: "profile-1",
              profile_name: "Codex CN",
              runtime_provider: "codex",
              eligible: true,
              proposed: {
                name: "Codex CN",
                base_url: "https://api.example.test/v1",
                model: "gpt-5",
                suggested_protocol: "openai_responses",
              },
              matches: [],
              api_key_sources: [{ kind: "inline_api_key", env_var: "OPENAI_API_KEY", configured: true }],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        ),
      ),
    );

    render(
      <StrictMode>
        <ModelProviderMigrationPanel profileId="profile-1" onMigrated={() => {}} onError={() => {}} />
      </StrictMode>,
    );

    expect(await screen.findByText("Migrate to model provider")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Migrate profile" })).toBeEnabled();
  });

  it("handles null matches and api_key_sources from the API", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              profile_id: "profile-1",
              profile_name: "Codex CN",
              runtime_provider: "codex",
              eligible: true,
              proposed: {
                name: "Codex CN",
                base_url: "https://api.example.test/v1",
              },
              matches: null,
              api_key_sources: null,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        ),
      ),
    );

    render(
      <StrictMode>
        <ModelProviderMigrationPanel profileId="profile-1" onMigrated={() => {}} onError={() => {}} />
      </StrictMode>,
    );

    expect(await screen.findByText("Migrate to model provider")).toBeInTheDocument();
  });

  it("hides after a successful migration", async () => {
    const eligiblePreview = {
      profile_id: "profile-1",
      profile_name: "Codex CN",
      runtime_provider: "codex",
      eligible: true,
      proposed: {
        name: "Codex CN",
        base_url: "https://api.example.test/v1",
      },
      matches: [],
      api_key_sources: [],
    };
    const ineligiblePreview = {
      ...eligiblePreview,
      eligible: false,
      reason: "runtime profile already references a model provider",
    };

    let migrated = false;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/model-provider-migration-preview")) {
          const body = migrated ? ineligiblePreview : eligiblePreview;
          return Promise.resolve(
            new Response(JSON.stringify(body), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/model-provider-migration") && init?.method === "POST") {
          migrated = true;
          return Promise.resolve(
            new Response(JSON.stringify({ profile: {}, provider: {} }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        return Promise.resolve(new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
      }),
    );

    const user = userEvent.setup();
    render(
      <StrictMode>
        <ModelProviderMigrationPanel profileId="profile-1" onMigrated={() => {}} onError={() => {}} />
      </StrictMode>,
    );

    expect(await screen.findByText("Migrate to model provider")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Migrate profile" }));

    await waitFor(() => {
      expect(screen.queryByText("Migrate to model provider")).not.toBeInTheDocument();
    });
  });
});
