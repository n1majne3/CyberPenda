import type { Skill } from "@/lib/api";
import {
  canPreviewLaunchSkills,
  enabledLaunchSkills,
  launchProfileIdForSkillsPreview,
  launchSkillsPreviewDetail,
} from "@/pages/taskLaunchSkills";

describe("taskLaunchSkills", () => {
  it("uses preset profile id directly for skills preview", () => {
    expect(launchProfileIdForSkillsPreview("codex-preset", "resolved-profile")).toBe("codex-preset");
    expect(launchProfileIdForSkillsPreview("", "resolved-profile")).toBe("resolved-profile");
  });

  it("allows skills preview when preset is selected", () => {
    expect(
      canPreviewLaunchSkills(
        { runtime: "", modelProviderId: "", modelOverride: "", runner: "sandbox" },
        "codex-preset",
      ),
    ).toBe(true);
  });

  it("requires runtime and model provider for auto-resolve preview", () => {
    expect(
      canPreviewLaunchSkills(
        { runtime: "codex", modelProviderId: "mimo", modelOverride: "", runner: "sandbox" },
        "",
      ),
    ).toBe(true);
    expect(
      canPreviewLaunchSkills(
        { runtime: "codex", modelProviderId: "", modelOverride: "", runner: "sandbox" },
        "",
      ),
    ).toBe(false);
  });

  it("filters enabled skills for preview", () => {
    const skills: Skill[] = [
      {
        id: "recon-helper",
        name: "Recon Helper",
        enabled: true,
        created_at: "",
        updated_at: "",
      },
      {
        id: "disabled-skill",
        name: "Disabled",
        enabled: false,
        created_at: "",
        updated_at: "",
      },
    ];
    expect(enabledLaunchSkills(skills).map((skill) => skill.id)).toEqual(["recon-helper"]);
  });

  it("explains preset vs auto-resolved profile semantics", () => {
    expect(launchSkillsPreviewDetail(true)).toMatch(/preset/i);
    expect(launchSkillsPreviewDetail(false)).toMatch(/auto-resolved|launch resolution/i);
  });
});