import { useEffect, useState } from "react";
import { apiGet, apiPut, apiDelete, type CredentialBinding } from "@/lib/api";
import { Button, Card, Input, Label, Badge } from "@/components/ui";
import { Trash2, Plus, Ban } from "lucide-react";

export function CredentialBindingsPage() {
  const [bindings, setBindings] = useState<CredentialBinding[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ credential_ref: "", kind: "env", value: "" });

  async function load() {
    try {
      const d = await apiGet<{ bindings: CredentialBinding[] }>("/api/credential-bindings");
      setBindings(d.bindings ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    load();
  }, []);

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
