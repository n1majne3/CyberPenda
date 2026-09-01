import { useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { CheckCircle2, Plus, Search, Trash2 } from "lucide-react";
import { apiGet, apiPost, apiPatch, apiDelete, mergedConfigPreview, projectedConfig, type ModelProvider, type RuntimeExtension, type RuntimeExtensionCatalogItem, type RuntimePlugin, type RuntimeProfile } from "@/lib/api";
import { ModelProviderMigrationPanel } from "@/pages/ModelProviderMigrationPanel";
import { codexMultiAgentTOMLLines, enrichPreviewWithModelProvider } from "@/pages/runtimeProfilePreview";
import {
  applyModelProviderSelection,
  buildProfileFields,
  displayReasoningEffort,
  REASONING_EFFORT_VALUES,
  compatibleProtocolsForRuntime,
  isModelProviderCompatibleWithRuntime,
  modelProviderSupportedProtocols,
  profileListModelHint,
  selectableModelProviders,
  showLegacyModelFields,
} from "@/pages/runtimeProfileForm";
import { cn } from "@/lib/utils";
import { Button, Input, Label, Badge, Chip, Textarea, Select, Card, CardTitle } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SaveActionButton } from "@/components/SaveActionButton";
import {
  SectionLabel,
  SettingsAlert,
  SettingsPageHeader,
  SettingsPanel,
  SettingsSplitLayout,
  SettingsPageShell,
} from "@/components/shared";
import {
  SettingsDetailPane,
  SettingsListColumn,
  SettingsScrollPanel,
} from "@/components/settingsLibrary";

const FALLBACK_PROVIDER_IDS = ["codex", "claude_code", "pi", "hermes", "fake"] as const;
// HIDDEN_PROVIDER_IDS are real, registered providers that should not be
// selectable when creating a profile (e.g. the in-process fake harness used
// for tests). Profiles already using one are still displayed and editable.
const HIDDEN_PROVIDER_IDS = new Set(["fake"]);
const RUNNERS = ["sandbox", "host"] as const;

const PROVIDER_LABELS: Record<string, string> = {
  codex: "Codex",
  claude_code: "Claude Code",
  pi: "Pi",
  fake: "Fake harness",
};

const DEFAULT_API_KEY_ENV: Record<string, string> = {
  codex: "OPENAI_API_KEY",
  claude_code: "ANTHROPIC_AUTH_TOKEN",
  pi: "ANTHROPIC_API_KEY",
};

// The daemon redacts stored API keys to this sentinel in profile payloads;
// runtimeProfileForm.ts carries the same constant for save-side handling.
const API_KEY_CONFIGURED = "[configured]";

const DEFAULT_DAEMON_MCP_PORT = "8787";

let runtimeExtensionCatalogRequest: Promise<{ items: RuntimeExtensionCatalogItem[] }> | null = null;

function loadRuntimeExtensionCatalog() {
  if (!runtimeExtensionCatalogRequest) {
    runtimeExtensionCatalogRequest = apiGet<{ items: RuntimeExtensionCatalogItem[] }>("/api/runtime-extension-catalog").catch((error) => {
      runtimeExtensionCatalogRequest = null;
      throw error;
    });
  }
  return runtimeExtensionCatalogRequest;
}

type RuntimeProfileFields = RuntimeProfile["fields"];
type RuntimeExtensionFormRef = {
  id: string;
  enabled: boolean;
  config: string;
};

type ProfileForm = {
  name: string;
  provider: string;
  binary_path: string;
  model: string;
  endpoint: string;
  model_provider_id: string;
  model_provider_protocol: string;
  model_override: string;
  reasoning_effort: string;
  custom_args: string;
  env: string;
  api_key_env: string;
  api_key: string;
  runtime_extensions: RuntimeExtensionFormRef[];
  mcp_servers: string;
  default_runner: string;
  sandbox_image: string;
  credential_refs: string;
  codex_multi_agent_state: "inherit" | "on" | "off";
  codex_multi_agent_max_threads: string;
  codex_multi_agent_max_depth: string;
};

const emptyForm: ProfileForm = {
  name: "",
  provider: "codex",
  binary_path: "",
  model: "",
  endpoint: "",
  model_provider_id: "",
  model_provider_protocol: "",
  model_override: "",
  reasoning_effort: "high",
  custom_args: "",
  env: "",
  api_key_env: "",
  api_key: "",
  runtime_extensions: [],
  mcp_servers: "",
  default_runner: "sandbox",
  sandbox_image: "",
  credential_refs: "",
  codex_multi_agent_state: "inherit",
  codex_multi_agent_max_threads: "",
  codex_multi_agent_max_depth: "",
};

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
  const [extensionCatalog, setExtensionCatalog] = useState<RuntimeExtensionCatalogItem[]>([]);
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
      void loadRuntimeExtensionCatalog()
        .then((catalogData) => setExtensionCatalog(catalogData.items ?? []))
        .catch(() => setExtensionCatalog([]));
    } catch (e) {
      setError((e as Error).message);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    // Initial load on mount. load() is reused by event handlers.
    load();
  }, []);
  /* eslint-enable react-hooks/set-state-in-effect */

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    // Sync the editor draft to the selected profile (or clear it). This is an
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
      // Custom Args conflicts are rejected by Runtime Profile validation on the
      // daemon; the UI surfaces that 400 without stripping or rewriting draft values.
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
      await apiDelete(`/api/runtime-profiles/${id}`);
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
      await apiPatch(`/api/runtime-profiles/${selected.id}`, {
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

  const previewConfig = selected
    ? JSON.stringify(
        buildGeneratedConfigPreview(
          draft?.provider ?? selected.provider,
          buildProfileFields(draft ?? profileToForm(selected, effectivePlugins), effectivePlugins),
          draft ?? profileToForm(selected, effectivePlugins),
          pluginFor(effectivePlugins, draft?.provider ?? selected.provider),
          modelProviders,
        ),
        null,
        2
      )
    : "";

  const [configEditorOpen, setConfigEditorOpen] = useState(false);
  const [configDraft, setConfigDraft] = useState("");
  const [configImporting, setConfigImporting] = useState(false);
  const [configImportKeys, setConfigImportKeys] = useState<{ key: string; field?: string; message: string }[]>([]);
  const [confirmSwitchProviderId, setConfirmSwitchProviderId] = useState<string | null>(null);
  const [mergedPreview, setMergedPreview] = useState<{ id: string; text: string } | null>(null);
  const activeProfileId = selected?.id;
  const activeProfileUpdatedAt = selected?.updated_at;
  const activeProfileOverlay = selected?.fields.custom_config_file;
  // Only the preview fetched for the currently selected profile is shown, so
  // switching profiles never flashes the previous profile's merged config.
  const mergedPreviewText =
    mergedPreview && activeProfileId && mergedPreview.id === activeProfileId ? mergedPreview.text : "";

  // Show the final merged result (structured + Custom Config File
  // overlay) exactly as projection will produce it.
  useEffect(() => {
    if (!activeProfileId) return;
    let cancelled = false;
    mergedConfigPreview(activeProfileId)
      .then((payload) => {
        if (!cancelled) setMergedPreview({ id: activeProfileId, text: JSON.stringify(payload?.merged ?? {}, null, 2) });
      })
      .catch(() => {
        if (!cancelled) setMergedPreview({ id: activeProfileId, text: "" });
      });
    return () => {
      cancelled = true;
    };
  }, [activeProfileId, activeProfileUpdatedAt, activeProfileOverlay]);

  async function openConfigEditor() {
    if (!selected) return;
    setConfigImportKeys([]);
    setConfigEditorOpen(true);
    // The editor opens on the complete provider-native projected
    // file (redacted), never a preview envelope; fall back to the local
    // preview only if the endpoint is unavailable.
    setConfigDraft(previewConfig);
    try {
      const payload = await projectedConfig(selected.id);
      if (payload?.text) setConfigDraft(payload.text);
    } catch {
      // keep the local preview fallback
    }
  }

  async function importConfigText() {
    if (!selected) return;
    setConfigImporting(true);
    setConfigImportKeys([]);
    try {
      await apiPost<{
        profile: RuntimeProfile;
        mapped_keys: string[];
      }>(`/api/runtime-profiles/${selected.id}/import-config`, {
        config_text: configDraft,
      });
      await load();
      setConfigEditorOpen(false);
      showSavedNotice();
    } catch (e) {
      setError((e as Error).message);
      // Per-key detail travels on the ApiError body, not the message.
      const body = (e as { body?: unknown }).body as { keys?: { key: string; field?: string; message: string }[] } | undefined;
      if (body?.keys?.length) setConfigImportKeys(body.keys);
    } finally {
      setConfigImporting(false);
    }
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
              extensionCatalog={extensionCatalog}
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
              extensionCatalog={extensionCatalog}
            />
            <div className="min-w-0">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-sm font-medium leading-none text-muted-foreground">Generated config preview</p>
                <Button size="sm" variant="outline" onClick={openConfigEditor}>
                  Edit config
                </Button>
              </div>
              <pre className="mt-1 max-h-64 w-full max-w-full overflow-x-auto rounded-md border border-border bg-muted/30 p-3 text-xs">
                {previewConfig}
              </pre>
              {selected.fields.custom_config_file?.trim() ? (
                <div className="mt-2 min-w-0">
                  <p className="text-sm font-medium leading-none text-muted-foreground">
                    Custom config file (remainder, deep-merged on projection)
                  </p>
                  <pre className="mt-1 max-h-40 w-full max-w-full overflow-x-auto rounded-md border border-border bg-muted/30 p-3 text-xs">
                    {selected.fields.custom_config_file}
                  </pre>
                </div>
              ) : null}
              {mergedPreviewText.trim() ? (
                <div className="mt-2 min-w-0">
                  <p className="text-sm font-medium leading-none text-muted-foreground">
                    Final merged config (structured + custom config file)
                  </p>
                  <pre data-testid="merged-config-preview" className="mt-1 max-h-64 w-full max-w-full overflow-x-auto rounded-md border border-border bg-muted/30 p-3 text-xs">
                    {mergedPreviewText}
                  </pre>
                </div>
              ) : null}
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
      {configEditorOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
          <Card className="flex max-h-[90vh] w-full max-w-3xl flex-col gap-3 overflow-hidden p-4">
            <CardTitle className="text-base">Edit runtime config</CardTitle>
            <p className="text-sm text-muted-foreground">
              Edit the generated config for {selected?.name}. On import, keys the structured fields express map back to
              them; Managed Config Keys and secret-shaped values are refused; the remainder is stored as the Custom
              Config File.
            </p>
            <label className="sr-only" htmlFor="runtime-config-editor">
              Runtime config editor
            </label>
            <textarea
              id="runtime-config-editor"
              aria-label="Runtime config editor"
              className="min-h-[40vh] w-full resize-y rounded-md border border-border bg-muted/30 p-3 font-mono text-xs"
              value={configDraft}
              onChange={(event) => setConfigDraft(event.target.value)}
              spellCheck={false}
            />
            {configImportKeys.length > 0 && (
              <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm">
                <p className="font-medium">Import refused — fix these keys and retry:</p>
                <ul className="mt-1 list-disc space-y-1 pl-5">
                  {configImportKeys.map((keyError) => (
                    <li key={keyError.key}>
                      <span className="font-mono">{keyError.key}</span>: {keyError.message}
                      {keyError.field ? <span className="text-muted-foreground"> (owned by the {keyError.field} field)</span> : null}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setConfigEditorOpen(false)}>
                Cancel
              </Button>
              <Button disabled={configImporting} onClick={() => void importConfigText()}>
                {configImporting ? "Importing…" : "Import config"}
              </Button>
            </div>
          </Card>
        </div>
      )}
    </SettingsPageShell>
  );
}

function fallbackRuntimePlugins(): RuntimePlugin[] {
  return FALLBACK_PROVIDER_IDS.map((id) => ({
    schema_version: 1,
    id,
    name: PROVIDER_LABELS[id] ?? id,
    binary: { default: id === "claude_code" ? "claude" : id === "fake" ? "fake" : id },
    capabilities: {
      sandbox: true,
      host: true,
      mcp_config: id !== "fake",
      streaming_transcript: id !== "fake",
      resume: true,
    },
    profile_schema: {
      fields: [
        "binary_path",
        "model",
        "endpoint",
        "custom_args",
        "env",
        "api_keys",
        "credential_refs",
        "runtime_extensions",
        "mcp_servers",
        "default_runner",
        "sandbox_image",
        ...(id === "codex" ? ["codex_multi_agent"] : []),
      ].map((name) => ({
        name,
        type: name === "codex_multi_agent" ? "codex_multi_agent" : "string",
        label: name,
      })),
    },
    config_projection:
      id === "claude_code"
        ? { primitive: "claude_settings", config_path: "runtime-home/claude/settings.json", mcp_config_path: "workdir/.mcp.json" }
        : id === "codex"
          ? { primitive: "codex_home", config_path: "runtime-home/codex/config.toml" }
          : id === "pi"
            ? { primitive: "pi_agent", config_path: "runtime-home/pi/agent/models.json", mcp_config_path: "runtime-home/pi/agent/mcp.json" }
            : { primitive: "none" },
    launch: { args: [] },
    process_env: fallbackProcessEnv(id),
    credential_env: DEFAULT_API_KEY_ENV[id] ? [DEFAULT_API_KEY_ENV[id]] : [],
    transcript: { parser: fallbackTranscriptParser(id) },
  }));
}

function fallbackProcessEnv(provider: string): Record<string, string> | undefined {
  if (provider === "claude_code") return { CLAUDE_HOME: "{{runtime_home}}/claude" };
  if (provider === "codex") return { CODEX_HOME: "{{runtime_home}}/codex" };
  if (provider === "pi") return { PI_CODING_AGENT_DIR: "{{runtime_home}}/pi/agent" };
  return undefined;
}

function fallbackTranscriptParser(provider: string): string {
  if (provider === "claude_code") return "claude_stream_json";
  if (provider === "codex") return "codex_json";
  if (provider === "pi") return "pi_json_session";
  return "plain_runtime_output";
}

function pluginIDs(plugins: RuntimePlugin[]): string[] {
  const ids = plugins.map((plugin) => plugin.id);
  return ids.length > 0 ? ids : [...FALLBACK_PROVIDER_IDS];
}

function pluginFor(plugins: RuntimePlugin[], provider: string): RuntimePlugin | undefined {
  return plugins.find((plugin) => plugin.id === provider);
}

function modelProviderModels(provider: ModelProvider) {
  return Array.from(new Set([...(provider.catalog?.manual ?? []), ...(provider.catalog?.refreshed ?? [])])).sort();
}

// defaultProvider returns the first selectable (non-hidden) plugin id, so that
// creating a new profile never defaults to a hidden provider like the fake
// harness. Falls back to the first plugin or "codex" when none qualify.
function defaultProvider(plugins: RuntimePlugin[]): string {
  const first = plugins.find((plugin) => !HIDDEN_PROVIDER_IDS.has(plugin.id));
  return first?.id ?? plugins[0]?.id ?? "codex";
}

function pluginLabel(plugins: RuntimePlugin[], provider: string): string {
  return pluginFor(plugins, provider)?.name || PROVIDER_LABELS[provider] || provider;
}

function pluginHasField(plugin: RuntimePlugin | undefined, field: string): boolean {
  if (!plugin) return true;
  return plugin.profile_schema.fields.some((item) => item.name === field);
}

function defaultAPIKeyEnv(provider: string, plugins: RuntimePlugin[]): string | undefined {
  return pluginFor(plugins, provider)?.credential_env?.[0] || DEFAULT_API_KEY_ENV[provider];
}

function ProfileEditor({
  title,
  form,
  onChange,
  onSave,
  onCancel,
  saveLabel = "Save",
  saveDisabled,
  savePending,
  hideActions,
  plugins,
  modelProviders,
  extensions,
  extensionCatalog,
}: {
  title?: string;
  form: ProfileForm;
  onChange: (form: ProfileForm) => void;
  onSave?: () => void;
  onCancel?: () => void;
  saveLabel?: string;
  saveDisabled?: boolean;
  savePending?: boolean;
  hideActions?: boolean;
  plugins: RuntimePlugin[];
  modelProviders: ModelProvider[];
  extensions: RuntimeExtension[];
  extensionCatalog: RuntimeExtensionCatalogItem[];
}) {
  const [extensionToAdd, setExtensionToAdd] = useState("");
  const [catalogItemToAdd, setCatalogItemToAdd] = useState("");
  const [manualExtensionID, setManualExtensionID] = useState("");
  const plugin = pluginFor(plugins, form.provider);
  const selectableProviders = selectableModelProviders(modelProviders, plugin, form.model_provider_id);
  const selectedModelProvider = modelProviders.find((provider) => provider.id === form.model_provider_id);
  const compatibleProtocols = selectedModelProvider ? compatibleProtocolsForRuntime(plugin, selectedModelProvider) : [];
  const providerModels = selectedModelProvider ? modelProviderModels(selectedModelProvider) : [];
  const providerOptions = (plugin
    ? plugins
    : [
        ...plugins,
        {
          schema_version: 1,
          id: form.provider,
          name: form.provider,
          binary: { default: form.provider },
          capabilities: { sandbox: true, host: true, mcp_config: false, streaming_transcript: false, resume: false },
          profile_schema: { fields: [] },
          config_projection: { primitive: "generic_config" },
          launch: { args: ["{{binary}}", "{{goal}}"] },
          transcript: { parser: "plain_runtime_output" },
        },
      ]
  ).filter((p) => p.id === form.provider || !HIDDEN_PROVIDER_IDS.has(p.id));
  const has = (field: string) => pluginHasField(plugin, field);
  const legacyModelFields = showLegacyModelFields(form);
  const apiKeyPlaceholder = defaultAPIKeyEnv(form.provider, plugins) ?? "API_KEY";
  const compatibleExtensions = extensions.filter((extension) =>
    extension.compatible_runtime_plugins.includes(form.provider)
  );
  const extensionByID = new Map(extensions.map((extension) => [extension.id, extension]));
  const availableExtensions = compatibleExtensions.filter(
    (extension) => !form.runtime_extensions.some((ref) => ref.id === extension.id)
  );
  const compatibleCatalogItems = extensionCatalog.filter((item) => item.provider === form.provider);
  const catalogItemID = (item: RuntimeExtensionCatalogItem) => item.install_ref || item.id;
  const catalogByRefID = new Map<string, RuntimeExtensionCatalogItem>();
  for (const item of extensionCatalog) {
    catalogByRefID.set(item.id, item);
    if (item.install_ref) catalogByRefID.set(item.install_ref, item);
  }
  const availableCatalogItems = compatibleCatalogItems.filter(
    (item) => !form.runtime_extensions.some((ref) => ref.id === catalogItemID(item))
  );
  const selectedExtensionID = availableExtensions.some((extension) => extension.id === extensionToAdd)
    ? extensionToAdd
    : availableExtensions[0]?.id || "";
  const selectedCatalogItemID = availableCatalogItems.some((item) => catalogItemID(item) === catalogItemToAdd)
    ? catalogItemToAdd
    : availableCatalogItems[0] ? catalogItemID(availableCatalogItems[0]) : "";
  const trimmedManualExtensionID = manualExtensionID.trim();
  const manualRegistryExtension = extensionByID.get(trimmedManualExtensionID);
  const manualExtensionIncompatible = Boolean(
    manualRegistryExtension && !manualRegistryExtension.compatible_runtime_plugins.includes(form.provider)
  );
  const manualExtensionDuplicate = form.runtime_extensions.some((ref) => ref.id === trimmedManualExtensionID);
  const canAddManualExtension =
    trimmedManualExtensionID !== "" && !manualExtensionDuplicate && !manualExtensionIncompatible;
  const addRuntimeExtension = () => {
    const extension = availableExtensions.find((item) => item.id === selectedExtensionID);
    if (!extension) return;
    onChange({
      ...form,
      runtime_extensions: [
        ...form.runtime_extensions,
        { id: extension.id, enabled: true, config: formatEnv(extension.config) },
      ],
    });
    setExtensionToAdd("");
  };
  const addCatalogRuntimeExtension = () => {
    const item = availableCatalogItems.find((candidate) => catalogItemID(candidate) === selectedCatalogItemID);
    if (!item) return;
    const config = {
      registry: item.registry,
      ...(item.install_ref ? { install_ref: item.install_ref } : {}),
      ...(item.source_url ? { source_url: item.source_url } : {}),
    };
    onChange({
      ...form,
      runtime_extensions: [
        ...form.runtime_extensions,
        { id: catalogItemID(item), enabled: true, config: formatEnv(config) },
      ],
    });
    setCatalogItemToAdd("");
  };
  const addManualRuntimeExtension = () => {
    if (!canAddManualExtension) return;
    onChange({
      ...form,
      runtime_extensions: [
        ...form.runtime_extensions,
        {
          id: trimmedManualExtensionID,
          enabled: true,
          config: manualRegistryExtension ? formatEnv(manualRegistryExtension.config) : "",
        },
      ],
    });
    setManualExtensionID("");
  };
  const updateRuntimeExtension = (index: number, patch: Partial<RuntimeExtensionFormRef>) => {
    onChange({
      ...form,
      runtime_extensions: form.runtime_extensions.map((ref, i) =>
        i === index ? { ...ref, ...patch } : ref
      ),
    });
  };
  const removeRuntimeExtension = (index: number) => {
    onChange({
      ...form,
      runtime_extensions: form.runtime_extensions.filter((_, i) => i !== index),
    });
  };

  return (
    <div className="space-y-5">
      {title && <h3 className="font-medium">{title}</h3>}
      <section className="rounded-lg border border-border bg-card shadow-sm">
        <div className="border-b border-border px-4 py-3">
          <span className="text-sm font-medium">Basics and model</span>
        </div>
        <div className="grid grid-cols-2 gap-x-4 gap-y-4 p-4">
        <div>
          <Label htmlFor="profile-name">Name</Label>
          <Input
            id="profile-name"
            name="profile_name"
            value={form.name}
            onChange={(e) => onChange({ ...form, name: e.target.value })}
            placeholder="Codex Default…"
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <div>
          <Label htmlFor="profile-provider">Provider</Label>
          <Select
            id="profile-provider"
            name="provider"
            value={form.provider}
            onChange={(e) => {
              const provider = e.target.value;
              onChange({
                ...form,
                provider,
                api_key_env: form.api_key_env || defaultAPIKeyEnv(provider, plugins) || "",
                runtime_extensions: compatibleRuntimeExtensionRefs(form.runtime_extensions, provider, extensions),
                codex_multi_agent_state: provider === "codex" ? form.codex_multi_agent_state : "inherit",
                codex_multi_agent_max_threads: provider === "codex" ? form.codex_multi_agent_max_threads : "",
                codex_multi_agent_max_depth: provider === "codex" ? form.codex_multi_agent_max_depth : "",
              });
            }}
          >
            {providerOptions.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name || p.id}
              </option>
            ))}
          </Select>
        </div>
        <div>
          <Label htmlFor="profile-model-provider">Model provider</Label>
          <Select
            id="profile-model-provider"
            name="model_provider_id"
            value={form.model_provider_id}
            onChange={(e) => onChange(applyModelProviderSelection(form, e.target.value))}
          >
            <option value="">Legacy / none</option>
            {selectableProviders.map((provider) => {
              const compatible = isModelProviderCompatibleWithRuntime(provider, plugin);
              return (
                <option key={provider.id} value={provider.id}>
                  {provider.name} ({provider.api_key_env}){compatible ? "" : " (incompatible)"}
                </option>
              );
            })}
          </Select>
          {selectedModelProvider && (
            <p className="mt-1 text-[11px] text-muted-foreground">
              {selectedModelProvider.base_url} · {modelProviderSupportedProtocols(selectedModelProvider).join(", ") || "no protocols"}
            </p>
          )}
        </div>
        <div>
          <Label htmlFor="profile-model-provider-protocol">Model protocol</Label>
          <Select
            id="profile-model-provider-protocol"
            name="model_provider_protocol"
            value={form.model_provider_protocol}
            onChange={(e) => onChange({ ...form, model_provider_protocol: e.target.value })}
            disabled={!form.model_provider_id}
          >
            <option value="">Auto</option>
            {compatibleProtocols.map((protocol) => (
              <option key={protocol} value={protocol}>{protocol}</option>
            ))}
            {form.model_provider_protocol && !compatibleProtocols.includes(form.model_provider_protocol) && (
              <option value={form.model_provider_protocol}>{form.model_provider_protocol} (stale)</option>
            )}
          </Select>
        </div>
        <div>
          <Label htmlFor="profile-model-override">Model override</Label>
          <Select
            id="profile-model-override"
            name="model_override"
            value={form.model_override}
            onChange={(e) => onChange({ ...form, model_override: e.target.value })}
            disabled={!form.model_provider_id}
          >
            <option value="">Use provider default</option>
            {providerModels.map((model) => (
              <option key={model} value={model}>{model}</option>
            ))}
            {form.model_override && !providerModels.includes(form.model_override) && (
              <option value={form.model_override}>{form.model_override} (stale)</option>
            )}
          </Select>
        </div>
        <div>
          <Label id="profile-reasoning-effort-label">Reasoning effort</Label>
          <div
            className="mt-1.5 flex rounded-lg border border-input p-0.5"
            role="group"
            aria-labelledby="profile-reasoning-effort-label"
          >
            {REASONING_EFFORT_VALUES.map((effort) => (
              <button
                key={effort}
                type="button"
                aria-pressed={displayReasoningEffort(form.reasoning_effort) === effort}
                onClick={() => onChange({ ...form, reasoning_effort: effort })}
                className={cn(
                  "flex-1 rounded-md px-2 py-1 text-xs transition-colors",
                  displayReasoningEffort(form.reasoning_effort) === effort
                    ? "bg-primary font-medium text-primary-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {effort}
              </button>
            ))}
          </div>
        </div>
        {has("binary_path") && <div>
          <Label htmlFor="profile-binary-path">Binary path</Label>
          <Input
            id="profile-binary-path"
            name="binary_path"
            value={form.binary_path}
            onChange={(e) => onChange({ ...form, binary_path: e.target.value })}
            placeholder={plugin?.binary.default ? `/usr/local/bin/${plugin.binary.default}…` : "/usr/local/bin/codex…"}
            autoComplete="off"
            spellCheck={false}
          />
        </div>}
        {has("model") && legacyModelFields && <div>
          <Label htmlFor="profile-model">Model</Label>
          <Input
            id="profile-model"
            name="model"
            value={form.model}
            onChange={(e) => onChange({ ...form, model: e.target.value })}
            placeholder="gpt-5…"
            autoComplete="off"
            spellCheck={false}
          />
        </div>}
        {has("endpoint") && legacyModelFields && <div>
          <Label htmlFor="profile-endpoint">Endpoint</Label>
          <Input
            id="profile-endpoint"
            name="endpoint"
            type="url"
            inputMode="url"
            value={form.endpoint}
            onChange={(e) => onChange({ ...form, endpoint: e.target.value })}
            placeholder="https://api.example.test/v1…"
            autoComplete="off"
            spellCheck={false}
          />
        </div>}
        {has("default_runner") && <div>
          <Label htmlFor="profile-default-runner">Default runner</Label>
          <Select
            id="profile-default-runner"
            name="default_runner"
            value={form.default_runner}
            onChange={(e) => onChange({ ...form, default_runner: e.target.value })}
          >
            {RUNNERS.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </Select>
        </div>}
        </div>
        {plugin && (
          <div className="border-t border-border px-4 py-3">
            <div className="flex flex-wrap gap-1.5">
              <Chip variant="neutral">{plugin.id}</Chip>
              <Chip variant="neutral">{plugin.config_projection.primitive}</Chip>
              <Chip variant="neutral">{plugin.transcript.parser}</Chip>
              {plugin.capabilities.sandbox && (
                <Chip className="border-transparent bg-primary text-primary-foreground">sandbox</Chip>
              )}
              {plugin.capabilities.host && (
                <Chip className="border-transparent bg-primary text-primary-foreground">host</Chip>
              )}
              {plugin.capabilities.mcp_config && (
                <Chip className="border-transparent bg-primary text-primary-foreground">mcp</Chip>
              )}
            </div>
          </div>
        )}
      </section>
      <section className="rounded-lg border border-border bg-card shadow-sm">
        <div className="border-b border-border px-4 py-3">
          <span className="text-sm font-medium">Environment and advanced</span>
        </div>
        <div className="grid grid-cols-2 gap-x-4 gap-y-4 p-4">
        {has("custom_args") && <div className="col-span-2">
          <Label htmlFor="profile-custom-args">Custom args</Label>
          <Textarea
            id="profile-custom-args"
            name="custom_args"
            value={form.custom_args}
            onChange={(e) => onChange({ ...form, custom_args: e.target.value })}
            placeholder="--json…"
            autoComplete="off"
            spellCheck={false}
          />
        </div>}
        {has("env") && <div className="col-span-2">
          <Label htmlFor="profile-env">Environment</Label>
          <p className="text-[11px] text-muted-foreground mb-1">KEY=VALUE lines or a JSON object</p>
          <Textarea
            id="profile-env"
            name="env"
            value={form.env}
            onChange={(e) => onChange({ ...form, env: e.target.value })}
            placeholder={'ANTHROPIC_BASE_URL=https://api.example.test\nANTHROPIC_MODEL=claude-sonnet…'}
            autoComplete="off"
            spellCheck={false}
          />
        </div>}
        {has("api_keys") && legacyModelFields && <div>
          <Label htmlFor="profile-api-key-env">API key env</Label>
          <Input
            id="profile-api-key-env"
            name="api_key_env"
            value={form.api_key_env}
            onChange={(e) => onChange({ ...form, api_key_env: e.target.value })}
            placeholder={`${apiKeyPlaceholder}…`}
            autoComplete="off"
            spellCheck={false}
          />
        </div>}
        {has("api_keys") && legacyModelFields && <div>
          <Label htmlFor="profile-api-key">API key</Label>
          <div className="relative">
            <Input
              id="profile-api-key"
              name="api_key"
              type="password"
              value={form.api_key}
              onChange={(e) => onChange({ ...form, api_key: e.target.value })}
              placeholder="sk-…"
              autoComplete="off"
              spellCheck={false}
              className="pr-16"
            />
            {form.api_key.trim() === API_KEY_CONFIGURED && (
              <span className="pointer-events-none absolute right-2 top-1/2 flex -translate-y-1/2 items-center gap-1 text-[11px] text-success">
                <CheckCircle2 className="h-3.5 w-3.5" /> configured
              </span>
            )}
          </div>
          <p className="text-[11px] text-muted-foreground mt-1">
            Stored on this profile only. Leave as [configured] to keep the existing key.
          </p>
        </div>}
        {has("mcp_servers") && <div className="col-span-2">
          <Label htmlFor="profile-mcp-servers">MCP servers JSON</Label>
          <Textarea
            id="profile-mcp-servers"
            name="mcp_servers"
            value={form.mcp_servers}
            onChange={(e) => onChange({ ...form, mcp_servers: e.target.value })}
            placeholder='[{"name":"project","mode":"trusted","url":"http://127.0.0.1:8787/mcp"}]…'
            autoComplete="off"
            spellCheck={false}
          />
        </div>}
        {has("sandbox_image") && <div className="col-span-2">
          <Label htmlFor="profile-sandbox-image">Sandbox image</Label>
          <Input
            id="profile-sandbox-image"
            name="sandbox_image"
            value={form.sandbox_image}
            onChange={(e) => onChange({ ...form, sandbox_image: e.target.value })}
            placeholder="ghcr.io/n1majne3/cyberpenda-sandbox:latest..."
            autoComplete="off"
            spellCheck={false}
          />
          <p className="text-[11px] text-muted-foreground mt-1">
            Override the daemon sandbox image for tasks using this profile.
          </p>
        </div>}
        {form.provider === "codex" && <div className="col-span-2 space-y-2 rounded-lg border border-border p-3">
          <div>
            <Label htmlFor="profile-multi-agent-state">In-turn multi-agent tools</Label>
            <Select
              id="profile-multi-agent-state"
              name="codex_multi_agent_state"
              value={form.codex_multi_agent_state}
              onChange={(e) => onChange({ ...form, codex_multi_agent_state: e.target.value as ProfileForm["codex_multi_agent_state"] })}
            >
              <option value="inherit">Codex default (no keys projected)</option>
              <option value="on">On — project spawn tools + caps</option>
              <option value="off">Off — project the off keys</option>
            </Select>
            <p className="text-[11px] text-muted-foreground mt-1">
              On projects <code className="text-[11px]">features.multi_agent</code> and <code className="text-[11px]">agents</code> caps so turns receive spawn tools. Off writes the off keys for every model. Codex default stores nothing and lets Codex decide.
            </p>
          </div>
          {form.codex_multi_agent_state === "on" && <div className="grid grid-cols-2 gap-3">
            <div>
              <Label htmlFor="profile-multi-agent-threads">Max concurrent agent threads</Label>
              <Input
                id="profile-multi-agent-threads"
                name="codex_multi_agent_max_threads"
                type="number"
                min={1}
                value={form.codex_multi_agent_max_threads}
                onChange={(e) => onChange({ ...form, codex_multi_agent_max_threads: e.target.value })}
                placeholder="Codex default (6)"
                autoComplete="off"
              />
            </div>
            <div>
              <Label htmlFor="profile-multi-agent-depth">Max agent depth</Label>
              <Input
                id="profile-multi-agent-depth"
                name="codex_multi_agent_max_depth"
                type="number"
                min={1}
                value={form.codex_multi_agent_max_depth}
                onChange={(e) => onChange({ ...form, codex_multi_agent_max_depth: e.target.value })}
                placeholder="Codex default (1)"
                autoComplete="off"
              />
            </div>
          </div>}
        </div>}
        {has("credential_refs") && <div className="col-span-2">
          <Label htmlFor="profile-credential-refs">Credential refs</Label>
          <Textarea
            id="profile-credential-refs"
            name="credential_refs"
            value={form.credential_refs}
            onChange={(e) => onChange({ ...form, credential_refs: e.target.value })}
            placeholder="codex-api-key…"
            rows={2}
            autoComplete="off"
            spellCheck={false}
          />
          <p className="text-[11px] text-muted-foreground mt-1">
            Resolved via global or project credential bindings at preflight.
          </p>
        </div>}
        {has("runtime_extensions") && <div className="col-span-2">
          <Label htmlFor="profile-runtime-extension">Runtime extensions</Label>
          <div className="mt-1 flex gap-2">
            <Select
              id="profile-runtime-extension"
              name="runtime_extension"
              className="flex-1"
              value={selectedExtensionID}
              onChange={(e) => setExtensionToAdd(e.target.value)}
              disabled={availableExtensions.length === 0}
            >
              {availableExtensions.length === 0 ? (
                <option value="">No compatible registry extensions</option>
              ) : (
                availableExtensions.map((extension) => (
                  <option key={extension.id} value={extension.id}>
                    {extension.name || extension.id}
                  </option>
                ))
              )}
            </Select>
            <Button type="button" size="sm" variant="outline" onClick={addRuntimeExtension} disabled={!selectedExtensionID}>
              <Plus className="h-4 w-4" />
              Add
            </Button>
          </div>
          <div className="mt-2 flex gap-2">
            <Select
              id="profile-catalog-extension"
              name="catalog_extension"
              className="flex-1"
              value={selectedCatalogItemID}
              onChange={(e) => setCatalogItemToAdd(e.target.value)}
              disabled={availableCatalogItems.length === 0}
            >
              {availableCatalogItems.length === 0 ? (
                <option value="">No catalog packages available</option>
              ) : (
                availableCatalogItems.map((item) => (
                  <option key={`${item.registry}:${catalogItemID(item)}`} value={catalogItemID(item)}>
                    {item.name || catalogItemID(item)}
                  </option>
                ))
              )}
            </Select>
            <Button type="button" size="sm" variant="outline" onClick={addCatalogRuntimeExtension} disabled={!selectedCatalogItemID}>
              <Plus className="h-4 w-4" />
              Add package
            </Button>
          </div>
          <div className="mt-2 flex gap-2">
            <Input
              id="profile-manual-extension-id"
              name="manual_extension_id"
              value={manualExtensionID}
              onChange={(e) => setManualExtensionID(e.target.value)}
              placeholder="manual_extension_id…"
              autoComplete="off"
              spellCheck={false}
            />
            <Button type="button" size="sm" variant="outline" onClick={addManualRuntimeExtension} disabled={!canAddManualExtension}>
              <Plus className="h-4 w-4" />
              Add manual
            </Button>
          </div>
          {extensions.length === 0 && (
            <p className="mt-1 text-[11px] text-muted-foreground">
              No registry extensions loaded. Manual refs can be saved, but launch requires the daemon registry to resolve them.
            </p>
          )}
          {manualExtensionIncompatible && (
            <p className="mt-1 text-[11px] text-destructive">
              Registry extension is not compatible with this provider.
            </p>
          )}
          <div className="mt-2 space-y-2">
            {form.runtime_extensions.length === 0 && (
              <p className="text-[11px] text-muted-foreground">No runtime extensions enabled for this profile.</p>
            )}
            {form.runtime_extensions.map((ref, index) => {
              const extension = extensionByID.get(ref.id);
              const catalogItem = catalogByRefID.get(ref.id);
              return (
                <div key={`${ref.id}-${index}`} className="rounded-md border border-border p-3 space-y-2">
                  <div className="flex items-start justify-between gap-3">
	                    <label className="flex items-start gap-2 text-sm">
	                      <input
	                        type="checkbox"
	                        name="runtime_extension_enabled"
	                        className="mt-1 h-4 w-4 accent-primary"
	                        checked={ref.enabled}
                        onChange={(e) => updateRuntimeExtension(index, { enabled: e.target.checked })}
                      />
                      <span>
                        <span className="flex flex-wrap items-center gap-1.5">
                          <span className="font-medium">{extension?.name || catalogItem?.name || ref.id}</span>
                          <Badge variant="outline">{ref.id}</Badge>
                          {catalogItem && <Badge variant="outline">{catalogItem.registry}</Badge>}
                          {!extension && !catalogItem && <Badge variant="outline">manual</Badge>}
                          {!ref.enabled && <Badge variant="default">disabled</Badge>}
                        </span>
                        {extension?.description && (
                          <span className="mt-1 block text-xs text-muted-foreground">{extension.description}</span>
                        )}
                        {!extension && catalogItem?.description && (
                          <span className="mt-1 block text-xs text-muted-foreground">{catalogItem.description}</span>
                        )}
                        {extension?.projection && (
                          <span className="mt-1 block text-[11px] text-muted-foreground">
                            {extension.projection.location}: <code>{extension.projection.path}</code>
                          </span>
                        )}
                      </span>
                    </label>
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      aria-label={`Remove ${extension?.name || catalogItem?.name || ref.id} runtime extension`}
                      onClick={() => removeRuntimeExtension(index)}
                    >
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                  <div>
                    <Label htmlFor={`runtime-extension-config-${index}`}>Config</Label>
                    <Textarea
                      id={`runtime-extension-config-${index}`}
                      name="runtime_extension_config"
                      value={ref.config}
                      onChange={(e) => updateRuntimeExtension(index, { config: e.target.value })}
                      placeholder="KEY=value…"
                      rows={2}
                      autoComplete="off"
                      spellCheck={false}
                    />
                  </div>
                </div>
              );
            })}
          </div>
          {compatibleExtensions.length > 0 && (
            <div className="mt-2 flex flex-wrap gap-1">
              {compatibleExtensions.map((extension) => (
                <Badge key={extension.id} variant="outline">{extension.id}</Badge>
              ))}
            </div>
          )}
        </div>}
        </div>
      </section>
      {form.provider === "claude_code" && form.endpoint.includes("bigmodel.cn") && (
        <div className="space-y-1 rounded-lg border border-border bg-muted/30 p-3 text-xs text-muted-foreground">
          <p className="font-medium text-foreground">Zhipu GLM runtime notes</p>
          <p>Endpoint: use <code className="text-[11px]">https://open.bigmodel.cn/api/anthropic</code> (not Minimax).</p>
          <p>Launch adds <code className="text-[11px]">--strict-mcp-config --mcp-config workdir/.mcp.json</code>; smoke may need <code className="text-[11px]">--permission-mode bypassPermissions</code> in custom args.</p>
          <p>Third-party APIs may not expose local MCP tools in the model tool list — allow JSON-RPC fallback to PENTEST_MCP_URL.</p>
        </div>
      )}
      {form.provider === "pi" && form.default_runner === "sandbox" && (
        <p className="text-[11px] text-muted-foreground">
          Pi sandbox sets <code>PI_CODING_AGENT_DIR=/task/runtime-home/pi/agent</code>; pi is preinstalled in <code>ghcr.io/n1majne3/cyberpenda-sandbox:latest</code>.
        </p>
      )}
      {!hideActions && (
        <div className="flex gap-2">
          <SaveActionButton
            label={saveLabel}
            pending={savePending}
            disabled={saveDisabled}
            onClick={onSave}
          />
          {onCancel && (
            <Button size="sm" variant="ghost" onClick={onCancel}>
              Cancel
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

function profileToForm(profile: RuntimeProfile, plugins: RuntimePlugin[]): ProfileForm {
  const apiKeyEntries = Object.entries(profile.fields.api_keys ?? {});
  const [apiKeyEnv = "", apiKeyValue = ""] = apiKeyEntries[0] ?? [];
  return {
    name: profile.name,
    provider: profile.provider,
    binary_path: profile.fields.binary_path ?? "",
    model: profile.fields.model_provider_id ? "" : (profile.fields.model ?? ""),
    endpoint: profile.fields.model_provider_id ? "" : (profile.fields.endpoint ?? ""),
    model_provider_id: profile.fields.model_provider_id ?? "",
    model_provider_protocol: profile.fields.model_provider_protocol ?? "",
    model_override: profile.fields.model_override ?? "",
    reasoning_effort: displayReasoningEffort(profile.fields.reasoning_effort),
    custom_args: (profile.fields.custom_args ?? []).join("\n"),
    env: formatEnv(profile.fields.env),
    api_key_env: profile.fields.model_provider_id ? "" : (apiKeyEnv || defaultAPIKeyEnv(profile.provider, plugins) || ""),
    api_key: profile.fields.model_provider_id ? "" : apiKeyValue,
    runtime_extensions: (profile.fields.runtime_extensions ?? []).map((ref) => ({
      id: ref.id,
      enabled: ref.enabled ?? true,
      config: formatEnv(ref.config),
    })),
    mcp_servers: formatMCPServers(profile.fields.mcp_servers),
    default_runner: profile.fields.default_runner ?? "sandbox",
    sandbox_image: profile.fields.sandbox_image ?? "",
    credential_refs: (profile.fields.credential_refs ?? []).join("\n"),
    codex_multi_agent_state: profile.fields.codex_multi_agent
      ? profile.fields.codex_multi_agent.enabled
        ? "on"
        : "off"
      : "inherit",
    codex_multi_agent_max_threads:
      profile.fields.codex_multi_agent?.max_concurrent_threads_per_session?.toString() ?? "",
    codex_multi_agent_max_depth: profile.fields.codex_multi_agent?.max_depth?.toString() ?? "",
  };
}

function compatibleRuntimeExtensionRefs(
  refs: RuntimeExtensionFormRef[],
  provider: string,
  extensions: RuntimeExtension[]
): RuntimeExtensionFormRef[] {
  return refs.filter((ref) => {
    const extension = extensions.find((item) => item.id === ref.id);
    if (!extension) return true;
    return extension.compatible_runtime_plugins.includes(provider);
  });
}

function formatEnv(env?: Record<string, string>): string {
  if (!env) return "";
  return Object.entries(env)
    .map(([key, value]) => `${key}=${value}`)
    .join("\n");
}

function buildGeneratedConfigPreview(
  provider: string,
  fields: RuntimeProfileFields,
  form?: ProfileForm,
  plugin?: RuntimePlugin,
  modelProviders: ModelProvider[] = [],
): Record<string, unknown> {
  const mcpServers = buildPreviewMCPServers(fields);
  const mcpPreview = formatMCPServerPreview(mcpServers);
  const launchPreview = buildLaunchPreview(provider, fields, form, (mcpServers?.length ?? 0) > 0, plugin);
  const configPath = plugin?.config_projection.config_path;
  const mcpConfigPath = plugin?.config_projection.mcp_config_path;
  const runtimeExtensionPreview = fields.runtime_extensions?.length
    ? { runtime_extensions: fields.runtime_extensions }
    : {};

  if (provider === "claude_code") {
    const env: Record<string, string> = { ...(fields.env ?? {}) };
    if (fields.endpoint && !env.ANTHROPIC_BASE_URL) env.ANTHROPIC_BASE_URL = fields.endpoint;
    if (fields.model && !env.ANTHROPIC_MODEL) env.ANTHROPIC_MODEL = fields.model;
    return {
      provider,
      settings_path: configPath ?? "runtime-home/claude/settings.json",
      env,
      ...runtimeExtensionPreview,
      ...(mcpPreview ? { mcp_servers: mcpPreview, mcp_config_path: mcpConfigPath ?? "workdir/.mcp.json" } : {}),
      ...(fields.api_keys && Object.keys(fields.api_keys).length > 0
        ? { api_keys: redactedAPIKeyPreview(fields) }
        : {}),
      ...(fields.default_runner ? { default_runner: fields.default_runner } : {}),
      task_context_path: "workdir/.pentest/context.json",
      launch_preview: launchPreview,
    };
  }

  if (provider === "codex") {
    if (fields.model_provider_id) {
      const base: Record<string, unknown> = {
        provider,
        config_path: configPath ?? "runtime-home/codex/config.toml",
        ...runtimeExtensionPreview,
        ...(mcpPreview ? { mcp_servers: mcpPreview } : {}),
        ...(fields.default_runner ? { default_runner: fields.default_runner } : {}),
        task_context_path: "workdir/.pentest/context.json",
        launch_preview: launchPreview,
      };
      return enrichPreviewWithModelProvider(base, fields, modelProviders, plugin);
    }

    const providerId = fields.env?.CODEX_MODEL_PROVIDER?.trim() || "custom";
    const wireApi = fields.env?.CODEX_WIRE_API?.trim() || "responses";
    const providerName = fields.env?.CODEX_PROVIDER_NAME?.trim() || "Custom";
    const endpoint = fields.endpoint?.trim() || fields.env?.OPENAI_BASE_URL?.trim() || "";
    const configToml = [
      fields.model ? `model = "${fields.model}"` : null,
      endpoint ? `model_provider = "${providerId}"` : null,
      endpoint ? `cli_auth_credentials_store = "file"` : null,
      endpoint ? "" : null,
      endpoint ? `[model_providers.${providerId}]` : null,
      endpoint ? `name = "${providerName}"` : null,
      endpoint ? `base_url = "${endpoint.replace(/\/$/, "")}"` : null,
      endpoint ? `wire_api = "${wireApi}"` : null,
      endpoint ? "requires_openai_auth = true" : null,
      ...appendCodexMCPTOMLPreview(mcpServers),
      ...codexMultiAgentTOMLLines(fields),
    ]
      .filter((line): line is string => line !== null)
      .join("\n");

    return {
      provider,
      config_path: configPath ?? "runtime-home/codex/config.toml",
      config_toml: configToml,
      ...runtimeExtensionPreview,
      ...(mcpPreview ? { mcp_servers: mcpPreview } : {}),
      ...(fields.api_keys && Object.keys(fields.api_keys).length > 0
        ? {
            auth_path: "runtime-home/codex/auth.json",
            auth_json: redactedAPIKeyPreview(fields),
            api_keys: redactedAPIKeyPreview(fields),
          }
        : {}),
      ...(fields.default_runner ? { default_runner: fields.default_runner } : {}),
      task_context_path: "workdir/.pentest/context.json",
      launch_preview: launchPreview,
    };
  }

  if (provider === "pi") {
    const providerId = fields.env?.PI_PROVIDER_ID?.trim() || "custom";
    const api =
      fields.env?.PI_API?.trim() ||
      (fields.endpoint?.toLowerCase().includes("anthropic")
        ? "anthropic-messages"
        : fields.endpoint?.toLowerCase().includes("generativelanguage") ||
            fields.endpoint?.toLowerCase().includes("googleapis")
          ? "google-generative-ai"
          : "openai-completions");
    const apiKeyEnv = Object.keys(fields.api_keys ?? {})[0];
    const apiKeyRef = apiKeyEnv ? `$${apiKeyEnv}` : undefined;
    const modelsJson: Record<string, unknown> = {
      providers: {
        [providerId]: {
          ...(fields.endpoint ? { baseUrl: fields.endpoint.replace(/\/$/, "") } : {}),
          api,
          ...(apiKeyRef ? { apiKey: apiKeyRef } : {}),
          ...(fields.model ? { models: [{ id: fields.model }] } : {}),
        },
      },
    };

    return {
      provider,
      models_path: configPath ?? "runtime-home/pi/agent/models.json",
      models_json: modelsJson,
      ...runtimeExtensionPreview,
      ...(mcpPreview ? { mcp_servers: mcpPreview, mcp_config_path: mcpConfigPath ?? "runtime-home/pi/agent/mcp.json" } : {}),
      ...(fields.api_keys && Object.keys(fields.api_keys).length > 0
        ? {
            auth_path: "runtime-home/pi/agent/auth.json",
            auth_json: buildPiAuthPreview(fields),
            api_keys: redactedAPIKeyPreview(fields),
          }
        : {}),
      ...(fields.default_runner ? { default_runner: fields.default_runner } : {}),
      task_context_path: "workdir/.pentest/context.json",
      launch_preview: launchPreview,
    };
  }

  const cfg: Record<string, unknown> = { provider };
  if (fields.binary_path) cfg.binary = fields.binary_path;
  if (fields.model) cfg.model = fields.model;
  if (fields.endpoint) cfg.endpoint = fields.endpoint;
  if (fields.model_provider_id) cfg.model_provider_id = fields.model_provider_id;
  if (fields.model_provider_protocol) cfg.model_provider_protocol = fields.model_provider_protocol;
  if (fields.model_override) cfg.model_override = fields.model_override;
  if (fields.custom_args?.length) cfg.custom_args = fields.custom_args;
  if (fields.env && Object.keys(fields.env).length > 0) cfg.env = fields.env;
  if (fields.api_keys && Object.keys(fields.api_keys).length > 0) {
    cfg.api_keys = redactedAPIKeyPreview(fields);
  }
  if (fields.runtime_extensions?.length) cfg.runtime_extensions = fields.runtime_extensions;
  if (mcpPreview) cfg.mcp_servers = mcpPreview;
  if (fields.default_runner) cfg.default_runner = fields.default_runner;
  return cfg;
}

function buildLaunchPreview(
  provider: string,
  fields: RuntimeProfileFields,
  form: ProfileForm | undefined,
  hasMCP: boolean,
  plugin?: RuntimePlugin
): Record<string, unknown> {
  const sandbox = fields.default_runner === "sandbox";
  const runtimeHome = sandbox ? "/task/runtime-home" : "runtime-home";
  const workdir = sandbox ? "/task/workdir" : "workdir";
  const binary = fields.binary_path?.trim() || plugin?.binary.default || fallbackBinary(provider);
  const subcommand = fields.env?.PENTEST_CODEX_SUBCOMMAND?.trim() || "exec";
  const configPath = previewRuntimePath(defaultConfigPath(provider, plugin), sandbox);
  const mcpConfigPath = previewRuntimePath(defaultMCPConfigPath(provider, plugin), sandbox);
  const customArgs = fields.custom_args ?? [];
  const lists: Record<string, string[]> = {
    custom_args: customArgs,
  };
  if (provider === "codex" && subcommand === "exec" && !hasCLIOption(customArgs, "--skip-git-repo-check")) {
    lists.codex_exec_args = ["--skip-git-repo-check"];
  }
  if (hasMCP && mcpConfigPath) {
    lists.mcp_args = ["--strict-mcp-config", "--mcp-config", mcpConfigPath];
  }
  if (subcommand !== "exec") {
    lists.codex_goal_prefix = ["--"];
  }
  if (hasMCP) {
    lists.claude_goal_prefix = ["--"];
  }
  if (!hasCLIOption(customArgs, "--provider")) {
    const providerId = fields.env?.PI_PROVIDER_ID?.trim() || (fields.endpoint?.trim() ? "custom" : "");
    if (providerId) lists.pi_provider_args = ["--provider", providerId];
  }
  const scalars: Record<string, string> = {
    binary,
    model: fields.model ?? "",
    endpoint: fields.endpoint ?? "",
    config_path: configPath,
    mcp_config_path: mcpConfigPath,
    goal: "<goal>",
    codex_subcommand: subcommand,
    runtime_home: runtimeHome,
    workdir,
  };

  const args = plugin?.launch.args?.length
    ? renderLaunchTemplate(plugin.launch, scalars, lists)
    : renderCompatibilityLaunch(provider, fields, hasMCP, configPath, mcpConfigPath, binary);
  const processEnv: Record<string, string> = renderProcessEnvTemplate(plugin?.process_env, {
    ...scalars,
    provider_home: runtimeHome + "/" + providerHomeDir(provider),
  });

  for (const [key, value] of Object.entries(fields.env ?? {})) {
    processEnv[key] = value;
  }
  for (const key of Object.keys(fields.api_keys ?? {})) {
    processEnv[key] = "[REDACTED at launch]";
  }

  if (sandbox) {
    processEnv.IS_SANDBOX = "1";
    processEnv.PENTEST_SKILLS_DIR = "/task/skills";
    if (form?.endpoint?.includes("bigmodel.cn") || fields.endpoint?.includes("bigmodel.cn")) {
      processEnv.ANTHROPIC_BASE_URL = fields.endpoint ?? form?.endpoint ?? "";
    }
  }

  return { argv: args, process_env: processEnv, runner: fields.default_runner ?? "sandbox" };
}

function renderCompatibilityLaunch(
  provider: string,
  fields: RuntimeProfileFields,
  hasMCP: boolean,
  configPath: string,
  mcpConfigPath: string,
  binary: string
): string[] {
  const args = [binary];
  const customArgs = fields.custom_args ?? [];
  if (provider === "codex") {
    const subcommand = fields.env?.PENTEST_CODEX_SUBCOMMAND?.trim() || "exec";
    args.push(subcommand);
    if (fields.model) args.push("--model", fields.model);
    if (subcommand === "exec" && !hasCLIOption(customArgs, "--skip-git-repo-check")) {
      args.push("--skip-git-repo-check");
    }
    args.push(...customArgs);
    if (subcommand !== "exec") args.push("--");
    args.push("<goal>");
    return args;
  }
  if (provider === "claude_code") {
    if (fields.model) args.push("--model", fields.model);
    if (configPath) args.push("--settings", configPath);
    if (hasMCP && mcpConfigPath) args.push("--strict-mcp-config", "--mcp-config", mcpConfigPath);
    if (!hasCLIOption(customArgs, "-p") && !hasCLIOption(customArgs, "--print")) args.push("-p");
    if (!hasCLIOption(customArgs, "--output-format")) args.push("--output-format", "stream-json");
    if (!hasCLIOption(customArgs, "--verbose")) args.push("--verbose");
    args.push(...customArgs);
    if (hasMCP) args.push("--");
    args.push("<goal>");
    return args;
  }
  if (provider === "pi") {
    if (!hasCLIOption(customArgs, "--provider")) {
      const providerId = fields.env?.PI_PROVIDER_ID?.trim() || (fields.endpoint?.trim() ? "custom" : "");
      if (providerId) args.push("--provider", providerId);
    }
    if (fields.model) args.push("--model", fields.model);
    args.push(...customArgs, "<goal>");
  }
  return args.filter(Boolean);
}

function renderLaunchTemplate(
  launch: RuntimePlugin["launch"],
  scalars: Record<string, string>,
  lists: Record<string, string[]>
): string[] {
  const templateArgs = suppressSingletonDefaults(launch.args, launch.singleton_options ?? [], lists.custom_args ?? []);
  const out: string[] = [];
  for (let i = 0; i < templateArgs.length; i += 1) {
    const arg = templateArgs[i];
    const nextPlaceholder = placeholderName(templateArgs[i + 1]);
    if (
      nextPlaceholder &&
      arg.startsWith("-") &&
      !Object.prototype.hasOwnProperty.call(lists, nextPlaceholder) &&
      placeholderEmpty(nextPlaceholder, scalars, lists)
    ) {
      i += 1;
      continue;
    }
    const placeholder = placeholderName(arg);
    if (placeholder) {
      if (Object.prototype.hasOwnProperty.call(lists, placeholder)) {
        out.push(...nonEmptyStrings(lists[placeholder]));
        continue;
      }
      const value = (scalars[placeholder] ?? "").trim();
      if (value) out.push(value);
      continue;
    }
    const rendered = renderScalarFragments(arg, scalars).trim();
    if (rendered) out.push(rendered);
  }
  return out;
}

function renderProcessEnvTemplate(
  processEnv: Record<string, string> | undefined,
  scalars: Record<string, string>
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(processEnv ?? {})) {
    const rendered = renderScalarFragments(value, scalars).trim();
    if (rendered) out[key] = rendered;
  }
  return out;
}

function suppressSingletonDefaults(
  args: string[],
  groups: { options: string[]; arity: number }[],
  customArgs: string[]
): string[] {
  const out: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const group = groups.find((item) => item.options.includes(args[i]) && item.options.some((option) => hasCLIOption(customArgs, option)));
    if (group) {
      i += group.arity;
      continue;
    }
    out.push(args[i]);
  }
  return out;
}

function placeholderName(value: string | undefined): string | null {
  if (!value?.startsWith("{{") || !value.endsWith("}}")) return null;
  const name = value.slice(2, -2).trim();
  return name || null;
}

function placeholderEmpty(name: string, scalars: Record<string, string>, lists: Record<string, string[]>): boolean {
  if (Object.prototype.hasOwnProperty.call(lists, name)) return nonEmptyStrings(lists[name]).length === 0;
  return !(scalars[name] ?? "").trim();
}

function nonEmptyStrings(values: string[] | undefined): string[] {
  return (values ?? []).map((value) => value.trim()).filter(Boolean);
}

function renderScalarFragments(value: string, scalars: Record<string, string>): string {
  return value.replace(/{{\s*([^}]+)\s*}}/g, (_, name: string) => scalars[name.trim()] ?? "");
}

function fallbackBinary(provider: string): string {
  if (provider === "claude_code") return "claude";
  if (provider === "codex" || provider === "pi" || provider === "fake") return provider;
  return provider;
}

function providerHomeDir(provider: string): string {
  return provider === "claude_code" ? "claude" : provider;
}

function defaultConfigPath(provider: string, plugin?: RuntimePlugin): string {
  if (plugin?.config_projection.config_path) return plugin.config_projection.config_path;
  if (provider === "claude_code") return "runtime-home/claude/settings.json";
  if (provider === "codex") return "runtime-home/codex/config.toml";
  if (provider === "pi") return "runtime-home/pi/agent/models.json";
  return "";
}

function defaultMCPConfigPath(provider: string, plugin?: RuntimePlugin): string {
  if (plugin?.config_projection.mcp_config_path) return plugin.config_projection.mcp_config_path;
  if (provider === "claude_code") return "workdir/.mcp.json";
  if (provider === "pi") return "runtime-home/pi/agent/mcp.json";
  return "";
}

function previewRuntimePath(path: string, sandbox: boolean): string {
  if (!path) return "";
  if (!sandbox || path.startsWith("/")) return path;
  return "/task/" + path;
}

function hasCLIOption(args: string[] | undefined, option: string): boolean {
  return (args ?? []).some((arg) => arg === option || arg.startsWith(`${option}=`));
}

function redactedAPIKeyPreview(fields: RuntimeProfileFields): Record<string, string> {
  return Object.fromEntries(
    Object.keys(fields.api_keys ?? {})
      .filter((key) => key.trim())
      .map((key) => [key, "[REDACTED at launch]"])
  );
}

function buildPiAuthPreview(fields: RuntimeProfileFields): Record<string, { type: string; key: string }> {
  const apiKeyEnv = Object.keys(fields.api_keys ?? {})
    .filter((key) => key.trim())
    .sort()[0];
  if (!apiKeyEnv) return {};
  const providerId = fields.env?.PI_PROVIDER_ID?.trim() || "custom";
  return {
    [providerId]: { type: "api_key", key: "[REDACTED at launch]" },
  };
}

function trustedMCPDisabled(env?: Record<string, string>): boolean {
  const value = (env?.PENTEST_DISABLE_TRUSTED_MCP ?? "").trim().toLowerCase();
  return value === "1" || value === "true" || value === "yes";
}

function previewMCPEndpointURL(sandbox: boolean): string {
  const host = sandbox ? "host.docker.internal" : "127.0.0.1";
  return `http://${host}:${DEFAULT_DAEMON_MCP_PORT}/mcp`;
}

function buildPreviewMCPServers(fields: RuntimeProfileFields): RuntimeProfileFields["mcp_servers"] {
  const servers = [...(fields.mcp_servers ?? [])];
  if (trustedMCPDisabled(fields.env)) return servers;

  const sandbox = fields.default_runner === "sandbox";
  const trustedURL = previewMCPEndpointURL(sandbox);
  const normalized = trustedURL.replace(/\/$/, "");
  if (servers.some((server) => (server.url ?? "").replace(/\/$/, "") === normalized)) {
    return servers;
  }
  return [{ name: "pentest", mode: "trusted", url: trustedURL }, ...servers];
}

function formatMCPServerPreview(
  servers?: RuntimeProfileFields["mcp_servers"]
): Array<Record<string, unknown>> | undefined {
  if (!servers?.length) return undefined;
  return servers.map((server) => ({
    name: server.name,
    mode: server.mode,
    ...(server.command ? { command: server.command } : {}),
    ...(server.url ? { url: server.url } : {}),
    ...(server.args?.length ? { args: server.args } : {}),
    ...(server.env && Object.keys(server.env).length > 0 ? { env: server.env } : {}),
  }));
}

function appendCodexMCPTOMLPreview(servers?: RuntimeProfileFields["mcp_servers"]): Array<string | null> {
  if (!servers?.length) return [];
  const lines: Array<string | null> = ["", "[mcp_servers]"];
  for (const server of servers) {
    const name = server.name?.trim();
    if (!name) continue;
    lines.push("", `[mcp_servers.${name}]`);
    if (server.url) {
      lines.push(`url = "${server.url}"`, "enabled = true");
      continue;
    }
    if (server.command) {
      lines.push(`command = "${server.command}"`, "enabled = true");
    }
  }
  return lines;
}

function formatMCPServers(servers?: RuntimeProfileFields["mcp_servers"]): string {
  if (!servers || servers.length === 0) return "";
  return JSON.stringify(servers, null, 2);
}
