import { render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
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
