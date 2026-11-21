import { useEffect, useMemo, useState } from "react";
import { KeyRound, LoaderCircle, Plus, RefreshCw, Search } from "lucide-react";
import { apiGet, apiPut, apiDelete, type CredentialBinding, type ModelProvider, type RuntimeProfile } from "@/lib/api";
import { Button } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SettingsAlert, SettingsPageHeader, SettingsPanel, SettingsSplitLayout, SettingsPageShell } from "@/components/shared";
import { SettingsListColumn } from "@/components/settingsLibrary";
import { cn } from "@/lib/utils";
import { CredentialBindingsTable } from "./CredentialBindingsTable";
import { CredentialBindingCreatePane, CredentialBindingEditPane } from "./CredentialBindingForms";

type StatusFilter = "all" | "active" | "disabled";
type SourceFilter = "all" | "env" | "literal";

const emptyForm = { credential_ref: "", kind: "literal", value: "", destination_env: "" };

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
            <CredentialBindingsTable
              filteredBindings={filteredBindings}
              modelProviderForRef={modelProviderForRef}
              profilesUsingRef={profilesUsingRef}
              startEdit={startEdit}
              setConfirmDelete={setConfirmDelete}
              bindings={bindings}
            />
          )}
        </SettingsListColumn>

        <SettingsListColumn>
          {editing ? (
            <CredentialBindingEditPane
              editing={editing}
              cancelEdit={cancelEdit}
              saveEdit={saveEdit}
              saving={saving}
              editSaveDisabled={editSaveDisabled}
              form={form}
              setForm={setForm}
              sourceValueLabel={sourceValueLabel}
              editingLiteralKeep={editingLiteralKeep}
              destinationEnvRequired={destinationEnvRequired}
            />
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
            <CredentialBindingCreatePane
              cancelCreate={cancelCreate}
              create={create}
              saving={saving}
              form={form}
              destinationEnvRequired={destinationEnvRequired}
              setForm={setForm}
              sourceValueLabel={sourceValueLabel}
            />
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
