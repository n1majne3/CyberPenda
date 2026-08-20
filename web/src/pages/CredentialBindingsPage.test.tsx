import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { CredentialBindingsPage } from "./CredentialBindingsPage";

function renderPage() {
  return render(
    <MemoryRouter>
      <CredentialBindingsPage />
    </MemoryRouter>,
  );
}

describe("CredentialBindingsPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("uses a library-first layout with compact binding rows and a management panel", async () => {
    mockApi({
      "/api/credential-bindings": {
        bindings: [
          {
            id: "binding-1",
            credential_ref: "OPENAI_API_KEY",
            scope: "global",
            source: { kind: "env", value: "OPENAI_API_KEY" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/runtime-profiles": { profiles: [] },
      "/api/model-providers": { providers: [] },
    });

    renderPage();

    const layout = await screen.findByTestId("credential-bindings-settings-layout");
    expect(layout).toHaveClass(
      "grid",
      "min-w-0",
      "lg:grid-cols-[minmax(0,1fr)_minmax(320px,380px)]",
    );
    expect(screen.getByTestId("credential-bindings-settings-list")).toHaveClass(
      "min-w-0",
      "flex-col",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );
    expect(layout).toHaveClass("lg:min-h-0", "lg:flex-1");
    expect(await screen.findByTestId("credentials-library-list")).toBeInTheDocument();
    expect(await screen.findByTestId("credential-row-binding-1")).toBeInTheDocument();
    expect(screen.getByLabelText("Search credentials")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Filter by status" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Filter by source kind" })).toBeInTheDocument();

    expect(screen.getByTestId("credential-binding-create-panel")).toHaveClass(
      "rounded-lg",
      "border",
      "bg-card",
      "min-w-0",
      "overflow-hidden",
    );
    expect(screen.getByRole("heading", { name: "Library actions" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Credential reference")).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole("button", { name: /New binding/i })[0]!);

    expect(screen.getByRole("button", { name: "Create binding" })).toBeDisabled();
    expect(screen.getByRole("heading", { name: "New binding" })).toBeInTheDocument();
  });

  it("associates creation labels with named controls", async () => {
    mockApi({
      "/api/credential-bindings": { bindings: [] },
      "/api/runtime-profiles": { profiles: [] },
      "/api/model-providers": { providers: [] },
    });

    renderPage();
    const newBindingButtons = await screen.findAllByRole("button", { name: /New binding/i });
    await userEvent.click(newBindingButtons[0]!);

    // Default kind is now "literal"; the source value field is "Secret value".
    expect(screen.getByLabelText("Credential reference")).toHaveAttribute("name", "credential_ref");
    expect(screen.getByLabelText("Source kind")).toHaveAttribute("name", "source_kind");
    expect(screen.getByLabelText("Secret value")).toHaveAttribute("name", "source_value");
    expect(screen.getByLabelText("Secret value")).toHaveAttribute("autocomplete", "off");
    // destination_env is the dedicated runtime env var name field.
    expect(screen.getByLabelText("Runtime environment variable name")).toHaveAttribute("name", "destination_env");
  });

  it("requires confirmation before deleting a credential binding", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const fetchMock = mockApi({
      "/api/credential-bindings": {
        bindings: [
          {
            id: "binding-1",
            credential_ref: "OPENAI_API_KEY",
            scope: "global",
            source: { kind: "env", value: "OPENAI_API_KEY" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/runtime-profiles": { profiles: [] },
      "/api/model-providers": { providers: [] },
    });

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Delete OPENAI_API_KEY binding/i }));

    expect(confirm).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog", { name: /Delete credential binding OPENAI_API_KEY/ })).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/credential-bindings/binding-1") && init?.method === "DELETE",
      ),
    ).toBe(false);
  });

  it("filters the credential library by search and status", async () => {
    mockApi({
      "/api/credential-bindings": {
        bindings: [
          {
            id: "binding-1",
            credential_ref: "OPENAI_API_KEY",
            scope: "global",
            source: { kind: "env", value: "OPENAI_API_KEY" },
            created_at: "",
            updated_at: "",
          },
          {
            id: "binding-2",
            credential_ref: "legacy-token",
            scope: "global",
            source: { kind: "literal", value: "secret" },
            disabled: true,
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/runtime-profiles": {
        profiles: [
          {
            id: "profile-1",
            name: "Codex Default",
            provider: "codex",
            fields: { credential_refs: ["OPENAI_API_KEY"] },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/model-providers": {
        providers: [
          {
            id: "mp-1",
            name: "OpenAI",
            api_key_env: "OPENAI_API_KEY",
            protocols: [],
            created_at: "",
            updated_at: "",
          },
        ],
      },
    });

    renderPage();

    expect(await screen.findByTestId("credential-row-binding-1")).toBeInTheDocument();
    expect(screen.getByTestId("credential-row-binding-2")).toBeInTheDocument();
    expect(screen.getByText("Codex Default")).toBeInTheDocument();
    // OPENAI_API_KEY matches a Model Provider's api_key_env → provider key.
    expect(screen.getByText(/Provider key · OpenAI/i)).toBeInTheDocument();
    expect(screen.getByText("legacy-token")).toBeInTheDocument();
    // legacy-token matches no provider → global env var classification.
    expect(screen.getByText("Global env var")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /disabled/i }));
    expect(screen.queryByTestId("credential-row-binding-1")).not.toBeInTheDocument();
    expect(screen.getByTestId("credential-row-binding-2")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /^all/i }));
    await userEvent.type(screen.getByLabelText("Search credentials"), "legacy");
    expect(screen.queryByTestId("credential-row-binding-1")).not.toBeInTheDocument();
    expect(screen.getByTestId("credential-row-binding-2")).toBeInTheDocument();
  });

  it("masks literal secret values in the library list", async () => {
    mockApi({
      "/api/credential-bindings": {
        bindings: [
          {
            id: "binding-1",
            credential_ref: "stored-secret",
            scope: "global",
            source: { kind: "literal", value: "sk-super-secret" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/runtime-profiles": { profiles: [] },
      "/api/model-providers": { providers: [] },
    });

    renderPage();

    expect(await screen.findByText("stored-secret")).toBeInTheDocument();
    expect(screen.getByText("••••••••")).toBeInTheDocument();
    expect(screen.queryByText("sk-super-secret")).not.toBeInTheDocument();
  });

  // Regression: a literal binding created through the form MUST carry
  // destination_env, otherwise projection silently fails at launch (the root
  // cause of NSSCTF_AGENT_TOKEN not reaching the runtime).
  it("sends destination_env when creating a literal binding", async () => {
    const fetchMock = mockApi({
      "/api/credential-bindings": { bindings: [] },
      "/api/runtime-profiles": { profiles: [] },
      "/api/model-providers": { providers: [] },
    });

    renderPage();
    const newBindingButtons = await screen.findAllByRole("button", { name: /New binding/i });
    await userEvent.click(newBindingButtons[0]!);

    // Choose literal kind and fill both the secret value and the env var name.
    await userEvent.selectOptions(screen.getByLabelText("Source kind"), "literal");
    await userEvent.type(screen.getByLabelText("Credential reference"), "NSSCTF_AGENT_TOKEN");
    await userEvent.type(screen.getByLabelText("Secret value"), "nss_agent_secret");
    await userEvent.type(screen.getByLabelText("Runtime environment variable name"), "NSSCTF_AGENT_TOKEN");

    await userEvent.click(screen.getByRole("button", { name: "Create binding" }));

    const putCall = fetchMock.mock.calls.find(
      ([input, init]) => String(input).includes("/api/credential-bindings") && init?.method === "PUT",
    );
    expect(putCall).toBeDefined();
    const body = JSON.parse((putCall![1]!.body as string) ?? "{}");
    expect(body.credential_ref).toBe("NSSCTF_AGENT_TOKEN");
    expect(body.source.destination_env).toBe("NSSCTF_AGENT_TOKEN");
    expect(body.source.kind).toBe("literal");
  });

  it("edits an existing binding and preserves the stored literal when the value is left blank", async () => {
    const fetchMock = mockApi({
      "/api/credential-bindings": {
        bindings: [
          {
            id: "binding-1",
            credential_ref: "NSSCTF_AGENT_TOKEN",
            scope: "global",
            source: { kind: "literal", value: "[configured]", destination_env: "NSSCTF_AGENT_TOKEN" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/runtime-profiles": { profiles: [] },
      "/api/model-providers": { providers: [] },
    });

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Edit NSSCTF_AGENT_TOKEN binding/i }));

    expect(screen.getByRole("heading", { name: "Edit binding" })).toBeInTheDocument();
    // The reference is immutable; it renders disabled.
    expect(screen.getByLabelText("Credential reference")).toBeDisabled();
    expect((screen.getByLabelText("Secret value") as HTMLInputElement).value).toBe("");
    expect((screen.getByLabelText("Runtime environment variable name") as HTMLInputElement).value).toBe(
      "NSSCTF_AGENT_TOKEN",
    );

    // Blank secret keeps the stored value: Save stays enabled and sends the
    // sentinel instead of an empty string.
    const saveButton = screen.getByRole("button", { name: "Save changes" });
    expect(saveButton).toBeEnabled();
    await userEvent.click(saveButton);

    const putCall = fetchMock.mock.calls.find(
      ([input, init]) => String(input).includes("/api/credential-bindings") && init?.method === "PUT",
    );
    expect(putCall).toBeDefined();
    const body = JSON.parse((putCall![1]!.body as string) ?? "{}");
    expect(body.credential_ref).toBe("NSSCTF_AGENT_TOKEN");
    expect(body.source.kind).toBe("literal");
    expect(body.source.value).toBe("[configured]");
    expect(body.source.destination_env).toBe("NSSCTF_AGENT_TOKEN");
  });

  it("sends the new value when editing an env binding", async () => {
    const fetchMock = mockApi({
      "/api/credential-bindings": {
        bindings: [
          {
            id: "binding-1",
            credential_ref: "OPENAI_API_KEY",
            scope: "global",
            source: { kind: "env", value: "OPENAI_API_KEY", destination_env: "OPENAI_API_KEY" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/runtime-profiles": { profiles: [] },
      "/api/model-providers": { providers: [] },
    });

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Edit OPENAI_API_KEY binding/i }));

    const valueField = await screen.findByLabelText("Host environment variable name");
    expect((valueField as HTMLInputElement).value).toBe("OPENAI_API_KEY");
    await userEvent.clear(valueField);
    await userEvent.type(valueField, "OPENAI_KEY_V2");

    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    const putCall = fetchMock.mock.calls.find(
      ([input, init]) => String(input).includes("/api/credential-bindings") && init?.method === "PUT",
    );
    expect(putCall).toBeDefined();
    const body = JSON.parse((putCall![1]!.body as string) ?? "{}");
    expect(body.credential_ref).toBe("OPENAI_API_KEY");
    expect(body.source.kind).toBe("env");
    expect(body.source.value).toBe("OPENAI_KEY_V2");
    // destination_env is an independent field; it keeps its prefilled value.
    expect(body.source.destination_env).toBe("OPENAI_API_KEY");
  });
});
