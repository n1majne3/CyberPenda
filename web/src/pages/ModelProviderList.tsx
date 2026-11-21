import type { SetStateAction, Dispatch } from "react";
import type { CredentialBinding, ModelProvider } from "@/lib/api";
import { Chip, Button } from "@/components/ui";
import { settingsListItemClasses } from "@/components/sharedStyles";
import { cn } from "@/lib/utils";
import { providerProtocols, providerDisplayURL, shortProtocol } from "./modelProviderProtocols";
import { Plus, Server } from "lucide-react";
import { SettingsPanel } from "@/components/shared";
import { SettingsListColumn, SettingsSearchField } from "@/components/settingsLibrary";

export function ModelProviderList({
  filteredProviders,
  selectedId,
  creating,
  bindings,
  setCreating,
  setSelectedId,
  setSavedNotice,
}: {
  filteredProviders: ModelProvider[];
  selectedId: string;
  creating: boolean;
  bindings: CredentialBinding[];
  setCreating: Dispatch<SetStateAction<boolean>>;
  setSelectedId: Dispatch<SetStateAction<string>>;
  setSavedNotice: Dispatch<SetStateAction<boolean>>;
}) {
  return (
    <ul className="space-y-1">
      {filteredProviders.map((provider) => {
        const isSelected = selectedId === provider.id && !creating;
        const protocols = providerProtocols(provider);
        const binding = bindings.find((item) => item.credential_ref === provider.api_key_env);
        const hasApiKey = Boolean(binding && !binding.disabled);
        const displayUrl = providerDisplayURL(provider);
        return (
          <li key={provider.id}>
            <button
              type="button"
              aria-pressed={isSelected}
              aria-current={isSelected ? "true" : undefined}
              className={settingsListItemClasses(isSelected, "w-full px-2.5 py-2.5")}
              onClick={() => {
                setCreating(false);
                setSelectedId(provider.id);
                setSavedNotice(false);
              }}
            >
              <span className="flex items-center gap-2">
                <span
                  className={cn(
                    "h-1.5 w-1.5 flex-none rounded-full",
                    hasApiKey ? "bg-success" : "bg-muted-foreground/40",
                  )}
                />
                <span className="truncate text-sm font-medium text-foreground">{provider.name}</span>
                {hasApiKey && (
                  <Chip variant="success" className="h-4 flex-none text-[9px]">key set</Chip>
                )}
              </span>
              {displayUrl ? (
                <span className="mt-1 block truncate font-mono text-xs text-muted-foreground">
                  {displayUrl}
                </span>
              ) : (
                <span className="mt-1 block truncate font-mono text-xs text-muted-foreground">
                  {provider.api_key_env}
                </span>
              )}
              {protocols.length > 0 && (
                <span className="mt-1.5 flex flex-wrap gap-1">
                  {protocols.slice(0, 2).map((protocol) => (
                    <Chip key={protocol} variant="neutral" className="h-4 text-[9px]">
                      {shortProtocol(protocol)}
                    </Chip>
                  ))}
                  {protocols.length > 2 && (
                    <Chip variant="neutral" className="h-4 text-[9px]">
                      +{protocols.length - 2}
                    </Chip>
                  )}
                </span>
              )}
            </button>
          </li>
        );
      })}
    </ul>
  );
}

export function ModelProviderListColumn({
  providers,
  query,
  setQuery,
  configuredKeyCount,
  startCreate,
  filteredProviders,
  selectedId,
  creating,
  bindings,
  setCreating,
  setSelectedId,
  setSavedNotice,
}: {
  providers: ModelProvider[];
  query: string;
  setQuery: Dispatch<SetStateAction<string>>;
  configuredKeyCount: number;
  startCreate: () => void;
  filteredProviders: ModelProvider[];
  selectedId: string;
  creating: boolean;
  bindings: CredentialBinding[];
  setCreating: Dispatch<SetStateAction<boolean>>;
  setSelectedId: Dispatch<SetStateAction<string>>;
  setSavedNotice: Dispatch<SetStateAction<boolean>>;
}) {
  return (
    <SettingsListColumn data-testid="model-providers-settings-list">
      <SettingsPanel className="gap-3 p-3 lg:shrink-0">
        <div className="flex items-baseline justify-between gap-2">
          <div>
            <p className="text-sm font-medium">Providers</p>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              Select one to edit endpoints and catalog
            </p>
          </div>
          <div className="shrink-0 text-right tabular-nums">
            <span className="text-lg font-semibold tracking-tight">{providers.length}</span>
            <span className="ml-1 text-[11px] text-muted-foreground"> total</span>
          </div>
        </div>

        <SettingsSearchField
          id="model-providers-search"
          name="model_providers_search"
          value={query}
          onChange={setQuery}
          placeholder="Search name, URL, key…"
          size="sm"
          aria-label="Search model providers"
        />

        {providers.length > 0 && (
          <p className="text-[11px] text-muted-foreground">
            <span className="tabular-nums font-medium text-foreground">{configuredKeyCount}</span>
            {" "}with API key binding
          </p>
        )}
      </SettingsPanel>

      <SettingsPanel className="gap-1 p-2 lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain">
        {providers.length === 0 ? (
          <div className="flex flex-col items-center gap-3 px-2 py-8 text-center">
            <div className="flex h-10 w-10 items-center justify-center rounded-full bg-muted">
              <Server className="h-4 w-4 text-muted-foreground" />
            </div>
            <p className="text-sm text-muted-foreground">No model providers yet.</p>
            <Button size="sm" onClick={startCreate}>
              <Plus className="h-3.5 w-3.5" /> New provider
            </Button>
          </div>
        ) : filteredProviders.length === 0 ? (
          <div className="px-2 py-8 text-center">
            <p className="text-sm font-medium">No matching providers</p>
            <p className="mt-1 text-xs text-muted-foreground">Try a different search.</p>
            <Button size="sm" variant="outline" className="mt-3" onClick={() => setQuery("")}>
              Clear search
            </Button>
          </div>
        ) : (
          <ModelProviderList
            filteredProviders={filteredProviders}
            selectedId={selectedId}
            creating={creating}
            bindings={bindings}
            setCreating={setCreating}
            setSelectedId={setSelectedId}
            setSavedNotice={setSavedNotice}
          />
        )}
      </SettingsPanel>
    </SettingsListColumn>
  );
}
