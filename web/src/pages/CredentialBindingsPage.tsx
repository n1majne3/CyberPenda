import { useEffect, useState } from "react";
import { apiGet, apiPut, apiDelete, type CredentialBinding, type ModelProvider, type RuntimeProfile } from "@/lib/api";
import { Button, Card, Input, Label, Badge, Select } from "@/components/ui";
import { PageContainer } from "@/components/shared";
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

  async function remove(id: string) {
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
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-xl font-semibold">Credential bindings</h2>
          <p className="text-xs text-muted-foreground">
            Global bindings. Project overrides live on each project dashboard. Model provider API keys are usually configured on the Model providers page.
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating((v) => !v)}>
          <Plus className="h-4 w-4 mr-1" /> New binding
        </Button>
      </div>

      {creating && (
        <Card className="mb-4 space-y-3">
          <div>
            <Label>Credential reference</Label>
            <Input value={form.credential_ref} onChange={(e) => setForm({ ...form, credential_ref: e.target.value })} placeholder="codex-api-key" />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Source kind</Label>
              <Select
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
              <Label>
                {form.kind === "env"
                  ? "Environment variable name"
                  : form.kind === "literal"
                    ? "Secret value"
                    : form.kind === "file"
                      ? "File path"
                      : "Command"}
              </Label>
              <Input
                type={form.kind === "literal" ? "password" : "text"}
                value={form.value}
                onChange={(e) => setForm({ ...form, value: e.target.value })}
                placeholder={
                  form.kind === "env"
                    ? "OPENAI_API_KEY"
                    : form.kind === "literal"
                      ? "sk-..."
                      : form.kind === "file"
                        ? "/path/to/secret"
                        : "op read ..."
                }
                autoComplete="off"
              />
              {form.kind === "env" && (
                <p className="mt-1 text-[11px] text-muted-foreground">
                  Use the name of an existing environment variable, not the secret itself.
                </p>
              )}
            </div>
          </div>
          <Button size="sm" onClick={create} disabled={!form.credential_ref.trim() || !form.value.trim()}>Create</Button>
        </Card>
      )}

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      <div className="space-y-2">
        {bindings.map((b) => {
          const provider = modelProviderForRef(b.credential_ref);
          return (
          <Card key={b.id} className="flex-row items-center justify-between">
            <div className="flex items-center gap-2">
              <Badge variant={b.disabled ? "destructive" : "primary"}>{b.scope}</Badge>
              <span className="font-medium font-mono text-sm">{b.credential_ref}</span>
              {provider && (
                <Badge variant="outline">model provider: {provider.name}</Badge>
              )}
              {b.disabled ? (
                <Badge variant="destructive"><Ban className="h-3 w-3 mr-1" />disabled</Badge>
              ) : (
                <Badge variant="outline">{b.source.kind}: {b.source.value}</Badge>
              )}
              {profilesUsingRef(b.credential_ref).length > 0 && (
                <span className="text-xs text-muted-foreground">
                  profiles: {profilesUsingRef(b.credential_ref).join(", ")}
                </span>
              )}
            </div>
            <Button size="icon" variant="ghost" onClick={() => remove(b.id)}>
              <Trash2 className="h-4 w-4 text-destructive" />
            </Button>
          </Card>
        )})}
        {bindings.length === 0 && !error && (
          <p className="text-sm text-muted-foreground">No global bindings yet.</p>
        )}
      </div>
    </PageContainer>
  );
}
