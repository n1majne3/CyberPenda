import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage } from "./RuntimeProfilesPage.testSupport";

describe("RuntimeProfilesPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });


  it("hides legacy model fields when a model provider is selected", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "codex",
                    provider: "codex",
                    fields: {
                      model_provider_id: "mimo",
                      model: "gpt-5",
                      endpoint: "https://legacy.example.test/v1",
                    },
                    created_at: "",
                    updated_at: "2026-06-25T00:00:00Z",
                  },
                ],
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
                    id: "mimo",
                    name: "Mimo",
                    base_url: "https://token-plan-cn.xiaomimimo.com/v1",
                    protocols: ["openai_chat_completions"],
                    api_key_env: "MIMO_API_KEY",
                    catalog: { manual: ["mimo-v2-flash"], default_model: "mimo-v2-flash" },
                    created_at: "",
                    updated_at: "",
                  },
                  {
                    id: "openai-proxy",
                    name: "OpenAI Proxy",
                    base_url: "https://api.example.test/v1",
                    protocols: ["openai_responses"],
                    api_key_env: "OPENAI_PROXY_API_KEY",
                    catalog: { manual: ["gpt-5"], default_model: "gpt-5" },
                    created_at: "",
                    updated_at: "",
                  },
                  {
                    id: "anthropic",
                    name: "Anthropic",
                    base_url: "https://api.anthropic.com",
                    protocols: ["anthropic_messages"],
                    api_key_env: "ANTHROPIC_API_KEY",
                    catalog: { manual: ["claude-sonnet"], default_model: "claude-sonnet" },
                    created_at: "",
                    updated_at: "",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(
            new Response(JSON.stringify({ plugins: [] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-extensions")) {
          return Promise.resolve(
            new Response(JSON.stringify({ extensions: [] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-extension-catalog")) {
          return Promise.resolve(
            new Response(JSON.stringify({ items: [] }), {
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

    await userEvent.click(await screen.findByRole("button", { name: /codex/i }));
    expect(screen.queryByPlaceholderText("gpt-5")).not.toBeInTheDocument();
    expect(screen.queryByPlaceholderText("https://api.example.test/v1")).not.toBeInTheDocument();
    expect(screen.getByText("Model override")).toBeInTheDocument();

    const providerSelect = await screen.findByDisplayValue("Mimo (MIMO_API_KEY) (incompatible)");
    expect(providerSelect).toBeInTheDocument();
    for (const option of Array.from(providerSelect.querySelectorAll("option")).map((node) => node.textContent)) {
      expect(option).not.toMatch(/Anthropic \(ANTHROPIC_API_KEY\)$/);
      if (option?.includes("Mimo")) expect(option).toContain("(incompatible)");
      if (option?.includes("OpenAI Proxy")) expect(option).not.toContain("(incompatible)");
    }
  });


  it("shows saving and saved feedback when profile is saved", async () => {
    let patchStarted = false;
    let releaseSave!: () => void;
    const saveReleased = new Promise<void>((resolve) => { releaseSave = resolve; });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        const method = init?.method ?? "GET";
        if (url.includes("/api/runtime-profiles/profile-1") && method === "PATCH") {
          patchStarted = true;
          return saveReleased.then(() => new Promise<Response>((resolve) => {
            
            
            
              resolve(
                new Response(
                  JSON.stringify({
                    id: "profile-1",
                    name: "codex",
                    provider: "codex",
                    fields: { model_provider_id: "openai-proxy" },
                    created_at: "",
                    updated_at: "2026-06-25T00:00:01Z",
                  }),
                  { status: 200, headers: { "Content-Type": "application/json" } },
                ),
              );
          }));
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "codex",
                    provider: "codex",
                    fields: { model_provider_id: "openai-proxy" },
                    created_at: "",
                    updated_at: "2026-06-25T00:00:00Z",
                  },
                ],
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
                    id: "openai-proxy",
                    name: "OpenAI Proxy",
                    base_url: "https://api.example.test/v1",
                    protocols: ["openai_responses"],
                    api_key_env: "OPENAI_PROXY_API_KEY",
                    catalog: { manual: ["gpt-5"], default_model: "gpt-5" },
                    created_at: "",
                    updated_at: "",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-plugins")) {
          return Promise.resolve(
            new Response(JSON.stringify({ plugins: [] }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        if (url.includes("/api/runtime-extensions") || url.includes("/api/runtime-extension-catalog")) {
          return Promise.resolve(
            new Response(JSON.stringify(url.includes("catalog") ? { items: [] } : { extensions: [] }), {
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
    await userEvent.click(await screen.findByRole("button", { name: /codex/i }));
    await userEvent.click(await screen.findByRole("button", { name: "Save" }));

    await waitFor(() => expect(patchStarted).toBe(true));
    expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();
    releaseSave();
    expect(await screen.findByRole("button", { name: /Saved/ })).toBeInTheDocument();
    expect(document.querySelector(".save-check-pop")).not.toBeNull();
  });


  it("edits and saves the codex multi-agent tools control", async () => {
    const patchBodies: unknown[] = [];
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-extension-catalog")) {
        return new Promise<Response>(() => {});
      }
      if (url.includes("/api/runtime-profiles/profile-1") && method === "PATCH") {
        patchBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return Promise.resolve(
          new Response(JSON.stringify({ id: "profile-1", name: "Fast Codex", provider: "codex", fields: {}, created_at: "", updated_at: "" }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              profiles: [
                {
                  id: "profile-1",
                  name: "Fast Codex",
                  provider: "codex",
                  fields: { model: "gpt-5" },
                  created_at: "",
                  updated_at: "2026-06-19T00:00:00Z",
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(new Response(JSON.stringify({ plugins: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/runtime-extensions")) {
        return Promise.resolve(new Response(JSON.stringify({ extensions: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
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
    await userEvent.click(await screen.findByRole("button", { name: /^Fast Codex/i }));

    const stateSelect = screen.getByLabelText("In-turn multi-agent tools");
    expect(stateSelect).toHaveValue("inherit");
    expect(screen.queryByLabelText("Max concurrent agent threads")).not.toBeInTheDocument();

    await userEvent.selectOptions(stateSelect, "on");
    expect(screen.getByLabelText("Max concurrent agent threads")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Max concurrent agent threads"), "4");
    await userEvent.type(screen.getByLabelText("Max agent depth"), "2");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(patchBodies).toHaveLength(1));
    expect(patchBodies[0]).toMatchObject({
      fields: {
        codex_multi_agent: { enabled: true, max_concurrent_threads_per_session: 4, max_depth: 2 },
      },
    });
  });


  it("requires confirmation before deleting a runtime profile", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              profiles: [
                {
                  id: "profile-1",
                  name: "Codex Delete",
                  provider: "codex",
                  fields: {},
                  created_at: "",
                  updated_at: "2026-06-25T00:00:00Z",
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(new Response(JSON.stringify({ plugins: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/runtime-extensions") || url.includes("/api/runtime-extension-catalog")) {
        return Promise.resolve(new Response(JSON.stringify(url.includes("catalog") ? { items: [] } : { extensions: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Delete Codex Delete runtime profile/i }));

    expect(confirm).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog", { name: /Delete runtime profile Codex Delete/ })).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/runtime-profiles/profile-1") && init?.method === "DELETE",
      ),
    ).toBe(false);
  });


  it("surfaces daemon custom args conflict 400 without cleaning draft values", async () => {
    const serverConflict =
      'custom argument "--model other" redefines structured field model (model); use the Runtime Profile model instead';
    const profilePayload = {
      id: "profile-1",
      name: "Codex Clean",
      provider: "codex",
      fields: { model: "gpt-5", custom_args: ["--strict"] },
      created_at: "",
      updated_at: "2026-07-21T00:00:00Z",
    };
    const patchBodies: unknown[] = [];
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/api/runtime-profiles") && method === "GET") {
        return Promise.resolve(
          new Response(JSON.stringify({ profiles: [profilePayload] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-profiles/profile-1") && method === "PATCH") {
        if (init?.body) {
          patchBodies.push(JSON.parse(String(init.body)));
        }
        return Promise.resolve(
          new Response(JSON.stringify({ error: serverConflict }), {
            status: 400,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.includes("/api/runtime-plugins")) {
        return Promise.resolve(new Response(JSON.stringify({ plugins: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (url.includes("/api/runtime-extensions") || url.includes("/api/runtime-extension-catalog") || url.includes("/api/model-providers")) {
        return Promise.resolve(
          new Response(
            JSON.stringify(url.includes("catalog") ? { items: [] } : url.includes("model-providers") ? { providers: [] } : { extensions: [] }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Codex Clean/i }));
    const customArgs = await screen.findByLabelText(/Custom args/i);
    expect(customArgs).toHaveValue("--strict");
    await userEvent.clear(customArgs);
    await userEvent.type(customArgs, "--model other");
    await userEvent.click(await screen.findByRole("button", { name: "Save" }));

    
    expect(await screen.findByRole("alert")).toHaveTextContent(serverConflict);
    
    expect(screen.getByLabelText(/Custom args/i)).toHaveValue("--model other");
    
    expect(patchBodies).toHaveLength(1);
    expect(patchBodies[0]).toMatchObject({
      fields: { custom_args: ["--model other"] },
    });
    
    expect(screen.getByDisplayValue("--model other")).toBeInTheDocument();
  });
});
