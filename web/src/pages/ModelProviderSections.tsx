import type { SetStateAction, Dispatch } from "react";
import { RefreshCw, Zap } from "lucide-react";
import type { ModelProvider, CredentialBinding } from "@/lib/api";
import { Button, Input, Label, Select, Textarea, Badge } from "@/components/ui";
import { SectionLabel } from "@/components/shared";
import type { ModelProviderForm } from "./modelProviderForm";
import { endpointBaseURLForProtocol, providerApiKeyPlaceholder } from "./modelProviderForm";
import { PROTOCOLS, toggleProtocol, PROTOCOL_LABELS } from "./modelProviderProtocols";

export function ModelProviderCatalogSection({
  selected,
  creating,
  refresh,
  refreshCapabilityCache,
  cacheRefreshing,
  form,
  setForm,
  models,
  cacheRefreshedAt,
  cacheHints,
}: {
  selected: ModelProvider | null;
  creating: boolean;
  refresh: (provider: ModelProvider) => Promise<void>;
  refreshCapabilityCache: () => Promise<void>;
  cacheRefreshing: boolean;
  form: ModelProviderForm;
  setForm: Dispatch<SetStateAction<ModelProviderForm>>;
  models: string[];
  cacheRefreshedAt: string;
  cacheHints: Record<string, { context_window?: number | undefined; max_output_tokens?: number | undefined; }>;
}) {
  return (
    <section className="rounded-lg border border-border bg-card shadow-sm">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <span className="text-sm font-medium">Catalog</span>
        <div className="flex items-center gap-1.5">
          {selected && !creating && (
            <Button variant="outline" size="sm" onClick={() => refresh(selected)}>
              <RefreshCw className="h-3 w-3" /> Refresh models
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={() => void refreshCapabilityCache()} disabled={cacheRefreshing}>
            <Zap className={`h-3 w-3 ${cacheRefreshing ? "animate-spin motion-reduce:animate-none" : ""}`} />
            Refresh capability cache
          </Button>
        </div>
      </div>
      <div className="grid gap-3 p-4 md:grid-cols-2">
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
          <p className="mt-1 text-[11px] text-muted-foreground">One model id per line. Empty window/max fields follow the models.dev cache.</p>
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
          {models.length > 0 && (
            <div className="mt-3 space-y-2">
              {cacheRefreshedAt && (
                <p className="text-[11px] text-muted-foreground">
                  Capability cache updated {cacheRefreshedAt}.
                </p>
              )}
              {models.map((model) => {
                const override = form.limits?.[model] ?? {};
                const hint = cacheHints[model];
                return (
                  <div key={model} className="grid grid-cols-2 gap-2">
                    <div className="col-span-2 text-[11px] font-mono text-muted-foreground">{model}</div>
                    <Input
                      inputMode="numeric"
                      placeholder={hint?.context_window ? String(hint.context_window) : "Context window"}
                      value={override.context_window ?? ""}
                      onChange={(e) => setForm({
                        ...form,
                        limits: { ...(form.limits ?? {}), [model]: { ...override, context_window: e.target.value } },
                      })}
                      aria-label={`${model} context window`}
                    />
                    <Input
                      inputMode="numeric"
                      placeholder={hint?.max_output_tokens ? String(hint.max_output_tokens) : "Max output"}
                      value={override.max_output_tokens ?? ""}
                      onChange={(e) => setForm({
                        ...form,
                        limits: { ...(form.limits ?? {}), [model]: { ...override, max_output_tokens: e.target.value } },
                      })}
                      aria-label={`${model} max output tokens`}
                    />
                  </div>
                );
              })}
            </div>
          )}
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
                  <SectionLabel className="mb-1.5">Refreshed catalog</SectionLabel>
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
  );
}

export function ModelProviderConnectionSection({
  form,
  setForm,
  selected,
  selectedBinding,
  endpointErrors,
}: {
  form: ModelProviderForm;
  setForm: Dispatch<SetStateAction<ModelProviderForm>>;
  selected: ModelProvider | null;
  selectedBinding: CredentialBinding | undefined;
  endpointErrors: Record<string, string>;
}) {
  return (
    <section className="rounded-lg border border-border bg-card shadow-sm">
      <div className="border-b border-border px-4 py-3">
        <span className="text-sm font-medium">Connections and protocols</span>
      </div>
      <div className="grid gap-3 p-4 md:grid-cols-2">
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
        <div className="md:col-span-2">
          <Label htmlFor="provider-api-key">API key</Label>
          <p className="mt-1 text-[11px] text-muted-foreground">
            Stored as a local credential for{" "}
            <code className="rounded bg-muted px-1 py-0.5">
              {selected?.api_key_env ?? "the generated provider key"}
            </code>
            . The secret is never shown again.
          </p>
          <Input
            id="provider-api-key"
            name="api_key"
            type="password"
            value={form.api_key}
            onChange={(e) => setForm({ ...form, api_key: e.target.value })}
            placeholder={providerApiKeyPlaceholder(selectedBinding)}
            autoComplete="off"
            spellCheck={false}
            className="mt-1.5"
          />
          {selectedBinding && (
            <p className="mt-1 text-[11px] text-muted-foreground">
              {selectedBinding.source.kind === "literal"
                ? "Current API key: [configured]"
                : <>Current source: {selectedBinding.source.kind}: {selectedBinding.source.value}. Enter an API key here to replace it with a local credential.</>}
            </p>
          )}
        </div>
      </div>
      <div className="border-t border-border px-4 py-3">
        <span className="text-xs font-medium text-muted-foreground">Protocols &amp; endpoints</span>
        <fieldset className="mt-2 space-y-2">
          <legend className="sr-only">Supported protocols</legend>
          {PROTOCOLS.map((protocol) => {
            const enabled = form.protocols.includes(protocol);
            return (
              <div key={protocol} className="flex items-start gap-3">
                <input
                  type="checkbox"
                  id={`provider-protocol-${protocol}`}
                  name="protocols"
                  checked={enabled}
                  onChange={(e) => setForm(toggleProtocol(form, protocol, e.target.checked))}
                  className="mt-2 flex-none accent-foreground"
                />
                <Label
                  htmlFor={`provider-protocol-${protocol}`}
                  className="mt-1.5 w-[160px] flex-none cursor-pointer"
                >
                  <span className="sr-only">{protocol}</span>
                  <span aria-hidden="true">{PROTOCOL_LABELS[protocol] ?? protocol}</span>
                </Label>
                <div className="min-w-0 flex-1">
                  <Label htmlFor={`provider-endpoint-${protocol}`} className="sr-only">
                    {protocol} endpoint base URL
                  </Label>
                  <Input
                    id={`provider-endpoint-${protocol}`}
                    name={`endpoint_${protocol}`}
                    type="url"
                    inputMode="url"
                    value={enabled ? endpointBaseURLForProtocol(form, protocol) : ""}
                    onChange={(e) => setForm({
                      ...form,
                      endpoint_base_urls: {
                        ...form.endpoint_base_urls,
                        [protocol]: e.target.value,
                      },
                    })}
                    placeholder="Not configured"
                    disabled={!enabled}
                    autoComplete="off"
                    spellCheck={false}
                    className="font-mono text-xs"
                  />
                  {enabled && endpointErrors[protocol] && (
                    <p className="mt-1 text-[11px] text-destructive">{endpointErrors[protocol]}</p>
                  )}
                </div>
              </div>
            );
          })}
        </fieldset>
      </div>
    </section>
  );
}
