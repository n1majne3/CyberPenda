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
      "flex",
      "min-w-0",
      "flex-col",
      "lg:overflow-y-auto",
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
    expect(screen.getByText("npm")).toBeInTheDocument();
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

  it("disables and enables all current skills for the selected Runtime Profile", async () => {
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
            enabled: true,
            created_at: "",
            updated_at: "",
          },
          {
            id: "report-helper",
            name: "Report Helper",
            enabled: false,
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills/profiles/profile-1/opt-out": {},
    });

    renderPage();

    await screen.findByText("Recon Helper");
    await userEvent.click(screen.getByRole("button", { name: "Disable all skills" }));

    expect(
      screen.getByRole("alertdialog", { name: "Disable all skills for Codex Default?" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/future imported Skills remain default-on/i)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Disable all" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/profiles/profile-1/opt-out",
      expect.objectContaining({ method: "PUT" }),
    );

    await userEvent.click(screen.getByRole("button", { name: "Enable all skills" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/profiles/profile-1/opt-out",
      expect.objectContaining({ method: "DELETE" }),
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

  it("explains user-created Runtime Profile scope for Skill Opt-Outs", async () => {
    mockApi({
      "/api/runtime-profiles": {
        profiles: [
          {
            id: "auto-1",
            name: "Codex · MiMo",
            provider: "codex",
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
      await screen.findByText(/Skill opt-outs apply to this Runtime Profile/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/new Task or Session/i)).toBeInTheDocument();
  });

  it("strips source prefixes from built-in skill names, ids, and descriptions", async () => {
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

    const builtinRow = await screen.findByTestId("skill-card-cyberstrikeai-vulnerabilities-xss");
    expect(within(builtinRow).getAllByText("vulnerabilities-xss")).toHaveLength(2);
    expect(screen.getByText("XSS testing methodology")).toBeInTheDocument();
    expect(within(builtinRow).getByText("builtin")).toBeInTheDocument();
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

  it("shows default-on Skill state when no Runtime Profile exists", async () => {
    mockApi({
      "/api/runtime-profiles": { profiles: [] },
      "/api/skills": {
        skills: [
          {
            id: "api-security",
            name: "API Security",
            description: "API assessment guidance",
            enabled: true,
            source_provenance: { kind: "builtin" },
            created_at: "",
            updated_at: "",
          },
        ],
      },
    });

    renderPage();

    const switchControl = await screen.findByRole("switch", { name: /create a Runtime Profile to manage API Security/i });
    expect(switchControl).toBeDisabled();
    expect(switchControl).toHaveAttribute("aria-checked", "true");
    expect(within(screen.getByTestId("skill-card-api-security")).queryByText("—")).not.toBeInTheDocument();
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

    expect(await screen.findByRole("heading", { name: "Upload / edit Skill" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Skill ID")).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole("button", { name: /new skill/i })[0]!);
    expect(await screen.findByLabelText("Skill ID")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Upload / edit Skill" })).toBeInTheDocument();

    const formPanel = screen.getByTestId("skills-form-panel");
    expect(within(formPanel).getByRole("button", { name: /publish skill/i })).toBeInTheDocument();
  });

  it("uploads a ZIP or TAR Skill bundle as multipart form data", async () => {
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
      "/api/skills?runtime_profile_id=profile-1": { skills: [] },
      "/api/skills/import": {},
    });

    renderPage();

    await screen.findByRole("heading", { name: "Upload / edit Skill" });
    const archiveInput = screen.getByLabelText("Skill bundle archive");
    expect(archiveInput).toHaveClass("sr-only");
    expect(screen.getByText("Choose file")).toBeInTheDocument();
    expect(screen.getByText("No file selected")).toBeInTheDocument();
    const archive = new File(["archive bytes"], "recon-helper.zip", { type: "application/zip" });
    await userEvent.upload(archiveInput, archive);
    expect(screen.getByText("recon-helper.zip")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Upload archive" }));

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/import",
      expect.objectContaining({ method: "POST", body: expect.any(FormData) }),
    );
    const uploadCall = fetchMock.mock.calls.find(([url, init]) =>
      url === "/api/skills/import" && init?.body instanceof FormData,
    );
    expect(uploadCall).toBeDefined();
    expect((uploadCall?.[1]?.body as FormData).get("archive")).toBe(archive);
  });
});
