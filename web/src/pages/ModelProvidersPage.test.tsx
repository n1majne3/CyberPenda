import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ModelProvidersPage } from "./ModelProvidersPage";
import { renderPage } from "./ModelProvidersPage.testSupport";

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
      "lg:grid-cols-[240px_minmax(0,1fr)]",
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


  it("preselects the provider named by the selected search param", async () => {
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

    render(
      <StrictMode>
        <MemoryRouter initialEntries={["/model-providers?selected=openai"]}>
          <ModelProvidersPage />
        </MemoryRouter>
      </StrictMode>,
    );

    expect(await screen.findByRole("button", { name: /OpenAI/i })).toHaveAttribute("aria-current", "true");
    expect(screen.getByRole("button", { name: /MiMo/i })).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("heading", { name: "OpenAI" })).toBeInTheDocument();
  });
});
