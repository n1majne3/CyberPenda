import { useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { CheckCircle2, Plus, Search, Trash2 } from "lucide-react";
import { apiGet, apiPost, apiPatch, apiDelete, type ModelProvider, type RuntimeExtension, type RuntimePlugin, type RuntimeProfile } from "@/lib/api";
import { ModelProviderMigrationPanel } from "@/pages/ModelProviderMigrationPanel";
import { RuntimeProfileConfig } from "@/pages/RuntimeProfileConfig";
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
import { Button, Input, Label, Badge, Chip, Textarea, Select } from "@/components/ui";
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

const FALLBACK_PROVIDER_IDS = ["codex", "claude_code", "pi", "fake"] as const;
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
}) {
  const [extensionToAdd, setExtensionToAdd] = useState("");
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
  const selectedExtensionID = availableExtensions.some((extension) => extension.id === extensionToAdd)
    ? extensionToAdd
    : availableExtensions[0]?.id || "";
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
            <Input
              id="profile-manual-extension-id"
              name="manual_extension_id"
              value={manualExtensionID}
              onChange={(e) => setManualExtensionID(e.target.value)}
              placeholder="npm:@scope/package or local extension ID…"
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
              No registry extensions loaded. Local IDs require a registry entry. For packages, set install_ref in Config.
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
                          <span className="font-medium">{extension?.name || ref.id}</span>
                          <Badge variant="outline">{ref.id}</Badge>
                          {!extension && <Badge variant="outline">manual</Badge>}
                          {!ref.enabled && <Badge variant="default">disabled</Badge>}
                        </span>
                        {extension?.description && (
                          <span className="mt-1 block text-xs text-muted-foreground">{extension.description}</span>
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
                      aria-label={`Remove ${extension?.name || ref.id} runtime extension`}
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

function formatMCPServers(servers?: RuntimeProfileFields["mcp_servers"]): string {
  if (!servers || servers.length === 0) return "";
  return JSON.stringify(servers, null, 2);
}
