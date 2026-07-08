import { useEffect, useRef, useState } from "react";
import { RefreshCw, Trash2 } from "lucide-react";
import { apiDelete, apiGet, apiPatch, apiPost, apiPut, type CredentialBinding, type ModelProvider } from "@/lib/api";
import { Button, Input, Label, Select, Textarea, Badge } from "@/components/ui";
import { SaveActionButton } from "@/components/SaveActionButton";
import {
  PageContainer,
  SettingsAlert,
  SettingsListPanel,
  SettingsPageHeader,
  SettingsPanel,
  SettingsSplitLayout,
} from "@/components/shared";
import { settingsListItemClasses } from "@/components/sharedStyles";
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

  // Compute the candidate model list inline. It's a cheap derivation and the
  // React Compiler cannot preserve a useMemo over the selected provider (which
  // may be mutated later), so manual memoization would be skipped anyway.
  const baseModels = selected ? catalogModels(selected) : [];
  const manualModels = splitLines(form.manual_models);
  const extraModels = form.default_model ? [form.default_model] : [];
  const models = Array.from(new Set([...baseModels, ...manualModels, ...extraModels])).sort();

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
    <PageContainer className="max-w-6xl">
      <SettingsPageHeader
        title="Model providers"
        description="Reusable model endpoints, supported protocols, catalogs, and generated API key env vars."
        actions={
        <Button variant="outline" onClick={() => { setCreating(true); setSelectedId(""); setForm(emptyForm); }}>
          New provider
        </Button>
        }
      />
      {error && <SettingsAlert>{error}</SettingsAlert>}

      <SettingsSplitLayout data-testid="model-providers-settings-layout">
        <SettingsListPanel data-testid="model-providers-settings-list" className="space-y-2">
          {providers.length === 0 && <p className="text-sm text-muted-foreground">No model providers yet.</p>}
          {providers.map((provider) => (
            <button
              type="button"
              key={provider.id}
              aria-pressed={selectedId === provider.id && !creating}
              aria-current={selectedId === provider.id && !creating ? "true" : undefined}
              className={settingsListItemClasses(selectedId === provider.id && !creating)}
              onClick={() => { setCreating(false); setSelectedId(provider.id); }}
            >
              <span className="block font-medium">{provider.name}</span>
              <span className="block truncate font-mono text-[11px] text-muted-foreground">{provider.api_key_env}</span>
            </button>
          ))}
        </SettingsListPanel>

        <SettingsPanel data-testid="model-providers-settings-detail" className="space-y-4">
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
                onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                placeholder="https://api.example.test/v1…"
                autoComplete="off"
                spellCheck={false}
              />
            </div>
          </div>

          <div className="space-y-3 rounded-lg border border-border bg-muted/30 p-3">
            <div>
              <Label htmlFor="provider-api-key">API key</Label>
              <p className="mt-1 text-[11px] text-muted-foreground">
                Stored as a local credential for <code>{selected?.api_key_env ?? "the generated provider key"}</code>. The secret is never shown again.
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

          <fieldset>
            <legend className="text-sm font-medium leading-none text-muted-foreground">Supported protocols</legend>
            <div className="mt-2 flex flex-wrap gap-2">
              {PROTOCOLS.map((protocol) => (
                <label key={protocol} className="inline-flex items-center gap-2 rounded-md border border-border px-2 py-1 text-sm">
                  <input
                    type="checkbox"
                    name="protocols"
                    checked={form.protocols.includes(protocol)}
                    onChange={(e) => setForm(toggleProtocol(form, protocol, e.target.checked))}
                  />
                  {protocol}
                </label>
              ))}
            </div>
          </fieldset>

          {form.protocols.length > 0 && (
            <fieldset className="space-y-3">
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
                    />
                    {endpointErrors[protocol] && (
                      <p className="mt-1 text-[11px] text-destructive">{endpointErrors[protocol]}</p>
                    )}
                  </div>
                ))}
              </div>
            </fieldset>
          )}

          <div className="grid gap-3 md:grid-cols-2">
            <div>
              <Label htmlFor="provider-manual-models">Manual models</Label>
              <Textarea
                id="provider-manual-models"
                name="manual_models"
                value={form.manual_models}
                onChange={(e) => setForm({ ...form, manual_models: e.target.value })}
                placeholder="mimo-v2.5-pro…"
                rows={5}
                autoComplete="off"
                spellCheck={false}
              />
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
                  <div>Generated API key env: <code>{selected.api_key_env}</code></div>
                  <div className="flex flex-wrap gap-1">
                    {(selected.catalog?.refreshed ?? []).map((model) => <Badge key={model} variant="outline">{model}</Badge>)}
                  </div>
                </div>
              )}
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <SaveActionButton
              label={creating ? "Create provider" : "Save provider"}
              pending={saving}
              saved={savedNotice}
              disabled={!canSubmit}
              onClick={() => void (creating ? create() : save())}
            />
            {selected && <Button variant="outline" onClick={() => refresh(selected)}><RefreshCw className="h-4 w-4" /> Refresh models</Button>}
            {selected && <Button variant="destructive" onClick={() => remove(selected)}><Trash2 className="h-4 w-4" /> Delete</Button>}
          </div>
        </SettingsPanel>
      </SettingsSplitLayout>
    </PageContainer>
  );
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
