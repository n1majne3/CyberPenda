import { useEffect, useMemo, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { apiGet, apiPost, apiPatch, apiDelete, type RuntimeProfile } from "@/lib/api";
import { cn } from "@/lib/utils";
import { Button, Card, Input, Label, Badge, Textarea } from "@/components/ui";

const PROVIDERS = ["codex", "claude_code", "pi", "fake"] as const;
const RUNNERS = ["sandbox", "host"] as const;

const PROVIDER_LABELS: Record<string, string> = {
  codex: "Codex",
  claude_code: "Claude Code",
  pi: "Pi",
  fake: "Fake harness",
};

const DEFAULT_API_KEY_ENV: Record<string, string> = {
  codex: "OPENAI_API_KEY",
  claude_code: "ANTHROPIC_AUTH_TOKEN",
  pi: "ANTHROPIC_API_KEY",
};

const API_KEY_CONFIGURED = "[configured]";
const DEFAULT_DAEMON_MCP_PORT = "8787";

type RuntimeProfileFields = RuntimeProfile["fields"];
type ProfileForm = {
  name: string;
  provider: string;
  binary_path: string;
  model: string;
  endpoint: string;
  custom_args: string;
  env: string;
  api_key_env: string;
  api_key: string;
  mcp_servers: string;
  default_runner: string;
  sandbox_image: string;
  credential_refs: string;
};

const emptyForm: ProfileForm = {
  name: "",
  provider: "codex",
  binary_path: "",
  model: "",
  endpoint: "",
  custom_args: "",
  env: "",
  api_key_env: "",
  api_key: "",
  mcp_servers: "",
  default_runner: "sandbox",
  sandbox_image: "",
  credential_refs: "",
};

export function RuntimeProfilesPage() {
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<ProfileForm>(emptyForm);
  const [draft, setDraft] = useState<ProfileForm | null>(null);

  const selected = profiles.find((p) => p.id === selectedId) ?? null;

  const grouped = useMemo(() => {
    const buckets = new Map<string, RuntimeProfile[]>();
    for (const provider of PROVIDERS) buckets.set(provider, []);
    for (const profile of profiles) {
      const key = PROVIDERS.includes(profile.provider as (typeof PROVIDERS)[number])
        ? profile.provider
        : "other";
      if (!buckets.has(key)) buckets.set(key, []);
      buckets.get(key)!.push(profile);
    }
    return buckets;
  }, [profiles]);

  async function load() {
    try {
      const d = await apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles");
      const loaded = d.profiles ?? [];
      setProfiles(loaded);
      setSelectedId((current) => {
        if (current && loaded.some((p) => p.id === current)) return current;
        return loaded[0]?.id ?? null;
      });
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    load();
  }, []);

  useEffect(() => {
    if (!selected) {
      setDraft(null);
      return;
    }
    setDraft(profileToForm(selected));
  }, [selected?.id, selected?.updated_at]);

  async function create() {
    try {
      const created = await apiPost<RuntimeProfile>("/api/runtime-profiles", {
        name: form.name,
        provider: form.provider,
        fields: buildFields(form),
      });
      setForm(emptyForm);
      setCreating(false);
      await load();
      setSelectedId(created.id);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function remove(id: string) {
    try {
      await apiDelete(`/api/runtime-profiles/${id}`);
      if (selectedId === id) setSelectedId(null);
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function saveSelected() {
    if (!selected || !draft) return;
    try {
      await apiPatch(`/api/runtime-profiles/${selected.id}`, {
        name: draft.name,
        provider: draft.provider,
        fields: buildFields(draft),
      });
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  const previewConfig = selected
    ? JSON.stringify(
        buildGeneratedConfigPreview(
          draft?.provider ?? selected.provider,
          buildFields(draft ?? profileToForm(selected)),
          draft ?? profileToForm(selected)
        ),
        null,
        2
      )
    : "";

  return (
    <div className="p-8 max-w-6xl">
      <div className="mb-6">
        <h2 className="text-xl font-semibold">Runtime profiles</h2>
        <p className="text-sm text-muted-foreground mt-1">
          Manage global runtime configurations here — provider, model, endpoint, env keys, MCP, and runner.
          Profiles are stored in pentest and projected into each task workspace at launch.
        </p>
      </div>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      <div className="grid grid-cols-1 lg:grid-cols-[240px_1fr] gap-4 min-h-[520px]">
        <Card className="p-3 flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Profiles</span>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setCreating(true);
                setSelectedId(null);
                setForm({ ...emptyForm, provider: "codex" });
              }}
            >
              <Plus className="h-4 w-4" />
            </Button>
          </div>

          <div className="flex-1 overflow-y-auto space-y-4">
            {PROVIDERS.map((provider) => {
              const items = grouped.get(provider) ?? [];
              if (items.length === 0) return null;
              return (
                <div key={provider}>
                  <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wide mb-1.5 px-1">
                    {PROVIDER_LABELS[provider] ?? provider}
                  </p>
                  <div className="space-y-1">
                    {items.map((p) => (
                      <button
                        key={p.id}
                        type="button"
                        onClick={() => {
                          setCreating(false);
                          setSelectedId(p.id);
                        }}
                        className={cn(
                          "w-full text-left rounded-md px-2.5 py-2 text-sm transition-colors",
                          selectedId === p.id && !creating
                            ? "bg-primary/10 text-foreground ring-1 ring-primary/30"
                            : "hover:bg-muted/60 text-muted-foreground hover:text-foreground"
                        )}
                      >
                        <span className="font-medium block truncate">{p.name}</span>
                        {p.fields.model && (
                          <span className="text-[11px] truncate block opacity-70">{p.fields.model}</span>
                        )}
                      </button>
                    ))}
                  </div>
                </div>
              );
            })}
            {profiles.length === 0 && (
              <p className="text-sm text-muted-foreground px-1">No profiles yet. Add one to get started.</p>
            )}
          </div>
        </Card>

        <Card className="p-4">
          {creating ? (
            <ProfileEditor
              title="New profile"
              form={form}
              onChange={setForm}
              onSave={create}
              onCancel={() => setCreating(false)}
              saveLabel="Create"
              saveDisabled={!form.name.trim()}
            />
          ) : selected && draft ? (
            <div className="space-y-4">
              <div className="flex items-start justify-between gap-3">
                <div>
                  <div className="flex items-center gap-2 mb-1">
                    <h3 className="font-medium">{selected.name}</h3>
                    <Badge variant="primary">{PROVIDER_LABELS[selected.provider] ?? selected.provider}</Badge>
                  </div>
                  <p className="text-xs text-muted-foreground font-mono truncate">{selected.id}</p>
                </div>
                <div className="flex gap-2">
                  <Button size="sm" onClick={saveSelected}>
                    Save
                  </Button>
                  <Button size="icon" variant="ghost" onClick={() => remove(selected.id)}>
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </div>
              </div>
              <ProfileEditor form={draft} onChange={setDraft} hideActions />
              <div>
                <Label>Generated config preview</Label>
                <pre className="mt-1 rounded-md border border-border bg-muted/30 p-3 text-xs overflow-x-auto max-h-96">
                  {previewConfig}
                </pre>
              </div>
            </div>
          ) : (
            <div className="h-full flex items-center justify-center text-sm text-muted-foreground">
              Select a profile or create a new one.
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}

function ProfileEditor({
  title,
  form,
  onChange,
  onSave,
  onCancel,
  saveLabel = "Save",
  saveDisabled,
  hideActions,
}: {
  title?: string;
  form: ProfileForm;
  onChange: (form: ProfileForm) => void;
  onSave?: () => void;
  onCancel?: () => void;
  saveLabel?: string;
  saveDisabled?: boolean;
  hideActions?: boolean;
}) {
  return (
    <div className="space-y-3">
      {title && <h3 className="font-medium">{title}</h3>}
      <div className="grid grid-cols-2 gap-3">
        <div>
          <Label>Name</Label>
          <Input value={form.name} onChange={(e) => onChange({ ...form, name: e.target.value })} placeholder="Codex Default" />
        </div>
        <div>
          <Label>Provider</Label>
          <select
            className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
            value={form.provider}
            onChange={(e) => onChange({ ...form, provider: e.target.value })}
          >
            {PROVIDERS.map((p) => (
              <option key={p} value={p}>
                {PROVIDER_LABELS[p] ?? p}
              </option>
            ))}
          </select>
        </div>
        <div>
          <Label>Binary path</Label>
          <Input
            value={form.binary_path}
            onChange={(e) => onChange({ ...form, binary_path: e.target.value })}
            placeholder="/usr/local/bin/codex"
          />
        </div>
        <div>
          <Label>Model</Label>
          <Input value={form.model} onChange={(e) => onChange({ ...form, model: e.target.value })} placeholder="gpt-5" />
        </div>
        <div>
          <Label>Endpoint</Label>
          <Input
            value={form.endpoint}
            onChange={(e) => onChange({ ...form, endpoint: e.target.value })}
            placeholder="https://api.example.test/v1"
          />
        </div>
        <div>
          <Label>Default runner</Label>
          <select
            className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
            value={form.default_runner}
            onChange={(e) => onChange({ ...form, default_runner: e.target.value })}
          >
            {RUNNERS.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </div>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div>
          <Label>Custom args</Label>
          <Textarea value={form.custom_args} onChange={(e) => onChange({ ...form, custom_args: e.target.value })} placeholder="--json" />
        </div>
        <div>
          <Label>Environment</Label>
          <p className="text-[11px] text-muted-foreground mb-1">KEY=VALUE lines or a JSON object</p>
          <Textarea
            value={form.env}
            onChange={(e) => onChange({ ...form, env: e.target.value })}
            placeholder={'ANTHROPIC_BASE_URL=https://api.example.test\nANTHROPIC_MODEL=claude-sonnet'}
          />
        </div>
        <div>
          <Label>API key env</Label>
          <Input
            value={form.api_key_env}
            onChange={(e) => onChange({ ...form, api_key_env: e.target.value })}
            placeholder={DEFAULT_API_KEY_ENV[form.provider] ?? "API_KEY"}
          />
        </div>
        <div>
          <Label>API key</Label>
          <Input
            type="password"
            value={form.api_key}
            onChange={(e) => onChange({ ...form, api_key: e.target.value })}
            placeholder="sk-..."
            autoComplete="off"
          />
          <p className="text-[11px] text-muted-foreground mt-1">
            Stored on this profile only. Leave as [configured] to keep the existing key.
          </p>
        </div>
        <div>
          <Label>MCP servers JSON</Label>
          <Textarea
            value={form.mcp_servers}
            onChange={(e) => onChange({ ...form, mcp_servers: e.target.value })}
            placeholder='[{"name":"project","mode":"trusted","url":"http://127.0.0.1:8787/mcp"}]'
          />
        </div>
        <div>
          <Label>Sandbox image</Label>
          <Input
            value={form.sandbox_image}
            onChange={(e) => onChange({ ...form, sandbox_image: e.target.value })}
            placeholder="gemini_kali-gemini-kali:latest (daemon default if empty)"
          />
          <p className="text-[11px] text-muted-foreground mt-1">
            Override the daemon sandbox image for tasks using this profile.
          </p>
        </div>
        <div className="col-span-2">
          <Label>Credential refs</Label>
          <Textarea
            value={form.credential_refs}
            onChange={(e) => onChange({ ...form, credential_refs: e.target.value })}
            placeholder="codex-api-key"
            rows={2}
          />
          <p className="text-[11px] text-muted-foreground mt-1">
            Resolved via global or project credential bindings at preflight.
          </p>
        </div>
      </div>
      {form.provider === "claude_code" && form.endpoint.includes("bigmodel.cn") && (
        <Card className="border-muted bg-muted/20 p-3 text-xs text-muted-foreground space-y-1">
          <p className="font-medium text-foreground">智谱 GLM runtime notes</p>
          <p>Endpoint: use <code className="text-[11px]">https://open.bigmodel.cn/api/anthropic</code> (not Minimax).</p>
          <p>Launch adds <code className="text-[11px]">--strict-mcp-config --mcp-config workdir/.mcp.json</code>; smoke may need <code className="text-[11px]">--permission-mode bypassPermissions</code> in custom args.</p>
          <p>Third-party APIs may not expose local MCP tools in the model tool list — allow JSON-RPC fallback to PENTEST_MCP_URL.</p>
        </Card>
      )}
      {form.provider === "pi" && form.default_runner === "sandbox" && (
        <p className="text-[11px] text-muted-foreground">
          Pi sandbox sets <code>PI_CODING_AGENT_DIR=/task/runtime-home/pi/agent</code>; pi is preinstalled in <code>pentest-sandbox:latest</code>.
        </p>
      )}
      {!hideActions && (
        <div className="flex gap-2">
          <Button size="sm" onClick={onSave} disabled={saveDisabled}>
            {saveLabel}
          </Button>
          {onCancel && (
            <Button size="sm" variant="ghost" onClick={onCancel}>
              Cancel
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

function profileToForm(profile: RuntimeProfile): ProfileForm {
  const apiKeyEntries = Object.entries(profile.fields.api_keys ?? {});
  const [apiKeyEnv = "", apiKeyValue = ""] = apiKeyEntries[0] ?? [];
  return {
    name: profile.name,
    provider: profile.provider,
    binary_path: profile.fields.binary_path ?? "",
    model: profile.fields.model ?? "",
    endpoint: profile.fields.endpoint ?? "",
    custom_args: (profile.fields.custom_args ?? []).join("\n"),
    env: formatEnv(profile.fields.env),
    api_key_env: apiKeyEnv || DEFAULT_API_KEY_ENV[profile.provider] || "",
    api_key: apiKeyValue,
    mcp_servers: formatMCPServers(profile.fields.mcp_servers),
    default_runner: profile.fields.default_runner ?? "sandbox",
    sandbox_image: profile.fields.sandbox_image ?? "",
    credential_refs: (profile.fields.credential_refs ?? []).join("\n"),
  };
}

function buildFields(form: ProfileForm): RuntimeProfileFields {
  const fields: RuntimeProfileFields = {};
  const binaryPath = emptyToUndefined(form.binary_path);
  const model = emptyToUndefined(form.model);
  const endpoint = emptyToUndefined(form.endpoint);
  const customArgs = splitLines(form.custom_args);
  const env = parseEnv(form.env);
  const mcpServers = parseMCPServers(form.mcp_servers);
  const defaultRunner = emptyToUndefined(form.default_runner);
  const sandboxImage = emptyToUndefined(form.sandbox_image);
  const apiKeyEnv = emptyToUndefined(form.api_key_env) ?? DEFAULT_API_KEY_ENV[form.provider];
  const apiKey = form.api_key.trim();

  if (binaryPath) fields.binary_path = binaryPath;
  if (model) fields.model = model;
  if (endpoint) fields.endpoint = endpoint;
  if (customArgs.length > 0) fields.custom_args = customArgs;
  if (Object.keys(env).length > 0) fields.env = env;
  if (apiKeyEnv && apiKey) {
    fields.api_keys = { [apiKeyEnv]: apiKey };
  } else if (apiKeyEnv && apiKey === API_KEY_CONFIGURED) {
    fields.api_keys = { [apiKeyEnv]: API_KEY_CONFIGURED };
  }
  if (mcpServers && mcpServers.length > 0) fields.mcp_servers = mcpServers;
  if (defaultRunner) fields.default_runner = defaultRunner;
  if (sandboxImage) fields.sandbox_image = sandboxImage;
  const credentialRefs = splitLines(form.credential_refs);
  if (credentialRefs.length > 0) fields.credential_refs = credentialRefs;
  return fields;
}

function splitLines(value: string): string[] {
  return value
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
}

function parseEnv(value: string): Record<string, string> {
  const trimmed = value.trim();
  if (!trimmed) return {};

  if (trimmed.startsWith("{")) {
    try {
      const parsed: unknown = JSON.parse(trimmed);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return Object.fromEntries(
          Object.entries(parsed as Record<string, unknown>)
            .filter(([key]) => key.trim())
            .map(([key, raw]) => [key.trim(), stringifyEnvValue(raw)])
        );
      }
    } catch {
      // Fall through to line-based parsing.
    }
  }

  const out: Record<string, string> = {};
  for (const line of splitLines(value)) {
    const eq = line.indexOf("=");
    if (eq !== -1) {
      const key = line.slice(0, eq).trim();
      const envValue = line.slice(eq + 1).trim();
      if (key) out[key] = envValue;
      continue;
    }

    const jsonLine = line.match(/^"?([^":]+)"?\s*:\s*(.+?)"?,?$/);
    if (jsonLine) {
      const key = jsonLine[1].trim();
      const envValue = jsonLine[2].trim().replace(/^"|"$/g, "").replace(/,$/, "");
      if (key) out[key] = envValue;
    }
  }
  return out;
}

function stringifyEnvValue(raw: unknown): string {
  if (raw == null) return "";
  return String(raw);
}

function formatEnv(env?: Record<string, string>): string {
  if (!env) return "";
  return Object.entries(env)
    .map(([key, value]) => `${key}=${value}`)
    .join("\n");
}

function buildGeneratedConfigPreview(
  provider: string,
  fields: RuntimeProfileFields,
  form?: ProfileForm
): Record<string, unknown> {
  const mcpServers = buildPreviewMCPServers(fields);
  const mcpPreview = formatMCPServerPreview(mcpServers);
  const launchPreview = buildLaunchPreview(provider, fields, form, (mcpServers?.length ?? 0) > 0);

  if (provider === "claude_code") {
    const env: Record<string, string> = { ...(fields.env ?? {}) };
    if (fields.endpoint && !env.ANTHROPIC_BASE_URL) env.ANTHROPIC_BASE_URL = fields.endpoint;
    if (fields.model && !env.ANTHROPIC_MODEL) env.ANTHROPIC_MODEL = fields.model;
    return {
      provider,
      settings_path: "runtime-home/claude/settings.json",
      env,
      ...(mcpPreview ? { mcp_servers: mcpPreview, mcp_config_path: "workdir/.mcp.json" } : {}),
      ...(fields.api_keys && Object.keys(fields.api_keys).length > 0
        ? { api_keys: redactedAPIKeyPreview(fields) }
        : {}),
      ...(fields.default_runner ? { default_runner: fields.default_runner } : {}),
      task_context_path: "workdir/.pentest/context.json",
      launch_preview: launchPreview,
    };
  }

  if (provider === "codex") {
    const providerId = fields.env?.CODEX_MODEL_PROVIDER?.trim() || "custom";
    const wireApi = fields.env?.CODEX_WIRE_API?.trim() || "responses";
    const providerName = fields.env?.CODEX_PROVIDER_NAME?.trim() || "Custom";
    const endpoint = fields.endpoint?.trim() || fields.env?.OPENAI_BASE_URL?.trim() || "";
    const configToml = [
      fields.model ? `model = "${fields.model}"` : null,
      endpoint ? `model_provider = "${providerId}"` : null,
      endpoint ? `cli_auth_credentials_store = "file"` : null,
      endpoint ? "" : null,
      endpoint ? `[model_providers.${providerId}]` : null,
      endpoint ? `name = "${providerName}"` : null,
      endpoint ? `base_url = "${endpoint.replace(/\/$/, "")}"` : null,
      endpoint ? `wire_api = "${wireApi}"` : null,
      endpoint ? "requires_openai_auth = true" : null,
      ...appendCodexMCPTOMLPreview(mcpServers),
    ]
      .filter((line): line is string => line !== null)
      .join("\n");

    return {
      provider,
      config_path: "runtime-home/codex/config.toml",
      config_toml: configToml,
      ...(mcpPreview ? { mcp_servers: mcpPreview } : {}),
      ...(fields.api_keys && Object.keys(fields.api_keys).length > 0
        ? {
            auth_path: "runtime-home/codex/auth.json",
            auth_json: redactedAPIKeyPreview(fields),
            api_keys: redactedAPIKeyPreview(fields),
          }
        : {}),
      ...(fields.default_runner ? { default_runner: fields.default_runner } : {}),
      task_context_path: "workdir/.pentest/context.json",
      launch_preview: launchPreview,
    };
  }

  if (provider === "pi") {
    const providerId = fields.env?.PI_PROVIDER_ID?.trim() || "custom";
    const api =
      fields.env?.PI_API?.trim() ||
      (fields.endpoint?.toLowerCase().includes("anthropic")
        ? "anthropic-messages"
        : fields.endpoint?.toLowerCase().includes("generativelanguage") ||
            fields.endpoint?.toLowerCase().includes("googleapis")
          ? "google-generative-ai"
          : "openai-completions");
    const apiKeyEnv = Object.keys(fields.api_keys ?? {})[0];
    const apiKeyRef = apiKeyEnv ? `$${apiKeyEnv}` : undefined;
    const modelsJson: Record<string, unknown> = {
      providers: {
        [providerId]: {
          ...(fields.endpoint ? { baseUrl: fields.endpoint.replace(/\/$/, "") } : {}),
          api,
          ...(apiKeyRef ? { apiKey: apiKeyRef } : {}),
          ...(fields.model ? { models: [{ id: fields.model }] } : {}),
        },
      },
    };

    return {
      provider,
      models_path: "runtime-home/pi/agent/models.json",
      models_json: modelsJson,
      ...(mcpPreview ? { mcp_servers: mcpPreview, mcp_config_path: "runtime-home/pi/agent/mcp.json" } : {}),
      ...(fields.api_keys && Object.keys(fields.api_keys).length > 0
        ? {
            auth_path: "runtime-home/pi/agent/auth.json",
            auth_json: buildPiAuthPreview(fields),
            api_keys: redactedAPIKeyPreview(fields),
          }
        : {}),
      ...(fields.default_runner ? { default_runner: fields.default_runner } : {}),
      task_context_path: "workdir/.pentest/context.json",
      launch_preview: launchPreview,
    };
  }

  const cfg: Record<string, unknown> = { provider };
  if (fields.binary_path) cfg.binary = fields.binary_path;
  if (fields.model) cfg.model = fields.model;
  if (fields.endpoint) cfg.endpoint = fields.endpoint;
  if (fields.custom_args?.length) cfg.custom_args = fields.custom_args;
  if (fields.env && Object.keys(fields.env).length > 0) cfg.env = fields.env;
  if (fields.api_keys && Object.keys(fields.api_keys).length > 0) {
    cfg.api_keys = redactedAPIKeyPreview(fields);
  }
  if (mcpPreview) cfg.mcp_servers = mcpPreview;
  if (fields.default_runner) cfg.default_runner = fields.default_runner;
  return cfg;
}

function buildLaunchPreview(
  provider: string,
  fields: RuntimeProfileFields,
  form: ProfileForm | undefined,
  hasMCP: boolean
): Record<string, unknown> {
  const sandbox = fields.default_runner === "sandbox";
  const binary = fields.binary_path?.trim() || (provider === "codex" ? "codex" : provider === "claude_code" ? "claude" : provider === "pi" ? "pi" : "");
  const args: string[] = [binary];
  const processEnv: Record<string, string> = {};

  if (provider === "codex") {
    const sub = fields.env?.PENTEST_CODEX_SUBCOMMAND?.trim() || "run";
    args.push(sub);
    if (fields.model) args.push("--model", fields.model);
    for (const arg of fields.custom_args ?? []) args.push(arg);
    args.push("<goal>");
    processEnv.CODEX_HOME = sandbox ? "/task/runtime-home/codex" : "runtime-home/codex";
  } else if (provider === "claude_code") {
    if (fields.model) args.push("--model", fields.model);
    args.push("--settings", sandbox ? "/task/runtime-home/claude/settings.json" : "runtime-home/claude/settings.json");
    if (hasMCP) {
      args.push("--strict-mcp-config", "--mcp-config", sandbox ? "/task/workdir/.mcp.json" : "workdir/.mcp.json");
    }
    for (const arg of fields.custom_args ?? []) args.push(arg);
    if (hasMCP) args.push("--", "<goal>");
    else args.push("<goal>");
    processEnv.CLAUDE_HOME = sandbox ? "/task/runtime-home/claude" : "runtime-home/claude";
    if (sandbox) processEnv.IS_SANDBOX = "1";
  } else if (provider === "pi") {
    if (!hasCLIOption(fields.custom_args, "--provider")) {
      const providerId = fields.env?.PI_PROVIDER_ID?.trim() || (fields.endpoint?.trim() ? "custom" : "");
      if (providerId) args.push("--provider", providerId);
    }
    if (fields.model) args.push("--model", fields.model);
    for (const arg of fields.custom_args ?? []) args.push(arg);
    args.push("<goal>");
    processEnv.PI_CODING_AGENT_DIR = sandbox ? "/task/runtime-home/pi/agent" : "runtime-home/pi/agent";
  }

  for (const [key, value] of Object.entries(fields.env ?? {})) {
    processEnv[key] = value;
  }
  for (const key of Object.keys(fields.api_keys ?? {})) {
    processEnv[key] = "[REDACTED at launch]";
  }

  if (sandbox) {
    processEnv.PENTEST_SKILLS_DIR = "/opt/pentest/skills";
    if (form?.endpoint?.includes("bigmodel.cn") || fields.endpoint?.includes("bigmodel.cn")) {
      processEnv.ANTHROPIC_BASE_URL = fields.endpoint ?? form?.endpoint ?? "";
    }
  }

  return { argv: args, process_env: processEnv, runner: fields.default_runner ?? "sandbox" };
}

function hasCLIOption(args: string[] | undefined, option: string): boolean {
  return (args ?? []).some((arg) => arg === option || arg.startsWith(`${option}=`));
}

function redactedAPIKeyPreview(fields: RuntimeProfileFields): Record<string, string> {
  return Object.fromEntries(
    Object.keys(fields.api_keys ?? {})
      .filter((key) => key.trim())
      .map((key) => [key, "[REDACTED at launch]"])
  );
}

function buildPiAuthPreview(fields: RuntimeProfileFields): Record<string, { type: string; key: string }> {
  const apiKeyEnv = Object.keys(fields.api_keys ?? {})
    .filter((key) => key.trim())
    .sort()[0];
  if (!apiKeyEnv) return {};
  const providerId = fields.env?.PI_PROVIDER_ID?.trim() || "custom";
  return {
    [providerId]: { type: "api_key", key: "[REDACTED at launch]" },
  };
}

function trustedMCPDisabled(env?: Record<string, string>): boolean {
  const value = (env?.PENTEST_DISABLE_TRUSTED_MCP ?? "").trim().toLowerCase();
  return value === "1" || value === "true" || value === "yes";
}

function previewMCPEndpointURL(sandbox: boolean): string {
  const host = sandbox ? "host.docker.internal" : "127.0.0.1";
  return `http://${host}:${DEFAULT_DAEMON_MCP_PORT}/mcp`;
}

function buildPreviewMCPServers(fields: RuntimeProfileFields): RuntimeProfileFields["mcp_servers"] {
  const servers = [...(fields.mcp_servers ?? [])];
  if (trustedMCPDisabled(fields.env)) return servers;

  const sandbox = fields.default_runner === "sandbox";
  const trustedURL = previewMCPEndpointURL(sandbox);
  const normalized = trustedURL.replace(/\/$/, "");
  if (servers.some((server) => (server.url ?? "").replace(/\/$/, "") === normalized)) {
    return servers;
  }
  return [{ name: "pentest", mode: "trusted", url: trustedURL }, ...servers];
}

function formatMCPServerPreview(
  servers?: RuntimeProfileFields["mcp_servers"]
): Array<Record<string, unknown>> | undefined {
  if (!servers?.length) return undefined;
  return servers.map((server) => ({
    name: server.name,
    mode: server.mode,
    ...(server.command ? { command: server.command } : {}),
    ...(server.url ? { url: server.url } : {}),
    ...(server.args?.length ? { args: server.args } : {}),
    ...(server.env && Object.keys(server.env).length > 0 ? { env: server.env } : {}),
  }));
}

function appendCodexMCPTOMLPreview(servers?: RuntimeProfileFields["mcp_servers"]): Array<string | null> {
  if (!servers?.length) return [];
  const lines: Array<string | null> = ["", "[mcp_servers]"];
  for (const server of servers) {
    const name = server.name?.trim();
    if (!name) continue;
    lines.push("", `[mcp_servers.${name}]`);
    if (server.url) {
      lines.push(`url = "${server.url}"`, "enabled = true");
      continue;
    }
    if (server.command) {
      lines.push(`command = "${server.command}"`, "enabled = true");
    }
  }
  return lines;
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
