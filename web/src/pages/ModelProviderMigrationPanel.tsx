import { useEffect, useState } from "react";
import { ArrowRightLeft } from "lucide-react";
import { apiGet, apiPost, type ModelProviderMigrationPreview } from "@/lib/api";
import { Button, Card, Input, Label, Badge } from "@/components/ui";

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
    setPreview(null);
    void apiGet<ModelProviderMigrationPreview>(
      `/api/runtime-profiles/${encodeURIComponent(profileId)}/model-provider-migration-preview`,
    )
      .then((data) => {
        if (cancelled) return;
        applyPreview(normalizePreview(data));
      })
      .catch((error) => {
        if (!cancelled) onError((error as Error).message);
      });
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
    <Card className="space-y-3 border-primary/20 bg-primary/5 p-4">
      <div className="flex items-start gap-2">
        <ArrowRightLeft className="mt-0.5 h-4 w-4 text-primary" />
        <div>
          <h4 className="text-sm font-medium">Migrate to model provider</h4>
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

      <div className="space-y-2">
        <Label>Migration target</Label>
        <div className="flex flex-wrap gap-3 text-sm">
          <label className="inline-flex items-center gap-2">
            <input type="radio" checked={action === "create"} onChange={() => setAction("create")} />
            Create new provider
          </label>
          {(preview.matches ?? []).length > 0 && (
            <label className="inline-flex items-center gap-2">
              <input type="radio" checked={action === "reuse"} onChange={() => setAction("reuse")} />
              Reuse existing match
            </label>
          )}
        </div>
      </div>

      {action === "create" ? (
        <div>
          <Label>Provider name</Label>
          <Input value={providerName} onChange={(e) => setProviderName(e.target.value)} />
        </div>
      ) : (
        <div>
          <Label>Existing provider</Label>
          <select
            className="flex h-8 w-full rounded-lg border border-input bg-transparent px-2.5 text-sm"
            value={providerId}
            onChange={(e) => setProviderId(e.target.value)}
          >
            {(preview.matches ?? []).map((match) => (
              <option key={match.provider.id} value={match.provider.id}>
                {match.provider.name} ({match.provider.base_url})
              </option>
            ))}
          </select>
        </div>
      )}

      {(preview.api_key_sources ?? []).some((source) => source.kind === "inline_api_key") && (
        <label className="inline-flex items-center gap-2 text-sm">
          <input type="checkbox" checked={migrateAPIKey} onChange={(e) => setMigrateAPIKey(e.target.checked)} />
          Copy inline API key into the model provider credential binding
        </label>
      )}

      <Button size="sm" onClick={() => void migrate()} disabled={!canMigrate || loading}>
        {loading ? "Migrating…" : "Migrate profile"}
      </Button>
    </Card>
  );
}