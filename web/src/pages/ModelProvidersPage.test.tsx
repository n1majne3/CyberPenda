import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ModelProvidersPage } from "./ModelProvidersPage";

function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter>
        <ModelProvidersPage />
      </MemoryRouter>
    </StrictMode>,
  );
}

describe("ModelProvidersPage", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("enables Save provider without a configured API key binding", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/model-providers")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                providers: [
                  {
                    id: "mimo",
                    name: "MiMo",
                    base_url: "https://api.example.test/v1",
                    protocols: ["openai_responses"],
                    api_key_env: "MIMO_API_KEY",
                    catalog: { manual: ["mimo-v2"], default_model: "mimo-v2" },
                    created_at: "2026-06-25T00:00:00Z",
                    updated_at: "2026-06-25T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/credential-bindings")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                bindings: [
                  {
                    id: "binding-1",
                    credential_ref: "MIMO_API_KEY",
                    scope: "global",
                    source: { kind: "env", value: "MIMO_API_KEY" },
                    created_at: "2026-06-25T00:00:00Z",
                    updated_at: "2026-06-25T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({}), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();

    expect(await screen.findByText("MiMo")).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save provider" })).toBeEnabled();
    });
  });
});