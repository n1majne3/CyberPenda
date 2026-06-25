import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
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

describe("RuntimeProfilesPage", () => {
  afterEach(() => {
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
});
