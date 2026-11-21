import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SkillsPage } from "./SkillsPage";

afterEach(() => { vi.unstubAllGlobals(); });

function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter>
        <SkillsPage />
      </MemoryRouter>
    </StrictMode>,
  );
}

describe("SkillsPage Runtime Profile switch", () => {
  it("keeps the Skills of the latest Runtime Profile when an older read finishes late", async () => {
    let releaseFirst = () => {};
    const firstGate = new Promise<void>((resolve) => { releaseFirst = resolve; });
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    const skill = (id: string, name: string) => ({ id, name, description: "", enabled: true, created_at: "", updated_at: "" });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/api/runtime-profiles")) {
        return json({ profiles: [
          { id: "profile-1", name: "First", provider: "codex", fields: {}, created_at: "", updated_at: "" },
          { id: "profile-2", name: "Second", provider: "codex", fields: {}, created_at: "", updated_at: "" },
        ] });
      }
      if (url.includes("runtime_profile_id=profile-1")) {
        await firstGate;
        return json({ skills: [skill("alpha", "Alpha Skill")] });
      }
      if (url.includes("runtime_profile_id=profile-2")) return json({ skills: [skill("beta", "Beta Skill")] });
      return json({ skills: [] });
    }));

    renderPage();

    const select = await screen.findByLabelText("Runtime profile view");
    await screen.findByRole("option", { name: "Second (codex)" });
    await userEvent.selectOptions(select, "profile-2");
    expect(await screen.findByText("Beta Skill")).toBeInTheDocument();
    releaseFirst();
    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByText("Alpha Skill")).not.toBeInTheDocument();
    expect(screen.getByText("Beta Skill")).toBeInTheDocument();
  });
});
