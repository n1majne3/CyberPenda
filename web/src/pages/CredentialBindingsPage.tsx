import { useEffect, useMemo, useState } from "react";
import {
  Ban,
  KeyRound,
  LoaderCircle,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { apiGet, apiPut, apiDelete, type CredentialBinding, type ModelProvider, type RuntimeProfile } from "@/lib/api";
import { Badge, Button, Input, Label, Select } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  SettingsAlert,
  SettingsPageHeader,
  SettingsPanel,
  SettingsSplitLayout,
  SettingsPageShell,
} from "@/components/shared";
import {
  SettingsChipFilter,
  SettingsDetailPane,
  SettingsListColumn,
  SettingsSearchField,
  SettingsSegmentedFilter,
  SettingsStatSummary,
} from "@/components/settingsLibrary";
import { cn } from "@/lib/utils";

type StatusFilter = "all" | "active" | "disabled";
type SourceFilter = "all" | "env" | "literal";

const emptyForm = { credential_ref: "", kind: "literal", value: "", destination_env: "" };

const SOURCE_KIND_LABELS: Record<string, string> = {
  env: "Environment variable",
  literal: "Literal secret",
};

export function CredentialBindingsPage() {
  const [bindings, setBindings] = useState<CredentialBinding[]>([]);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState(emptyForm);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>("all");
  const [confirmDelete, setConfirmDelete] = useState<{ id: string; credentialRef: string } | null>(null);
  const [loading, setLoading] = useState(true);

  async function load() {
    setLoading(true);
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
    } finally {
      setLoading(false);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    // Initial load on mount. load() is reused by event handlers.
    load();
  }, []);
  /* eslint-enable react-hooks/set-state-in-effect */

  function startCreate() {
    setForm(emptyForm);
    setCreating(true);
  }

  function cancelCreate() {
    setForm(emptyForm);
    setCreating(false);
  }

  async function create() {
    // destination_env is the runtime env var name. For env sources it defaults
    // to the host variable name (value); for literal/file/command it is
    // required, otherwise projection silently fails at launch.
    const destinationEnv =
      form.kind === "env"
        ? (form.destination_env.trim() || form.value.trim())
        : form.destination_env.trim();
    if (!form.credential_ref.trim() || !form.value.trim() || !destinationEnv) return;
    setSaving(true);
    setError(null);
    try {
      await apiPut("/api/credential-bindings", {
        credential_ref: form.credential_ref,
        source: { kind: form.kind, value: form.value, destination_env: destinationEnv },
      });
      setForm(emptyForm);
      setCreating(false);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function remove(id: string) {
    setError(null);
    try {
      await apiDelete(`/api/credential-bindings/${id}`);
      await load();
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

  const activeCount = useMemo(() => bindings.filter((b) => !b.disabled).length, [bindings]);
  const disabledCount = bindings.length - activeCount;

  const filteredBindings = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return bindings.filter((binding) => {
      if (statusFilter === "active" && binding.disabled) return false;
      if (statusFilter === "disabled" && !binding.disabled) return false;
      if (sourceFilter !== "all" && binding.source.kind !== sourceFilter) return false;
      if (!needle) return true;
      const provider = modelProviders.find((item) => item.api_key_env === binding.credential_ref);
      const usedBy = profiles
        .filter((profile) => (profile.fields.credential_refs ?? []).includes(binding.credential_ref))
        .map((profile) => profile.name);
      const haystack = [
        binding.credential_ref,
        binding.scope,
        binding.source.kind,
        binding.source.value ?? "",
        provider?.name ?? "",
        usedBy.join(" "),
      ]
        .join(" ")
        .toLowerCase();
      return haystack.includes(needle);
    });
  }, [bindings, query, statusFilter, sourceFilter, modelProviders, profiles]);

  const sourceValueLabel =
    form.kind === "env" ? "Host environment variable name" : "Secret value";

  // destination_env (the runtime env var name) is required for literal
  // sources. For env sources it defaults to the host variable name, so the
  // field is optional.
  const destinationEnvRequired = form.kind !== "env";

  return (
    <SettingsPageShell>
      <SettingsPageHeader
        className="mb-4 shrink-0"
        title="Credential bindings"
        eyebrow="Configuration"
        description="Global credential sources. Project overrides stay on project dashboards; model provider API keys usually live with Model providers."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" onClick={() => load()} aria-label="Refresh credentials">
              <RefreshCw className="h-4 w-4" /> Refresh
            </Button>
            <Button onClick={startCreate} aria-label="New binding">
              <Plus className="h-4 w-4" /> New binding
            </Button>
          </div>
        }
      />

      {error && <SettingsAlert className="mb-3 shrink-0">{error}</SettingsAlert>}

      <SettingsSplitLayout data-testid="credential-bindings-settings-layout" variant="management" fill>
        <SettingsListColumn data-testid="credential-bindings-settings-list">
          <SettingsPanel className="gap-4 lg:shrink-0">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
              <div className="min-w-0">
                <p className="text-sm font-medium">Credential library</p>
                <p className="mt-1 text-xs text-muted-foreground">
                  Bindings resolve at preflight for runtime profiles and model providers.
                </p>
              </div>
              <SettingsStatSummary value={activeCount} unit="active" total={bindings.length} />
            </div>

            <div className="flex flex-col gap-3 border-t border-border pt-4">
              <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
                <SettingsSearchField
                  id="credentials-search"
                  name="credentials_search"
                  value={query}
                  onChange={setQuery}
                  placeholder="Search ref, source, provider, or profile…"
                  aria-label="Search credentials"
                />
                <SettingsSegmentedFilter
                  aria-label="Filter by status"
                  value={statusFilter}
                  onChange={setStatusFilter}
                  options={[
                    { id: "all", label: "All", count: bindings.length },
                    { id: "active", label: "Active", count: activeCount },
                    { id: "disabled", label: "Disabled", count: disabledCount },
                  ]}
                />
              </div>

              <SettingsChipFilter
                aria-label="Filter by source kind"
                value={sourceFilter}
                onChange={setSourceFilter}
                options={[
                  { id: "all", label: "Any source" },
                  { id: "env", label: "env" },
                  { id: "literal", label: "literal" },
                ]}
              />
            </div>
          </SettingsPanel>

          {loading ? (
            <SettingsPanel className="items-center justify-center py-12 text-center lg:min-h-0 lg:flex-1 lg:overflow-y-auto" role="status" aria-label="Loading credential bindings">
              <LoaderCircle className="h-5 w-5 animate-spin text-muted-foreground motion-reduce:animate-none" />
              <p className="mt-2 text-sm text-muted-foreground">Loading credential bindings…</p>
            </SettingsPanel>
          ) : bindings.length === 0 ? (
            <SettingsPanel className="items-center justify-center py-12 text-center lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
              <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                <KeyRound className="h-5 w-5 text-muted-foreground" />
              </div>
              <div>
                <p className="font-medium">No global bindings yet</p>
                <p className="mt-1 text-sm text-muted-foreground">
                  Create a binding when a runtime needs a credential ref outside model provider setup.
                </p>
              </div>
              <Button size="sm" onClick={startCreate}>
                <Plus className="h-3.5 w-3.5" /> New binding
              </Button>
            </SettingsPanel>
          ) : filteredBindings.length === 0 ? (
            <SettingsPanel className="items-center justify-center py-10 text-center lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
              <p className="font-medium">No matching bindings</p>
              <p className="mt-1 text-sm text-muted-foreground">
                Try a different search or clear the filters.
              </p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setQuery("");
                  setStatusFilter("all");
                  setSourceFilter("all");
                }}
              >
                Clear filters
              </Button>
            </SettingsPanel>
          ) : (
            <SettingsPanel
              className="flex flex-col gap-0 overflow-hidden p-0 lg:min-h-0 lg:flex-1"
              data-testid="credentials-library-list"
            >
              <div className="hidden border-b border-border bg-muted/30 px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground sm:grid sm:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] sm:gap-3 lg:shrink-0">
                <span>Reference</span>
                <span>Source</span>
                <span>Used by</span>
                <span className="w-9 text-right"> </span>
              </div>
              <ul className="divide-y divide-border lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain">
                {filteredBindings.map((binding) => {
                  const provider = modelProviderForRef(binding.credential_ref);
                  const usedByProfiles = profilesUsingRef(binding.credential_ref);
                  const destinationEnv =
                    binding.source.destination_env ||
                    (binding.source.kind === "env" ? binding.source.value : "");
                  const sourceDisplay = binding.disabled
                    ? null
                    : formatSourceDisplay(binding.source.kind, binding.source.value);

                  return (
                    <li
                      key={binding.id}
                      data-testid={`credential-row-${binding.id}`}
                      className={cn(
                        "px-4 py-3 transition-colors",
                        binding.disabled && "opacity-75",
                      )}
                    >
                      <div className="grid items-start gap-3 sm:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-center">
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-2">
                            <span className="truncate font-mono text-sm font-medium">
                              {binding.credential_ref}
                            </span>
                            <Badge variant={binding.disabled ? "destructive" : "primary"} size="sm">
                              {binding.scope}
                            </Badge>
                            {binding.disabled && (
                              <Badge variant="destructive" size="sm">
                                <Ban className="h-3 w-3" />
                                disabled
                              </Badge>
                            )}
                          </div>
                          {provider && (
                            <p className="mt-1 text-xs text-muted-foreground">
                              Model provider · {provider.name}
                            </p>
                          )}
                        </div>

                        <div className="min-w-0">
                          {binding.disabled ? (
                            <span className="text-xs text-muted-foreground">Not resolved</span>
                          ) : (
                            <div className="min-w-0">
                              {destinationEnv && (
                                <p className="truncate font-mono text-[11px] font-medium" title={destinationEnv}>
                                  {destinationEnv}
                                </p>
                              )}
                              <div className="mt-0.5 flex items-center gap-1">
                                <Badge variant="outline" size="sm" className="font-normal">
                                  {binding.source.kind}
                                </Badge>
                                {sourceDisplay && (
                                  <span className="truncate font-mono text-[11px] text-muted-foreground" title={sourceDisplay}>
                                    {sourceDisplay}
                                  </span>
                                )}
                              </div>
                            </div>
                          )}
                        </div>

                        <div className="min-w-0">
                          {/* Classification first: a binding is either a Model
                              Provider key (managed on the Model providers page)
                              or a Global Environment Variable (injected into
                              every Runtime). Profile references are secondary. */}
                          {provider ? (
                            <span className="text-xs font-medium text-muted-foreground">
                              Provider key · {provider.name}
                            </span>
                          ) : (
                            <span className="text-xs font-medium text-muted-foreground">
                              Global env var
                            </span>
                          )}
                          {usedByProfiles.length > 0 && (
                            <div className="mt-1 flex flex-wrap gap-1">
                              {usedByProfiles.slice(0, 3).map((name) => (
                                <Badge key={name} variant="outline" size="sm" className="max-w-full truncate font-normal">
                                  {name}
                                </Badge>
                              ))}
                              {usedByProfiles.length > 3 && (
                                <span className="text-[11px] text-muted-foreground">
                                  +{usedByProfiles.length - 3} more
                                </span>
                              )}
                            </div>
                          )}
                        </div>

                        <div className="flex justify-end">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Delete ${binding.credential_ref} binding`}
                            onClick={() => setConfirmDelete({ id: binding.id, credentialRef: binding.credential_ref })}
                            className="text-muted-foreground hover:text-destructive"
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
                        </div>
                      </div>
                    </li>
                  );
                })}
              </ul>
              {filteredBindings.length !== bindings.length && (
                <div className="border-t border-border bg-muted/20 px-4 py-2 text-xs text-muted-foreground">
                  Showing {filteredBindings.length} of {bindings.length} bindings
                </div>
              )}
            </SettingsPanel>
          )}
        </SettingsListColumn>

        <SettingsListColumn>
          {!creating ? (
            <SettingsPanel data-testid="credential-binding-create-panel" className="gap-4 lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain">
              <div>
                <h3 className="font-medium">Library actions</h3>
                <p className="mt-1 text-sm text-muted-foreground">
                  Reference an existing secret source without storing the value in the UI unless you choose literal.
                </p>
              </div>
              <Button onClick={startCreate} className="w-full justify-start">
                <Plus className="h-4 w-4" /> New binding
              </Button>
              <div className="rounded-md border border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
                Prefer model-provider API keys on the Model providers page when the secret is only for LLM auth.
              </div>
            </SettingsPanel>
          ) : (
            <SettingsDetailPane
              data-testid="credential-binding-create-panel"
              className="lg:flex-1"
              header={
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="font-medium">New binding</h3>
                    <p className="mt-1 text-sm text-muted-foreground">
                      Reference an existing secret source without storing the value in the UI.
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={cancelCreate}
                    aria-label="Cancel binding form"
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              }
              footer={
                <>
                  <Button
                    onClick={create}
                    disabled={
                      saving ||
                      !form.credential_ref.trim() ||
                      !form.value.trim() ||
                      (destinationEnvRequired && !form.destination_env.trim())
                    }
                  >
                    Create binding
                  </Button>
                  <Button variant="outline" onClick={cancelCreate} disabled={saving}>
                    Cancel
                  </Button>
                </>
              }
              bodyClassName="space-y-3"
            >
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

              <div>
                <Label htmlFor="credential-source-kind">Source kind</Label>
                <Select
                  id="credential-source-kind"
                  name="source_kind"
                  value={form.kind}
                  onChange={(e) => setForm({ ...form, kind: e.target.value, value: "" })}
                >
                  <option value="literal">literal — stored secret (recommended)</option>
                  <option value="env">env — read from host environment</option>
                </Select>
                <p className="mt-1 text-[11px] text-muted-foreground">
                  {SOURCE_KIND_LABELS[form.kind] ?? form.kind}
                  {form.kind === "literal" && " · value is stored by the daemon"}
                  {form.kind === "env" && " · secret stays out of the daemon"}
                </p>
              </div>

              <div>
                <Label htmlFor="credential-source-value">{sourceValueLabel}</Label>
                <Input
                  id="credential-source-value"
                  name="source_value"
                  type={form.kind === "literal" ? "password" : "text"}
                  value={form.value}
                  onChange={(e) => setForm({ ...form, value: e.target.value })}
                  placeholder={form.kind === "env" ? "OPENAI_API_KEY…" : "sk-…"}
                  autoComplete="off"
                  spellCheck={false}
                  className={form.kind === "literal" ? undefined : "font-mono text-xs"}
                />
                {form.kind === "env" && (
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    Use the host environment variable name, not the secret.
                  </p>
                )}
              </div>

              <div>
                <Label htmlFor="credential-destination-env">Runtime environment variable name</Label>
                <Input
                  id="credential-destination-env"
                  name="destination_env"
                  value={form.destination_env}
                  onChange={(e) => setForm({ ...form, destination_env: e.target.value })}
                  placeholder="NSSCTF_AGENT_TOKEN…"
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-xs"
                />
                <p className="mt-1 text-[11px] text-muted-foreground">
                  The runtime env var name. Injected into every Runtime.
                  {destinationEnvRequired
                    ? " Required."
                    : " Defaults to the host variable name when empty."}
                </p>
              </div>
            </SettingsDetailPane>
          )}
        </SettingsListColumn>
      </SettingsSplitLayout>
      <ConfirmDialog
        open={confirmDelete !== null}
        title={confirmDelete ? `Delete credential binding ${confirmDelete.credentialRef}?` : "Delete credential binding?"}
        description="Bindings resolve at preflight for runtime profiles and model providers."
        confirmLabel="Delete"
        destructive
        onConfirm={() => {
          const target = confirmDelete;
          setConfirmDelete(null);
          if (target) void remove(target.id);
        }}
        onCancel={() => setConfirmDelete(null)}
      />
    </SettingsPageShell>
  );
}

function formatSourceDisplay(kind: string, value?: string): string {
  if (!value) return "";
  if (kind === "literal") return "••••••••";
  return value;
}
