import { useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Plus, Search, Trash2 } from "lucide-react";
import { apiGet, apiPost, apiPatch, apiDelete, type ModelProvider, type RuntimeExtension, type RuntimePlugin, type RuntimeProfile } from "@/lib/api";
import { ModelProviderMigrationPanel } from "@/pages/ModelProviderMigrationPanel";
import { RuntimeProfileConfig } from "@/pages/RuntimeProfileConfig";
import { buildProfileFields, profileListModelHint } from "@/pages/runtimeProfileForm";
import { cn } from "@/lib/utils";
import { Button, Chip } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SaveActionButton } from "@/components/SaveActionButton";
import { SectionLabel, SettingsAlert, SettingsPageHeader, SettingsPanel, SettingsSplitLayout, SettingsPageShell } from "@/components/shared";
import { SettingsDetailPane, SettingsListColumn, SettingsScrollPanel } from "@/components/settingsLibrary";
import { ProfileEditor } from "./RuntimeProfileEditor";
import { profileToForm, type ProfileForm, emptyForm, fallbackRuntimePlugins, pluginIDs, defaultProvider, pluginLabel } from "./runtimeProfileEditorModel";

function ProfileListButton({
  profile,
  modelProviders,
  selected,
  onSelect,
}: {
  profile: RuntimeProfile;
  modelProviders: ModelProvider[];
  selected: boolean;
  onSelect: () => void;
}) {
  const modelHint = profileListModelHint(profile.fields, modelProviders);
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected ? "true" : undefined}
      className={cn(
        "block w-full rounded-md px-2 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        selected ? "border border-primary/20 bg-primary/[0.03]" : "hover:bg-muted/50",
      )}
    >
      <div className="flex items-center gap-2">
        <span className="truncate text-sm font-medium">{profile.name}</span>
      </div>
      {modelHint && (
        <div className="mt-0.5 truncate text-xs text-muted-foreground">{modelHint}</div>
      )}
    </button>
  );
}

export function RuntimeProfilesPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [plugins, setPlugins] = useState<RuntimePlugin[]>([]);
  const [extensions, setExtensions] = useState<RuntimeExtension[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(() => searchParams.get("profile"));
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [savedNotice, setSavedNotice] = useState(false);
  const savedNoticeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [creating, setCreating] = useState(false);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [form, setForm] = useState<ProfileForm>(emptyForm);
  const [draft, setDraft] = useState<ProfileForm | null>(null);
  const [profileQuery, setProfileQuery] = useState("");

  const selected = profiles.find((p) => p.id === selectedId) ?? null;
  const fallbackPlugins = useMemo(() => fallbackRuntimePlugins(), []);
  const effectivePlugins = plugins.length > 0 ? plugins : fallbackPlugins;
  const providerIds = useMemo(() => {
    const ids = pluginIDs(effectivePlugins);
    if (profiles.some((profile) => !ids.includes(profile.provider))) ids.push("other");
    return ids;
  }, [effectivePlugins, profiles]);

  const grouped = useMemo(() => {
    const buckets = new Map<string, RuntimeProfile[]>();
    for (const provider of providerIds) buckets.set(provider, []);
    for (const profile of profiles) {
      const key = providerIds.includes(profile.provider)
        ? profile.provider
        : "other";
      if (!buckets.has(key)) buckets.set(key, []);
      buckets.get(key)!.push(profile);
    }
    return buckets;
  }, [profiles, providerIds]);

  const normalizedProfileQuery = profileQuery.trim().toLowerCase();
  const filteredGrouped = useMemo(() => {
    if (!normalizedProfileQuery) return grouped;
    const buckets = new Map<string, RuntimeProfile[]>();
    for (const [provider, items] of grouped) {
      buckets.set(
        provider,
        items.filter((profile) =>
          [
            profile.name,
            pluginLabel(effectivePlugins, profile.provider),
            profileListModelHint(profile.fields, modelProviders) ?? "",
          ].some((value) => value.toLowerCase().includes(normalizedProfileQuery)),
        ),
      );
    }
    return buckets;
  }, [grouped, normalizedProfileQuery, effectivePlugins, modelProviders]);
  const visibleProfileCount = useMemo(
    () => Array.from(filteredGrouped.values()).reduce((sum, items) => sum + items.length, 0),
    [filteredGrouped],
  );

  async function load() {
    try {
      const [profileData, pluginData, extensionData] = await Promise.all([
        apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles"),
        apiGet<{ plugins: RuntimePlugin[] }>("/api/runtime-plugins"),
        apiGet<{ extensions: RuntimeExtension[] }>("/api/runtime-extensions"),
      ]);
      void apiGet<{ providers: ModelProvider[] }>("/api/model-providers")
        .then((providerData) => setModelProviders(providerData.providers ?? []))
        .catch(() => setModelProviders([]));
      const loaded = profileData.profiles ?? [];
      setPlugins(pluginData.plugins ?? []);
      setExtensions(extensionData.extensions ?? []);
      setProfiles(loaded);
      setSelectedId((current) => {
        if (current && loaded.some((p) => p.id === current)) return current;
        return loaded[0]?.id ?? null;
      });
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    load();
  }, []);
  /* eslint-enable react-hooks/set-state-in-effect */

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    // intentional synchronous derivation, not a cascading render.
    if (!selected) {
      setDraft(null);
      return;
    }
    setDraft(profileToForm(selected, effectivePlugins));
  }, [selected?.id, selected?.updated_at, effectivePlugins]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  useEffect(() => {
    return () => {
      if (savedNoticeTimer.current) clearTimeout(savedNoticeTimer.current);
    };
  }, []);

  useEffect(() => {
    const current = searchParams.get("profile");
    if ((selectedId ?? "") === (current ?? "")) return;
    const next = new URLSearchParams(searchParams);
    if (selectedId) next.set("profile", selectedId);
    else next.delete("profile");
    setSearchParams(next, { replace: true });
  }, [selectedId, searchParams, setSearchParams]);

  function showSavedNotice() {
    setSavedNotice(true);
    if (savedNoticeTimer.current) clearTimeout(savedNoticeTimer.current);
    savedNoticeTimer.current = setTimeout(() => setSavedNotice(false), 2000);
  }

  async function create() {
    if (saving) return;
    setSaving(true);
    setError(null);
    setSavedNotice(false);
    try {
      const created = await apiPost<RuntimeProfile>("/api/runtime-profiles", {
        name: form.name,
        provider: form.provider,
        fields: buildProfileFields(form, effectivePlugins),
      });
      setForm(emptyForm);
      setCreating(false);
      await load();
      setSelectedId(created.id);
      showSavedNotice();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function remove(id: string) {
    try {
      await apiDelete(`/api/runtime-profiles/${encodeURIComponent(id)}`);
      if (selectedId === id) setSelectedId(null);
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function saveSelected(confirmProviderSwitch = false) {
    if (!selected || !draft || saving) return;
    setSaving(true);
    setError(null);
    setSavedNotice(false);
    try {
      // Authoritative conflict rejection lives in daemon Runtime Profile
      // validation. A 400 leaves the draft (including Custom Args) intact.
      // The Custom Config File is owned by the config editor's import flow,
      // not this form; carry the stored overlay through so a form save never
      // wipes it.
      await apiPatch(`/api/runtime-profiles/${encodeURIComponent(selected.id)}`, {
        name: draft.name,
        provider: draft.provider,
        fields: {
          ...buildProfileFields(draft, effectivePlugins),
          custom_config_file: confirmProviderSwitch ? "" : (selected.fields.custom_config_file ?? ""),
        },
        ...(confirmProviderSwitch ? { confirm_provider_switch_clears_overlay: true } : {}),
      });
      await load();
      showSavedNotice();
    } catch (e) {
      const error = e as { message?: string; status?: number };
      // The daemon answers a provider switch that would drop a non-empty
      // Custom Config File with 409; ask the operator once and retry.
      if (!confirmProviderSwitch && error.status === 409) {
        setConfirmSwitchProviderId(selected.id);
        return;
      }
      setError(error.message ?? String(e));
    } finally {
      setSaving(false);
    }
  }

  const [configProfileId, setConfigProfileId] = useState<string | null>(null);
  const [confirmSwitchProviderId, setConfirmSwitchProviderId] = useState<string | null>(null);
  const hasUnsavedChanges = !!selected && !!draft && JSON.stringify(draft) !== JSON.stringify(profileToForm(selected, effectivePlugins));

  async function configImported() {
    setConfigProfileId(null);
    await load();
    showSavedNotice();
  }

  return (
    <SettingsPageShell data-testid="runtime-profiles-page" className="mx-auto max-w-6xl gap-0 p-0 lg:overflow-hidden lg:p-0">
      <header className="flex-none border-b border-border px-6 py-5 lg:px-8">
        <SettingsPageHeader
          className="mb-0"
        title="Runtime profiles"
        eyebrow="Configuration"
        actions={
          <Button
            size="sm"
            aria-label="New runtime profile"
            onClick={() => {
              setCreating(true);
              setSelectedId(null);
              setForm({ ...emptyForm, provider: defaultProvider(effectivePlugins) });
            }}
          >
            <Plus className="h-4 w-4" /> New profile
          </Button>
        }
      />
      </header>

      {error && <SettingsAlert className="mx-4 my-3 shrink-0 lg:mx-6">{error}</SettingsAlert>}

      <SettingsSplitLayout
        data-testid="runtime-profiles-settings-layout"
        className="gap-0 lg:grid-cols-[320px_minmax(0,1fr)]"
        fill
      >
        <SettingsListColumn className="gap-0 border-r border-border bg-card" data-testid="runtime-profiles-settings-list">
          <SettingsPanel className="gap-2 rounded-none border-0 border-b border-border p-4 shadow-none lg:shrink-0">
            <div className="flex items-center justify-between gap-2">
              <p className="text-[13px] font-medium">Profiles</p>
              <p className="text-[11px] text-muted-foreground tabular-nums">
                {normalizedProfileQuery ? `${visibleProfileCount} of ${profiles.length}` : `${profiles.length} total`}
              </p>
            </div>
            <div className="flex h-8 items-center gap-2 rounded-md border border-input bg-background px-2">
              <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
              <input
                type="search"
                aria-label="Filter runtime profiles"
                placeholder="Search name, provider, model…"
                value={profileQuery}
                onChange={(event) => setProfileQuery(event.target.value)}
                className="w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground"
              />
            </div>
          </SettingsPanel>

          <SettingsScrollPanel className="rounded-none border-0 shadow-none">
            <div className="space-y-3">
              {providerIds.map((provider) => {
                const items = filteredGrouped.get(provider) ?? [];
                if (items.length === 0) return null;
                return (
                  <div key={provider}>
                    <div className="px-2 pt-1 pb-1.5">
                      <SectionLabel>{pluginLabel(effectivePlugins, provider)}</SectionLabel>
                    </div>
                    <div className="space-y-0.5">
                      {items.map((p) => (
                        <ProfileListButton
                          key={p.id}
                          profile={p}
                          modelProviders={modelProviders}
                          selected={selectedId === p.id && !creating}
                          onSelect={() => {
                            setCreating(false);
                            setSelectedId(p.id);
                          }}
                        />
                      ))}
                    </div>
                  </div>
                );
              })}
              {profiles.length === 0 && (
                <p className="px-1 text-sm text-muted-foreground">No profiles yet. Add one to get started.</p>
              )}
              {profiles.length > 0 && visibleProfileCount === 0 && (
                <p className="px-1 text-sm text-muted-foreground">
                  No profiles match &quot;{profileQuery.trim()}&quot;.
                </p>
              )}
            </div>
          </SettingsScrollPanel>
        </SettingsListColumn>

        {creating ? (
          <SettingsDetailPane
            data-testid="runtime-profiles-settings-detail"
            className="rounded-none border-0 shadow-none"
            bodyClassName="px-6 py-5"
            header={
              <div className="min-w-0">
                <h3 className="font-medium">New profile</h3>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  Configure runtime, model provider, MCP, and extensions.
                </p>
              </div>
            }
            footer={
              <>
                <SaveActionButton
                  label="Create"
                  pending={saving}
                  disabled={!form.name.trim()}
                  onClick={() => void create()}
                />
                <Button size="sm" variant="ghost" onClick={() => setCreating(false)}>
                  Cancel
                </Button>
              </>
            }
          >
            <ProfileEditor
              form={form}
              onChange={setForm}
              hideActions
              plugins={effectivePlugins}
              modelProviders={modelProviders}
              extensions={extensions}
            />
          </SettingsDetailPane>
        ) : selected && draft ? (
          <SettingsDetailPane
            data-testid="runtime-profiles-settings-detail"
            className="rounded-none border-0 shadow-none"
            bodyClassName="px-6 py-5"
            header={
              <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="text-base font-semibold">{selected.name}</h2>
                    <Chip variant="neutral">{pluginLabel(effectivePlugins, selected.provider)}</Chip>
                  </div>
                  <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground">{selected.id}</p>
                </div>
                <div className="flex shrink-0 flex-wrap items-center gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    aria-label={`Delete ${selected.name} runtime profile`}
                    onClick={() => setConfirmDeleteId(selected.id)}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                  <SaveActionButton
                    pending={saving}
                    saved={savedNotice}
                    onClick={() => void saveSelected()}
                  />
                </div>
              </div>
            }
          >
            <ModelProviderMigrationPanel
              profileId={selected.id}
              profileUpdatedAt={selected.updated_at}
              onMigrated={load}
              onError={setError}
            />
            <ProfileEditor
              form={draft}
              onChange={setDraft}
              hideActions
              plugins={effectivePlugins}
              modelProviders={modelProviders}
              extensions={extensions}
            />
            <div className="min-w-0 space-y-3">
              <Button size="sm" variant="outline" aria-expanded={configProfileId === selected.id} aria-controls="actual-runtime-config" onClick={() => setConfigProfileId(configProfileId === selected.id ? null : selected.id)}>
                {configProfileId === selected.id ? "Hide actual config" : "View actual config"}
              </Button>
              {configProfileId === selected.id && (
                <RuntimeProfileConfig
                  key={`${selected.id}:${selected.updated_at}`}
                  profileId={selected.id}
                  canEdit={!hasUnsavedChanges && !saving}
                  onImported={configImported}
                />
              )}
            </div>
          </SettingsDetailPane>
        ) : (
          <SettingsDetailPane
            data-testid="runtime-profiles-settings-detail"
            className="rounded-none border-0 shadow-none"
            emptyContent="Select a profile or create a new one."
          />
        )}
      </SettingsSplitLayout>
      <ConfirmDialog
        open={confirmDeleteId !== null}
        title={confirmDeleteId ? `Delete runtime profile ${profiles.find((item) => item.id === confirmDeleteId)?.name ?? confirmDeleteId}?` : "Delete runtime profile?"}
        description="Removes the profile and its MCP, skills, and extension configuration."
        confirmLabel="Delete"
        destructive
        onConfirm={() => {
          const id = confirmDeleteId;
          setConfirmDeleteId(null);
          if (id) void remove(id);
        }}
        onCancel={() => setConfirmDeleteId(null)}
      />
      <ConfirmDialog
        open={confirmSwitchProviderId !== null}
        title="Switch runtime provider?"
        description="This profile has a non-empty Custom Config File. Switching the runtime provider clears it because the overlay format is provider-specific."
        confirmLabel="Switch and clear"
        destructive
        onConfirm={() => {
          setConfirmSwitchProviderId(null);
          void saveSelected(true);
        }}
        onCancel={() => setConfirmSwitchProviderId(null)}
      />
    </SettingsPageShell>
  );
}
