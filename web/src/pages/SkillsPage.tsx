import { useEffect, useMemo, useRef, useState } from "react";
import { Plus, RefreshCw } from "lucide-react";
import { apiDelete, apiGet, apiPost, apiPostForm, apiPut, type RuntimeProfile, type Skill } from "@/lib/api";
import { Button, Label, Select } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  SettingsAlert,
  SettingsPageHeader,
  SettingsSplitLayout,
  SettingsPageShell,
} from "@/components/shared";
import { SettingsListColumn } from "@/components/settingsLibrary";
import { SkillBulkActions, type DisableAllTarget } from "./SkillBulkActions";
import { SkillEditorPanel, type FormMode, type SkillForm } from "./SkillEditorPanel";
import { SkillsLibraryList, type StatusFilter } from "./SkillsLibraryList";
import {
  displaySkillDescription,
  displaySkillId,
  displaySkillName,
  profileSkillEnabled,
  sourceLabel,
} from "./skillDisplay";

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

  const skillsRequest = useRef(0);

  async function loadSkills(nextProfileId = profileId) {
    const request = ++skillsRequest.current;
    setSkillsLoading(true);
    try {
      const suffix = nextProfileId ? `?runtime_profile_id=${encodeURIComponent(nextProfileId)}` : "";
      const data = await apiGet<{ skills: Skill[] }>(`/api/skills${suffix}`);
      if (request === skillsRequest.current) setSkills(data.skills ?? []);
    } catch (cause) {
      if (request === skillsRequest.current) throw cause;
    } finally {
      if (request === skillsRequest.current) setSkillsLoading(false);
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

          <SkillBulkActions
            selectedProfile={selectedProfile}
            skillCount={skills.length}
            globalEnabledCount={globalEnabledCount}
            globalOptedOutCount={globalOptedOutCount}
            profileEnabledCount={profileEnabledCount}
            profileOptedOutCount={profileOptedOutCount}
            bulkUpdating={bulkUpdating}
            skillsLoading={skillsLoading}
            setAllGlobalSkillsOptOut={setAllGlobalSkillsOptOut}
            setAllSkillsOptOut={setAllSkillsOptOut}
            setConfirmDisableAllTarget={setConfirmDisableAllTarget}
          />

          <SkillsLibraryList
            skills={skills}
            filteredSkills={filteredSkills}
            selectedProfile={selectedProfile}
            editingId={editingId}
            enabledCount={enabledCount}
            optedOutCount={optedOutCount}
            statusFilter={statusFilter}
            query={query}
            skillsLoading={skillsLoading}
            setStatusFilter={setStatusFilter}
            setQuery={setQuery}
            startCreate={startCreate}
            setImportOpen={setImportOpen}
            toggleGlobalOptOut={toggleGlobalOptOut}
            toggleOptOut={toggleOptOut}
            editSkill={editSkill}
            setConfirmDeleteSkill={setConfirmDeleteSkill}
          />
        </SettingsListColumn>

        <SkillEditorPanel
          form={form}
          formMode={formMode}
          archiveFile={archiveFile}
          importOpen={importOpen}
          importPackage={importPackage}
          saving={saving}
          cancelForm={cancelForm}
          importArchive={importArchive}
          importSkill={importSkill}
          publishSkill={publishSkill}
          setArchiveFile={setArchiveFile}
          setForm={setForm}
          setImportPackage={setImportPackage}
          toggleImportOpen={() => setImportOpen((open) => !open)}
          startCreate={startCreate}
        />
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
