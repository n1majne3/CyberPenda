import type { ModelProvider, RuntimeExtension, RuntimePlugin } from "@/lib/api";
import { applyModelProviderSelection, displayReasoningEffort, REASONING_EFFORT_VALUES, isModelProviderCompatibleWithRuntime, modelProviderSupportedProtocols } from "@/pages/runtimeProfileForm";
import { cn } from "@/lib/utils";
import { Input, Label, Chip, Select } from "@/components/ui";
import { RUNNERS, type ProfileForm, defaultAPIKeyEnv, compatibleRuntimeExtensionRefs } from "./runtimeProfileEditorModel";

export function RuntimeProfileBasicsSection({
  form,
  onChange,
  plugins,
  extensions,
  providerOptions,
  selectableProviders,
  plugin,
  selectedModelProvider,
  compatibleProtocols,
  providerModels,
  has,
  legacyModelFields,
}: {
  form: ProfileForm;
  onChange: (form: ProfileForm) => void;
  plugins: RuntimePlugin[];
  extensions: RuntimeExtension[];
  providerOptions: RuntimePlugin[];
  selectableProviders: ModelProvider[];
  plugin: RuntimePlugin | undefined;
  selectedModelProvider: ModelProvider | undefined;
  compatibleProtocols: string[];
  providerModels: string[];
  has: (field: string) => boolean;
  legacyModelFields: boolean;
}) {
  return (
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
  );
}
