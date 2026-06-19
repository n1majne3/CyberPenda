import { useEffect, useState } from "react";
import { apiGet, apiPut, apiDelete, type CredentialBinding, type RuntimeProfile } from "@/lib/api";
import { Button, Card, Input, Label, Badge } from "@/components/ui";
import { Trash2, Plus, Ban } from "lucide-react";

export function CredentialBindingsPage() {
  const [bindings, setBindings] = useState<CredentialBinding[]>([]);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ credential_ref: "", kind: "env", value: "" });

  async function load() {
    try {
      const [d, p] = await Promise.all([
        apiGet<{ bindings: CredentialBinding[] }>("/api/credential-bindings"),
        apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles"),
      ]);
      setBindings(d.bindings ?? []);
      setProfiles(p.profiles ?? []);
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

  return (
    <div className="p-8 max-w-4xl">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-xl font-semibold">Credential bindings</h2>
          <p className="text-xs text-muted-foreground">Global bindings. Project overrides live on each project dashboard.</p>
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
              <select
                className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={form.kind}
                onChange={(e) => setForm({ ...form, kind: e.target.value })}
              >
                <option value="env">env</option>
                <option value="file">file</option>
                <option value="command">command</option>
              </select>
            </div>
            <div>
              <Label>Value (env var name / path / command)</Label>
              <Input value={form.value} onChange={(e) => setForm({ ...form, value: e.target.value })} placeholder="CODEX_API_KEY" />
            </div>
          </div>
          <Button size="sm" onClick={create} disabled={!form.credential_ref.trim() || !form.value.trim()}>Create</Button>
        </Card>
      )}

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      <div className="space-y-2">
        {bindings.map((b) => (
          <Card key={b.id} className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Badge variant={b.disabled ? "destructive" : "primary"}>{b.scope}</Badge>
              <span className="font-medium font-mono text-sm">{b.credential_ref}</span>
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
        ))}
        {bindings.length === 0 && !error && (
          <p className="text-sm text-muted-foreground">No global bindings yet.</p>
        )}
      </div>
    </div>
  );
}
