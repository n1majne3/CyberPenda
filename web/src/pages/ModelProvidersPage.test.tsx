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
    vi.restoreAllMocks();
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

    const providerButton = await screen.findByRole("button", { name: /MiMo/i });
    expect(providerButton).toHaveAttribute("type", "button");
    expect(providerButton).toHaveAttribute("aria-pressed", "true");
    expect(providerButton).toHaveClass("focus-visible:ring-3");
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save provider" })).toBeEnabled();
    });
  });

  it("associates labels with named provider form controls", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/model-providers")) {
          return Promise.resolve(new Response(JSON.stringify({ providers: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
        }
        if (url.includes("/api/credential-bindings")) {
          return Promise.resolve(new Response(JSON.stringify({ bindings: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
        }
        return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
      }),
    );

    renderPage();

    expect(await screen.findByLabelText("Name")).toHaveAttribute("name", "provider_name");
    expect(screen.getByLabelText("Base URL")).toHaveAttribute("name", "base_url");
    expect(screen.getByLabelText("API key")).toHaveAttribute("name", "api_key");
    expect(screen.getByLabelText("API key")).toHaveAttribute("autocomplete", "off");
  });

  it("groups supported protocol checkboxes under a named fieldset", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/model-providers")) {
          return Promise.resolve(new Response(JSON.stringify({ providers: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
        }
        if (url.includes("/api/credential-bindings")) {
          return Promise.resolve(new Response(JSON.stringify({ bindings: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
        }
        return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
      }),
    );

    renderPage();

    const group = await screen.findByRole("group", { name: "Supported protocols" });
    expect(group).toContainElement(screen.getByRole("checkbox", { name: "openai_responses" }));
  });

  it("requires confirmation before deleting a model provider", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
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
                  created_at: "",
                  updated_at: "",
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/credential-bindings")) {
        return Promise.resolve(new Response(JSON.stringify({ bindings: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Delete/i }));

    expect(confirm).toHaveBeenCalledWith("Delete model provider MiMo?");
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/model-providers/mimo") && init?.method === "DELETE",
      ),
    ).toBe(false);
  });
});
