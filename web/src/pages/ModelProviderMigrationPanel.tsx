import { useEffect, useState } from "react";
import { ArrowRightLeft } from "lucide-react";
import { apiGet, apiPost, type ModelProviderMigrationPreview } from "@/lib/api";
import { Button, Input, Label, Badge, Select } from "@/components/ui";

type Props = {
  profileId: string;
  profileUpdatedAt?: string;
  onMigrated: () => void | Promise<void>;
  onError: (message: string) => void;
};

function normalizePreview(data: ModelProviderMigrationPreview): ModelProviderMigrationPreview {
  return {
    ...data,
    matches: data.matches ?? [],
    api_key_sources: data.api_key_sources ?? [],
  };
}

export function ModelProviderMigrationPanel({ profileId, profileUpdatedAt, onMigrated, onError }: Props) {
  const [preview, setPreview] = useState<ModelProviderMigrationPreview | null>(null);
  const [loading, setLoading] = useState(false);
  const [action, setAction] = useState<"create" | "reuse">("create");
  const [providerId, setProviderId] = useState("");
  const [providerName, setProviderName] = useState("");
  const [migrateAPIKey, setMigrateAPIKey] = useState(true);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      // Clear the stale preview before refetching, but after the first await so
      // the setState is not synchronous within the effect body.
      await Promise.resolve();
      if (cancelled) return;
      setPreview(null);
      try {
        const data = await apiGet<ModelProviderMigrationPreview>(
          `/api/runtime-profiles/${encodeURIComponent(profileId)}/model-provider-migration-preview`,
        );
        if (cancelled) return;
        applyPreview(normalizePreview(data));
      } catch (error) {
        if (!cancelled) onError((error as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [profileId, profileUpdatedAt, onError]);

  function applyPreview(data: ModelProviderMigrationPreview) {
    setPreview(data);
    setProviderName(data.proposed?.name ?? "");
    setProviderId(data.matches[0]?.provider.id ?? "");
    setAction(data.matches.length > 0 ? "reuse" : "create");
    setMigrateAPIKey(data.api_key_sources.some((source) => source.kind === "inline_api_key" && source.configured));
  }

  if (!preview?.eligible) return null;

  async function migrate() {
    setLoading(true);
    try {
      await apiPost(`/api/runtime-profiles/${encodeURIComponent(profileId)}/model-provider-migration`, {
        action,
        provider_id: action === "reuse" ? providerId : undefined,
        provider_name: action === "create" ? providerName : undefined,
        migrate_api_key: migrateAPIKey,
      });
      const refreshed = normalizePreview(
        await apiGet<ModelProviderMigrationPreview>(
          `/api/runtime-profiles/${encodeURIComponent(profileId)}/model-provider-migration-preview`,
        ),
      );
      applyPreview(refreshed);
      await onMigrated();
    } catch (error) {
      onError((error as Error).message);
    } finally {
      setLoading(false);
    }
  }

  const canMigrate = action === "create" ? !!providerName.trim() : !!providerId;

  return (
    <section
      role="region"
      aria-label="Model provider migration"
      className="space-y-3 rounded-lg border border-info/20 bg-muted/30 p-4"
    >
      <div className="flex items-start gap-2">
        <ArrowRightLeft className="mt-0.5 h-4 w-4 text-info" />
        <div>
          <h3 className="text-sm font-medium">Migrate to model provider</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            Move legacy endpoint, model, and API key settings into a reusable model provider, then clear them from this profile.
          </p>
        </div>
      </div>

      <div className="grid gap-2 text-sm md:grid-cols-2">
        <div><span className="text-muted-foreground">Base URL:</span> <code>{preview.proposed.base_url}</code></div>
        {preview.proposed.model && <div><span className="text-muted-foreground">Model:</span> <code>{preview.proposed.model}</code></div>}
        {preview.proposed.suggested_protocol && (
          <div><span className="text-muted-foreground">Protocol:</span> <code>{preview.proposed.suggested_protocol}</code></div>
        )}
      </div>

      {(preview.api_key_sources ?? []).length > 0 && (
        <div className="flex flex-wrap gap-1">
          {(preview.api_key_sources ?? []).map((source) => (
            <Badge key={`${source.kind}-${source.env_var ?? source.credential_ref}`} variant="outline">
              {source.kind}
              {source.env_var ? `: ${source.env_var}` : ""}
              {source.credential_ref ? `: ${source.credential_ref}` : ""}
              {source.configured ? " [configured]" : ""}
            </Badge>
          ))}
        </div>
      )}

      <fieldset className="space-y-2" role="group" aria-label="Migration target">
        <legend className="text-sm font-medium leading-none text-muted-foreground">Migration target</legend>
        <div className="flex flex-wrap gap-3 text-sm">
          <label className="inline-flex items-center gap-2">
            <input type="radio" name="migration_target" checked={action === "create"} onChange={() => setAction("create")} />
            Create new provider
          </label>
          {(preview.matches ?? []).length > 0 && (
            <label className="inline-flex items-center gap-2">
              <input type="radio" name="migration_target" checked={action === "reuse"} onChange={() => setAction("reuse")} />
              Reuse existing match
            </label>
          )}
        </div>
      </fieldset>

      {action === "create" ? (
        <div>
          <Label htmlFor="migration-provider-name">Provider name</Label>
          <Input
            id="migration-provider-name"
            name="provider_name"
            value={providerName}
            onChange={(e) => setProviderName(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </div>
      ) : (
        <div>
          <Label htmlFor="migration-existing-provider">Existing provider</Label>
          <Select
            id="migration-existing-provider"
            name="existing_provider"
            value={providerId}
            onChange={(e) => setProviderId(e.target.value)}
          >
            {(preview.matches ?? []).map((match) => (
              <option key={match.provider.id} value={match.provider.id}>
                {match.provider.name} ({match.provider.base_url})
              </option>
            ))}
          </Select>
        </div>
      )}

      {(preview.api_key_sources ?? []).some((source) => source.kind === "inline_api_key") && (
        <label className="inline-flex items-center gap-2 text-sm">
          <input name="migrate_api_key" type="checkbox" checked={migrateAPIKey} onChange={(e) => setMigrateAPIKey(e.target.checked)} />
          Copy inline API key into the model provider credential binding
        </label>
      )}

      <Button size="sm" onClick={() => void migrate()} disabled={!canMigrate || loading}>
        {loading ? "Migrating…" : "Migrate profile"}
      </Button>
    </section>
  );
}
