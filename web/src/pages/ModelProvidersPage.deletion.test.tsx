import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage } from "./ModelProvidersPage.testSupport";

describe("ModelProvidersPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
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

    expect(confirm).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog", { name: /Delete model provider MiMo/ })).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/model-providers/mimo") && init?.method === "DELETE",
      ),
    ).toBe(false);
  });


  it("offers an explicit detach delete when the provider is referenced by runtime profiles", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/api/model-providers/mimo") && init?.method === "DELETE" && url.includes("detach=true")) {
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      if (url.includes("/api/model-providers/mimo") && init?.method === "DELETE") {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              error: "model provider is referenced by a runtime profile",
              profiles: ["Pi Preset"],
            }),
            { status: 409, headers: { "Content-Type": "application/json" } },
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
    
    const firstDialog = await screen.findByRole("alertdialog", { name: /Delete model provider MiMo/ });
    await userEvent.click(within(firstDialog).getByRole("button", { name: "Delete" }));

    const dialog = await screen.findByRole("alertdialog", { name: /Delete model provider MiMo/ });
    expect(dialog).toHaveTextContent("Pi Preset");
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/model-providers/mimo?detach=true") && init?.method === "DELETE",
      ),
    ).toBe(false);

    await userEvent.click(screen.getByRole("button", { name: "Clear references and delete" }));

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input, init]) =>
          String(input).includes("/api/model-providers/mimo?detach=true") && init?.method === "DELETE",
        ),
      ).toBe(true);
    });
  });
});
