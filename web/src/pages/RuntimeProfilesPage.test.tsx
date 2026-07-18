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
                    kind: "manual",
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
    expect(layout).toHaveClass(
      "grid",
      "min-w-0",
      "lg:grid-cols-[minmax(220px,280px)_minmax(0,1fr)]",
      "lg:min-h-0",
      "lg:flex-1",
    );
    expect(screen.getByTestId("runtime-profiles-settings-list")).toHaveClass(
      "min-w-0",
      "flex-col",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );
    expect(screen.getByTestId("runtime-profiles-settings-detail")).toHaveClass(
      "min-w-0",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );

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

    const providerSelect = screen.getByDisplayValue("Mimo (MIMO_API_KEY) (incompatible)");
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
            }, 40);
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

  it("groups launch-resolved profiles separately from presets", async () => {
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
                    kind: "manual",
                    fields: { model_provider_id: "mimo" },
                    created_at: "",
                    updated_at: "2026-06-25T00:00:00Z",
                  },
                  {
                    id: "auto-1",
                    name: "Codex · MiMo",
                    provider: "codex",
                    kind: "launch_resolve",
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
    expect(screen.getByText("Presets")).toBeInTheDocument();
    expect(screen.getByText(/Launch-resolved \(1\)/)).toBeInTheDocument();
    expect(screen.queryByText("Codex · MiMo")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Launch-resolved \(1\)/ }));
    expect(await screen.findByText("Codex · MiMo")).toBeInTheDocument();
  });

  it("promotes a launch-resolved profile to a preset", async () => {
    let promoted = false;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        const method = init?.method ?? "GET";
        if (url.includes("/api/runtime-profiles/auto-1/promote") && method === "POST") {
          promoted = true;
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: "auto-1",
                name: "Codex · MiMo",
                provider: "codex",
                kind: "manual",
                fields: { model_provider_id: "mimo" },
                created_at: "",
                updated_at: "2026-06-25T00:00:01Z",
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
                    id: "auto-1",
                    name: "Codex · MiMo",
                    provider: "codex",
                    kind: promoted ? "manual" : "launch_resolve",
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
    await userEvent.click(await screen.findByRole("button", { name: /Launch-resolved \(1\)/ }));
    await userEvent.click((await screen.findAllByRole("button", { name: /Codex · MiMo/i }))[0]);
    await userEvent.click(await screen.findByRole("button", { name: "Promote to preset" }));

    await waitFor(() => expect(promoted).toBe(true));
    expect(screen.queryByRole("button", { name: "Promote to preset" })).not.toBeInTheDocument();
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
    const previewSection = label.closest("div");
    const preview = previewSection?.querySelector("pre");

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
    await userEvent.click(await screen.findByRole("button", { name: /Codex Delete/i }));
    await userEvent.click(await screen.findByRole("button", { name: /Delete Codex Delete runtime profile/i }));

    expect(confirm).toHaveBeenCalledWith("Delete runtime profile Codex Delete?");
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/runtime-profiles/profile-1") && init?.method === "DELETE",
      ),
    ).toBe(false);
  });
});
