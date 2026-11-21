import { Link } from "react-router-dom";
import type { CredentialBinding, ModelProvider } from "@/lib/api";
import { Button } from "@/components/ui";
import { ActionMenu } from "@/components/ActionMenu";

export function CredentialBindingDetails({ binding, provider, profileNames, onEdit, onDelete }: {
  binding: CredentialBinding;
  provider?: ModelProvider;
  profileNames: string[];
  onEdit: () => void;
  onDelete: () => void;
}) {
  const destination = binding.source.destination_env || (binding.source.kind === "env" ? binding.source.value : "");
  return <section className="min-w-0 space-y-6" aria-label="Credential details">
    <header className="flex items-center justify-between gap-4">
      <h2 className="break-all font-mono text-[15px] font-semibold">{binding.credential_ref}</h2>
      <ActionMenu label="More"><Button variant="ghost" onClick={onEdit}>Edit binding</Button><Button variant="ghost" onClick={onDelete} className="text-destructive">Delete binding</Button></ActionMenu>
    </header>
    <dl className="grid gap-6 border-y border-border py-6 text-xs sm:grid-cols-2">
      <div><dt className="text-muted-foreground">Source</dt><dd className="mt-2">{binding.disabled ? "Not resolved" : binding.source.kind === "env" ? "Host environment" : "Secret value (hidden)"}</dd></div>
      <div><dt className="text-muted-foreground">Status</dt><dd className="mt-2">{binding.disabled ? "Disabled" : "Active"}</dd></div>
      {binding.source.kind === "env" && !binding.disabled && <div><dt className="text-muted-foreground">Host environment variable</dt><dd className="mt-2 break-all font-mono">{binding.source.value}</dd></div>}
      <div><dt className="text-muted-foreground">Destination environment variable</dt><dd className="mt-2 break-all font-mono">{destination || "Not set"}</dd></div>
      <div><dt className="text-muted-foreground">Applies to</dt><dd className="mt-2">{provider ? <Link className="text-signal hover:underline" to={`/model-providers?selected=${provider.id}`}>{provider.name} Model Provider</Link> : "Every Runtime launch"}</dd></div>
      <div><dt className="text-muted-foreground">Referenced by Runtime Profiles</dt><dd className="mt-2">{profileNames.length ? profileNames.join(", ") : "No explicit profile references"}</dd></div>
    </dl>
    <p className="text-[11px] leading-5 text-muted-foreground">Only references and sources are shown. Secret values are never displayed. Bindings resolve during preflight.</p>
  </section>;
}
