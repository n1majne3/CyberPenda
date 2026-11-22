import { LocationProbe } from "@/test/RouteControls";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RuntimeProfilesPage } from "./RuntimeProfilesPage";
import { renderPage } from "./RuntimeProfilesPage.testSupport";

describe("RuntimeProfilesPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });


  it("shows runtime profiles without fetching a remote extension catalog", async () => {
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
                    fields: { model: "gpt-5", runtime_extensions: [{ id: "npm:pi-tools", enabled: true, config: { install_ref: "npm:pi-tools", registry: "pi.dev/packages" } }] },
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
    expect(screen.queryByRole("button", { name: "Add package" })).not.toBeInTheDocument();
    expect(
      vi.mocked(fetch).mock.calls.filter(([input]) => {
        const url = typeof input === "string" ? input : input.toString();
        return url.includes("/api/runtime-extension-catalog");
      }),
    ).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: /^Fast Codex/ }));
    await userEvent.click(await screen.findByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      const call = vi.mocked(fetch).mock.calls.find(([, init]) => init?.method === "PATCH");
      expect(call).toBeDefined();
      expect(JSON.parse(String(call?.[1]?.body)).fields.runtime_extensions).toEqual([
        { id: "npm:pi-tools", enabled: true, config: { install_ref: "npm:pi-tools", registry: "pi.dev/packages" } },
      ]);
    });
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
    expect(screen.getByTestId("runtime-profiles-page")).toHaveClass("mx-auto", "max-w-[1280px]");
    expect(layout).toHaveClass(
      "grid",
      "min-w-0",
      "gap-8",
      "lg:grid-cols-[240px_minmax(0,1fr)]",
      "lg:min-h-0",
      "lg:flex-1",
    );
    expect(screen.getByTestId("runtime-profiles-settings-list")).toHaveClass(
      "min-w-0",
      "flex-col",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );
    expect(screen.getByTestId("runtime-profiles-settings-list")).toHaveClass("gap-4");
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
    expect(screen.getByLabelText("Runtime")).toHaveAttribute("name", "provider");
    expect(screen.getByLabelText("Default runner")).toHaveAttribute("name", "default_runner");
  });
});
