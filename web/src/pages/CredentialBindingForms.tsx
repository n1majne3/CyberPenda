import type { SetStateAction, Dispatch } from "react";
import { X } from "lucide-react";
import { Button, Input, Label, Select } from "@/components/ui";
import { SettingsDetailPane } from "@/components/settingsLibrary";
import { SOURCE_KIND_LABELS } from "./credentialBindingSource";
import type { CredentialBinding } from "@/lib/api";

export function CredentialBindingCreatePane({
  cancelCreate,
  create,
  saving,
  form,
  destinationEnvRequired,
  setForm,
  sourceValueLabel,
}: {
  cancelCreate: () => void;
  create: () => Promise<void>;
  saving: boolean;
  form: { credential_ref: string; kind: string; value: string; destination_env: string; };
  destinationEnvRequired: boolean;
  setForm: Dispatch<SetStateAction<{ credential_ref: string; kind: string; value: string; destination_env: string; }>>;
  sourceValueLabel: "Host environment variable name" | "Secret value";
}) {
  return (
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
  );
}

export function CredentialBindingEditPane({
  editing,
  cancelEdit,
  saveEdit,
  saving,
  editSaveDisabled,
  form,
  setForm,
  sourceValueLabel,
  editingLiteralKeep,
  destinationEnvRequired,
}: {
  editing: CredentialBinding;
  cancelEdit: () => void;
  saveEdit: () => Promise<void>;
  saving: boolean;
  editSaveDisabled: boolean;
  form: { credential_ref: string; kind: string; value: string; destination_env: string; };
  setForm: Dispatch<SetStateAction<{ credential_ref: string; kind: string; value: string; destination_env: string; }>>;
  sourceValueLabel: "Host environment variable name" | "Secret value";
  editingLiteralKeep: boolean;
  destinationEnvRequired: boolean;
}) {
  return (
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
  );
}
