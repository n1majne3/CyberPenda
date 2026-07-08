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
      "min-w-0",
      "flex-col",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );
    expect(screen.getByTestId("model-providers-settings-detail")).toHaveClass(
      "min-w-0",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );
    expect(layout).toHaveClass("lg:min-h-0", "lg:flex-1");
    expect(screen.getByLabelText("Search model providers")).toBeInTheDocument();

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

  it("derives composed endpoints from a shared base URL in the quick setup flow", async () => {
    // From scratch: one shared provider base URL should derive protocol-specific
    // endpoints before save. OpenAI protocols use it as-is; Anthropic Messages
    // drops the final /v1 segment. The saved payload holds composed endpoint
    // records and no separate shared-base field.
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/api/model-providers") && init?.method === "POST") {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              id: "hub",
              name: "Hub",
              base_url: "https://hub.example.test/v1",
              endpoints: [
                { protocol: "openai_chat_completions", base_url: "https://hub.example.test/v1" },
                { protocol: "openai_responses", base_url: "https://hub.example.test/v1" },
                { protocol: "anthropic_messages", base_url: "https://hub.example.test" },
              ],
              api_key_env: "HUB_API_KEY",
              catalog: {},
              created_at: "2026-07-08T00:00:00Z",
              updated_at: "2026-07-08T00:00:00Z",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(
          new Response(JSON.stringify({ providers: [] }), { status: 200, headers: { "Content-Type": "application/json" } }),
        );
      }
      if (url.includes("/api/credential-bindings")) {
        return Promise.resolve(new Response(JSON.stringify({ bindings: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    // Empty provider list starts on a blank New provider form.
    await screen.findByText("No model providers yet.");
    expect(screen.getByRole("button", { name: "Create provider" })).toBeDisabled();

    await userEvent.type(await screen.findByLabelText("Name"), "Hub");
    await userEvent.type(screen.getByLabelText("Base URL"), "https://hub.example.test/v1");
    await userEvent.type(screen.getByLabelText("API key"), "sk-test");

    // Enable all three protocols from the blank draft form.
    await userEvent.click(screen.getByRole("checkbox", { name: "openai_responses" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "openai_chat_completions" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "anthropic_messages" }));

    // The derived endpoint inputs reflect quick-setup derivation without the
    // user typing a single per-protocol value.
    expect(screen.getByLabelText("openai_chat_completions endpoint base URL")).toHaveValue("https://hub.example.test/v1");
    expect(screen.getByLabelText("openai_responses endpoint base URL")).toHaveValue("https://hub.example.test/v1");
    expect(screen.getByLabelText("anthropic_messages endpoint base URL")).toHaveValue("https://hub.example.test");

    await userEvent.click(screen.getByRole("button", { name: "Create provider" }));

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input, init]) => {
          if (!String(input).includes("/api/model-providers") || init?.method !== "POST") return false;
          const body = JSON.parse(String(init.body));
          if (body.shared_base_url !== undefined) return false;
          const byProtocol = Object.fromEntries(body.endpoints.map((e: { protocol: string; base_url: string }) => [e.protocol, e.base_url]));
          return (
            byProtocol.openai_chat_completions === "https://hub.example.test/v1" &&
            byProtocol.openai_responses === "https://hub.example.test/v1" &&
            byProtocol.anthropic_messages === "https://hub.example.test"
          );
        }),
      ).toBe(true);
    });
  });

  it("creates a draft provider without endpoint records", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/api/model-providers") && init?.method === "POST") {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              id: "draft",
              name: "Draft",
              base_url: "",
              endpoints: [],
              api_key_env: "DRAFT_API_KEY",
              catalog: {},
              created_at: "2026-07-08T00:00:00Z",
              updated_at: "2026-07-08T00:00:00Z",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(
          new Response(JSON.stringify({ providers: [] }), { status: 200, headers: { "Content-Type": "application/json" } }),
        );
      }
      if (url.includes("/api/credential-bindings")) {
        return Promise.resolve(new Response(JSON.stringify({ bindings: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await screen.findByText("No model providers yet.");
    expect(screen.queryByLabelText("openai_responses endpoint base URL")).not.toBeInTheDocument();

    await userEvent.type(await screen.findByLabelText("Name"), "Draft");
    await userEvent.type(screen.getByLabelText("API key"), "sk-test");
    await userEvent.click(screen.getByRole("button", { name: "Create provider" }));

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input, init]) => {
          if (!String(input).includes("/api/model-providers") || init?.method !== "POST") return false;
          const body = JSON.parse(String(init.body));
          return body.name === "Draft" && body.base_url === "" && Array.isArray(body.endpoints) && body.endpoints.length === 0;
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

  it("filters the provider library by search", async () => {
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
                  {
                    id: "openai",
                    name: "OpenAI",
                    base_url: "https://api.openai.com/v1",
                    protocols: ["openai_chat_completions"],
                    api_key_env: "OPENAI_API_KEY",
                    catalog: { manual: ["gpt-4o"], default_model: "gpt-4o" },
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

    expect(await screen.findByRole("button", { name: /MiMo/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /OpenAI/i })).toBeInTheDocument();

    // Use host/key needles that do not collide with MiMo's openai_responses protocol label.
    await userEvent.type(screen.getByLabelText("Search model providers"), "api.openai.com");
    expect(screen.queryByRole("button", { name: /MiMo/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /OpenAI/i })).toBeInTheDocument();

    await userEvent.clear(screen.getByLabelText("Search model providers"));
    await userEvent.type(screen.getByLabelText("Search model providers"), "mimo_api");
    expect(screen.getByRole("button", { name: /MiMo/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /OpenAI/i })).not.toBeInTheDocument();

    await userEvent.clear(screen.getByLabelText("Search model providers"));
    await userEvent.type(screen.getByLabelText("Search model providers"), "no-such-provider");
    expect(screen.getByText("No matching providers")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /clear search/i }));
    expect(screen.getByRole("button", { name: /MiMo/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /OpenAI/i })).toBeInTheDocument();
  });
});
