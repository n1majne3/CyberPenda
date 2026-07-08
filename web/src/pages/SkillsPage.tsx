import { useEffect, useMemo, useState } from "react";
import {
  BookOpen,
  Download,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { apiDelete, apiGet, apiPost, apiPut, type RuntimeProfile, type Skill } from "@/lib/api";
import { isLaunchResolvedProfile } from "@/pages/runtimeProfileKind";
import { Badge, Button, Input, Label, Select, Textarea } from "@/components/ui";
import {
  SettingsAlert,
  SettingsPageHeader,
  SettingsPanel,
  SettingsSplitLayout,
  SettingsPageShell,
} from "@/components/shared";
import {
  SettingsDetailPane,
  SettingsListColumn,
  SettingsSearchField,
  SettingsSegmentedFilter,
  SettingsStatSummary,
} from "@/components/settingsLibrary";
import { cn } from "@/lib/utils";

type SkillForm = {
  id: string;
  storage_id?: string;
  name: string;
  description: string;
  instruction: string;
  extra_files: string;
  source_provenance?: Skill["source_provenance"];
};

type FormMode = "idle" | "create" | "edit";
type StatusFilter = "all" | "enabled" | "opted_out";

const emptySkillForm: SkillForm = {
  id: "",
  name: "",
  description: "",
  instruction: "# New Skill\n\nDescribe when and how the runtime should use this skill.",
  extra_files: "{}",
};

export function SkillsPage() {
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [profileId, setProfileId] = useState("");
  const [skills, setSkills] = useState<Skill[]>([]);
  const [form, setForm] = useState<SkillForm>(emptySkillForm);
  const [formMode, setFormMode] = useState<FormMode>("idle");
  const [importPackage, setImportPackage] = useState("");
  const [importRef, setImportRef] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const selectedProfile = profiles.find((profile) => profile.id === profileId) ?? null;

  async function loadProfiles() {
    const data = await apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles");
    const loaded = data.profiles ?? [];
    setProfiles(loaded);
    setProfileId((current) => {
      if (current && loaded.some((profile) => profile.id === current)) return current;
      return loaded[0]?.id ?? "";
    });
  }

  async function loadSkills(nextProfileId = profileId) {
    const suffix = nextProfileId ? `?runtime_profile_id=${encodeURIComponent(nextProfileId)}` : "";
    const data = await apiGet<{ skills: Skill[] }>(`/api/skills${suffix}`);
    setSkills(data.skills ?? []);
  }

  useEffect(() => {
    (async () => {
      try {
        await loadProfiles();
        setError(null);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, []);

  useEffect(() => {
    (async () => {
      try {
        await loadSkills(profileId);
        setError(null);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
    // Reload skills when the selected profile changes. loadSkills is a component
    // closure that also reads profileId, so listing it would force a refetch on
    // every render; profileId alone is the intended trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [profileId]);

  function startCreate() {
    setForm(emptySkillForm);
    setFormMode("create");
    setImportOpen(false);
  }

  function cancelForm() {
    setForm(emptySkillForm);
    setFormMode("idle");
  }

  async function publishSkill() {
    if (!form.id.trim() || !form.name.trim()) return;
    const targetID = form.storage_id?.trim() || form.id.trim();
    setSaving(true);
    setError(null);
    try {
      await apiPut(`/api/skills/${encodeURIComponent(targetID)}`, {
        name: form.name,
        description: form.description,
        source_provenance: form.source_provenance,
        files: { ...parseExtraFiles(form.extra_files), "SKILL.md": form.instruction },
      });
      setForm(emptySkillForm);
      setFormMode("idle");
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function importSkill() {
    if (!importPackage.trim()) return;
    setSaving(true);
    setError(null);
    try {
      await apiPost("/api/skills/import", {
        source_kind: "npm",
        package: importPackage.trim(),
        ref: importRef.trim(),
      });
      setImportPackage("");
      setImportRef("");
      setImportOpen(false);
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function toggleOptOut(skill: Skill) {
    if (!selectedProfile) return;
    setError(null);
    try {
      const path = `/api/skills/${encodeURIComponent(skill.id)}/profiles/${encodeURIComponent(selectedProfile.id)}/opt-out`;
      if (skill.enabled) {
        await apiPut(path);
      } else {
        await apiDelete(path);
      }
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function editSkill(skill: Skill) {
    setError(null);
    try {
      const loaded = await apiGet<Skill>(`/api/skills/${encodeURIComponent(skill.id)}`);
      const files = loaded.files ?? {};
      const { ["SKILL.md"]: instruction, ...extraFiles } = files;
      setForm({
        id: displaySkillId(loaded),
        storage_id: loaded.id,
        name: displaySkillName(loaded),
        description: displaySkillDescription(loaded),
        instruction: instruction ?? "# " + displaySkillName(loaded),
        extra_files: JSON.stringify(extraFiles, null, 2),
        source_provenance: loaded.source_provenance,
      });
      setFormMode("edit");
      setImportOpen(false);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function deleteSkill(skill: Skill) {
    if (!window.confirm(`Delete skill ${displaySkillName(skill)}?`)) return;
    setError(null);
    try {
      await apiDelete(`/api/skills/${encodeURIComponent(skill.id)}?force_disable=true`);
      if (form.storage_id === skill.id || form.id === displaySkillId(skill)) {
        cancelForm();
      }
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  const enabledCount = useMemo(() => skills.filter((skill) => skill.enabled).length, [skills]);
  const optedOutCount = skills.length - enabledCount;

  const filteredSkills = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return skills.filter((skill) => {
      if (statusFilter === "enabled" && !skill.enabled) return false;
      if (statusFilter === "opted_out" && skill.enabled) return false;
      if (!needle) return true;
      const haystack = [
        displaySkillName(skill),
        displaySkillId(skill),
        displaySkillDescription(skill),
        sourceLabel(skill),
      ]
        .join(" ")
        .toLowerCase();
      return haystack.includes(needle);
    });
  }, [skills, query, statusFilter]);

  const editingId = formMode === "edit" ? form.storage_id : undefined;

  return (
    <SettingsPageShell>
      <SettingsPageHeader
        className="mb-4 shrink-0"
        title="Skills"
        description="Global runtime-agnostic Skill bundles. Skills are default-on unless a profile opts out."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" onClick={() => loadSkills()} aria-label="Refresh skills">
              <RefreshCw className="h-4 w-4" /> Refresh
            </Button>
            <Button onClick={startCreate} aria-label="New skill">
              <Plus className="h-4 w-4" /> New skill
            </Button>
          </div>
        }
      />

      {error && <SettingsAlert className="mb-3 shrink-0">{error}</SettingsAlert>}

      <SettingsSplitLayout data-testid="skills-settings-layout" variant="management" fill>
        <SettingsListColumn data-testid="skills-settings-list">
          <SettingsPanel className="gap-4 lg:shrink-0">
            <div className="flex flex-col gap-3 lg:flex-row lg:items-end lg:justify-between">
              <div className="min-w-0 flex-1">
                <Label htmlFor="skills-runtime-profile">Runtime profile view</Label>
                <Select
                  id="skills-runtime-profile"
                  name="runtime_profile"
                  value={profileId}
                  onChange={(event) => setProfileId(event.target.value)}
                  className="mt-1.5 max-w-md"
                >
                  {profiles.length === 0 && <option value="">All profiles</option>}
                  {profiles.map((profile) => (
                    <option key={profile.id} value={profile.id}>
                      {profile.name} ({profile.provider})
                      {isLaunchResolvedProfile(profile) ? " · launch-resolved" : ""}
                    </option>
                  ))}
                </Select>
                {selectedProfile && isLaunchResolvedProfile(selectedProfile) && (
                  <p className="mt-2 text-xs text-muted-foreground">
                    This profile was created by launch resolution. Skill opt-outs bind to this record and apply to future launches that resolve to the same runtime, model provider, and model override.
                  </p>
                )}
                {selectedProfile && !isLaunchResolvedProfile(selectedProfile) && profileId && (
                  <p className="mt-2 text-xs text-muted-foreground">
                    Skill opt-outs apply to this preset for every task that launches with it.
                  </p>
                )}
              </div>
              <SettingsStatSummary value={enabledCount} unit="enabled" total={skills.length} />
            </div>

            <div className="flex flex-col gap-3 border-t border-border pt-4 sm:flex-row sm:items-center">
              <SettingsSearchField
                id="skills-search"
                name="skills_search"
                value={query}
                onChange={setQuery}
                placeholder="Search name, id, or source…"
                aria-label="Search skills"
              />
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
            </div>
          </SettingsPanel>

          {skills.length === 0 ? (
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
            <SettingsPanel
              className="flex flex-col gap-0 overflow-hidden p-0 lg:min-h-0 lg:flex-1"
              data-testid="skills-library-list"
            >
              <div className="hidden border-b border-border bg-muted/30 px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground sm:grid sm:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_auto_auto] sm:gap-3 lg:shrink-0">
                <span>Skill</span>
                <span>Source</span>
                <span className="w-24 text-center">For profile</span>
                <span className="w-[4.5rem] text-right">Actions</span>
              </div>
              <ul className="divide-y divide-border lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain">
                {filteredSkills.map((skill) => {
                  const name = displaySkillName(skill);
                  const id = displaySkillId(skill);
                  const description = displaySkillDescription(skill);
                  const source = sourceLabel(skill);
                  const selected = editingId === skill.id;
                  return (
                    <li
                      key={skill.id}
                      data-testid={`skill-card-${skill.id}`}
                      className={cn(
                        "rounded-none border-0 bg-transparent px-4 py-3 transition-colors",
                        selected && "bg-accent/50",
                        !skill.enabled && "opacity-80",
                      )}
                    >
                      <div className="grid items-start gap-3 sm:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_auto_auto] sm:items-center">
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-2">
                            <h3 className="truncate font-medium leading-tight">{name}</h3>
                            <Badge variant={skill.enabled ? "success" : "outline"} size="sm">
                              {skill.enabled ? "enabled" : "opted out"}
                            </Badge>
                          </div>
                          <p className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">{id}</p>
                          {description && (
                            <p className="mt-1 line-clamp-2 text-sm text-muted-foreground sm:line-clamp-1">
                              {description}
                            </p>
                          )}
                        </div>

                        <div className="min-w-0">
                          {source ? (
                            <Badge variant="outline" size="sm" className="max-w-full truncate font-normal">
                              {source}
                            </Badge>
                          ) : (
                            <span className="text-xs text-muted-foreground">built-in</span>
                          )}
                        </div>

                        <div className="flex w-full items-center justify-between gap-2 sm:w-24 sm:justify-center">
                          <span className="text-xs text-muted-foreground sm:hidden">
                            {selectedProfile ? selectedProfile.name : "Profile"}
                          </span>
                          {selectedProfile ? (
                            <EnableSwitch
                              enabled={skill.enabled}
                              onClick={() => toggleOptOut(skill)}
                              ariaLabel={
                                skill.enabled
                                  ? `Opt out for ${selectedProfile.name}`
                                  : `Enable for ${selectedProfile.name}`
                              }
                            />
                          ) : (
                            <span className="text-xs text-muted-foreground">—</span>
                          )}
                        </div>

                        <div className="flex w-full items-center justify-end gap-1 sm:w-[4.5rem]">
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
                            onClick={() => deleteSkill(skill)}
                            aria-label={`Delete ${name}`}
                            className="text-muted-foreground hover:text-destructive"
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
                        </div>
                      </div>
                    </li>
                  );
                })}
              </ul>
              {filteredSkills.length !== skills.length && (
                <div className="border-t border-border bg-muted/20 px-4 py-2 text-xs text-muted-foreground">
                  Showing {filteredSkills.length} of {skills.length} skills
                </div>
              )}
            </SettingsPanel>
          )}
        </SettingsListColumn>

        <SettingsListColumn className="gap-4">
          {formMode === "idle" ? (
            <SettingsPanel data-testid="skills-form-panel" className="gap-4 lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain">
              <div>
                <h3 className="font-medium">Library actions</h3>
                <p className="mt-1 text-sm text-muted-foreground">
                  Publish a canonical bundle or import a package. Reusing a Skill ID updates it.
                </p>
              </div>
              <div className="flex flex-col gap-2">
                <Button onClick={startCreate} className="w-full justify-start">
                  <Plus className="h-4 w-4" /> New skill
                </Button>
                <Button
                  variant="outline"
                  className="w-full justify-start"
                  onClick={() => setImportOpen((open) => !open)}
                  aria-expanded={importOpen}
                >
                  <Download className="h-4 w-4" /> Import with npx skills
                </Button>
              </div>
              {importOpen && (
                <ImportForm
                  importPackage={importPackage}
                  importRef={importRef}
                  saving={saving}
                  onPackageChange={setImportPackage}
                  onRefChange={setImportRef}
                  onImport={importSkill}
                />
              )}
            </SettingsPanel>
          ) : (
            <SettingsDetailPane
              data-testid="skills-form-panel"
              className="lg:flex-1"
              header={
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="font-medium">
                      {formMode === "edit" ? "Edit Skill" : "Upload / edit Skill"}
                    </h3>
                    <p className="mt-1 text-sm text-muted-foreground">
                      Publishes a canonical bundle atomically. Reusing a Skill ID updates it.
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={cancelForm}
                    aria-label="Cancel skill form"
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              }
              footer={
                <>
                  <Button onClick={publishSkill} disabled={saving || !form.id.trim() || !form.name.trim()}>
                    Publish Skill
                  </Button>
                  <Button variant="outline" onClick={cancelForm} disabled={saving}>
                    Cancel
                  </Button>
                </>
              }
              bodyClassName="space-y-3"
            >
              <div>
                <Label htmlFor="skill-id">Skill ID</Label>
                <Input
                  id="skill-id"
                  name="skill_id"
                  value={form.id}
                  onChange={(e) =>
                    setForm({
                      ...form,
                      id: e.target.value,
                      storage_id: undefined,
                      source_provenance: undefined,
                    })
                  }
                  placeholder="recon-helper…"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              <div>
                <Label htmlFor="skill-name">Name</Label>
                <Input
                  id="skill-name"
                  name="skill_name"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="Recon Helper…"
                  autoComplete="off"
                />
              </div>
              <div>
                <Label htmlFor="skill-description">Description</Label>
                <Input
                  id="skill-description"
                  name="skill_description"
                  value={form.description}
                  onChange={(e) => setForm({ ...form, description: e.target.value })}
                  autoComplete="off"
                />
              </div>
              <div>
                <Label htmlFor="skill-instruction">SKILL.md</Label>
                <Textarea
                  id="skill-instruction"
                  name="skill_instruction"
                  value={form.instruction}
                  onChange={(e) => setForm({ ...form, instruction: e.target.value })}
                  autoComplete="off"
                  spellCheck={false}
                  size="lg"
                  className="font-mono text-xs leading-relaxed"
                />
              </div>
              <div>
                <Label htmlFor="skill-extra-files">Additional files JSON</Label>
                <Textarea
                  id="skill-extra-files"
                  name="skill_extra_files"
                  value={form.extra_files}
                  onChange={(e) => setForm({ ...form, extra_files: e.target.value })}
                  placeholder={'{"scripts/probe.sh":"#!/bin/sh\\n"}…'}
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-xs"
                />
              </div>
            </SettingsDetailPane>
          )}

          {formMode !== "idle" && (
            <SettingsPanel className="lg:shrink-0">
              <button
                type="button"
                className="flex w-full items-center justify-between text-left"
                onClick={() => setImportOpen((open) => !open)}
                aria-expanded={importOpen}
              >
                <div>
                  <h3 className="font-medium">Import with npx skills</h3>
                  <p className="mt-1 text-sm text-muted-foreground">
                    Structured import only; the daemon never accepts raw shell commands.
                  </p>
                </div>
                <Download className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", importOpen && "text-foreground")} />
              </button>
              {importOpen && (
                <ImportForm
                  importPackage={importPackage}
                  importRef={importRef}
                  saving={saving}
                  onPackageChange={setImportPackage}
                  onRefChange={setImportRef}
                  onImport={importSkill}
                />
              )}
            </SettingsPanel>
          )}
        </SettingsListColumn>
      </SettingsSplitLayout>
    </SettingsPageShell>
  );
}

function ImportForm({
  importPackage,
  importRef,
  saving,
  onPackageChange,
  onRefChange,
  onImport,
}: {
  importPackage: string;
  importRef: string;
  saving: boolean;
  onPackageChange: (value: string) => void;
  onRefChange: (value: string) => void;
  onImport: () => void;
}) {
  return (
    <div className="space-y-3 border-t border-border pt-3">
      <p className="text-xs text-muted-foreground">
        Structured import only; the daemon never accepts raw shell commands from this form.
      </p>
      <div>
        <Label htmlFor="import-package">Package/ref</Label>
        <Input
          id="import-package"
          name="import_package"
          value={importPackage}
          onChange={(e) => onPackageChange(e.target.value)}
          placeholder="@acme/recon-skill…"
          autoComplete="off"
          spellCheck={false}
        />
      </div>
      <div>
        <Label htmlFor="import-ref">Version/ref</Label>
        <Input
          id="import-ref"
          name="import_ref"
          value={importRef}
          onChange={(e) => onRefChange(e.target.value)}
          placeholder="latest…"
          autoComplete="off"
          spellCheck={false}
        />
      </div>
      <Button onClick={onImport} disabled={saving || !importPackage.trim()}>
        <Download className="h-4 w-4" /> Import
      </Button>
    </div>
  );
}

function EnableSwitch({
  enabled,
  onClick,
  ariaLabel,
}: {
  enabled: boolean;
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
      className={cn(
        "relative h-6 w-10 shrink-0 rounded-full border transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
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

function sourceLabel(skill: Skill) {
  const source = skill.source_provenance;
  if (source?.kind === "builtin") return "";
  if (!source || (!source.kind && !source.package && !source.ref)) return "manual";
  if (source.package && source.ref) return `${source.package}@${source.ref}`;
  if (source.package) return source.package;
  return source.kind ?? "manual";
}

function displaySkillId(skill: Skill) {
  if (skill.source_provenance?.kind !== "builtin") return skill.id;
  return skill.id.replace(/^(cyberstrikeai|strix)-/, "");
}

function displaySkillName(skill: Skill) {
  if (skill.source_provenance?.kind !== "builtin") return skill.name;
  return skill.name.replace(/^(cyberstrikeai|strix)-/, "");
}

function displaySkillDescription(skill: Skill) {
  const description = skill.description ?? "";
  if (skill.source_provenance?.kind !== "builtin") return description;
  return stripBuiltinSourcePrefix(description, skill);
}

function stripBuiltinSourcePrefix(value: string, skill: Skill) {
  let next = value.trim();
  for (const prefix of [skill.id, "cyberstrikeai", "strix", "Ed1s0nZ/CyberStrikeAI", "usestrix/strix"]) {
    if (next.toLowerCase().startsWith(prefix.toLowerCase())) {
      next = next.slice(prefix.length).replace(/^[\s:—–-]+/, "").trim();
    }
  }
  return next;
}

function parseExtraFiles(value: string): Record<string, string> {
  const trimmed = value.trim();
  if (!trimmed) return {};
  const parsed = JSON.parse(trimmed) as Record<string, unknown>;
  return Object.fromEntries(
    Object.entries(parsed)
      .filter(([, fileValue]) => typeof fileValue === "string")
      .map(([path, fileValue]) => [path, fileValue as string]),
  );
}
