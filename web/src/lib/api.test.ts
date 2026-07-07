import { afterEach, describe, expect, it, vi } from "vitest";
import { apiGet } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
});

describe("api client auth", () => {
  it("sends the dashboard URL token as a bearer token", async () => {
    window.history.replaceState(null, "", "/?token=secret");
    const fetchMock = vi.fn(async () => {
      return new Response(JSON.stringify({ projects: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    await apiGet("/api/projects");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/projects",
      expect.objectContaining({
        headers: expect.objectContaining({
          Authorization: "Bearer secret",
          "Content-Type": "application/json",
        }),
      }),
    );
  });
});
