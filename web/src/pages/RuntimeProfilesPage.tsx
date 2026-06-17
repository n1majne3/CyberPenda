import { useEffect, useState } from "react";
import { apiGet, apiPost, apiPatch, apiDelete, type RuntimeProfile } from "@/lib/api";
import { Button, Card, Input, Label, Badge } from "@/components/ui";
import { Trash2, Plus } from "lucide-react";

const PROVIDERS = ["fake", "codex", "claude_code", "pi"];

export function RuntimeProfilesPage() {
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ name: "", provider: "fake", binary_path: "", model: "", credential_refs: "" });

  async function load() {
    try {
      const d = await apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles");
      setProfiles(d.profiles ?? []);
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
      await apiPost("/api/runtime-profiles", {
        name: form.name,
        provider: form.provider,
        fields: {
          binary_path: form.binary_path || undefined,
          model: form.model || undefined,
          credential_refs: form.credential_refs.split("\n").map((s) => s.trim()).filter(Boolean),
        },
      });
      setForm({ name: "", provider: "fake", binary_path: "", model: "", credential_refs: "" });
      setCreating(false);
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function remove(id: string) {
    try {
      await apiDelete(`/api/runtime-profiles/${id}`);
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function patchField(p: RuntimeProfile, field: "model" | "binary_path", value: string) {
    try {
      const fields = { ...p.fields, [field]: value || undefined };
      await apiPatch(`/api/runtime-profiles/${p.id}`, { fields });
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div className="p-8 max-w-4xl">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-semibold">Runtime profiles</h2>
        <Button size="sm" onClick={() => setCreating((v) => !v)}>
          <Plus className="h-4 w-4 mr-1" /> New profile
        </Button>
      </div>

      {creating && (
        <Card className="mb-4 space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Name</Label>
              <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Codex Default" />
            </div>
            <div>
              <Label>Provider</Label>
              <select
                className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={form.provider}
                onChange={(e) => setForm({ ...form, provider: e.target.value })}
              >
                {PROVIDERS.map((p) => (
                  <option key={p} value={p}>{p}</option>
                ))}
              </select>
            </div>
            <div>
              <Label>Binary path</Label>
              <Input value={form.binary_path} onChange={(e) => setForm({ ...form, binary_path: e.target.value })} placeholder="/usr/local/bin/codex" />
            </div>
            <div>
              <Label>Model</Label>
              <Input value={form.model} onChange={(e) => setForm({ ...form, model: e.target.value })} placeholder="gpt-5" />
            </div>
          </div>
          <div>
            <Label>Credential refs (one per line)</Label>
            <Input value={form.credential_refs} onChange={(e) => setForm({ ...form, credential_refs: e.target.value })} placeholder="codex-api-key" />
          </div>
          <div className="flex gap-2">
            <Button size="sm" onClick={create} disabled={!form.name.trim()}>Create</Button>
            <Button size="sm" variant="ghost" onClick={() => setCreating(false)}>Cancel</Button>
          </div>
        </Card>
      )}

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      <div className="space-y-3">
        {profiles.map((p) => (
          <Card key={p.id}>
            <div className="flex items-center justify-between mb-2">
              <div className="flex items-center gap-2">
                <span className="font-medium">{p.name}</span>
                <Badge variant="primary">{p.provider}</Badge>
              </div>
              <Button size="icon" variant="ghost" onClick={() => remove(p.id)}>
                <Trash2 className="h-4 w-4 text-destructive" />
              </Button>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <Label>Binary path</Label>
                <Input defaultValue={p.fields.binary_path ?? ""} onBlur={(e) => patchField(p, "binary_path", e.target.value)} />
              </div>
              <div>
                <Label>Model</Label>
                <Input defaultValue={p.fields.model ?? ""} onBlur={(e) => patchField(p, "model", e.target.value)} />
              </div>
            </div>
            {p.fields.credential_refs && p.fields.credential_refs.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {p.fields.credential_refs.map((r) => (
                  <Badge key={r} variant="outline">cred: {r}</Badge>
                ))}
              </div>
            )}
          </Card>
        ))}
        {profiles.length === 0 && !error && (
          <p className="text-sm text-muted-foreground">No profiles yet.</p>
        )}
      </div>
    </div>
  );
}
