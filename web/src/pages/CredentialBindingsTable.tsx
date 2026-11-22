import type { SetStateAction, Dispatch } from "react";
import { Pencil, Trash2 } from "lucide-react";
import type { CredentialBinding, ModelProvider } from "@/lib/api";
import { Button } from "@/components/ui";
import { cn } from "@/lib/utils";

export function CredentialBindingsTable({
  filteredBindings, modelProviderForRef, profilesUsingRef, startEdit, setConfirmDelete, bindings,
  selectedId, onSelect,
}: {
  filteredBindings: CredentialBinding[];
  modelProviderForRef: (ref: string) => ModelProvider | undefined;
  profilesUsingRef: (ref: string) => string[];
  startEdit: (binding: CredentialBinding) => void;
  setConfirmDelete: Dispatch<SetStateAction<{ id: string; credentialRef: string; } | null>>;
  bindings: CredentialBinding[];
  selectedId?: string;
  onSelect: (binding: CredentialBinding) => void;
}) {
  return (
    <div data-testid="credentials-library-list" className="min-w-0 space-y-1">
      {filteredBindings.map((binding) => {
        const provider = modelProviderForRef(binding.credential_ref);
        const usedByProfiles = profilesUsingRef(binding.credential_ref);
        return <div key={binding.id} data-testid={`credential-row-${binding.id}`} className="group relative min-w-0">
          <button type="button" onClick={() => onSelect(binding)} aria-current={binding.id === selectedId ? "true" : undefined}
            aria-controls="credential-binding-details" aria-label={`View ${binding.credential_ref} binding`}
            className={cn("w-full rounded-md px-3 py-3 pr-16 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring", binding.id === selectedId ? "bg-muted/70" : "hover:bg-muted/40")}>
            <span className="block truncate font-mono text-xs font-medium" title={binding.credential_ref}>{binding.credential_ref}</span>
            <span className="mt-1 block text-[10px] text-muted-foreground">{binding.disabled ? "Disabled" : "Active"} · {binding.source.kind}</span>
            <span className="mt-0.5 block text-[10px] text-muted-foreground">{provider ? `Provider key · ${provider.name}` : "Global env var"}</span>
            {usedByProfiles.length > 0 && <span className="block text-[10px] text-muted-foreground">{usedByProfiles.length} Runtime Profiles</span>}
          </button>
          <div className="absolute right-1 top-2 flex gap-0.5 opacity-0 focus-within:opacity-100 group-hover:opacity-100">
            <Button variant="ghost" size="icon-sm" aria-label={`Edit ${binding.credential_ref} binding`} onClick={() => startEdit(binding)}><Pencil className="size-3" /></Button>
            <Button variant="ghost" size="icon-sm" aria-label={`Delete ${binding.credential_ref} binding`} onClick={() => setConfirmDelete({ id: binding.id, credentialRef: binding.credential_ref })}><Trash2 className="size-3" /></Button>
          </div>
        </div>;
      })}
      {filteredBindings.length !== bindings.length && <p className="px-3 py-2 text-[10px] text-muted-foreground">Showing {filteredBindings.length} of {bindings.length} bindings</p>}
    </div>
  );
}
