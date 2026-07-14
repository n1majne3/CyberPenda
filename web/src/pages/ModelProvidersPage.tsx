import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { KeyRound, Plus, RefreshCw, Server, Trash2, X } from "lucide-react";
import { apiDelete, apiGet, apiPatch, apiPost, apiPut, type CredentialBinding, type ModelProvider } from "@/lib/api";
import { Button, Input, Label, Select, Textarea, Badge } from "@/components/ui";
import { SaveActionButton } from "@/components/SaveActionButton";
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
} from "@/components/settingsLibrary";
import { settingsListItemClasses } from "@/components/sharedStyles";
import { cn } from "@/lib/utils";
import {
  buildModelProviderPayload,
  canSubmitModelProvider,
  endpointBaseURLForProtocol,
  endpointValidationErrors,
  providerApiKeyPlaceholder,
  providerToModelProviderForm,
  type ModelProviderForm,
} from "./modelProviderForm";

const PROTOCOLS = [
  "openai_chat_completions",
  "openai_responses",
  "anthropic_messages",
] as const;

const PROTOCOL_LABELS: Record<string, string> = {
  openai_chat_completions: "OpenAI Chat Completions",
  openai_responses: "OpenAI Responses",
  anthropic_messages: "Anthropic Messages",
};

type Form = ModelProviderForm;

const emptyForm: Form = {
  name: "",
  base_url: "",
  protocols: [],
  endpoint_base_urls: {},
  manual_models: "",
  default_model: "",
  api_key: "",
};

export function ModelProvidersPage() {
  const [providers, setProviders] = useState<ModelProvider[]>([]);
  const [bindings, setBindings] = useState<CredentialBinding[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [form, setForm] = useState<Form>(emptyForm);
  const [creating, setCreating] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [savedNotice, setSavedNotice] = useState(false);
  const [query, setQuery] = useState("");
  const savedNoticeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const selected = providers.find((p) => p.id === selectedId) ?? null;
  const selectedBinding = selected ? bindings.find((binding) => binding.credential_ref === selected.api_key_env) : undefined;
  const canSubmit = canSubmitModelProvider(form, creating);
  const endpointErrors = endpointValidationErrors(form);

  // When the selection (or its loaded binding) changes, reset the form to match
  // the selected provider. React allows adjusting state during render by
  // comparing against a stored previous value, which avoids a setState-in-effect.
  const formSelectionKey = selected && !creating
    ? `${selected.id}:${selected.updated_at ?? ""}:${selectedBinding?.updated_at ?? ""}`
    : "";
  const [lastFormKey, setLastFormKey] = useState("");
  if (lastFormKey !== formSelectionKey) {
    setLastFormKey(formSelectionKey);
    if (selected && !creating) {
      setForm(providerToModelProviderForm(selected, selectedBinding));
    } else if (formSelectionKey === "" && form !== emptyForm) {
      setForm(emptyForm);
    }
  }

  async function load() {
    try {
      const [data, credentialData] = await Promise.all([
        apiGet<{ providers: ModelProvider[] }>("/api/model-providers"),
        apiGet<{ bindings: CredentialBinding[] }>("/api/credential-bindings"),
      ]);
      const loaded = data.providers ?? [];
      setBindings(credentialData.bindings ?? []);
      setProviders(loaded);
      setSelectedId((current) => current && loaded.some((p) => p.id === current) ? current : loaded[0]?.id ?? "");
      setCreating(loaded.length === 0);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  // Initial load. load() is async, so every setState it dispatches runs after an
  // await (never synchronous in the effect body); the rule cannot prove that, so
  // it is suppressed for this canonical fetch-on-mount pattern.
  useEffect(() => {
    /* eslint-disable react-hooks/set-state-in-effect */
    void load();
    /* eslint-enable react-hooks/set-state-in-effect */
  }, []);

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
    try {
      const created = await apiPost<ModelProvider>("/api/model-providers", buildModelProviderPayload(form));
      await saveCredentialSource(created, form);
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

  async function save() {
    if (!selected || saving) return;
    setSaving(true);
    setError(null);
    setSavedNotice(false);
    try {
      await apiPatch<ModelProvider>(`/api/model-providers/${encodeURIComponent(selected.id)}`, buildModelProviderPayload(form));
      await saveCredentialSource(selected, form);
      await load();
      showSavedNotice();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
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
    if (!window.confirm(`Delete model provider ${provider.name}?`)) return;
    try {
      await apiDelete(`/api/model-providers/${encodeURIComponent(provider.id)}`);
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
        description="Reusable model endpoints, supported protocols, catalogs, and generated API key env vars."
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
        <SettingsListColumn data-testid="model-providers-settings-list">
          <SettingsPanel className="gap-3 p-3 lg:shrink-0">
            <div className="flex items-baseline justify-between gap-2">
              <div>
                <p className="text-sm font-medium">Providers</p>
                <p className="mt-0.5 text-[11px] text-muted-foreground">
                  Select one to edit endpoints and catalog
                </p>
              </div>
              <div className="shrink-0 text-right tabular-nums">
                <span className="text-lg font-semibold tracking-tight">{providers.length}</span>
                <span className="ml-1 text-[11px] text-muted-foreground">total</span>
              </div>
            </div>

            <SettingsSearchField
              id="model-providers-search"
              name="model_providers_search"
              value={query}
              onChange={setQuery}
              placeholder="Search name, URL, key…"
              size="sm"
              aria-label="Search model providers"
            />

            {providers.length > 0 && (
              <p className="text-[11px] text-muted-foreground">
                <span className="tabular-nums font-medium text-foreground">{configuredKeyCount}</span>
                {" "}with API key binding
              </p>
            )}
          </SettingsPanel>

          <SettingsPanel className="gap-1 p-2 lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain">
            {providers.length === 0 ? (
              <div className="flex flex-col items-center gap-3 px-2 py-8 text-center">
                <div className="flex h-10 w-10 items-center justify-center rounded-full bg-muted">
                  <Server className="h-4 w-4 text-muted-foreground" />
                </div>
                <p className="text-sm text-muted-foreground">No model providers yet.</p>
                <Button size="sm" onClick={startCreate}>
                  <Plus className="h-3.5 w-3.5" /> New provider
                </Button>
              </div>
            ) : filteredProviders.length === 0 ? (
              <div className="px-2 py-8 text-center">
                <p className="text-sm font-medium">No matching providers</p>
                <p className="mt-1 text-xs text-muted-foreground">Try a different search.</p>
                <Button size="sm" variant="outline" className="mt-3" onClick={() => setQuery("")}>
                  Clear search
                </Button>
              </div>
            ) : (
              <ul className="space-y-1">
                {filteredProviders.map((provider) => {
                  const isSelected = selectedId === provider.id && !creating;
                  const protocols = providerProtocols(provider);
                  const binding = bindings.find((item) => item.credential_ref === provider.api_key_env);
                  const displayUrl = providerDisplayURL(provider);
                  return (
                    <li key={provider.id}>
                      <button
                        type="button"
                        aria-pressed={isSelected}
                        aria-current={isSelected ? "true" : undefined}
                        className={settingsListItemClasses(isSelected, "w-full px-2.5 py-2.5")}
                        onClick={() => {
                          setCreating(false);
                          setSelectedId(provider.id);
                          setSavedNotice(false);
                        }}
                      >
                        <span className="flex items-start justify-between gap-2">
                          <span className="min-w-0">
                            <span className="block truncate font-medium text-foreground">{provider.name}</span>
                            {displayUrl ? (
                              <span className="mt-0.5 block truncate font-mono text-[11px] opacity-70">
                                {displayUrl}
                              </span>
                            ) : (
                              <span className="mt-0.5 block font-mono text-[11px] opacity-70">
                                {provider.api_key_env}
                              </span>
                            )}
                          </span>
                          <KeyStatusBadge binding={binding} />
                        </span>
                        {protocols.length > 0 && (
                          <span className="mt-1.5 flex flex-wrap gap-1">
                            {protocols.slice(0, 2).map((protocol) => (
                              <Badge key={protocol} variant="outline" size="sm" className="font-normal opacity-80">
                                {shortProtocol(protocol)}
                              </Badge>
                            ))}
                            {protocols.length > 2 && (
                              <span className="text-[10px] opacity-60">+{protocols.length - 2}</span>
                            )}
                          </span>
                        )}
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
          </SettingsPanel>
        </SettingsListColumn>

        <SettingsDetailPane
          data-testid="model-providers-settings-detail"
          header={
            <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0">
                <h3 className="font-medium">
                  {creating ? "New provider" : selected ? selected.name : "Provider details"}
                </h3>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  {creating
                    ? "Define endpoints, protocols, and an API key for launches."
                    : "Update endpoints, catalog, and credential binding."}
                </p>
                {!creating && selected && (
                  <div className="mt-2 flex flex-wrap items-center gap-1.5">
                    <Badge variant="outline" size="sm" className="font-mono font-normal">
                      {selected.api_key_env}
                    </Badge>
                    {selected.catalog?.default_model && (
                      <Badge variant="primary" size="sm">
                        default · {selected.catalog.default_model}
                      </Badge>
                    )}
                    <KeyStatusBadge binding={selectedBinding} />
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
          footer={
            <>
              <SaveActionButton
                label={creating ? "Create provider" : "Save provider"}
                pending={saving}
                saved={savedNotice}
                disabled={!canSubmit}
                onClick={() => void (creating ? create() : save())}
              />
              {selected && !creating && (
                <Button variant="outline" onClick={() => refresh(selected)}>
                  <RefreshCw className="h-4 w-4" /> Refresh models
                </Button>
              )}
              {selected && !creating && (
                <Button variant="destructive" onClick={() => remove(selected)}>
                  <Trash2 className="h-4 w-4" /> Delete
                </Button>
              )}
            </>
          }
        >
            <section className="space-y-2.5">
              <SectionLabel>Identity</SectionLabel>
              <div className="grid gap-3 md:grid-cols-2">
                <div>
                  <Label htmlFor="provider-name">Name</Label>
                  <Input
                    id="provider-name"
                    name="provider_name"
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                    placeholder="MiMo…"
                    autoComplete="off"
                    spellCheck={false}
                  />
                </div>
                <div>
                  <Label htmlFor="provider-base-url">Base URL</Label>
                  <Input
                    id="provider-base-url"
                    name="base_url"
                    type="url"
                    inputMode="url"
                    value={form.base_url}
                    onChange={(e) => setForm({
                      ...form,
                      base_url: e.target.value,
                      endpoint_base_urls: {},
                    })}
                    placeholder="https://api.example.test/v1…"
                    autoComplete="off"
                    spellCheck={false}
                  />
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    Shared base used to derive protocol endpoints in quick setup.
                  </p>
                </div>
              </div>
            </section>

            <section className="space-y-2.5 rounded-lg border border-border bg-muted/30 p-3">
              <div className="flex items-start gap-2">
                <KeyRound className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1 space-y-2.5">
                  <div>
                    <Label htmlFor="provider-api-key">API key</Label>
                    <p className="mt-1 text-[11px] text-muted-foreground">
                      Stored as a local credential for{" "}
                      <code className="rounded bg-background/80 px-1 py-0.5">
                        {selected?.api_key_env ?? "the generated provider key"}
                      </code>
                      . The secret is never shown again.
                    </p>
                  </div>
                  <Input
                    id="provider-api-key"
                    name="api_key"
                    type="password"
                    value={form.api_key}
                    onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                    placeholder={providerApiKeyPlaceholder(selectedBinding)}
                    autoComplete="off"
                    spellCheck={false}
                  />
                  {selectedBinding && (
                    <p className="text-[11px] text-muted-foreground">
                      {selectedBinding.source.kind === "literal"
                        ? "Current API key: [configured]"
                        : <>Current source: {selectedBinding.source.kind}: {selectedBinding.source.value}. Enter an API key here to replace it with a local credential.</>}
                    </p>
                  )}
                </div>
              </div>
            </section>

            <section className="space-y-2.5">
              <SectionLabel>Protocols & endpoints</SectionLabel>
              <fieldset>
                <legend className="text-sm font-medium leading-none text-muted-foreground">Supported protocols</legend>
                <div className="mt-2 flex flex-wrap gap-2">
                  {PROTOCOLS.map((protocol) => (
                    <label
                      key={protocol}
                      className={cn(
                        "inline-flex cursor-pointer items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm transition-colors",
                        form.protocols.includes(protocol)
                          ? "border-foreground/20 bg-foreground/5"
                          : "border-border hover:bg-muted/50",
                      )}
                    >
                      <input
                        type="checkbox"
                        name="protocols"
                        checked={form.protocols.includes(protocol)}
                        onChange={(e) => setForm(toggleProtocol(form, protocol, e.target.checked))}
                        className="accent-foreground"
                      />
                      <span>
                        <span className="sr-only">{protocol}</span>
                        <span aria-hidden="true">{PROTOCOL_LABELS[protocol] ?? protocol}</span>
                      </span>
                    </label>
                  ))}
                </div>
              </fieldset>

              {form.protocols.length > 0 && (
                <fieldset className="space-y-2.5">
                  <legend className="text-sm font-medium leading-none text-muted-foreground">Endpoint base URLs</legend>
                  <div className="grid gap-3 md:grid-cols-2">
                    {form.protocols.map((protocol) => (
                      <div key={protocol}>
                        <Label htmlFor={`provider-endpoint-${protocol}`}>{protocol} endpoint base URL</Label>
                        <Input
                          id={`provider-endpoint-${protocol}`}
                          name={`endpoint_${protocol}`}
                          type="url"
                          inputMode="url"
                          value={endpointBaseURLForProtocol(form, protocol)}
                          onChange={(e) => setForm({
                            ...form,
                            endpoint_base_urls: {
                              ...form.endpoint_base_urls,
                              [protocol]: e.target.value,
                            },
                          })}
                          placeholder="https://api.example.test/v1"
                          autoComplete="off"
                          spellCheck={false}
                          className="font-mono text-xs"
                        />
                        {endpointErrors[protocol] && (
                          <p className="mt-1 text-[11px] text-destructive">{endpointErrors[protocol]}</p>
                        )}
                      </div>
                    ))}
                  </div>
                </fieldset>
              )}
            </section>

            <section className="space-y-2.5">
              <SectionLabel>Catalog</SectionLabel>
              <div className="grid gap-3 md:grid-cols-2">
                <div>
                  <Label htmlFor="provider-manual-models">Manual models</Label>
                  <Textarea
                    id="provider-manual-models"
                    name="manual_models"
                    value={form.manual_models}
                    onChange={(e) => setForm({ ...form, manual_models: e.target.value })}
                    placeholder="mimo-v2.5-pro…"
                    rows={3}
                    autoComplete="off"
                    spellCheck={false}
                    className="min-h-[72px] font-mono text-xs"
                  />
                  <p className="mt-1 text-[11px] text-muted-foreground">One model id per line.</p>
                </div>
                <div>
                  <Label htmlFor="provider-default-model">Default model</Label>
                  <Select
                    id="provider-default-model"
                    name="default_model"
                    value={form.default_model}
                    onChange={(e) => setForm({ ...form, default_model: e.target.value })}
                  >
                    <option value="">No default</option>
                    {models.map((model) => <option key={model} value={model}>{model}</option>)}
                  </Select>
                  {selected && (
                    <div className="mt-3 space-y-2 text-sm">
                      <div className="text-xs text-muted-foreground">
                        Generated API key env:{" "}
                        <code className="rounded bg-muted px-1 py-0.5 font-mono text-[11px] text-foreground">
                          {selected.api_key_env}
                        </code>
                      </div>
                      {(selected.catalog?.refreshed ?? []).length > 0 && (
                        <div>
                          <p className="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                            Refreshed catalog
                          </p>
                          <div className="flex flex-wrap gap-1">
                            {(selected.catalog?.refreshed ?? []).map((model) => (
                              <Badge key={model} variant="outline" size="sm" className="font-mono font-normal">
                                {model}
                              </Badge>
                            ))}
                          </div>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              </div>
            </section>
        </SettingsDetailPane>
      </SettingsSplitLayout>
    </SettingsPageShell>
  );
}

function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <h4 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
      {children}
    </h4>
  );
}

function KeyStatusBadge({ binding }: { binding?: CredentialBinding }) {
  if (!binding || binding.disabled) {
    return (
      <Badge variant="outline" size="sm" className="shrink-0 font-normal text-muted-foreground">
        no key
      </Badge>
    );
  }
  if (binding.source.kind === "literal") {
    return (
      <Badge variant="success" size="sm" className="shrink-0 font-normal">
        key set
      </Badge>
    );
  }
  return (
    <Badge variant="info" size="sm" className="shrink-0 font-normal">
      {binding.source.kind}
    </Badge>
  );
}

function providerProtocols(provider: ModelProvider): string[] {
  if (provider.endpoints?.length) {
    return provider.endpoints.map((endpoint) => endpoint.protocol);
  }
  return provider.protocols ?? [];
}

function providerDisplayURL(provider: ModelProvider): string {
  if (provider.base_url) return provider.base_url;
  return provider.endpoints?.[0]?.base_url ?? "";
}

function shortProtocol(protocol: string): string {
  if (protocol === "openai_chat_completions") return "chat";
  if (protocol === "openai_responses") return "responses";
  if (protocol === "anthropic_messages") return "anthropic";
  return protocol;
}

async function saveCredentialSource(provider: ModelProvider, form: Form) {
  const value = form.api_key.trim();
  if (!value || value === "[configured]") return;
  await apiPut("/api/credential-bindings", {
    credential_ref: provider.api_key_env,
    source: { kind: "literal", value },
  });
}

function catalogModels(provider: ModelProvider): string[] {
  return Array.from(new Set([...(provider.catalog?.manual ?? []), ...(provider.catalog?.refreshed ?? [])])).sort();
}

function splitLines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function toggle(values: string[], value: string, checked: boolean): string[] {
  const next = new Set(values);
  if (checked) next.add(value);
  else next.delete(value);
  return Array.from(next);
}

function toggleProtocol(form: Form, protocol: string, checked: boolean): Form {
  const endpointBaseURLs = { ...form.endpoint_base_urls };
  if (!checked) delete endpointBaseURLs[protocol];
  return {
    ...form,
    protocols: toggle(form.protocols, protocol, checked),
    endpoint_base_urls: endpointBaseURLs,
  };
}
