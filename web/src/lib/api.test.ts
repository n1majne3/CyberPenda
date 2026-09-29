import { afterEach, describe, expect, it, vi } from "vitest";
import {
  apiGet,
  apiPost,
  clearOperatorCredential,
  operatorCredentialIsNeeded,
  resetOperatorCredentialStateForTests,
  submitOperatorToken,
  subscribeOperatorCredentialState,
  takeDeniedRequestRetry,
} from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
  resetOperatorCredentialStateForTests();
});

describe("demo API", () => {
  it("loads the sample Project through the normal API client without a daemon", async () => {
    vi.stubEnv("VITE_DEMO_MODE", "true");
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const result = await apiGet<{ projects: Array<{ id: string; name: string }> }>("/api/projects");

    expect(result.projects).toEqual([
      expect.objectContaining({ id: "demo-project", name: "VulnCastle" }),
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
    // The denied mutation triggers one session-repair attempt, never a replay;
    // the explicit-credential request is left untouched.
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/projects");
    expect(fetchMock.mock.calls[1][0]).toBe("/api/operator-session");
    expect(fetchMock.mock.calls[2][0]).toBe("/api/v2/projects/p/fgs");
  });
  it("keeps the original error if browser session setup is denied", async () => {
    window.sessionStorage.setItem("pentest.authToken", "configured-token");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error:"original denial"}), {status:403}))
      .mockResolvedValueOnce(new Response(null, {status:401}));
    vi.stubGlobal("fetch",fetchMock);
    await expect(apiGet("/api/v2/projects/p/fgs")).rejects.toThrow("original denial");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    // The daemon rejected the stored token, so the sign-in form starts empty.
    expect(window.sessionStorage.getItem("pentest.authToken")).toBeNull();
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

describe("operator credential state", () => {
  it("drops a stale stored token and remembers the denied GET for one retry", async () => {
    window.sessionStorage.setItem("pentest.authToken", "stale-token");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error:"denied"}), {status:401}))
      .mockResolvedValueOnce(new Response(null, {status:401}));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiGet("/api/projects?view=tasks")).rejects.toThrow();

    expect(window.sessionStorage.getItem("pentest.authToken")).toBeNull();
    expect(operatorCredentialIsNeeded()).toBe(true);
    expect(takeDeniedRequestRetry()).toEqual({ path: "/api/projects?view=tasks" });
    expect(takeDeniedRequestRetry()).toBeNull();
  });

  it("retries the denied GET once after the daemon accepts the submitted token", async () => {
    const denied = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error:"denied"}), {status:401}))
      .mockResolvedValueOnce(new Response(null, {status:401}));
    vi.stubGlobal("fetch", denied);
    await expect(apiGet("/api/projects")).rejects.toThrow();

    const accepted = vi.fn()
      .mockResolvedValueOnce(new Response(null, {status:204}))
      .mockResolvedValueOnce(new Response(JSON.stringify({projects:[]}), {status:200}));
    vi.stubGlobal("fetch", accepted);

    await submitOperatorToken("fresh-token");
    const retry = takeDeniedRequestRetry();
    const retried = retry ? await apiGet(retry.path) : null;

    expect(retried).toEqual({projects:[]});
    expect(accepted).toHaveBeenCalledTimes(2);
    expect(accepted.mock.calls[1][0]).toBe("/api/projects");
    expect(takeDeniedRequestRetry()).toBeNull();
  });

  it("does not remember a denied mutation for an automatic retry", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error:"denied"}), {status:401}))
      .mockResolvedValueOnce(new Response(null, {status:401}));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiPost("/api/projects", {name:"test"})).rejects.toThrow();

    expect(operatorCredentialIsNeeded()).toBe(true);
    expect(takeDeniedRequestRetry()).toBeNull();
  });

  it("asks for an operator credential when a denial cannot be repaired", async () => {
    const listener = vi.fn();
    const unsubscribe = subscribeOperatorCredentialState(listener);
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error:"denied"}), {status:401}))
      .mockResolvedValueOnce(new Response(null, {status:401}));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiGet("/api/projects")).rejects.toThrow();

    expect(operatorCredentialIsNeeded()).toBe(true);
    expect(listener).toHaveBeenCalled();
    unsubscribe();
  });

  it("does not ask for a credential when the caller supplied an explicit one", async () => {
    const fetchMock = vi.fn(async () => new Response("{}", { status: 403 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiGet("/api/v2/projects/p/fgs", {headers:{Authorization:"Bearer explicit"}})).rejects.toThrow();

    expect(operatorCredentialIsNeeded()).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("repairs the session after a denied mutation without replaying it", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error:"denied"}), {status:403}))
      .mockResolvedValueOnce(new Response(null, {status:204}));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiPost("/api/projects", {name:"test"})).rejects.toThrow("denied");

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][0]).toBe("/api/operator-session");
    expect(operatorCredentialIsNeeded()).toBe(false);
  });

  it("stores a submitted operator token only after the daemon accepts it", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await submitOperatorToken(" fresh-token ");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/operator-session",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ Authorization: "Bearer fresh-token" }),
      }),
    );
    expect(window.sessionStorage.getItem("pentest.authToken")).toBe("fresh-token");
    expect(operatorCredentialIsNeeded()).toBe(false);
  });

  it("rejects a denied operator token without storing it", async () => {
    const fetchMock = vi.fn(async () =>
      new Response(JSON.stringify({error:"operator sign-in is required"}), {status:401}),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(submitOperatorToken("wrong-token")).rejects.toThrow();

    expect(window.sessionStorage.getItem("pentest.authToken")).toBeNull();
  });

  it("clears the credential and expires the daemon session on sign-out", async () => {
    window.sessionStorage.setItem("pentest.authToken", "old-token");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(null, {status:204}))
      .mockResolvedValueOnce(new Response(null, {status:401}));
    vi.stubGlobal("fetch", fetchMock);

    await clearOperatorCredential();

    expect(fetchMock.mock.calls[0][0]).toBe("/api/operator-session");
    expect(fetchMock.mock.calls[0][1].method).toBe("DELETE");
    expect(window.sessionStorage.getItem("pentest.authToken")).toBeNull();
    expect(operatorCredentialIsNeeded()).toBe(true);
  });

  it("stays signed in after sign-out when the local daemon re-establishes the session", async () => {
    window.sessionStorage.setItem("pentest.authToken", "old-token");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(null, {status:204}))
      .mockResolvedValueOnce(new Response(null, {status:204}));
    vi.stubGlobal("fetch", fetchMock);

    await clearOperatorCredential();

    expect(operatorCredentialIsNeeded()).toBe(false);
  });
});
