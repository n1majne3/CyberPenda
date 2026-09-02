import { useEffect, useMemo, useState } from "react";
import {
  Ban,
  BookOpen,
  CheckCheck,
  Download,
  LoaderCircle,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { apiDelete, apiGet, apiPost, apiPostForm, apiPut, type RuntimeProfile, type Skill } from "@/lib/api";
import { Button, Input, Label, Select, Textarea, buttonVariants } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  SettingsAlert,
  SettingsPageHeader,
  SettingsPanel,
  SettingsSplitLayout,
  SettingsPageShell,
} from "@/components/shared";
import {
  SettingsListColumn,
  SettingsSearchField,
  SettingsSegmentedFilter,
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
type DisableAllTarget =
  | { scope: "global" }
  | { scope: "profile"; profile: RuntimeProfile };

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
  const [archiveFile, setArchiveFile] = useState<File | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [confirmDeleteSkill, setConfirmDeleteSkill] = useState<Skill | null>(null);
  const [confirmDisableAllTarget, setConfirmDisableAllTarget] = useState<DisableAllTarget | null>(null);
  const [bulkUpdating, setBulkUpdating] = useState(false);
  const [skillsLoading, setSkillsLoading] = useState(true);

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
    setSkillsLoading(true);
    try {
      const suffix = nextProfileId ? `?runtime_profile_id=${encodeURIComponent(nextProfileId)}` : "";
      const data = await apiGet<{ skills: Skill[] }>(`/api/skills${suffix}`);
      setSkills(data.skills ?? []);
    } finally {
      setSkillsLoading(false);
    }
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
    const source = importPackage.trim();
    if (!source) return;
    if (!/^https?:\/\//i.test(source) && !/^[\w.-]+\/[\w.-]+(@[\w.-]+)?$/.test(source)) {
      setError("Enter a skill URL (https://…) or an owner/repo shorthand.");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await apiPost("/api/skills/import", {
        source_kind: "auto",
        source_url: source,
      });
      setImportPackage("");
      setImportOpen(false);
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function importArchive() {
    if (!archiveFile) return;
    setSaving(true);
    setError(null);
    try {
      const form = new FormData();
      form.append("archive", archiveFile);
      await apiPostForm("/api/skills/import", form);
      setArchiveFile(null);
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function toggleOptOut(skill: Skill) {
    if (!selectedProfile || skill.globally_opted_out) return;
    setError(null);
    try {
      const path = `/api/skills/${encodeURIComponent(skill.id)}/profiles/${encodeURIComponent(selectedProfile.id)}/opt-out`;
      if (profileSkillEnabled(skill)) {
        await apiPut(path);
      } else {
        await apiDelete(path);
      }
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function toggleGlobalOptOut(skill: Skill) {
    setError(null);
    try {
      const path = `/api/skills/${encodeURIComponent(skill.id)}/opt-out`;
      if (skill.globally_opted_out) {
        await apiDelete(path);
      } else {
        await apiPut(path);
      }
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function setAllSkillsOptOut(profile: RuntimeProfile, optedOut: boolean) {
    setBulkUpdating(true);
    setError(null);
    try {
      const path = `/api/skills/profiles/${encodeURIComponent(profile.id)}/opt-out`;
      if (optedOut) {
        await apiPut(path);
      } else {
        await apiDelete(path);
      }
      await loadSkills(profile.id);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBulkUpdating(false);
    }
  }

  async function setAllGlobalSkillsOptOut(optedOut: boolean) {
    setBulkUpdating(true);
    setError(null);
    try {
      const path = "/api/skills/opt-outs/global";
      if (optedOut) {
        await apiPut(path);
      } else {
        await apiDelete(path);
      }
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBulkUpdating(false);
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
  const globalEnabledCount = useMemo(
    () => skills.filter((skill) => !skill.globally_opted_out).length,
    [skills],
  );
  const globalOptedOutCount = skills.length - globalEnabledCount;
  const profileEnabledCount = useMemo(
    () => skills.filter((skill) => profileSkillEnabled(skill)).length,
    [skills],
  );
  const profileOptedOutCount = skills.length - profileEnabledCount;

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
        eyebrow="Library"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              onClick={() => {
                void loadSkills().catch((reason) => setError((reason as Error).message));
              }}
              aria-label="Refresh skills"
            >
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
          <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card p-4 shadow-sm lg:shrink-0">
            <div className="min-w-0">
              <Label htmlFor="skills-runtime-profile" className="text-xs font-medium">
                Runtime profile view
              </Label>
              <p className="mt-0.5 text-xs text-muted-foreground">
                Global Skill Opt-Outs affect direct launches and every Runtime Profile.
                {selectedProfile && profileId
                  ? " Profile Skill Opt-Outs apply only when this Runtime Profile is selected for a new Task or Session."
                  : ""}
              </p>
            </div>
            <Select
              id="skills-runtime-profile"
              name="runtime_profile"
              value={profileId}
              onChange={(event) => setProfileId(event.target.value)}
              className="w-[200px] flex-none"
            >
              {profiles.length === 0 && <option value="">All profiles</option>}
              {profiles.map((profile) => (
                <option key={profile.id} value={profile.id}>
                  {profile.name} ({profile.provider})
                </option>
              ))}
            </Select>
          </div>

          <div className="grid gap-2 rounded-lg border border-border bg-card p-3 shadow-sm sm:grid-cols-2 lg:shrink-0">
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
                  {globalEnabledCount}/{skills.length} enabled
                </span>
              </div>
              <div className="mt-3 flex gap-2">
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
                    ? `${profileEnabledCount}/${skills.length} allowed`
                    : "Unavailable"}
                </span>
              </div>
              <div className="mt-3 flex gap-2">
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
        </SettingsListColumn>

        <aside
          data-testid="skills-form-panel"
          className="flex min-w-0 flex-col gap-4 lg:min-h-0 lg:overflow-y-auto lg:overscroll-contain"
        >
          <div className="rounded-lg border border-border bg-card p-4 shadow-sm">
            <div className="flex items-center justify-between gap-3">
              <h3 className="text-sm font-medium">
                {formMode === "edit" ? "Edit Skill" : "Upload / edit Skill"}
              </h3>
              {formMode !== "idle" && (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={cancelForm}
                  aria-label="Cancel skill form"
                >
                  <X className="h-3.5 w-3.5" />
                </Button>
              )}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              Publish one canonical bundle atomically. Reusing a Skill ID updates it.
            </p>

            {formMode === "idle" ? (
              <div className="mt-4 space-y-3">
                <div className="space-y-3 rounded-md border border-border bg-muted/20 p-3">
                  <div>
                    <Label>Skill bundle archive</Label>
                    <div className="mt-1.5 flex h-9 min-w-0 items-center gap-2 rounded-lg border border-input bg-background px-1.5">
                      <input
                        id="skill-bundle-archive"
                        name="skill_bundle_archive"
                        type="file"
                        accept=".zip,.tar,.tar.gz,.tgz,application/zip,application/x-tar,application/gzip"
                        aria-label="Skill bundle archive"
                        onChange={(event) => setArchiveFile(event.target.files?.[0] ?? null)}
                        className="peer sr-only"
                      />
                      <label
                        htmlFor="skill-bundle-archive"
                        className={cn(
                          buttonVariants({ variant: "outline", size: "sm" }),
                          "h-7 shrink-0 cursor-pointer px-2.5 text-xs peer-focus-visible:ring-2 peer-focus-visible:ring-ring peer-focus-visible:ring-offset-2",
                        )}
                      >
                        Choose file
                      </label>
                      <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                        {archiveFile?.name ?? "No file selected"}
                      </span>
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">
                      Upload one .zip, .tar, .tar.gz, or .tgz bundle containing SKILL.md and optional scripts.
                    </p>
                  </div>
                  <Button
                    variant="outline"
                    onClick={importArchive}
                    disabled={saving || !archiveFile}
                    className="w-full"
                  >
                    <Download className="h-4 w-4" /> Upload archive
                  </Button>
                </div>
                <Button onClick={startCreate} className="w-full justify-start">
                  <Plus className="h-4 w-4" /> New skill
                </Button>
              </div>
            ) : (
              <>
                <div className="mt-4 space-y-3">
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
                </div>
                <div className="mt-4 flex gap-2">
                  <Button
                    onClick={publishSkill}
                    disabled={saving || !form.id.trim() || !form.name.trim()}
                    className="flex-1"
                  >
                    Publish Skill
                  </Button>
                  <Button variant="outline" onClick={cancelForm} disabled={saving}>
                    Cancel
                  </Button>
                </div>
              </>
            )}
          </div>

          <div className="rounded-lg border border-dashed border-border p-4">
            <button
              type="button"
              className="flex w-full items-center gap-2 text-left text-sm font-medium"
              onClick={() => setImportOpen((open) => !open)}
              aria-expanded={importOpen}
            >
              <Download className="h-4 w-4" /> Import skill from URL or repo
            </button>
            <p className="mt-1.5 text-xs text-muted-foreground">
              Structured imports let the daemon parse the source itself and never accept raw shell commands.
            </p>
            {importOpen && (
              <ImportForm
                importPackage={importPackage}
                saving={saving}
                onPackageChange={setImportPackage}
                onImport={importSkill}
              />
            )}
          </div>
        </aside>
      </SettingsSplitLayout>
      <ConfirmDialog
        open={confirmDisableAllTarget !== null}
        title={
          confirmDisableAllTarget?.scope === "global"
            ? "Disable all skills globally?"
            : confirmDisableAllTarget?.scope === "profile"
              ? `Disable all skills for ${confirmDisableAllTarget.profile.name}?`
            : "Disable all skills?"
        }
        description={
          confirmDisableAllTarget?.scope === "global"
            ? "This adds Global Skill Opt-Outs for all current Skills. Direct launches and every Runtime Profile stop receiving them. Started Runtime Owners do not change, and future imported Skills remain default-on."
            : "This adds Profile Skill Opt-Outs for all current Skills in this Runtime Profile. Started Runtime Owners do not change, and future imported Skills remain default-on."
        }
        confirmLabel={confirmDisableAllTarget?.scope === "global" ? "Disable globally" : "Disable for profile"}
        destructive
        onConfirm={() => {
          const target = confirmDisableAllTarget;
          setConfirmDisableAllTarget(null);
          if (target?.scope === "global") {
            void setAllGlobalSkillsOptOut(true);
          } else if (target?.scope === "profile") {
            void setAllSkillsOptOut(target.profile, true);
          }
        }}
        onCancel={() => setConfirmDisableAllTarget(null)}
      />
      <ConfirmDialog
        open={confirmDeleteSkill !== null}
        title={confirmDeleteSkill ? `Delete skill ${displaySkillName(confirmDeleteSkill)}?` : "Delete skill?"}
        description="The Skill is removed from the global library and future launches. Started Runtime Owners do not change."
        confirmLabel="Delete"
        destructive
        onConfirm={() => {
          const skill = confirmDeleteSkill;
          setConfirmDeleteSkill(null);
          if (skill) void deleteSkill(skill);
        }}
        onCancel={() => setConfirmDeleteSkill(null)}
      />
    </SettingsPageShell>
  );
}

function ImportForm({
  importPackage,
  saving,
  onPackageChange,
  onImport,
}: {
  importPackage: string;
  saving: boolean;
  onPackageChange: (value: string) => void;
  onImport: () => void;
}) {
  return (
    <div className="space-y-3 border-t border-border pt-3">
      <p className="text-xs text-muted-foreground">
        Structured import only; the daemon resolves the source itself and never accepts raw shell commands from this form.
      </p>
      <div>
        <Label htmlFor="import-package">Skill source</Label>
        <Input
          id="import-package"
          name="import_package"
          value={importPackage}
          onChange={(e) => onPackageChange(e.target.value)}
          placeholder="https://example.com/skills/@acme/skill  •  owner/repo  •  github.com/owner/repo"
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

function profileSkillEnabled(skill: Skill) {
  if (typeof skill.profile_opted_out === "boolean") return !skill.profile_opted_out;
  return skill.enabled;
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
