import type { SetStateAction, Dispatch } from "react";
import { Link } from "react-router-dom";
import { Pencil, Trash2 } from "lucide-react";
import type { CredentialBinding, ModelProvider } from "@/lib/api";
import { Badge, Button, Chip } from "@/components/ui";
import { cn } from "@/lib/utils";
import { formatSourceDisplay } from "./credentialBindingSource";

export function CredentialBindingsTable({
  filteredBindings,
  modelProviderForRef,
  profilesUsingRef,
  startEdit,
  setConfirmDelete,
  bindings,
}: {
  filteredBindings: CredentialBinding[];
  modelProviderForRef: (ref: string) => ModelProvider | undefined;
  profilesUsingRef: (ref: string) => string[];
  startEdit: (binding: CredentialBinding) => void;
  setConfirmDelete: Dispatch<SetStateAction<{ id: string; credentialRef: string; } | null>>;
  bindings: CredentialBinding[];
}) {
  return (
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
  );
}
