import { afterEach, describe, expect, it, vi } from "vitest";
import { apiGet } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
});

describe("api client auth", () => {
  it("sends the dashboard URL token as a bearer token", async () => {
    window.history.replaceState(null, "", "/?view=tasks&token=secret#activity");
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
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(
      "/?view=tasks#activity",
    );
    expect(window.sessionStorage.getItem("pentest.authToken")).toBe("secret");
  });
});
