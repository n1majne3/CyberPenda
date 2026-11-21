import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { ModelProvidersPage } from "./ModelProvidersPage";

afterEach(() => { vi.unstubAllGlobals(); });

const json = (body: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

it("does not create a second provider after the API key save fails", async () => {
  const created = {
    id: "hub",
    name: "Hub",
    endpoints: [{ protocol: "openai_responses", base_url: "https://hub.example.test/v1" }],
    api_key_env: "HUB_API_KEY",
    catalog: {},
    created_at: "2026-07-08T00:00:00Z",
    updated_at: "2026-07-08T00:00:00Z",
  };
  let providers: unknown[] = [];
  let bindingSaveFails = true;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/api/model-providers") && init?.method === "POST") {
      providers = [created];
      return json(created);
    }
    if (url.includes("/api/model-providers")) return json(init?.method === "PATCH" ? created : { providers });
    if (url.includes("/api/credential-bindings") && init?.method === "PUT") return bindingSaveFails ? json({ error: "vault locked" }, 500) : json({});
    if (url.includes("/api/credential-bindings")) return json({ bindings: [] });
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<MemoryRouter><ModelProvidersPage /></MemoryRouter>);
  await screen.findByText("No model providers yet.");
  await userEvent.type(await screen.findByLabelText("Name"), "Hub");
  await userEvent.type(screen.getByLabelText("Base URL"), "https://hub.example.test/v1");
  await userEvent.type(screen.getByLabelText("API key"), "sk-test");
  await userEvent.click(screen.getByRole("checkbox", { name: "openai_responses" }));
  await userEvent.click(screen.getByRole("button", { name: "Create provider" }));

  expect(await screen.findByText(/vault locked/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Create provider" })).not.toBeInTheDocument();
  await waitFor(() => expect(screen.getByLabelText("Name")).toHaveValue("Hub"));
  const posts = fetchMock.mock.calls.filter(([input, init]) => String(input).includes("/api/model-providers") && init?.method === "POST");
  expect(posts).toHaveLength(1);
  expect(screen.getByLabelText("API key")).toHaveValue("sk-test");

  bindingSaveFails = false;
  await userEvent.click(screen.getByRole("button", { name: "Save provider" }));
  await waitFor(() => {
    const puts = fetchMock.mock.calls.filter(([input, init]) => String(input).includes("/api/credential-bindings") && init?.method === "PUT");
    expect(puts).toHaveLength(2);
    expect(JSON.parse(String(puts[1]![1]!.body)).source.value).toBe("sk-test");
  });
});
