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
    return "Global Skill Opt-Outs apply first. Other library Skills follow the selected Runtime Profile and its Profile Skill Opt-Outs.";
  }
  return "Direct configuration captures the current Skills after Global Skill Opt-Outs. Later library changes do not change this Runtime Owner.";
}
