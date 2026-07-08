import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SkillsPage } from "./SkillsPage";

function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter>
        <SkillsPage />
      </MemoryRouter>
    </StrictMode>,
  );
}

describe("SkillsPage", () => {
  it("uses a library-first layout with compact skill rows and a management panel", async () => {
    mockApi({
      "/api/runtime-profiles": {
        profiles: [
          {
            id: "profile-1",
            name: "Codex Default",
            provider: "codex",
            fields: {},
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills?runtime_profile_id=profile-1": {
        skills: [
          {
            id: "recon-helper",
            name: "Recon Helper",
            description: "Reusable recon workflow",
            enabled: true,
            source_provenance: { kind: "npm", package: "@acme/recon-skill", ref: "1.2.3" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
    });

    renderPage();

    const layout = await screen.findByTestId("skills-settings-layout");
    expect(layout).toHaveClass(
      "grid",
      "min-w-0",
      "lg:grid-cols-[minmax(0,1fr)_minmax(320px,380px)]",
    );
    expect(screen.getByTestId("skills-settings-list")).toHaveClass(
      "min-w-0",
      "flex-col",
      "lg:min-h-0",
      "lg:overflow-hidden",
    );
    expect(layout).toHaveClass("lg:min-h-0", "lg:flex-1");
    expect(await screen.findByTestId("skills-library-list")).toBeInTheDocument();
    expect(screen.getByTestId("skills-form-panel")).toHaveClass(
      "rounded-lg",
      "border",
      "bg-card",
      "min-w-0",
      "overflow-hidden",
    );
    expect(await screen.findByTestId("skill-card-recon-helper")).toBeInTheDocument();
    expect(screen.getByLabelText("Search skills")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Filter by status" })).toBeInTheDocument();
  });

  it("lists global skills with source provenance and profile opt-out controls", async () => {
    const fetchMock = mockApi({
      "/api/runtime-profiles": {
        profiles: [
          {
            id: "profile-1",
            name: "Codex Default",
            provider: "codex",
            fields: {},
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills?runtime_profile_id=profile-1": {
        skills: [
          {
            id: "recon-helper",
            name: "Recon Helper",
            description: "Reusable recon workflow",
            enabled: true,
            source_provenance: { kind: "npm", package: "@acme/recon-skill", ref: "1.2.3" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills/recon-helper": {
        id: "recon-helper",
        name: "Recon Helper",
        description: "Reusable recon workflow",
        enabled: true,
        source_provenance: { kind: "npm", package: "@acme/recon-skill", ref: "1.2.3" },
        files: { "SKILL.md": "# Existing Recon", "scripts/probe.sh": "#!/bin/sh\n" },
        created_at: "",
        updated_at: "",
      },
    });

    renderPage();

    expect(await screen.findByRole("heading", { name: "Skills" })).toBeInTheDocument();
    expect(await screen.findByText("Recon Helper")).toBeInTheDocument();
    expect(screen.getByText("@acme/recon-skill@1.2.3")).toBeInTheDocument();
    expect(screen.queryByText("recon-api-key")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /edit Recon Helper/i }));
    expect(await screen.findByDisplayValue("# Existing Recon")).toBeInTheDocument();
    expect(screen.getByDisplayValue(/scripts\/probe\.sh/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Edit Skill" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("switch", { name: /opt out for Codex Default/i }));

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/recon-helper/profiles/profile-1/opt-out",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("filters the skill library by search and status", async () => {
    mockApi({
      "/api/runtime-profiles": {
        profiles: [
          {
            id: "profile-1",
            name: "Codex Default",
            provider: "codex",
            fields: {},
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills?runtime_profile_id=profile-1": {
        skills: [
          {
            id: "recon-helper",
            name: "Recon Helper",
            description: "Reusable recon workflow",
            enabled: true,
            source_provenance: { kind: "npm", package: "@acme/recon-skill", ref: "1.2.3" },
            created_at: "",
            updated_at: "",
          },
          {
            id: "auth-bypass",
            name: "Auth Bypass",
            description: "Auth checks",
            enabled: false,
            source_provenance: { kind: "manual" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
    });

    renderPage();

    expect(await screen.findByText("Recon Helper")).toBeInTheDocument();
    expect(screen.getByText("Auth Bypass")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /opted out/i }));
    expect(screen.queryByText("Recon Helper")).not.toBeInTheDocument();
    expect(screen.getByText("Auth Bypass")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /^all/i }));
    await userEvent.type(screen.getByLabelText("Search skills"), "recon");
    expect(screen.getByText("Recon Helper")).toBeInTheDocument();
    expect(screen.queryByText("Auth Bypass")).not.toBeInTheDocument();
  });

  it("explains launch-resolved profile scope for skill opt-outs", async () => {
    mockApi({
      "/api/runtime-profiles": {
        profiles: [
          {
            id: "auto-1",
            name: "Codex · MiMo",
            provider: "codex",
            kind: "launch_resolve",
            fields: { model_provider_id: "mimo" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills?runtime_profile_id=auto-1": { skills: [] },
    });

    renderPage();

    expect(
      await screen.findByText(/created by launch resolution/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/future launches that resolve/i)).toBeInTheDocument();
  });

  it("does not show source labels or source-prefixed ids for built-in skills", async () => {
    const fetchMock = mockApi({
      "/api/runtime-profiles": {
        profiles: [
          {
            id: "profile-1",
            name: "Codex Default",
            provider: "codex",
            fields: {},
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills?runtime_profile_id=profile-1": {
        skills: [
          {
            id: "cyberstrikeai-vulnerabilities-xss",
            name: "cyberstrikeai-vulnerabilities-xss",
            description: "cyberstrikeai-vulnerabilities-xss: XSS testing methodology",
            enabled: true,
            source_provenance: { kind: "builtin" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills/cyberstrikeai-vulnerabilities-xss": {
        id: "cyberstrikeai-vulnerabilities-xss",
        name: "cyberstrikeai-vulnerabilities-xss",
        description: "cyberstrikeai-vulnerabilities-xss: XSS testing methodology",
        enabled: true,
        source_provenance: { kind: "builtin" },
        files: { "SKILL.md": "# XSS Testing" },
        created_at: "",
        updated_at: "",
      },
    });

    renderPage();

    expect(await screen.findByRole("heading", { name: "vulnerabilities-xss" })).toBeInTheDocument();
    expect(screen.getByText("XSS testing methodology")).toBeInTheDocument();
    expect(screen.queryByText("builtin")).not.toBeInTheDocument();
    expect(screen.queryByText(/cyberstrikeai/i)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /edit vulnerabilities-xss/i }));
    expect(await screen.findByLabelText("Skill ID")).toHaveValue("vulnerabilities-xss");
    expect(screen.queryByDisplayValue(/cyberstrikeai/i)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /publish skill/i }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/cyberstrikeai-vulnerabilities-xss",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("keeps the create form collapsed until New skill is chosen", async () => {
    mockApi({
      "/api/runtime-profiles": {
        profiles: [
          {
            id: "profile-1",
            name: "Codex Default",
            provider: "codex",
            fields: {},
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills?runtime_profile_id=profile-1": { skills: [] },
    });

    renderPage();

    expect(await screen.findByRole("heading", { name: "Library actions" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Skill ID")).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole("button", { name: /new skill/i })[0]!);
    expect(await screen.findByLabelText("Skill ID")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Upload / edit Skill" })).toBeInTheDocument();

    const formPanel = screen.getByTestId("skills-form-panel");
    expect(within(formPanel).getByRole("button", { name: /publish skill/i })).toBeInTheDocument();
  });
});
