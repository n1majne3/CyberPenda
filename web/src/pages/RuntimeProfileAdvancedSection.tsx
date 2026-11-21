import type { SetStateAction, Dispatch } from "react";
import { CheckCircle2, Plus, Trash2 } from "lucide-react";
import type { RuntimeExtension } from "@/lib/api";
import { Button, Input, Label, Badge, Textarea, Select } from "@/components/ui";
import { API_KEY_CONFIGURED, type RuntimeExtensionFormRef, type ProfileForm } from "./runtimeProfileEditorModel";

export function RuntimeProfileAdvancedSection({
  has,
  form,
  onChange,
  legacyModelFields,
  apiKeyPlaceholder,
  selectedExtensionID,
  setExtensionToAdd,
  availableExtensions,
  addRuntimeExtension,
  manualExtensionID,
  setManualExtensionID,
  addManualRuntimeExtension,
  canAddManualExtension,
  extensions,
  manualExtensionIncompatible,
  extensionByID,
  updateRuntimeExtension,
  removeRuntimeExtension,
  compatibleExtensions,
}: {
  has: (field: string) => boolean;
  form: ProfileForm;
  onChange: (form: ProfileForm) => void;
  legacyModelFields: boolean;
  apiKeyPlaceholder: string;
  selectedExtensionID: string;
  setExtensionToAdd: Dispatch<SetStateAction<string>>;
  availableExtensions: RuntimeExtension[];
  addRuntimeExtension: () => void;
  manualExtensionID: string;
  setManualExtensionID: Dispatch<SetStateAction<string>>;
  addManualRuntimeExtension: () => void;
  canAddManualExtension: boolean;
  extensions: RuntimeExtension[];
  manualExtensionIncompatible: boolean;
  extensionByID: Map<string, RuntimeExtension>;
  updateRuntimeExtension: (index: number, patch: Partial<RuntimeExtensionFormRef>) => void;
  removeRuntimeExtension: (index: number) => void;
  compatibleExtensions: RuntimeExtension[];
}) {
  return (
    <details className="rounded-lg border border-border bg-card">
      <summary className="cursor-pointer px-4 py-3 text-sm font-medium">Environment and advanced</summary>
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
    </details>
  );
}
