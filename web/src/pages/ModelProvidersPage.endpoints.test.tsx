import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage } from "./ModelProvidersPage.testSupport";

describe("ModelProvidersPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
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


  it("keeps an edited shared base URL after save reloads the provider", async () => {
    let provider = {
      id: "mimo",
      name: "MiMo",
      base_url: "https://old.example.test/v1",
      endpoints: [
        { protocol: "openai_responses", base_url: "https://old.example.test/v1" },
      ],
      api_key_env: "MIMO_API_KEY",
      catalog: { manual: ["mimo-v2"], default_model: "mimo-v2" },
      created_at: "2026-06-25T00:00:00Z",
      updated_at: "2026-06-25T00:00:00Z",
    };
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/api/model-providers/mimo") && init?.method === "PATCH") {
        const body = JSON.parse(String(init.body));
        provider = {
          ...provider,
          ...body,
          
          base_url: body.endpoints[0]?.base_url ?? "",
          updated_at: "2026-06-25T00:00:01Z",
        };
        return Promise.resolve(
          new Response(JSON.stringify(provider), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/model-providers")) {
        return Promise.resolve(
          new Response(JSON.stringify({ providers: [provider] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
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
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    const baseURL = await screen.findByLabelText("Base URL");
    expect(baseURL).toHaveValue("https://old.example.test/v1");

    await userEvent.clear(baseURL);
    await userEvent.type(baseURL, "https://new.example.test/v2");
    await userEvent.click(screen.getByRole("button", { name: "Save provider" }));

    await waitFor(() => {
      expect(baseURL).toHaveValue("https://new.example.test/v2");
    });
  });


  it("derives composed endpoints from a shared base URL in the quick setup flow", async () => {
    
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

    await screen.findByText("No model providers yet.");
    expect(screen.getByRole("button", { name: "Create provider" })).toBeDisabled();

    await userEvent.type(await screen.findByLabelText("Name"), "Hub");
    await userEvent.type(screen.getByLabelText("Base URL"), "https://hub.example.test/v1");
    await userEvent.type(screen.getByLabelText("API key"), "sk-test");

    await userEvent.click(screen.getByRole("checkbox", { name: "openai_responses" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "openai_chat_completions" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "anthropic_messages" }));

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

    
    
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input, init]) => {
          if (!String(input).includes("/api/credential-bindings") || init?.method !== "PUT") return false;
          const body = JSON.parse(String(init.body));
          return (
            body.credential_ref === "HUB_API_KEY" &&
            body.source.kind === "literal" &&
            body.source.destination_env === "HUB_API_KEY"
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
    
    
    const draftEndpoint = screen.getByLabelText("openai_responses endpoint base URL");
    expect(draftEndpoint).toBeDisabled();
    expect(draftEndpoint).toHaveValue("");

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
});
