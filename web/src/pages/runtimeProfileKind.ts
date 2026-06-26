import type { RuntimeProfile } from "@/lib/api";

export function isManualRuntimeProfile(profile: RuntimeProfile): boolean {
  return profile.kind !== "launch_resolve";
}

export function isLaunchResolvedProfile(profile: RuntimeProfile): boolean {
  return profile.kind === "launch_resolve";
}