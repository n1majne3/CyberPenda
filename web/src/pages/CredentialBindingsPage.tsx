import { useEffect, useState } from "react";
import { apiGet, apiPut, apiDelete, type CredentialBinding, type ModelProvider, type RuntimeProfile } from "@/lib/api";
import { Button, Input, Label, Badge, Select } from "@/components/ui";
import {
  PageContainer,
  SettingsAlert,
  SettingsListPanel,
  SettingsPageHeader,
  SettingsPanel,
  SettingsSplitLayout,
} from "@/components/shared";
import { Trash2, Plus, Ban } from "lucide-react";

export function CredentialBindingsPage() {
  const [bindings, setBindings] = useState<CredentialBinding[]>([]);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ credential_ref: "", kind: "env", value: "" });

  async function load() {
    try {
      const [d, p, providers] = await Promise.all([
        apiGet<{ bindings: CredentialBinding[] }>("/api/credential-bindings"),
        apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles"),
        apiGet<{ providers: ModelProvider[] }>("/api/model-providers"),
      ]);
      setBindings(d.bindings ?? []);
      setProfiles(p.profiles ?? []);
      setModelProviders(providers.providers ?? []);
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

  async function create() {
    try {
      await apiPut("/api/credential-bindings", {
        credential_ref: form.credential_ref,
        source: { kind: form.kind, value: form.value },
      });
      setForm({ credential_ref: "", kind: "env", value: "" });
      setCreating(false);
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function remove(id: string, credentialRef: string) {
    if (!window.confirm(`Delete credential binding ${credentialRef}?`)) return;
    try {
      await apiDelete(`/api/credential-bindings/${id}`);
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  function profilesUsingRef(ref: string): string[] {
    return profiles
      .filter((p) => (p.fields.credential_refs ?? []).includes(ref))
      .map((p) => p.name);
  }

  function modelProviderForRef(ref: string): ModelProvider | undefined {
    return modelProviders.find((provider) => provider.api_key_env === ref);
  }

  return (
    <PageContainer className="max-w-4xl">
      <SettingsPageHeader
        title="Credential bindings"
        description="Global credential sources. Project overrides stay on project dashboards; model provider API keys usually live with Model providers."
        actions={
        <Button size="sm" onClick={() => setCreating((v) => !v)}>
          <Plus className="h-4 w-4" /> New binding
        </Button>
        }
      />

      {error && <SettingsAlert>{error}</SettingsAlert>}

      <SettingsSplitLayout data-testid="credential-bindings-settings-layout">
        <SettingsListPanel data-testid="credential-bindings-settings-list" className="space-y-2">
          {bindings.map((b) => {
            const provider = modelProviderForRef(b.credential_ref);
            return (
              <div
                key={b.id}
                className="flex min-w-0 flex-col gap-3 rounded-md border border-border bg-background p-3 sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <Badge variant={b.disabled ? "destructive" : "primary"}>{b.scope}</Badge>
                  <span className="min-w-0 truncate font-mono text-sm font-medium">{b.credential_ref}</span>
                  {provider && (
                    <Badge variant="outline">model provider: {provider.name}</Badge>
                  )}
                  {b.disabled ? (
                    <Badge variant="destructive"><Ban className="h-3 w-3" />disabled</Badge>
                  ) : (
                    <Badge variant="outline">{b.source.kind}: {b.source.value}</Badge>
                  )}
                  {profilesUsingRef(b.credential_ref).length > 0 && (
                    <span className="text-xs text-muted-foreground">
                      profiles: {profilesUsingRef(b.credential_ref).join(", ")}
                    </span>
                  )}
                </div>
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label={`Delete ${b.credential_ref} binding`}
                  onClick={() => remove(b.id, b.credential_ref)}
                >
                  <Trash2 className="h-4 w-4 text-destructive" />
                </Button>
              </div>
            );
          })}
          {bindings.length === 0 && !error && (
            <p className="text-sm text-muted-foreground">No global bindings yet.</p>
          )}
        </SettingsListPanel>

        <SettingsPanel data-testid="credential-binding-create-panel" className="space-y-3">
          {creating ? (
            <>
              <div>
                <h3 className="text-sm font-medium">New binding</h3>
                <p className="mt-1 text-xs text-muted-foreground">
                  Reference an existing secret source without storing the value in the UI.
                </p>
              </div>
              <div>
                <Label htmlFor="credential-ref">Credential reference</Label>
                <Input
                  id="credential-ref"
                  name="credential_ref"
                  value={form.credential_ref}
                  onChange={(e) => setForm({ ...form, credential_ref: e.target.value })}
                  placeholder="codex-api-key…"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <Label htmlFor="credential-source-kind">Source kind</Label>
                  <Select
                    id="credential-source-kind"
                    name="source_kind"
                    value={form.kind}
                    onChange={(e) => setForm({ ...form, kind: e.target.value, value: "" })}
                  >
                    <option value="env">env</option>
                    <option value="literal">literal</option>
                    <option value="file">file</option>
                    <option value="command">command</option>
                  </Select>
                </div>
                <div>
                  <Label htmlFor="credential-source-value">
                    {form.kind === "env"
                      ? "Environment variable name"
                      : form.kind === "literal"
                        ? "Secret value"
                        : form.kind === "file"
                          ? "File path"
                          : "Command"}
                  </Label>
                  <Input
                    id="credential-source-value"
                    name="source_value"
                    type={form.kind === "literal" ? "password" : "text"}
                    value={form.value}
                    onChange={(e) => setForm({ ...form, value: e.target.value })}
                    placeholder={
                      form.kind === "env"
                        ? "OPENAI_API_KEY…"
                        : form.kind === "literal"
                          ? "sk-…"
                          : form.kind === "file"
                            ? "/path/to/secret…"
                            : "op read …"
                    }
                    autoComplete="off"
                    spellCheck={false}
                  />
                  {form.kind === "env" && (
                    <p className="mt-1 text-[11px] text-muted-foreground">
                      Use the environment variable name, not the secret.
                    </p>
                  )}
                </div>
              </div>
              <Button size="sm" onClick={create} disabled={!form.credential_ref.trim() || !form.value.trim()}>
                Create binding
              </Button>
            </>
          ) : (
            <div className="flex h-full min-h-[160px] items-center justify-center text-center text-sm text-muted-foreground">
              Create a binding when a runtime needs a credential ref outside model provider setup.
            </div>
          )}
        </SettingsPanel>
      </SettingsSplitLayout>
    </PageContainer>
  );
}
