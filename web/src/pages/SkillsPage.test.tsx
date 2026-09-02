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

  it("supports global Skill Opt-Out independently of the selected Runtime Profile", async () => {
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
            globally_opted_out: false,
            created_at: "",
            updated_at: "",
          },
          {
            id: "report-helper",
            name: "Report Helper",
            enabled: false,
            globally_opted_out: true,
            profile_opted_out: false,
            created_at: "",
            updated_at: "",
          },
        ],
      },
      "/api/skills/recon-helper/opt-out": {},
      "/api/skills/report-helper/opt-out": {},
    });

    renderPage();

    const reconRow = await screen.findByTestId("skill-card-recon-helper");
    await userEvent.click(
      within(reconRow).getByRole("switch", { name: "Opt out globally for Recon Helper" }),
    );
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/recon-helper/opt-out",
      expect.objectContaining({ method: "PUT" }),
    );

    const reportRow = screen.getByTestId("skill-card-report-helper");
    expect(
      within(reportRow).getByRole("switch", { name: "Enable globally for Report Helper" }),
    ).toBeInTheDocument();
    const preservedProfileSwitch = within(reportRow).getByRole("switch", {
      name: /Opt out for Codex Default/i,
    });
    expect(preservedProfileSwitch).toBeChecked();
    expect(preservedProfileSwitch).toBeDisabled();

    await userEvent.click(
      within(reportRow).getByRole("switch", { name: "Enable globally for Report Helper" }),
    );
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/report-helper/opt-out",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  it("offers separate bulk controls for Global and Profile Skill Opt-Outs", async () => {
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
      "/api/skills/opt-outs/global": {},
      "/api/skills/profiles/profile-1/opt-out": {},
      "/api/skills?runtime_profile_id=profile-1": {
        skills: [
          {
            id: "recon-helper",
            name: "Recon Helper",
            enabled: true,
            globally_opted_out: false,
            profile_opted_out: false,
            created_at: "",
            updated_at: "",
          },
          {
            id: "report-helper",
            name: "Report Helper",
            enabled: false,
            globally_opted_out: true,
            profile_opted_out: true,
            created_at: "",
            updated_at: "",
          },
        ],
      },
    });

    renderPage();

    await screen.findByText("Recon Helper");

    expect(screen.getByRole("group", { name: "Global Skill bulk actions" })).toBeInTheDocument();
    expect(
      screen.getByRole("group", { name: "Codex Default Profile Skill bulk actions" }),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Disable all skills globally" }));

    const globalDialog = screen.getByRole("alertdialog", {
      name: "Disable all skills globally?",
    });
    expect(globalDialog).toBeInTheDocument();
    expect(
      within(globalDialog).getByText(/direct launches and every Runtime Profile/i),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Disable globally" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/opt-outs/global",
      expect.objectContaining({ method: "PUT" }),
    );

    await userEvent.click(screen.getByRole("button", { name: "Enable all skills globally" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/opt-outs/global",
      expect.objectContaining({ method: "DELETE" }),
    );

    await userEvent.click(
      screen.getByRole("button", { name: "Disable all skills for Codex Default" }),
    );

    expect(
      screen.getByRole("alertdialog", { name: "Disable all skills for Codex Default?" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/future imported Skills remain default-on/i)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Disable for profile" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/profiles/profile-1/opt-out",
      expect.objectContaining({ method: "PUT" }),
    );

    await userEvent.click(
      screen.getByRole("button", { name: "Enable all skills for Codex Default" }),
    );
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
      await screen.findByText(/Global Skill Opt-Outs affect direct launches and every Runtime Profile/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/Profile Skill Opt-Outs apply only when this Runtime Profile/i)).toBeInTheDocument();
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

  it("keeps the Global Skill control active when no Runtime Profile exists", async () => {
    const fetchMock = mockApi({
      "/api/runtime-profiles": { profiles: [] },
      "/api/skills/opt-outs/global": {},
      "/api/skills/api-security/opt-out": {},
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

    const globalSwitch = await screen.findByRole("switch", {
      name: "Opt out globally for API Security",
    });
    expect(globalSwitch).toBeEnabled();
    await userEvent.click(globalSwitch);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/api-security/opt-out",
      expect.objectContaining({ method: "PUT" }),
    );

    const profileSwitch = screen.getByRole("switch", {
      name: /create a Runtime Profile to manage API Security/i,
    });
    expect(profileSwitch).toBeDisabled();
    expect(profileSwitch).toHaveAttribute("aria-checked", "true");
    expect(within(screen.getByTestId("skill-card-api-security")).queryByText("—")).not.toBeInTheDocument();

    const globalBulkButton = screen.getByRole("button", {
      name: "Disable all skills globally",
    });
    expect(globalBulkButton).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "Disable all skills for a Runtime Profile" }),
    ).toBeDisabled();

    await userEvent.click(globalBulkButton);
    await userEvent.click(screen.getByRole("button", { name: "Disable globally" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/skills/opt-outs/global",
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
