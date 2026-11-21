import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { LoaderCircle, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { apiDelete, apiGet, apiPatch, apiPost, apiPut, ApiError, type CredentialBinding, type ModelProvider } from "@/lib/api";
import { Button, Chip } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SaveActionButton } from "@/components/SaveActionButton";
import { SettingsAlert, SettingsPageHeader, SettingsSplitLayout, SettingsPageShell } from "@/components/shared";
import { SettingsDetailPane } from "@/components/settingsLibrary";
import { buildModelProviderPayload, canSubmitModelProvider, endpointValidationErrors, providerToModelProviderForm } from "./modelProviderForm";
import { type Form, providerProtocols, splitLines } from "./modelProviderProtocols";
import { ModelProviderCatalogSection, ModelProviderConnectionSection } from "./ModelProviderSections";
import { ModelProviderListColumn } from "./ModelProviderList";

const emptyForm: Form = {
  name: "",
  base_url: "",
  protocols: [],
  endpoint_base_urls: {},
  manual_models: "",
  default_model: "",
  refreshed_models: [],
  api_key: "",
  limits: {},
};

export function ModelProvidersPage() {
  const [providers, setProviders] = useState<ModelProvider[]>([]);
  const [bindings, setBindings] = useState<CredentialBinding[]>([]);
  const [searchParams] = useSearchParams();
  const [selectedId, setSelectedId] = useState("");
  const [form, setForm] = useState<Form>(emptyForm);
  const [creating, setCreating] = useState(true);
  const [confirmDelete, setConfirmDelete] = useState<ModelProvider | null>(null);
  const [confirmDetach, setConfirmDetach] = useState<{ provider: ModelProvider; profiles: string[] } | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [savedNotice, setSavedNotice] = useState(false);
  const [query, setQuery] = useState("");
  const [cacheHints, setCacheHints] = useState<Record<string, { context_window?: number; max_output_tokens?: number }>>({});
  const [cacheRefreshedAt, setCacheRefreshedAt] = useState("");
  const [cacheRefreshing, setCacheRefreshing] = useState(false);
  const savedNoticeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const selected = providers.find((p) => p.id === selectedId) ?? null;
  const selectedBinding = selected ? bindings.find((binding) => binding.credential_ref === selected.api_key_env) : undefined;
  const canSubmit = canSubmitModelProvider(form, creating);
  const endpointErrors = endpointValidationErrors(form);

  // comparing against a stored previous value, which avoids a setState-in-effect.
  const formSelectionKey = selected && !creating
    ? `${selected.id}:${selected.updated_at ?? ""}:${selectedBinding?.updated_at ?? ""}`
    : "";
  const [lastFormKey, setLastFormKey] = useState("");
  const [unsavedApiKey, setUnsavedApiKey] = useState<{ providerId: string; value: string } | null>(null);
  if (lastFormKey !== formSelectionKey) {
    setLastFormKey(formSelectionKey);
    if (selected && !creating) {
      const next = providerToModelProviderForm(selected, selectedBinding);
      if (unsavedApiKey?.providerId === selected.id) {
        setForm({ ...next, api_key: unsavedApiKey.value });
      } else {
        if (unsavedApiKey) setUnsavedApiKey(null);
        setForm(next);
      }
    } else if (formSelectionKey === "" && form !== emptyForm) {
      setForm(emptyForm);
    }
  }

  const load = useCallback(async () => {
    try {
      const [data, credentialData] = await Promise.all([
        apiGet<{ providers: ModelProvider[] }>("/api/model-providers"),
        apiGet<{ bindings: CredentialBinding[] }>("/api/credential-bindings"),
      ]);
      const loaded = data.providers ?? [];
      setBindings(credentialData.bindings ?? []);
      setProviders(loaded);
      // Deep links (e.g. from the credential library) preselect a provider via
      // ?selected=<id>; it only wins when nothing is selected yet and the id
      // matches a loaded provider.
      const requestedId = searchParams.get("selected") ?? "";
      setSelectedId((current) => {
        if (current && loaded.some((p) => p.id === current)) return current;
        if (requestedId && loaded.some((p) => p.id === requestedId)) return requestedId;
        return loaded[0]?.id ?? "";
      });
      setCreating(loaded.length === 0);
      setLoaded(true);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [searchParams]);

  const loadCapabilityCacheStatus = useCallback(async () => {
    try {
      const status = await apiGet<{ refreshed_at?: string }>("/api/model-capability-cache");
      setCacheRefreshedAt(status.refreshed_at ?? "");
    } catch {
      // Cache status is advisory; the form still works without it.
    }
  }, []);

  // Initial load. Both async loaders dispatch state updates only after an await;
  // the rule cannot prove that for this canonical fetch-on-mount pattern.
  useEffect(() => {
    /* eslint-disable react-hooks/set-state-in-effect */
    void load();
    void loadCapabilityCacheStatus();
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [load, loadCapabilityCacheStatus]);

  useEffect(() => {
    return () => {
      if (savedNoticeTimer.current) clearTimeout(savedNoticeTimer.current);
    };
  }, []);

  function showSavedNotice() {
    setSavedNotice(true);
    if (savedNoticeTimer.current) clearTimeout(savedNoticeTimer.current);
    savedNoticeTimer.current = setTimeout(() => setSavedNotice(false), 2000);
  }

  function startCreate() {
    setCreating(true);
    setSelectedId("");
    setForm(emptyForm);
    setSavedNotice(false);
  }

  // Compute the candidate model list inline. It's a cheap derivation and the
  // React Compiler cannot preserve a useMemo over the selected provider (which
  // may be mutated later), so manual memoization would be skipped anyway.
  const baseModels = selected ? catalogModels(selected) : [];
  const manualModels = splitLines(form.manual_models);
  const extraModels = form.default_model ? [form.default_model] : [];
  const models = Array.from(new Set([...baseModels, ...manualModels, ...extraModels])).sort();
  const catalogModelKey = models.join("\0");
  useEffect(() => {
    if (!catalogModelKey) return;
    const ids = catalogModelKey.split("\0").filter(Boolean);
    let cancelled = false;
    apiPost<{ limits?: Record<string, { context_window?: number; max_output_tokens?: number }> }>(
      "/api/model-capability-cache/lookup",
      { ids },
    )
      .then((data) => {
        if (!cancelled) setCacheHints(data.limits ?? {});
      })
      .catch(() => {
        if (!cancelled) setCacheHints({});
      });
    return () => {
      cancelled = true;
    };
  }, [catalogModelKey, cacheRefreshedAt]);

  const filteredProviders = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return providers;
    return providers.filter((provider) => {
      const protocols = providerProtocols(provider);
      const binding = bindings.find((item) => item.credential_ref === provider.api_key_env);
      const haystack = [
        provider.name,
        provider.base_url,
        provider.api_key_env,
        provider.catalog?.default_model ?? "",
        protocols.join(" "),
        binding?.source.kind ?? "",
      ]
        .join(" ")
        .toLowerCase();
      return haystack.includes(needle);
    });
  }, [providers, query, bindings]);

  const configuredKeyCount = useMemo(
    () =>
      providers.filter((provider) =>
        bindings.some((binding) => binding.credential_ref === provider.api_key_env && !binding.disabled),
      ).length,
    [providers, bindings],
  );

  async function create() {
    if (saving) return;
    setSaving(true);
    setError(null);
    setSavedNotice(false);
    let created: ModelProvider | null = null;
    try {
      created = await apiPost<ModelProvider>("/api/model-providers", buildModelProviderPayload(form));
      await saveCredentialSource(created, form);
      setCreating(false);
      await load();
      setSelectedId(created.id);
      showSavedNotice();
    } catch (e) {
      const message = (e as Error).message;
      if (created) {
        setUnsavedApiKey({ providerId: created.id, value: form.api_key });
        setCreating(false);
        await load().catch(() => {});
        setSelectedId(created.id);
      }
      setError(message);
    } finally {
      setSaving(false);
    }
  }

  async function save() {
    if (!selected || saving) return;
    setSaving(true);
    setError(null);
    setSavedNotice(false);
    try {
      await apiPatch<ModelProvider>(`/api/model-providers/${encodeURIComponent(selected.id)}`, buildModelProviderPayload(form));
      await saveCredentialSource(selected, form);
      setUnsavedApiKey(null);
      await load();
      showSavedNotice();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function refreshCapabilityCache() {
    setCacheRefreshing(true);
    setError(null);
    try {
      const status = await apiPost<{ refreshed_at?: string }>("/api/model-capability-cache/refresh");
      setCacheRefreshedAt(status.refreshed_at ?? "");
      setCacheHints({});
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setCacheRefreshing(false);
    }
  }

  async function refresh(provider: ModelProvider) {
    try {
      await apiPost<ModelProvider>(`/api/model-providers/${encodeURIComponent(provider.id)}/refresh-models`);
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function remove(provider: ModelProvider) {
    try {
      await apiDelete(`/api/model-providers/${encodeURIComponent(provider.id)}`);
      setSelectedId("");
      setCreating(true);
      setForm(emptyForm);
      await load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 409) {
        // The daemon blocks deletion while runtime profiles reference the
        // provider; offer an explicit detach delete with the profile names.
        const body = err.body as { profiles?: string[] } | undefined;
        setConfirmDetach({ provider, profiles: body?.profiles ?? [] });
        return;
      }
      setError(err.message);
    }
  }

  async function removeDetaching(provider: ModelProvider) {
    setError(null);
    try {
      await apiDelete(`/api/model-providers/${encodeURIComponent(provider.id)}?detach=true`);
      setSelectedId("");
      setCreating(true);
      setForm(emptyForm);
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <SettingsPageShell>
      <SettingsPageHeader
        className="mb-4 shrink-0"
        title="Model providers"
        eyebrow="Configuration"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" onClick={() => void load()} aria-label="Refresh model providers">
              <RefreshCw className="h-4 w-4" /> Refresh
            </Button>
            <Button onClick={startCreate} aria-label="New provider">
              <Plus className="h-4 w-4" /> New provider
            </Button>
          </div>
        }
      />
      {error && <SettingsAlert className="mb-3 shrink-0">{error}</SettingsAlert>}

      <SettingsSplitLayout data-testid="model-providers-settings-layout" fill>
        <ModelProviderListColumn
          providers={providers}
          query={query}
          setQuery={setQuery}
          configuredKeyCount={configuredKeyCount}
          startCreate={startCreate}
          filteredProviders={filteredProviders}
          selectedId={selectedId}
          creating={creating}
          bindings={bindings}
          setCreating={setCreating}
          setSelectedId={setSelectedId}
          setSavedNotice={setSavedNotice}
        />

        <SettingsDetailPane
          data-testid="model-providers-settings-detail"
          header={
            <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0">
                <h3 className="font-medium">
                  {!loaded ? "Loading…" : creating ? "New provider" : selected ? selected.name : "Provider details"}
                </h3>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  {!loaded
                    ? "Loading providers…"
                    : creating
                      ? "Define endpoints, protocols, and an API key for launches."
                      : "Update endpoints, catalog, and credential binding."}
                </p>
                {!creating && selected && (
                  <div className="mt-2 flex flex-wrap items-center gap-1.5">
                    <Chip variant="neutral" className="font-mono">
                      {selected.api_key_env}
                    </Chip>
                    {selected.catalog?.default_model && (
                      <Chip variant="signal">default · {selected.catalog.default_model}</Chip>
                    )}
                    {selectedBinding && !selectedBinding.disabled && (
                      <Chip variant="success">key set</Chip>
                    )}
                  </div>
                )}
              </div>
              {creating && providers.length > 0 && (
                <Button variant="ghost" size="icon-sm" onClick={() => {
                  setCreating(false);
                  if (providers[0]) setSelectedId(providers[0].id);
                }} aria-label="Cancel new provider">
                  <X className="h-4 w-4" />
                </Button>
              )}
            </div>
          }
          bodyClassName="space-y-5 px-6 py-5"
        >
          {!loaded ? (
            <div role="status" aria-label="Loading model providers" className="flex items-center justify-center py-12 text-sm text-muted-foreground">
              <LoaderCircle className="mr-2 h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              Loading providers…
            </div>
          ) : (
            <>
              <ModelProviderConnectionSection
                form={form}
                setForm={setForm}
                selected={selected}
                selectedBinding={selectedBinding}
                endpointErrors={endpointErrors}
              />

              <ModelProviderCatalogSection
                selected={selected}
                creating={creating}
                refresh={refresh}
                refreshCapabilityCache={refreshCapabilityCache}
                cacheRefreshing={cacheRefreshing}
                form={form}
                setForm={setForm}
                models={models}
                cacheRefreshedAt={cacheRefreshedAt}
                cacheHints={cacheHints}
              />

              <div className="flex items-center justify-between">
                <SaveActionButton
                  label={creating ? "Create provider" : "Save provider"}
                  pending={saving}
                  saved={savedNotice}
                  disabled={!canSubmit}
                  onClick={() => void (creating ? create() : save())}
                />
                {selected && !creating && (
                  <Button
                    variant="outline"
                    className="border-destructive/30 text-destructive hover:bg-destructive/5"
                    onClick={() => setConfirmDelete(selected)}
                  >
                    <Trash2 className="h-4 w-4" /> Delete
                  </Button>
                )}
              </div>
            </>
          )}
        </SettingsDetailPane>
      </SettingsSplitLayout>
      <ConfirmDialog
        open={confirmDelete !== null}
        title={confirmDelete ? `Delete model provider ${confirmDelete.name}?` : "Delete model provider?"}
        description="This removes the provider and its endpoints. API key env vars stay in the credential library."
        confirmLabel="Delete"
        destructive
        onConfirm={() => {
          const provider = confirmDelete;
          setConfirmDelete(null);
          if (provider) void remove(provider);
        }}
        onCancel={() => setConfirmDelete(null)}
      />
      {confirmDetach && (
        <ConfirmDialog
          open
          title={`Delete model provider ${confirmDetach.provider.name}?`}
          description={`This provider is referenced by runtime profile${confirmDetach.profiles.length === 1 ? "" : "s"} ${confirmDetach.profiles.join(", ") || "(unknown)"}. Deleting clears the model provider reference from ${confirmDetach.profiles.length === 1 ? "this profile" : "these profiles"}; other profile settings stay unchanged.`}
          confirmLabel="Clear references and delete"
          destructive
          onConfirm={() => {
            const target = confirmDetach;
            setConfirmDetach(null);
            if (target) void removeDetaching(target.provider);
          }}
          onCancel={() => setConfirmDetach(null)}
        />
      )}
    </SettingsPageShell>
  );
}

async function saveCredentialSource(provider: ModelProvider, form: Form) {
  const value = form.api_key.trim();
  if (!value || value === "[configured]") return;
  await apiPut("/api/credential-bindings", {
    credential_ref: provider.api_key_env,
    // destination_env equals the generated env var name so the binding
    // projects under the provider key the runtime expects.
    source: { kind: "literal", value, destination_env: provider.api_key_env },
  });
}

function catalogModels(provider: ModelProvider): string[] {
  return Array.from(new Set([...(provider.catalog?.manual ?? []), ...(provider.catalog?.refreshed ?? [])])).sort();
}
