import { Ban, CheckCheck } from "lucide-react";
import { Button } from "@/components/ui";
import type { RuntimeProfile } from "@/lib/api";

export type DisableAllTarget =
  | { scope: "global" }
  | { scope: "profile"; profile: RuntimeProfile };

export function SkillBulkActions({
  selectedProfile,
  skillCount,
  globalEnabledCount,
  globalOptedOutCount,
  profileEnabledCount,
  profileOptedOutCount,
  bulkUpdating,
  skillsLoading,
  setAllGlobalSkillsOptOut,
  setAllSkillsOptOut,
  setConfirmDisableAllTarget,
}: {
  selectedProfile: RuntimeProfile | null;
  skillCount: number;
  globalEnabledCount: number;
  globalOptedOutCount: number;
  profileEnabledCount: number;
  profileOptedOutCount: number;
  bulkUpdating: boolean;
  skillsLoading: boolean;
  setAllGlobalSkillsOptOut: (optedOut: boolean) => Promise<void>;
  setAllSkillsOptOut: (profile: RuntimeProfile, optedOut: boolean) => Promise<void>;
  setConfirmDisableAllTarget: (target: DisableAllTarget) => void;
}) {
  return (
    <div className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,15rem),1fr))] gap-2 rounded-lg border border-border bg-card p-3 shadow-sm lg:shrink-0">
      <div
        role="group"
        aria-label="Global Skill bulk actions"
        className="rounded-md border border-border bg-muted/20 p-3"
      >
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-xs font-semibold">Global</p>
            <p className="mt-0.5 text-[11px] leading-4 text-muted-foreground">
              Direct launches and every Runtime Profile
            </p>
          </div>
          <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
            {globalEnabledCount}/{skillCount} enabled
          </span>
        </div>
        <div className="mt-3 flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            aria-label="Enable all skills globally"
            disabled={globalOptedOutCount === 0 || bulkUpdating || skillsLoading}
            onClick={() => void setAllGlobalSkillsOptOut(false)}
            className="flex-1"
          >
            <CheckCheck className="h-3.5 w-3.5" /> Enable all
          </Button>
          <Button
            size="sm"
            variant="outline"
            aria-label="Disable all skills globally"
            disabled={globalEnabledCount === 0 || bulkUpdating || skillsLoading}
            onClick={() => setConfirmDisableAllTarget({ scope: "global" })}
            className="flex-1 text-destructive hover:text-destructive"
          >
            <Ban className="h-3.5 w-3.5" /> Disable all
          </Button>
        </div>
      </div>

      <div
        role="group"
        aria-label={
          selectedProfile
            ? `${selectedProfile.name} Profile Skill bulk actions`
            : "Profile Skill bulk actions"
        }
        className="rounded-md border border-border bg-muted/20 p-3"
      >
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="truncate text-xs font-semibold">
              Profile{selectedProfile ? ` · ${selectedProfile.name}` : ""}
            </p>
            <p className="mt-0.5 text-[11px] leading-4 text-muted-foreground">
              {selectedProfile
                ? "Only this Profile; Global Opt-Outs still override"
                : "Create a Runtime Profile to use these controls"}
            </p>
          </div>
          <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
            {selectedProfile
              ? `${profileEnabledCount}/${skillCount} allowed`
              : "Unavailable"}
          </span>
        </div>
        <div className="mt-3 flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            aria-label={
              selectedProfile
                ? `Enable all skills for ${selectedProfile.name}`
                : "Enable all skills for a Runtime Profile"
            }
            disabled={!selectedProfile || profileOptedOutCount === 0 || bulkUpdating || skillsLoading}
            onClick={() => {
              if (selectedProfile) void setAllSkillsOptOut(selectedProfile, false);
            }}
            className="flex-1"
          >
            <CheckCheck className="h-3.5 w-3.5" /> Enable all
          </Button>
          <Button
            size="sm"
            variant="outline"
            aria-label={
              selectedProfile
                ? `Disable all skills for ${selectedProfile.name}`
                : "Disable all skills for a Runtime Profile"
            }
            disabled={!selectedProfile || profileEnabledCount === 0 || bulkUpdating || skillsLoading}
            onClick={() => {
              if (selectedProfile) {
                setConfirmDisableAllTarget({ scope: "profile", profile: selectedProfile });
              }
            }}
            className="flex-1 text-destructive hover:text-destructive"
          >
            <Ban className="h-3.5 w-3.5" /> Disable all
          </Button>
        </div>
      </div>
    </div>
  );
}
