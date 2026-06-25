import { useEffect, useMemo, useState } from "react";
import { BookOpen, PackagePlus, RefreshCw, Trash2 } from "lucide-react";
import { apiDelete, apiGet, apiPost, apiPut, type RuntimeProfile, type Skill } from "@/lib/api";
import { isLaunchResolvedProfile } from "@/pages/runtimeProfileKind";
import { Badge, Button, Card, Input, Label, Select, Textarea } from "@/components/ui";
import { PageContainer } from "@/components/shared";

type SkillForm = {
  id: string;
  storage_id?: string;
  name: string;
  description: string;
  instruction: string;
  extra_files: string;
  source_provenance?: Skill["source_provenance"];
};

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
  const [importPackage, setImportPackage] = useState("");
  const [importRef, setImportRef] = useState("");
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
  }, [profileId]);

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
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function deleteSkill(skill: Skill) {
    setError(null);
    try {
      await apiDelete(`/api/skills/${encodeURIComponent(skill.id)}?force_disable=true`);
      await loadSkills();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  const enabledCount = useMemo(() => skills.filter((skill) => skill.enabled).length, [skills]);

  return (
    <PageContainer className="max-w-6xl">
      <div className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h2 className="text-xl font-semibold">Skills</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Manage global runtime-agnostic Skill bundles. Skills are default-on for runtime profiles unless a profile opts out.
          </p>
        </div>
        <Button variant="outline" onClick={() => loadSkills()} aria-label="Refresh skills">
          <RefreshCw className="h-4 w-4" /> Refresh
        </Button>
      </div>

      {error && <p className="mb-4 text-sm text-destructive">{error}</p>}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <div className="space-y-4">
          <Card>
            <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
              <div>
                <Label>Runtime profile view</Label>
                <Select value={profileId} onChange={(event) => setProfileId(event.target.value)}>
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
              <div className="text-sm text-muted-foreground">
                {enabledCount} enabled / {skills.length} total
              </div>
            </div>
          </Card>

          {skills.length === 0 ? (
            <Card className="items-center py-10 text-center">
              <BookOpen className="h-8 w-8 text-muted-foreground" />
              <div>
                <p className="font-medium">No Skills yet</p>
                <p className="text-sm text-muted-foreground">Upload a bundle or import one through the controlled importer.</p>
              </div>
            </Card>
          ) : (
            skills.map((skill) => (
              <Card key={skill.id}>
                <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <h3 className="font-medium">{displaySkillName(skill)}</h3>
                      <Badge variant={skill.enabled ? "success" : "outline"}>
                        {skill.enabled ? "enabled" : "opted out"}
                      </Badge>
                    </div>
                    <p className="mt-1 font-mono text-xs text-muted-foreground">{displaySkillId(skill)}</p>
                    {displaySkillDescription(skill) && <p className="mt-2 text-sm text-muted-foreground">{displaySkillDescription(skill)}</p>}
                    {sourceLabel(skill) && (
                      <div className="mt-3 flex flex-wrap gap-2 text-xs">
                        <Badge variant="outline">{sourceLabel(skill)}</Badge>
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0 flex-wrap gap-2">
                    {selectedProfile && (
                    <Button variant="outline" size="sm" onClick={() => toggleOptOut(skill)}>
                      {skill.enabled ? `Opt out for ${selectedProfile.name}` : `Enable for ${selectedProfile.name}`}
                    </Button>
                  )}
                  <Button variant="secondary" size="sm" onClick={() => editSkill(skill)}>
                      Edit {displaySkillName(skill)}
                  </Button>
                    <Button variant="destructive" size="sm" onClick={() => deleteSkill(skill)} aria-label={`Delete ${displaySkillName(skill)}`}>
                      <Trash2 className="h-3.5 w-3.5" /> Delete
                    </Button>
                  </div>
                </div>
              </Card>
            ))
          )}
        </div>

        <div className="space-y-4">
          <Card>
            <div>
              <h3 className="font-medium">Upload / edit Skill</h3>
              <p className="text-sm text-muted-foreground">Publishes a canonical bundle atomically. Reusing a Skill ID updates it.</p>
            </div>
            <div className="space-y-3">
              <div>
                <Label htmlFor="skill-id">Skill ID</Label>
                <Input
                  id="skill-id"
                  value={form.id}
                  onChange={(e) => setForm({ ...form, id: e.target.value, storage_id: undefined, source_provenance: undefined })}
                  placeholder="recon-helper"
                />
              </div>
              <div>
                <Label htmlFor="skill-name">Name</Label>
                <Input id="skill-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Recon Helper" />
              </div>
              <div>
                <Label htmlFor="skill-description">Description</Label>
                <Input id="skill-description" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
              </div>
              <div>
                <Label htmlFor="skill-instruction">SKILL.md</Label>
                <Textarea id="skill-instruction" value={form.instruction} onChange={(e) => setForm({ ...form, instruction: e.target.value })} />
              </div>
              <div>
                <Label htmlFor="skill-extra-files">Additional files JSON</Label>
                <Textarea
                  id="skill-extra-files"
                  value={form.extra_files}
                  onChange={(e) => setForm({ ...form, extra_files: e.target.value })}
                  placeholder={'{"scripts/probe.sh":"#!/bin/sh\\n"}'}
                />
              </div>
              <Button onClick={publishSkill} disabled={saving || !form.id.trim() || !form.name.trim()}>
                Publish Skill
              </Button>
            </div>
          </Card>

          <Card>
            <div>
              <h3 className="font-medium">Import with npx skills</h3>
              <p className="text-sm text-muted-foreground">Structured import only; the daemon never accepts raw shell commands from this form.</p>
            </div>
            <div className="space-y-3">
              <div>
                <Label htmlFor="import-package">Package/ref</Label>
                <Input id="import-package" value={importPackage} onChange={(e) => setImportPackage(e.target.value)} placeholder="@acme/recon-skill" />
              </div>
              <div>
                <Label htmlFor="import-ref">Version/ref</Label>
                <Input id="import-ref" value={importRef} onChange={(e) => setImportRef(e.target.value)} placeholder="latest" />
              </div>
              <Button onClick={importSkill} disabled={saving || !importPackage.trim()}>
                <PackagePlus className="h-4 w-4" /> Import
              </Button>
            </div>
          </Card>
        </div>
      </div>
    </PageContainer>
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
