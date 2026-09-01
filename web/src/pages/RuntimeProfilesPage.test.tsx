import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useEffect } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RuntimeProfilesPage } from "./RuntimeProfilesPage";

function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter>
        <RuntimeProfilesPage />
      </MemoryRouter>
    </StrictMode>,
  );
}

function LocationProbe({ onChange }: { onChange: (search: string) => void }) {
  const location = useLocation();
  useEffect(() => {
    onChange(location.search);
  }, [location.search, onChange]);
  return null;
}

describe("RuntimeProfilesPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("shows runtime profiles without waiting for the remote extension catalog", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/runtime-extension-catalog")) {
          return new Promise<Response>(() => {});
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
        return Promise.resolve(
          new Response(JSON.stringify({}), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    const { findByText } = renderPage();

    expect(await findByText("Fast Codex")).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledWith("/api/runtime-extension-catalog", expect.anything());
    expect(
      vi.mocked(fetch).mock.calls.filter(([input]) => {
        const url = typeof input === "string" ? input : input.toString();
        return url.includes("/api/runtime-extension-catalog");
      }),
    ).toHaveLength(1);
  });

  it("shows the published sandbox image in the sandbox profile guidance", async () => {
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
                    name: "Pi Sandbox",
                    provider: "pi",
                    fields: { default_runner: "sandbox" },
                    created_at: "",
                    updated_at: "2026-07-18T00:00:00Z",
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
      }),
    );

    renderPage();

    await userEvent.click(await screen.findByRole("button", { name: /Pi Sandbox/i }));
    expect(screen.getByPlaceholderText("ghcr.io/n1majne3/cyberpenda-sandbox:latest...")).toBeInTheDocument();
    expect(screen.getByText("ghcr.io/n1majne3/cyberpenda-sandbox:latest")).toBeInTheDocument();
  });

  it("uses the shared Geist settings layout for profile selection and details", async () => {
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
                    name: "Codex Layout",
                    provider: "codex",
                    fields: { model: "gpt-5" },
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

    const layout = await screen.findByTestId("runtime-profiles-settings-layout");
    expect(screen.getByTestId("runtime-profiles-page")).toHaveClass("mx-auto", "max-w-6xl");
    expect(layout).toHaveClass(
      "grid",
      "min-w-0",
      "gap-0",
      "lg:grid-cols-[320px_minmax(0,1fr)]",
      "lg:min-h-0",
      "lg:flex-1",
    );
    expect(screen.getByTestId("runtime-profiles-settings-list")).toHaveClass(
      "min-w-0",
      "flex-col",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );
    expect(screen.getByTestId("runtime-profiles-settings-list")).toHaveClass("border-r", "bg-card");
    expect(screen.getByTestId("runtime-profiles-settings-detail")).toHaveClass(
      "min-w-0",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );
    expect(screen.getByTestId("runtime-profiles-settings-detail")).toHaveClass("rounded-none", "border-0", "shadow-none");

    const profileButton = await screen.findByRole("button", { name: /Codex Layout/i });
    expect(profileButton).toHaveAttribute("aria-current", "true");
    expect(profileButton).toHaveClass("rounded-md", "focus-visible:ring-2");
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
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        const method = init?.method ?? "GET";
        if (url.includes("/api/runtime-profiles/profile-1") && method === "PATCH") {
          patchStarted = true;
          return new Promise<Response>((resolve) => {
            // Keep the pending window long enough that the transient
            // "Saving…" state cannot vanish before the assertion under a
            // loaded test runner (was flaky at 40ms).
            setTimeout(() => {
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
            }, 300);
          });
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
    expect(await screen.findByRole("button", { name: /Saved/ })).toBeInTheDocument();
    expect(document.querySelector(".save-check-pop")).not.toBeNull();
  });

  it("shows every returned user-created Runtime Profile without kind grouping", async () => {
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
                    id: "preset-1",
                    name: "Codex MCP",
                    provider: "codex",
                    fields: { model_provider_id: "mimo" },
                    created_at: "",
                    updated_at: "2026-06-25T00:00:00Z",
                  },
                  {
                    id: "auto-1",
                    name: "Codex · MiMo",
                    provider: "codex",
                    fields: { model_provider_id: "mimo" },
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

    expect(await screen.findByText("Codex MCP")).toBeInTheDocument();
expect(screen.getByText("Codex · MiMo")).toBeInTheDocument();
    expect(screen.queryByText(/Launch-resolved/)).not.toBeInTheDocument();
    expect(screen.queryByText("Presets")).not.toBeInTheDocument();
  });

  it("does not expose the removed promote operation", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/api/runtime-profiles")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              profiles: [
                {
                  id: "auto-1",
                  name: "Codex · MiMo",
                  provider: "codex",
                  fields: { model_provider_id: "mimo" },
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
      });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await userEvent.click((await screen.findAllByRole("button", { name: /Codex · MiMo/i }))[0]);
    expect(screen.queryByRole("button", { name: "Promote to preset" })).not.toBeInTheDocument();
    // The removed promote endpoint must never be called.
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/promote") && (init?.method ?? "GET") === "POST",
      ),
    ).toBe(false);
  });

  it("keeps long Codex generated config preview from widening the page", async () => {
    const longEndpoint = `https://${"very-long-host-segment-".repeat(12)}example.test/v1`;
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
                    name: "Codex with long config",
                    provider: "codex",
                    fields: {
                      model: "gpt-5",
                      endpoint: longEndpoint,
                      mcp_servers: [
                        {
                          name: "external",
                          mode: "external",
                          url: `${longEndpoint}/mcp/${"deep-path-".repeat(12)}`,
                        },
                      ],
                    },
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

    expect(await screen.findByText("Codex with long config")).toBeInTheDocument();
    const label = await screen.findByText("Generated config preview");
    const previewSection = label.closest("div.min-w-0");
    const preview = previewSection?.querySelector("pre") ?? null;
    expect(preview).toHaveTextContent(longEndpoint);
    expect(previewSection).toHaveClass("min-w-0");
    expect(preview).toHaveClass("w-full", "max-w-full", "overflow-x-auto");
  });

  it("reflects the selected profile in the URL", async () => {
    const searches: string[] = [];
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
                    name: "Codex URL",
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
      }),
    );

    render(
      <StrictMode>
        <MemoryRouter>
          <LocationProbe onChange={(search) => searches.push(search)} />
          <RuntimeProfilesPage />
        </MemoryRouter>
      </StrictMode>,
    );

    await userEvent.click(await screen.findByRole("button", { name: /Codex URL/i }));
    await waitFor(() => expect(searches.at(-1)).toBe("?profile=profile-1"));
  });

  it("associates profile editor labels with named controls", async () => {
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
                    name: "Codex Labels",
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
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Codex Labels/i }));

    expect(screen.getByLabelText("Name")).toHaveAttribute("name", "profile_name");
    expect(screen.getByLabelText("Provider")).toHaveAttribute("name", "provider");
    expect(screen.getByLabelText("Default runner")).toHaveAttribute("name", "default_runner");
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
    await userEvent.click(await screen.findByRole("button", { name: /Fast Codex/i }));

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

    // Daemon is the authoritative seam: UI shows the 400 conflict message.
    expect(await screen.findByRole("alert")).toHaveTextContent(serverConflict);
    // Draft Custom Args are not stripped, reordered, or rewritten after failure.
    expect(screen.getByLabelText(/Custom args/i)).toHaveValue("--model other");
    // PATCH was attempted with the conflicting payload (no client-side rewrite).
    expect(patchBodies).toHaveLength(1);
    expect(patchBodies[0]).toMatchObject({
      fields: { custom_args: ["--model other"] },
    });
    // Failure does not clear the draft or rewrite Custom Args after the 400.
    expect(screen.getByDisplayValue("--model other")).toBeInTheDocument();
  });

  it("opens the config edit window on the projected config and imports the edit", async () => {
    const importCalls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/import-config")) {
          importCalls.push(String(init?.body ?? ""));
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profile: {
                  id: "profile-1",
                  name: "Claude Edit",
                  provider: "claude_code",
                  fields: {
                    env: { MY_TOOL_TAG: "abc" },
                    custom_config_file: '{\n  "enabledPlugins": {"warp@claude-code-warp": true}\n}',
                  },
                  created_at: "",
                  updated_at: "",
                },
                mapped_keys: ["env"],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          // After a successful import the refreshed list carries the imported
          // env plus the Custom Config File remainder.
          const imported = importCalls.length > 0;
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude Edit",
                    provider: "claude_code",
                    fields: imported
                      ? { env: { MY_TOOL_TAG: "abc" }, custom_config_file: '{\n  "enabledPlugins": {"warp@claude-code-warp": true}\n}' }
                      : {},
                    created_at: "",
                    updated_at: "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify(url.includes("model-providers") ? { providers: [] } : { plugins: [], extensions: [], items: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Claude Edit/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));

    const editor = await screen.findByLabelText(/config editor/i);
    await userEvent.type(editor, "MY_TOOL_TAG");
    await userEvent.click(await screen.findByRole("button", { name: /Import config/i }));

    expect(await screen.findByText(/warp@claude-code-warp/)).toBeInTheDocument();
    expect(importCalls).toHaveLength(1);
    expect(importCalls[0]).toContain("MY_TOOL_TAG");
  });

  it("renders per-key import errors in the config edit window", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/import-config")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                error: "profile config import refused: env.MY_API_TOKEN: key looks like a secret",
                keys: [{ key: "env.MY_API_TOKEN", message: "key looks like a secret; use the API keys structured field instead" }],
              }),
              { status: 400, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude Reject",
                    provider: "claude_code",
                    fields: {},
                    created_at: "",
                    updated_at: "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify(url.includes("model-providers") ? { providers: [] } : { plugins: [], extensions: [], items: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Claude Reject/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Import config/i }));

    expect(await screen.findAllByText(/env\.MY_API_TOKEN/)).not.toHaveLength(0);
    expect(screen.getAllByText(/looks like a secret/i).length).toBeGreaterThan(0);
  });

  it("confirms before a provider switch clears the Custom Config File", async () => {
    const patchCalls: Record<string, unknown>[] = [];
    let first = true;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/api/runtime-profiles/") && init?.method === "PATCH") {
          patchCalls.push(JSON.parse(String(init.body)));
          if (first) {
            first = false;
            return Promise.resolve(
              new Response(
                JSON.stringify({
                  error: "switching provider from claude_code to codex requires clearing the custom config file; confirm to discard it",
                  code: "provider_switch_needs_overlay_clear",
                }),
                { status: 409, headers: { "Content-Type": "application/json" } },
              ),
            );
          }
          return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude With Overlay",
                    provider: "claude_code",
                    fields: { custom_config_file: '{\n  "enabledPlugins": {"warp@claude-code-warp": true}\n}' },
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
          return Promise.resolve(new Response(JSON.stringify({ plugins: [] }), { status: 200 }));
        }
        if (url.includes("/api/runtime-extensions")) {
          return Promise.resolve(new Response(JSON.stringify({ extensions: [] }), { status: 200 }));
        }
        if (url.includes("/api/runtime-extension-catalog")) {
          return Promise.resolve(new Response(JSON.stringify({ items: [] }), { status: 200 }));
        }
        return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Claude With Overlay/i }));

    // Switch provider and save; the daemon refuses with 409.
    const providerSelect = await screen.findByLabelText("Provider", { selector: "#profile-provider" });
    await userEvent.selectOptions(providerSelect, "codex");
    await userEvent.click(await screen.findByRole("button", { name: /Save/i }));

    // The confirmation dialog opens instead of an error banner.
    expect(await screen.findByText(/Switch runtime provider\?/i)).toBeInTheDocument();
    expect(patchCalls).toHaveLength(1);
    expect(patchCalls[0]).not.toHaveProperty("confirm_provider_switch_clears_overlay");

    // Confirm: the retry carries the confirmation flag.
    await userEvent.click(await screen.findByRole("button", { name: /Switch and clear/i }));
    await waitFor(() => expect(patchCalls).toHaveLength(2));
    expect(patchCalls[1]).toMatchObject({ confirm_provider_switch_clears_overlay: true });
    expect(patchCalls[1].fields.custom_config_file ?? "").toBe("");
  });
});

  it("shows the final merged config after import", async () => {
    let imported = false;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/import-config")) {
          imported = true;
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profile: {
                  id: "profile-1",
                  name: "Claude Merge",
                  provider: "claude_code",
                  fields: { custom_config_file: '{"enabledPlugins":{"warp@claude-code-warp":true}}' },
                  created_at: "",
                  updated_at: "",
                },
                mapped_keys: [],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/merged-config-preview")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                provider: "claude_code",
                merged: {
                  provider: "claude_code",
                  enabledPlugins: { "warp@claude-code-warp": true },
                },
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/api/runtime-profiles")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                profiles: [
                  {
                    id: "profile-1",
                    name: "Claude Merge",
                    provider: "claude_code",
                    fields: imported
                      ? { custom_config_file: '{"enabledPlugins":{"warp@claude-code-warp":true}}' }
                      : {},
                    created_at: "",
                    updated_at: imported ? "2026-06-20T00:00:00Z" : "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify(url.includes("model-providers") ? { providers: [] } : { plugins: [], extensions: [], items: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Claude Merge/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Import config/i }));

    const merged = await screen.findByTestId("merged-config-preview");
    expect(merged).toHaveTextContent("warp@claude-code-warp");
  });

  it("seeds the config editor with the provider-native projected config", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/projected-config")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                provider: "codex",
                format: "toml",
                text: 'approval_policy = "never"\nsandbox_mode = "danger-full-access"\n',
                custom_config_file: "",
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/merged-config-preview")) {
          return Promise.resolve(
            new Response(JSON.stringify({ provider: "codex", merged: {} }), {
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
                    name: "Codex Seed",
                    provider: "codex",
                    fields: { model: "gpt-test" },
                    created_at: "",
                    updated_at: "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify(url.includes("model-providers") ? { providers: [] } : { plugins: [], extensions: [], items: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Codex Seed/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));

    const editor = await screen.findByLabelText(/config editor/i);
    await waitFor(() => {
      const value = (editor as HTMLTextAreaElement).value;
      expect(value).toContain("approval_policy");
      // The seed is provider-native TOML, not the JSON preview envelope.
      expect(value).not.toContain('"launch_preview"');
    });
  });

  it("reopens the config editor on the merged projected file including the Custom Config File", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input.toString();
        if (url.includes("/projected-config")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                provider: "claude_code",
                format: "json",
                text: JSON.stringify(
                  {
                    env: { ANTHROPIC_MODEL: "claude-opus-4-6" },
                    enabledPlugins: { "warp@claude-code-warp": true },
                  },
                  null,
                  2,
                ),
                custom_config_file: '{\n  "enabledPlugins": {\n    "warp@claude-code-warp": true\n  }\n}',
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        if (url.includes("/merged-config-preview")) {
          return Promise.resolve(
            new Response(JSON.stringify({ provider: "claude_code", merged: { enabledPlugins: { "warp@claude-code-warp": true } } }), {
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
                    name: "Claude Warp",
                    provider: "claude_code",
                    fields: { custom_config_file: '{\n  "enabledPlugins": {\n    "warp@claude-code-warp": true\n  }\n}' },
                    created_at: "",
                    updated_at: "2026-06-19T00:00:00Z",
                  },
                ],
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify(url.includes("model-providers") ? { providers: [] } : { plugins: [], extensions: [], items: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Claude Warp/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Edit config/i }));

    const editor = await screen.findByLabelText(/config editor/i);
    await waitFor(() => {
      expect((editor as HTMLTextAreaElement).value).toContain("warp@claude-code-warp");
    });
  });
