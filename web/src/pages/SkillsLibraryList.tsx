import { BookOpen, Download, LoaderCircle, Pencil, Plus, Trash2 } from "lucide-react";
import { SettingsPanel } from "@/components/shared";
import { SettingsSearchField, SettingsSegmentedFilter } from "@/components/settingsLibrary";
import { Button } from "@/components/ui";
import type { RuntimeProfile, Skill } from "@/lib/api";
import { cn } from "@/lib/utils";
import { displaySkillDescription, displaySkillId, displaySkillName, profileSkillEnabled } from "./skillDisplay";

export type StatusFilter = "all" | "enabled" | "opted_out";

export function SkillsLibraryList({
  skills,
  filteredSkills,
  selectedProfile,
  editingId,
  enabledCount,
  optedOutCount,
  statusFilter,
  query,
  skillsLoading,
  setStatusFilter,
  setQuery,
  startCreate,
  setImportOpen,
  toggleGlobalOptOut,
  toggleOptOut,
  editSkill,
  setConfirmDeleteSkill,
}: {
  skills: Skill[];
  filteredSkills: Skill[];
  selectedProfile: RuntimeProfile | null;
  editingId?: string;
  enabledCount: number;
  optedOutCount: number;
  statusFilter: StatusFilter;
  query: string;
  skillsLoading: boolean;
  setStatusFilter: (status: StatusFilter) => void;
  setQuery: (query: string) => void;
  startCreate: () => void;
  setImportOpen: (open: boolean) => void;
  toggleGlobalOptOut: (skill: Skill) => Promise<void>;
  toggleOptOut: (skill: Skill) => Promise<void>;
  editSkill: (skill: Skill) => Promise<void>;
  setConfirmDeleteSkill: (skill: Skill) => void;
}) {
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2 lg:shrink-0">
        <span className="text-base font-semibold">
          {enabledCount}{" "}
          <span className="text-xs font-normal text-muted-foreground">
            enabled · {skills.length} total
          </span>
        </span>
        <div className="flex flex-wrap items-center gap-2">
          <SettingsSegmentedFilter
            aria-label="Filter by status"
            value={statusFilter}
            onChange={setStatusFilter}
            options={[
              { id: "all", label: "All", count: skills.length },
              { id: "enabled", label: "Enabled", count: enabledCount },
              { id: "opted_out", label: "Opted out", count: optedOutCount },
            ]}
          />
          <SettingsSearchField
            id="skills-search"
            name="skills_search"
            value={query}
            onChange={setQuery}
            placeholder="Search name, id, or source…"
            aria-label="Search skills"
            className="w-[200px] flex-none"
          />
        </div>
      </div>

      {skillsLoading ? (
        <SettingsPanel className="items-center justify-center py-12 text-center lg:min-h-0 lg:flex-1 lg:overflow-y-auto" role="status" aria-label="Loading skills">
          <LoaderCircle className="h-5 w-5 animate-spin text-muted-foreground motion-reduce:animate-none" />
          <p className="mt-2 text-sm text-muted-foreground">Loading skills…</p>
        </SettingsPanel>
      ) : skills.length === 0 ? (
        <SettingsPanel className="items-center justify-center py-12 text-center lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <BookOpen className="h-5 w-5 text-muted-foreground" />
          </div>
          <div>
            <p className="font-medium">No Skills yet</p>
            <p className="mt-1 text-sm text-muted-foreground">
              Upload a bundle or import one through the controlled importer.
            </p>
          </div>
          <div className="flex flex-wrap justify-center gap-2">
            <Button size="sm" onClick={startCreate}>
              <Plus className="h-3.5 w-3.5" /> New skill
            </Button>
            <Button size="sm" variant="outline" onClick={() => setImportOpen(true)}>
              <Download className="h-3.5 w-3.5" /> Import package
            </Button>
          </div>
        </SettingsPanel>
      ) : filteredSkills.length === 0 ? (
        <SettingsPanel className="items-center justify-center py-10 text-center lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
          <p className="font-medium">No matching skills</p>
          <p className="mt-1 text-sm text-muted-foreground">
            Try a different search or clear the status filter.
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setQuery("");
              setStatusFilter("all");
            }}
          >
            Clear filters
          </Button>
        </SettingsPanel>
      ) : (
        <div
          className="overflow-hidden rounded-lg border border-border bg-card shadow-sm lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain"
          data-testid="skills-library-list"
        >
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs uppercase tracking-wider text-muted-foreground">
                <th className="px-4 py-2.5 font-medium">Skill</th>
                <th className="px-4 py-2.5 font-medium">Description</th>
                <th className="px-4 py-2.5 font-medium w-[70px]">Source</th>
                <th className="px-4 py-2.5 font-medium w-[60px]">Global</th>
                <th className="px-4 py-2.5 font-medium w-[60px]">Profile</th>
                <th className="px-4 py-2.5 font-medium w-[70px]"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {filteredSkills.map((skill) => {
                const name = displaySkillName(skill);
                const id = displaySkillId(skill);
                const description = displaySkillDescription(skill);
                const source = skill.source_provenance?.kind ?? "built-in";
                const selected = editingId === skill.id;
                return (
                  <tr
                    key={skill.id}
                    data-testid={`skill-card-${skill.id}`}
                    className={cn("hover:bg-muted/30", selected && "bg-accent/50")}
                  >
                    <td className="px-4 py-2.5">
                      <div className="font-medium">{name}</div>
                      <div className="font-mono text-xs text-muted-foreground">{id}</div>
                    </td>
                    <td className="px-4 py-2.5 text-xs text-muted-foreground">
                      {description || "—"}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-muted-foreground">{source}</td>
                    <td className="px-4 py-2.5">
                      <EnableSwitch
                        enabled={!skill.globally_opted_out}
                        onClick={() => toggleGlobalOptOut(skill)}
                        ariaLabel={
                          skill.globally_opted_out
                            ? `Enable globally for ${name}`
                            : `Opt out globally for ${name}`
                        }
                      />
                    </td>
                    <td className="px-4 py-2.5">
                      <EnableSwitch
                        enabled={profileSkillEnabled(skill)}
                        disabled={!selectedProfile || Boolean(skill.globally_opted_out)}
                        onClick={() => toggleOptOut(skill)}
                        ariaLabel={
                          selectedProfile
                            ? profileSkillEnabled(skill)
                              ? `Opt out for ${selectedProfile.name}`
                              : `Enable for ${selectedProfile.name}`
                            : `Create a Runtime Profile to manage ${name}`
                        }
                      />
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex items-center gap-0.5">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => editSkill(skill)}
                          aria-label={`Edit ${name}`}
                        >
                          <Pencil className="h-3.5 w-3.5" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => setConfirmDeleteSkill(skill)}
                          aria-label={`Delete ${name}`}
                          className="text-muted-foreground hover:text-destructive"
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {filteredSkills.length !== skills.length && (
            <div className="border-t border-border bg-muted/20 px-4 py-2 text-xs text-muted-foreground">
              Showing {filteredSkills.length} of {skills.length} skills
            </div>
          )}
        </div>
      )}
    </>
  );
}

function EnableSwitch({
  enabled,
  disabled = false,
  onClick,
  ariaLabel,
}: {
  enabled: boolean;
  disabled?: boolean;
  onClick: () => void;
  ariaLabel: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={enabled}
      aria-label={ariaLabel}
      onClick={onClick}
      disabled={disabled}
      className={cn(
        "relative h-6 w-10 shrink-0 rounded-full border transition-colors disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        enabled
          ? "border-success/30 bg-success"
          : "border-border bg-muted",
      )}
    >
      <span
        className={cn(
          "absolute top-0.5 left-0.5 h-[1.125rem] w-[1.125rem] rounded-full bg-white shadow-sm transition-transform duration-150",
          enabled && "translate-x-4",
        )}
      />
    </button>
  );
}
