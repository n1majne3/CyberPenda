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
  skillsLoading,
  setStatusFilter,
  setQuery,
  startCreate,
  setImportOpen,
  toggleGlobalOptOut,
  toggleOptOut,
  editSkill,
  onSelect,
  setConfirmDeleteSkill,
}: {
  skills: Skill[];
  filteredSkills: Skill[];
  selectedProfile: RuntimeProfile | null;
  editingId?: string;
  skillsLoading: boolean;
  setStatusFilter: (status: StatusFilter) => void;
  setQuery: (query: string) => void;
  startCreate: () => void;
  setImportOpen: (open: boolean) => void;
  toggleGlobalOptOut: (skill: Skill) => Promise<void>;
  toggleOptOut: (skill: Skill) => Promise<void>;
  editSkill: (skill: Skill) => Promise<void>;
  onSelect: (skill: Skill) => Promise<void>;
  setConfirmDeleteSkill: (skill: Skill) => void;
}) {
  return (
    <>


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
        <div className="min-w-0 space-y-1" data-testid="skills-library-list">
          {filteredSkills.map((skill) => {
            const name = displaySkillName(skill);
            const id = displaySkillId(skill);
            const description = displaySkillDescription(skill);
            const enabled = !skill.globally_opted_out && profileSkillEnabled(skill);
            return <div key={skill.id} data-testid={`skill-card-${skill.id}`} className={cn("group min-w-0 rounded-md px-3 py-3", editingId === skill.id && "bg-muted/70")}>
              <div className="flex items-start justify-between gap-1">
                <button type="button" onClick={() => void onSelect(skill)} className="min-w-0 flex-1 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" aria-label={`View ${name}`} aria-current={editingId === skill.id ? "true" : undefined} title={id}>
                  <span className="block truncate text-xs font-medium">{name}</span>
                  <span className="mt-1 line-clamp-2 text-[10px] leading-5 text-muted-foreground">{description || id}</span>
                </button>
                <div className="flex opacity-0 focus-within:opacity-100 group-hover:opacity-100">
                  <Button variant="ghost" size="icon-sm" onClick={() => void editSkill(skill)} aria-label={`Edit ${name}`}><Pencil className="size-3" /></Button>
                  <Button variant="ghost" size="icon-sm" onClick={() => setConfirmDeleteSkill(skill)} aria-label={`Delete ${name}`}><Trash2 className="size-3" /></Button>
                </div>
              </div>
              <p className="mt-1 text-[10px] text-muted-foreground"><span>{enabled ? "Enabled" : "Opted out"}</span> · <span>{skill.source_provenance?.kind ?? "built-in"}</span></p>
              <details className="mt-2 text-[10px] text-muted-foreground">
                <summary className="cursor-pointer">Enablement</summary>
                <div className="mt-3 flex items-center justify-between gap-2"><span>Global</span><EnableSwitch enabled={!skill.globally_opted_out} onClick={() => toggleGlobalOptOut(skill)} ariaLabel={skill.globally_opted_out ? `Enable globally for ${name}` : `Opt out globally for ${name}`} /></div>
                <div className="mt-3 flex items-center justify-between gap-2"><span>{selectedProfile?.name || "Runtime Profile"}</span><EnableSwitch enabled={profileSkillEnabled(skill)} disabled={!selectedProfile || Boolean(skill.globally_opted_out)} onClick={() => toggleOptOut(skill)}
                  ariaLabel={selectedProfile ? profileSkillEnabled(skill) ? `Opt out for ${selectedProfile.name}` : `Enable for ${selectedProfile.name}` : `Create a Runtime Profile to manage ${name}`} /></div>
              </details>
            </div>;
          })}
          {filteredSkills.length !== skills.length && <p className="px-3 py-2 text-[10px] text-muted-foreground">Showing {filteredSkills.length} of {skills.length} skills</p>}
        </div>
      )}
    </>
  );
}

export function SkillsLibraryToolbar({ skillsCount, enabledCount, optedOutCount, statusFilter, query, setStatusFilter, setQuery }: {
  skillsCount: number;
  enabledCount: number;
  optedOutCount: number;
  statusFilter: StatusFilter;
  query: string;
  setStatusFilter: (status: StatusFilter) => void;
  setQuery: (query: string) => void;
}) {
  return (
      <div className="mb-7 flex flex-wrap items-center justify-between gap-3">
        <span className="text-base font-semibold">
          {enabledCount}{" "}
          <span className="text-xs font-normal text-muted-foreground">
            enabled · {skillsCount} total
          </span>
        </span>
        <div className="order-first flex min-w-0 flex-1 flex-wrap items-center justify-between gap-3">
          <SettingsSegmentedFilter
            aria-label="Filter by status"
            value={statusFilter}
            onChange={setStatusFilter}
            options={[
              { id: "all", label: "All", count: skillsCount },
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
            className="order-first mr-auto max-w-80 min-w-48 flex-1"
          />
        </div>
      </div>
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
