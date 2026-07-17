import { vi } from "vitest";

/*
 * Test helper: install a fetch mock that maps URL substrings to canned JSON
 * responses, so page smoke tests can mount a page without a real daemon.
 *
 * Usage:
 *   mockApi({
 *     "/api/projects": { projects: [] },
 *     "/api/runtime-profiles": { profiles: [] },
 *   });
 *   render(<Page />);
 */
type Routes = Record<string, unknown>;

const defaultSemanticHealth = {
  schema: "blackboard-health/v2",
  revision: 0,
  status: "healthy",
  attention: {
    bytes: 128,
    estimated_tokens: 32,
    state: "within_target",
    complete: true,
    launchable: true,
    consolidation_offered: false,
    consolidation_required: false,
  },
  anomalies: [],
  proposals: [],
};

export function mockApi(routes: Routes) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input.toString();
    // First match wins; callers should order specific paths before prefixes.
    for (const [key, body] of Object.entries(routes)) {
      if (url.includes(key)) {
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
    }
    // Blackboard v2 health is required by ordinary Blackboard surfaces.
    if (url.includes("/api/v2/") && url.includes("/blackboard/health")) {
      return new Response(JSON.stringify(defaultSemanticHealth), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    // Default: empty object so unconfigured endpoints don't crash the page.
    return new Response(JSON.stringify({}), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}
