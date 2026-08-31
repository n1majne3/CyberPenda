import type { Skill } from "@/lib/api";
import type { LaunchForm } from "@/pages/taskLaunchForm";

export function launchProfileIdForSkillsPreview(presetId: string, resolvedProfileId: string): string {
  return presetId.trim() || resolvedProfileId.trim();
}

export function canPreviewLaunchSkills(form: Pick<LaunchForm, "runtime" | "modelProviderId">, presetId: string): boolean {
  if (presetId.trim()) return true;
  return form.runtime.trim() !== "" && form.modelProviderId.trim() !== "";
}

export function enabledLaunchSkills(skills: Skill[]): Skill[] {
  return skills.filter((skill) => skill.enabled);
}

export function launchSkillsPreviewDetail(presetMode: boolean): string {
  if (presetMode) {
    return "Skills follow the selected Runtime Profile. Library Skills are enabled by default unless this Profile has Opt-Outs.";
  }
  return "Direct configuration captures the current globally default-enabled Skills. Later library changes do not change this Runtime Owner.";
}
