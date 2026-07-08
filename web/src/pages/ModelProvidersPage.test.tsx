import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    expect(providerButton).toHaveClass("focus-visible:ring-2");
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save provider" })).toBeEnabled();
    });
  });

  it("uses the shared Geist settings layout for provider selection and details", async () => {
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
            new Response(JSON.stringify({ bindings: [] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
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

    const layout = await screen.findByTestId("model-providers-settings-layout");
    expect(layout).toHaveClass(
      "grid",
      "min-w-0",
      "lg:grid-cols-[minmax(220px,280px)_minmax(0,1fr)]",
    );
    expect(screen.getByTestId("model-providers-settings-list")).toHaveClass(
      "rounded-lg",
      "border",
      "bg-card",
      "p-3",
    );
    expect(screen.getByTestId("model-providers-settings-detail")).toHaveClass(
      "min-w-0",
      "overflow-hidden",
    );

    const providerButton = await screen.findByRole("button", { name: /MiMo/i });
    expect(providerButton).toHaveAttribute("aria-current", "true");
    expect(providerButton).toHaveClass("rounded-md", "focus-visible:ring-2");
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

  it("renders and submits endpoint-backed provider payloads", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/api/model-providers") && init?.method === "PATCH") {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              id: "split",
              name: "Split",
              base_url: "https://api.example.test/v1",
              endpoints: [
                { protocol: "openai_responses", base_url: "https://api.example.test/api/coding/paas/v4" },
                { protocol: "anthropic_messages", base_url: "https://api.example.test/api/anthropic" },
              ],
              api_key_env: "SPLIT_API_KEY",
              catalog: { manual: ["gpt"], default_model: "gpt" },
              created_at: "2026-06-25T00:00:00Z",
              updated_at: "2026-06-25T00:00:01Z",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              providers: [
                {
                  id: "split",
                  name: "Split",
                  base_url: "",
                  endpoints: [
                    { protocol: "openai_responses", base_url: "https://api.example.test/v1" },
                    { protocol: "anthropic_messages", base_url: "https://api.example.test/api/anthropic" },
                  ],
                  api_key_env: "SPLIT_API_KEY",
                  catalog: { manual: ["gpt"], default_model: "gpt" },
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
        return Promise.resolve(new Response(JSON.stringify({ bindings: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    const responsesEndpoint = await screen.findByLabelText("openai_responses endpoint base URL");
    expect(responsesEndpoint).toHaveValue("https://api.example.test/v1");
    fireEvent.change(responsesEndpoint, { target: { value: "https://api.example.test/api/coding/paas/v4/" } });
    await userEvent.click(screen.getByRole("button", { name: "Save provider" }));

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input, init]) => {
          if (!String(input).includes("/api/model-providers/split") || init?.method !== "PATCH") return false;
          const body = JSON.parse(String(init.body));
          return body.endpoints?.[0]?.base_url === "https://api.example.test/api/coding/paas/v4";
        }),
      ).toBe(true);
    });
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
