import { afterEach, describe, expect, it, vi } from "vitest";
import { apiGet, apiPost } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
});

describe("demo API", () => {
  it("loads the sample Project through the normal API client without a daemon", async () => {
    vi.stubEnv("VITE_DEMO_MODE", "true");
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const result = await apiGet<{ projects: Array<{ id: string; name: string }> }>("/api/projects");

    expect(result.projects).toEqual([
      expect.objectContaining({ id: "demo-project", name: "Acme External" }),
    ]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("serves the sample Skill library and Runtime Profile for the updated Skills UI", async () => {
    vi.stubEnv("VITE_DEMO_MODE", "true");
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const skills = await apiGet<{ skills: Array<{
      id: string;
      enabled: boolean;
      globally_opted_out?: boolean;
      profile_opted_out?: boolean;
    }> }>("/api/skills?runtime_profile_id=demo-profile");

    expect(skills.skills).toContainEqual(expect.objectContaining({ id: "tooling-nmap", enabled: true }));
    expect(skills.skills).toContainEqual(
      expect.objectContaining({ id: "tooling-nuclei", enabled: false, globally_opted_out: true }),
    );
    expect(skills.skills).toContainEqual(
      expect.objectContaining({ id: "tooling-sqlmap", enabled: false, profile_opted_out: true }),
    );

    const profiles = await apiGet<{ profiles: Array<{ id: string; provider: string }> }>("/api/runtime-profiles");
    expect(profiles.profiles).toEqual([expect.objectContaining({ id: "demo-profile", provider: "codex" })]);

    const single = await apiGet<{ id: string; files?: Record<string, string> }>("/api/skills/tooling-nmap");
    expect(single).toEqual(expect.objectContaining({ id: "tooling-nmap" }));
    expect(single.files?.["SKILL.md"]).toContain("# nmap");

    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("api client auth", () => {
  it("does not replay mutations or replace explicit credentials", async () => {
    const fetchMock = vi.fn(async () => new Response("{}", { status: 403 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(apiPost("/api/projects", {name:"test"})).rejects.toThrow();
    await expect(apiGet("/api/v2/projects/p/fgs", {headers:{Authorization:"Bearer explicit"}})).rejects.toThrow();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
  it("keeps the original error if browser session setup is denied", async () => {
    window.sessionStorage.setItem("pentest.authToken", "configured-token");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error:"original denial"}), {status:403}))
      .mockResolvedValueOnce(new Response(null, {status:401}));
    vi.stubGlobal("fetch",fetchMock);
    await expect(apiGet("/api/v2/projects/p/fgs")).rejects.toThrow("original denial");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(window.sessionStorage.getItem("pentest.authToken")).toBe("configured-token");
  });
  it("opens a direct Blackboard link and replaces a stale tab token with the browser session", async () => {
    window.sessionStorage.setItem("pentest.authToken", "old-daemon-token");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error:{code:"authority_denied",message:"Continuation Interface capability is invalid"}}), {status:403}))
      .mockResolvedValueOnce(new Response(null, {status:204}))
      .mockResolvedValueOnce(new Response(JSON.stringify({nodes:[]}), {status:200}));
    vi.stubGlobal("fetch", fetchMock);
    expect(await apiGet("/api/v2/projects/project-1/fgs")).toEqual({nodes:[]});
    expect(fetchMock.mock.calls[1][0]).toBe("/api/operator-session");
    expect(fetchMock.mock.calls[2][1].headers.Authorization).toBeUndefined();
    expect(window.sessionStorage.getItem("pentest.authToken")).toBeNull();
  });
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
