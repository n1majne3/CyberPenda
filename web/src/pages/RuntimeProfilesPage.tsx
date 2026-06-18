import { useEffect, useState } from "react";
import { apiGet, apiPost, apiPatch, apiDelete, type RuntimeProfile } from "@/lib/api";
import { Button, Card, Input, Label, Badge, Textarea } from "@/components/ui";
import { Trash2, Plus } from "lucide-react";

const PROVIDERS = ["fake", "codex", "claude_code", "pi"];
const RUNNERS = ["sandbox", "host"];

type RuntimeProfileFields = RuntimeProfile["fields"];
type ProfileForm = {
  name: string;
  provider: string;
  binary_path: string;
  model: string;
  endpoint: string;
  custom_args: string;
  env: string;
  credential_refs: string;
  mcp_servers: string;
  default_runner: string;
};

const emptyForm: ProfileForm = {
  name: "",
  provider: "fake",
  binary_path: "",
  model: "",
  endpoint: "",
  custom_args: "",
  env: "",
  credential_refs: "",
  mcp_servers: "",
  default_runner: "sandbox",
};

export function RuntimeProfilesPage() {
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<ProfileForm>(emptyForm);

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
        fields: buildFields(form),
      });
      setForm(emptyForm);
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

  async function patchFields(p: RuntimeProfile, fields: RuntimeProfileFields) {
    try {
      await apiPatch(`/api/runtime-profiles/${p.id}`, { fields });
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function patchField(p: RuntimeProfile, field: keyof RuntimeProfileFields, value: RuntimeProfileFields[keyof RuntimeProfileFields]) {
    await patchFields(p, { ...p.fields, [field]: value || undefined });
  }

  async function patchMCPServers(p: RuntimeProfile, value: string) {
    try {
      await patchField(p, "mcp_servers", parseMCPServers(value));
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div className="p-8 max-w-6xl">
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
            <div>
              <Label>Endpoint</Label>
              <Input value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} placeholder="https://api.example.test/v1" />
            </div>
            <div>
              <Label>Default runner</Label>
              <select
                className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={form.default_runner}
                onChange={(e) => setForm({ ...form, default_runner: e.target.value })}
              >
                {RUNNERS.map((r) => (
                  <option key={r} value={r}>{r}</option>
                ))}
              </select>
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Custom args</Label>
              <Textarea value={form.custom_args} onChange={(e) => setForm({ ...form, custom_args: e.target.value })} placeholder="--json" />
            </div>
            <div>
              <Label>Environment</Label>
              <Textarea value={form.env} onChange={(e) => setForm({ ...form, env: e.target.value })} placeholder="CODEX_ENV=test" />
            </div>
            <div>
              <Label>Credential refs</Label>
              <Textarea value={form.credential_refs} onChange={(e) => setForm({ ...form, credential_refs: e.target.value })} placeholder="codex-api-key" />
            </div>
            <div>
              <Label>MCP servers JSON</Label>
              <Textarea
                value={form.mcp_servers}
                onChange={(e) => setForm({ ...form, mcp_servers: e.target.value })}
                placeholder='[{"name":"project","mode":"trusted","url":"http://127.0.0.1:8787/mcp"}]'
              />
            </div>
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
              <div>
                <Label>Endpoint</Label>
                <Input defaultValue={p.fields.endpoint ?? ""} onBlur={(e) => patchField(p, "endpoint", e.target.value)} />
              </div>
              <div>
                <Label>Default runner</Label>
                <select
                  className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                  defaultValue={p.fields.default_runner ?? "sandbox"}
                  onBlur={(e) => patchField(p, "default_runner", e.target.value)}
                >
                  {RUNNERS.map((r) => (
                    <option key={r} value={r}>{r}</option>
                  ))}
                </select>
              </div>
              <div>
                <Label>Custom args</Label>
                <Textarea
                  defaultValue={(p.fields.custom_args ?? []).join("\n")}
                  onBlur={(e) => patchField(p, "custom_args", splitLines(e.target.value))}
                />
              </div>
              <div>
                <Label>Environment</Label>
                <Textarea
                  defaultValue={formatEnv(p.fields.env)}
                  onBlur={(e) => patchField(p, "env", parseEnv(e.target.value))}
                />
              </div>
              <div>
                <Label>Credential refs</Label>
                <Textarea
                  defaultValue={(p.fields.credential_refs ?? []).join("\n")}
                  onBlur={(e) => patchField(p, "credential_refs", splitLines(e.target.value))}
                />
              </div>
              <div>
                <Label>MCP servers JSON</Label>
                <Textarea
                  defaultValue={formatMCPServers(p.fields.mcp_servers)}
                  onBlur={(e) => patchMCPServers(p, e.target.value)}
                />
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

function buildFields(form: ProfileForm): RuntimeProfileFields {
  const fields: RuntimeProfileFields = {};
  const binaryPath = emptyToUndefined(form.binary_path);
  const model = emptyToUndefined(form.model);
  const endpoint = emptyToUndefined(form.endpoint);
  const customArgs = splitLines(form.custom_args);
  const env = parseEnv(form.env);
  const credentialRefs = splitLines(form.credential_refs);
  const mcpServers = parseMCPServers(form.mcp_servers);
  const defaultRunner = emptyToUndefined(form.default_runner);

  if (binaryPath) fields.binary_path = binaryPath;
  if (model) fields.model = model;
  if (endpoint) fields.endpoint = endpoint;
  if (customArgs.length > 0) fields.custom_args = customArgs;
  if (Object.keys(env).length > 0) fields.env = env;
  if (credentialRefs.length > 0) fields.credential_refs = credentialRefs;
  if (mcpServers && mcpServers.length > 0) fields.mcp_servers = mcpServers;
  if (defaultRunner) fields.default_runner = defaultRunner;
  return fields;
}

function splitLines(value: string): string[] {
  return value.split("\n").map((s) => s.trim()).filter(Boolean);
}

function parseEnv(value: string): Record<string, string> {
  return Object.fromEntries(
    splitLines(value).map((line) => {
      const idx = line.indexOf("=");
      if (idx === -1) return [line, ""];
      return [line.slice(0, idx).trim(), line.slice(idx + 1).trim()];
    }).filter(([key]) => key)
  );
}

function formatEnv(env?: Record<string, string>): string {
  if (!env) return "";
  return Object.entries(env).map(([key, value]) => `${key}=${value}`).join("\n");
}

function parseMCPServers(value: string): RuntimeProfileFields["mcp_servers"] {
  const trimmed = value.trim();
  if (!trimmed) return [];
  const parsed = JSON.parse(trimmed);
  return Array.isArray(parsed) ? parsed : [];
}

function formatMCPServers(servers?: RuntimeProfileFields["mcp_servers"]): string {
  if (!servers || servers.length === 0) return "";
  return JSON.stringify(servers, null, 2);
}

function emptyToUndefined(value: string) {
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}
