import { useEffect, useState } from "react";
import { apiPost, projectedConfig } from "@/lib/api";
import { Button } from "@/components/ui";

type Config = Awaited<ReturnType<typeof projectedConfig>>;
type KeyError = { key: string; field?: string; message: string };

export function RuntimeProfileConfig({ profileId, canEdit, onImported }: {
  profileId: string;
  canEdit: boolean;
  onImported: () => Promise<void>;
}) {
  const [config, setConfig] = useState<Config | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [importing, setImporting] = useState(false);
  const [keyErrors, setKeyErrors] = useState<KeyError[]>([]);

  useEffect(() => {
    let cancelled = false;
    void projectedConfig(profileId).then((data) => {
      if (typeof data.text !== "string") throw new Error("The server did not return a runtime config.");
      if (!cancelled) setConfig(data);
    }).catch((cause: unknown) => {
      if (!cancelled) setError(cause instanceof Error ? cause.message : "Could not load runtime config.");
    });
    return () => { cancelled = true; };
  }, [profileId]);

  async function importConfig() {
    if (!canEdit || importing) return;
    setImporting(true);
    setError(null);
    setKeyErrors([]);
    try {
      await apiPost(`/api/runtime-profiles/${encodeURIComponent(profileId)}/import-config`, { config_text: draft });
      await onImported();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not import runtime config.");
      setKeyErrors((cause as { body?: { keys?: KeyError[] } }).body?.keys ?? []);
    } finally {
      setImporting(false);
    }
  }

  return (
    <section id="actual-runtime-config" aria-label="Actual runtime config" className="min-w-0 space-y-3 rounded-md border border-border p-3">
      <p className="text-sm text-muted-foreground">Configuration generated from the saved profile, including custom settings. Secret values are redacted. Task-specific settings are added at launch.</p>
      {!canEdit && <p className="text-sm text-muted-foreground">Save profile changes before editing the runtime config.</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      {!config && !error && <p role="status" className="text-sm text-muted-foreground">Loading runtime config…</p>}
      {config && (editing ? (
        <>
          <p className="text-sm text-muted-foreground">Import maps supported keys back to the form and retains other settings in the Custom Config File. Managed keys and secret values are refused.</p>
          <label className="sr-only" htmlFor="runtime-config-editor">Runtime config editor</label>
          <textarea id="runtime-config-editor" className="min-h-[40vh] w-full resize-y rounded-md border border-border bg-muted/30 p-3 font-mono text-xs" value={draft} onChange={(event) => setDraft(event.target.value)} spellCheck={false} disabled={importing} />
          {keyErrors.length > 0 && <ul role="alert" className="list-inside list-disc text-sm text-destructive">
            {keyErrors.map((item) => <li key={item.key}><span className="font-mono">{item.key}</span>: {item.message}{item.field ? ` (owned by ${item.field})` : ""}</li>)}
          </ul>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" disabled={importing} onClick={() => { setEditing(false); setError(null); setKeyErrors([]); }}>Cancel</Button>
            <Button disabled={!canEdit || importing} onClick={() => void importConfig()}>{importing ? "Importing…" : "Import config"}</Button>
          </div>
        </>
      ) : (
        <>
          <pre className="max-h-96 w-full max-w-full overflow-x-auto rounded-md bg-muted/30 p-3 text-xs">{config.text}</pre>
          <Button size="sm" variant="outline" disabled={!canEdit} onClick={() => { setDraft(config.text); setEditing(true); }}>Edit config</Button>
        </>
      ))}
    </section>
  );
}
