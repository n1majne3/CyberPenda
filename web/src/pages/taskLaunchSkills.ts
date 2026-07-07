import type { Skill } from "@/lib/api";
import { launchRuntimeProfileId } from "@/pages/taskLaunchForm";
import type { LaunchForm } from "@/pages/taskLaunchForm";

export function launchProfileIdForSkillsPreview(presetId: string, resolvedProfileId: string): string {
  return launchRuntimeProfileId(presetId, resolvedProfileId);
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
    return "Skills follow the selected preset. Library skills are enabled by default unless this profile has opt-outs.";
  }
  return "Skills follow the matching runtime profile for this launch selection. Library skills are enabled by default unless that profile has opt-outs.";
}
