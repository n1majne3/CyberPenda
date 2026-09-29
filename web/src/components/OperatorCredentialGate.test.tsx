import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiGet, resetOperatorCredentialStateForTests } from "@/lib/api";
import { OperatorCredentialGate } from "./OperatorCredentialGate";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
  resetOperatorCredentialStateForTests();
});

async function driveIntoCredentialPrompt() {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ error: "denied" }), { status: 401 }))
    .mockResolvedValueOnce(new Response(null, { status: 401 }));
  vi.stubGlobal("fetch", fetchMock);
  await act(async () => {
    await expect(apiGet("/api/projects")).rejects.toThrow();
  });
}

describe("OperatorCredentialGate", () => {
  it("renders the workspace while the daemon authorizes the browser", () => {
    render(
      <OperatorCredentialGate>
        <div>workspace</div>
      </OperatorCredentialGate>,
    );
    expect(screen.getByText("workspace")).toBeInTheDocument();
    expect(screen.queryByText("Operator sign-in required")).not.toBeInTheDocument();
  });

  it("replaces the workspace with the sign-in form when a denial cannot be repaired", async () => {
    render(
      <OperatorCredentialGate>
        <div>workspace</div>
      </OperatorCredentialGate>,
    );
    await driveIntoCredentialPrompt();
    expect(await screen.findByText("Operator sign-in required")).toBeInTheDocument();
    expect(screen.queryByText("workspace")).not.toBeInTheDocument();
  });

  it("returns to the workspace after the daemon accepts the submitted token", async () => {
    const user = userEvent.setup();
    render(
      <OperatorCredentialGate>
        <div>workspace</div>
      </OperatorCredentialGate>,
    );
    await driveIntoCredentialPrompt();
    await screen.findByText("Operator sign-in required");

    vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 204 })));
    await user.type(screen.getByLabelText("Operator token"), "secret-token");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("workspace")).toBeInTheDocument();
    expect(window.sessionStorage.getItem("pentest.authToken")).toBe("secret-token");
  });

  it("retries the denied GET once after the daemon accepts the submitted token", async () => {
    const user = userEvent.setup();
    render(
      <OperatorCredentialGate>
        <div>workspace</div>
      </OperatorCredentialGate>,
    );
    await driveIntoCredentialPrompt();
    await screen.findByText("Operator sign-in required");

    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ projects: [] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await user.type(screen.getByLabelText("Operator token"), "secret-token");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("workspace")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][0]).toBe("/api/projects");
  });

  it("keeps the form and shows the daemon's error when the token is rejected", async () => {
    const user = userEvent.setup();
    render(
      <OperatorCredentialGate>
        <div>workspace</div>
      </OperatorCredentialGate>,
    );
    await driveIntoCredentialPrompt();
    await screen.findByText("Operator sign-in required");

    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ error: "operator sign-in is required" }), { status: 401 }),
      ),
    );
    await user.type(screen.getByLabelText("Operator token"), "wrong-token");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("operator sign-in is required");
    expect(screen.getByText("Operator sign-in required")).toBeInTheDocument();
    expect(screen.queryByText("workspace")).not.toBeInTheDocument();
    expect(window.sessionStorage.getItem("pentest.authToken")).toBeNull();
  });
});
