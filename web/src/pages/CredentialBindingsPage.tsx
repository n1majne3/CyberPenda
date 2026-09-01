import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  KeyRound,
  LoaderCircle,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  X,
} from "lucide-react";
import { apiGet, apiPut, apiDelete, type CredentialBinding, type ModelProvider, type RuntimeProfile } from "@/lib/api";
import { Badge, Button, Chip, Input, Label, Select } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  SettingsAlert,
  SettingsPageHeader,
  SettingsPanel,
  SettingsSplitLayout,
  SettingsPageShell,
} from "@/components/shared";
import {
  SettingsDetailPane,
  SettingsListColumn,
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
  const [editing, setEditing] = useState<CredentialBinding | null>(null);
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
    setEditing(null);
    setCreating(true);
  }

  function cancelCreate() {
    setForm(emptyForm);
    setCreating(false);
  }

  function startEdit(binding: CredentialBinding) {
    setCreating(false);
    setEditing(binding);
    setForm({
      credential_ref: binding.credential_ref,
      kind: binding.source.kind,
      // Literal secrets are redacted to [configured]; leaving the field blank
      // keeps the stored value instead of echoing the sentinel.
      value: binding.source.kind === "env" ? (binding.source.value ?? "") : "",
      destination_env: binding.source.destination_env ?? "",
    });
  }

  function cancelEdit() {
    setEditing(null);
    setForm(emptyForm);
  }

  async function saveEdit() {
    if (!editing) return;
    const destinationEnv =
      form.kind === "env"
        ? (form.destination_env.trim() || form.value.trim())
        : form.destination_env.trim();
    const trimmedValue = form.value.trim();
    const value = form.kind === "literal" && !trimmedValue ? "[configured]" : trimmedValue;
    setSaving(true);
    setError(null);
    try {
      await apiPut("/api/credential-bindings", {
        credential_ref: editing.credential_ref,
        // Preserve the disabled state: an edit changes the source, not whether
        // the binding resolves.
        disabled: editing.disabled ?? false,
        source: { kind: form.kind, value, destination_env: destinationEnv },
      });
      setEditing(null);
      setForm(emptyForm);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
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

  // Editing a literal binding may leave the value blank to keep the stored
  // secret (sentinel); any other literal save still needs a value.
  const editingLiteralKeep =
    editing !== null && form.kind === "literal" && editing.source.kind === "literal";
  const editValueMissing = !form.value.trim() && !editingLiteralKeep;
  const editSaveDisabled =
    !editing || editValueMissing || (destinationEnvRequired && !form.destination_env.trim());

  return (
    <SettingsPageShell>
      <SettingsPageHeader
        className="mb-4 shrink-0"
        title="Credential bindings"
        eyebrow="Configuration"
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
          <div className="flex items-center justify-between lg:shrink-0">
            <div>
              <h2 className="text-base font-semibold">Credential library</h2>
              <p className="mt-0.5 text-xs text-muted-foreground">Bindings resolve during preflight for Runtime Profiles and Model Providers.</p>
            </div>
            <div className="text-right">
              <span className="text-xl font-semibold">{activeCount}</span>
              <span className="text-xs text-muted-foreground"> active · {bindings.length} total</span>
            </div>
          </div>

          <div className="flex items-center gap-2 lg:shrink-0">
            <div className="flex rounded-lg border border-input p-0.5" role="group" aria-label="Filter by status">
              {(
                [
                  ["all", "All", bindings.length],
                  ["active", "Active", activeCount],
                  ["disabled", "Disabled", disabledCount],
                ] as const
              ).map(([id, label, count]) => (
                <button
                  key={id}
                  type="button"
                  onClick={() => setStatusFilter(id)}
                  aria-pressed={statusFilter === id}
                  className={cn(
                    "rounded-md px-2.5 py-1 text-xs",
                    statusFilter === id
                      ? "bg-primary font-medium text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {label} {count}
                </button>
              ))}
            </div>
            <div className="flex rounded-lg border border-input p-0.5" role="group" aria-label="Filter by source kind">
              {(
                [
                  ["all", "Any source"],
                  ["env", "env"],
                  ["literal", "literal"],
                ] as const
              ).map(([id, label]) => (
                <button
                  key={id}
                  type="button"
                  onClick={() => setSourceFilter(id)}
                  aria-pressed={sourceFilter === id}
                  className={cn(
                    "rounded-md px-2.5 py-1 text-xs",
                    sourceFilter === id
                      ? "bg-secondary font-medium"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {label}
                </button>
              ))}
            </div>
            <div className="flex-1" />
            <div className="flex h-8 w-[220px] items-center gap-2 rounded-md border border-input bg-background px-2">
              <Search className="h-3.5 w-3.5 text-muted-foreground" />
              <input
                id="credentials-search"
                name="credentials_search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search ref, source, provider, or profile…"
                aria-label="Search credentials"
                autoComplete="off"
                spellCheck={false}
                className="w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground"
              />
            </div>
          </div>

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
            <div
              className="overflow-hidden rounded-lg border border-border bg-card shadow-sm lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain"
              data-testid="credentials-library-list"
            >
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border text-left text-xs uppercase tracking-wider text-muted-foreground">
                    <th className="px-4 py-2.5 font-medium">Reference</th>
                    <th className="px-4 py-2.5 font-medium">Source</th>
                    <th className="px-4 py-2.5 font-medium">Used by</th>
                    <th className="w-[80px] px-4 py-2.5 font-medium"></th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
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
                      <tr
                        key={binding.id}
                        data-testid={`credential-row-${binding.id}`}
                        className={cn(
                          "group transition-colors hover:bg-muted/30",
                          binding.disabled && "opacity-75",
                        )}
                      >
                        <td className="px-4 py-3">
                          <div className="truncate font-mono font-medium">{binding.credential_ref}</div>
                          <div className="mt-1 flex items-center gap-1">
                            <Chip variant="neutral" className="h-4 text-[9px]">{binding.scope}</Chip>
                            {binding.disabled && (
                              <Chip variant="danger" className="h-4 text-[9px]">disabled</Chip>
                            )}
                          </div>
                          {provider && (
                            <div className="mt-0.5 text-xs text-muted-foreground">
                              Model provider ·{" "}
                              <Link
                                to={`/model-providers?selected=${provider.id}`}
                                className="text-info hover:underline"
                              >
                                {provider.name}
                              </Link>
                            </div>
                          )}
                        </td>
                        <td className="px-4 py-3">
                          {binding.disabled ? (
                            <span className="text-xs text-muted-foreground">Not resolved</span>
                          ) : (
                            <>
                              {destinationEnv && (
                                <div className="truncate font-mono text-xs" title={destinationEnv}>
                                  {destinationEnv}
                                </div>
                              )}
                              <div className="text-xs text-muted-foreground">
                                {binding.source.kind}
                                {sourceDisplay && (
                                  <> · <span className="font-mono">{sourceDisplay}</span></>
                                )}
                              </div>
                            </>
                          )}
                        </td>
                        <td className="px-4 py-3 text-muted-foreground">
                          {/* Classification first: a binding is either a Model
                              Provider key (managed on the Model providers page)
                              or a Global Environment Variable (injected into
                              every Runtime). Profile references are secondary. */}
                          {provider ? (
                            <span className="text-xs font-medium">
                              Provider key · {provider.name}
                            </span>
                          ) : (
                            <span className="text-xs font-medium">
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
                        </td>
                        <td className="px-4 py-3">
                          <div className="flex items-center justify-end gap-1 opacity-0 transition-opacity focus-within:opacity-100 group-hover:opacity-100">
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={`Edit ${binding.credential_ref} binding`}
                              onClick={() => startEdit(binding)}
                            >
                              <Pencil className="h-3.5 w-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={`Delete ${binding.credential_ref} binding`}
                              onClick={() => setConfirmDelete({ id: binding.id, credentialRef: binding.credential_ref })}
                              className="text-destructive"
                            >
                              <Trash2 className="h-3.5 w-3.5" />
                            </Button>
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
              {filteredBindings.length !== bindings.length && (
                <div className="border-t border-border bg-muted/20 px-4 py-2 text-xs text-muted-foreground">
                  Showing {filteredBindings.length} of {bindings.length} bindings
                </div>
              )}
            </div>
          )}
        </SettingsListColumn>

        <SettingsListColumn>
          {editing ? (
            <SettingsDetailPane
              data-testid="credential-binding-edit-panel"
              className="lg:flex-1"
              header={
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="font-medium">Edit binding</h3>
                    <p className="mt-1 text-sm text-muted-foreground">
                      Change the source for <span className="font-mono">{editing.credential_ref}</span>. References are immutable.
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={cancelEdit}
                    aria-label="Cancel edit form"
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              }
              footer={
                <>
                  <Button onClick={saveEdit} disabled={saving || editSaveDisabled}>
                    Save changes
                  </Button>
                  <Button variant="outline" onClick={cancelEdit} disabled={saving}>
                    Cancel
                  </Button>
                </>
              }
              bodyClassName="space-y-3"
            >
              <div>
                <Label htmlFor="credential-ref-edit">Credential reference</Label>
                <Input
                  id="credential-ref-edit"
                  name="credential_ref"
                  value={editing.credential_ref}
                  disabled
                  readOnly
                  className="font-mono text-xs"
                />
                <p className="mt-1 text-[11px] text-muted-foreground">
                  The reference identifies the binding; create a new binding to use a different reference.
                </p>
              </div>

              <div>
                <Label htmlFor="credential-source-kind-edit">Source kind</Label>
                <Select
                  id="credential-source-kind-edit"
                  name="source_kind"
                  value={form.kind}
                  onChange={(e) =>
                    setForm({
                      ...form,
                      kind: e.target.value,
                      value: e.target.value === editing.source.kind ? form.value : "",
                    })
                  }
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
                <Label htmlFor="credential-source-value-edit">{sourceValueLabel}</Label>
                <Input
                  id="credential-source-value-edit"
                  name="source_value"
                  type={form.kind === "literal" ? "password" : "text"}
                  value={form.value}
                  onChange={(e) => setForm({ ...form, value: e.target.value })}
                  placeholder={editingLiteralKeep ? "Leave blank to keep the stored secret" : form.kind === "env" ? "OPENAI_API_KEY…" : "sk-…"}
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
                <Label htmlFor="credential-destination-env-edit">Runtime environment variable name</Label>
                <Input
                  id="credential-destination-env-edit"
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
                  {destinationEnvRequired ? " Required." : " Defaults to the host variable name when empty."}
                </p>
              </div>
            </SettingsDetailPane>
          ) : !creating ? (
            <div
              data-testid="credential-binding-create-panel"
              className="rounded-lg border border-border bg-card p-4 shadow-sm"
            >
              <h3 className="text-sm font-medium">Library actions</h3>
              <p className="mt-1.5 text-xs text-muted-foreground">Reference existing credential sources without storing values in the UI unless you choose a literal source.</p>
              <Button onClick={startCreate} className="mt-3 w-full">
                <Plus className="h-4 w-4" /> New binding
              </Button>
              <p className="mt-3 text-xs text-muted-foreground">Manage Model Provider API keys on the Model Providers page when a credential is used only for LLM authentication.</p>
            </div>
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
