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

  it("associates creation labels with named controls", async () => {
    mockApi({
      "/api/credential-bindings": { bindings: [] },
      "/api/runtime-profiles": { profiles: [] },
      "/api/model-providers": { providers: [] },
    });

    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /New binding/i }));

    expect(screen.getByLabelText("Credential reference")).toHaveAttribute("name", "credential_ref");
    expect(screen.getByLabelText("Source kind")).toHaveAttribute("name", "source_kind");
    expect(screen.getByLabelText("Environment variable name")).toHaveAttribute("name", "source_value");
    expect(screen.getByLabelText("Environment variable name")).toHaveAttribute("autocomplete", "off");
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

    expect(confirm).toHaveBeenCalledWith("Delete credential binding OPENAI_API_KEY?");
    expect(
      fetchMock.mock.calls.some(([input, init]) =>
        String(input).includes("/api/credential-bindings/binding-1") && init?.method === "DELETE",
      ),
    ).toBe(false);
  });
});
