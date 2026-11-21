import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { History, Loader2 } from "lucide-react";
import { blackboardHref, formatBlackboardV2Error, primaryLabelForDetail, readCurrentDetail, readSemanticHistory, readSnapshot, recordHref, type CurrentDetail, type HistoryItem } from "@/lib/blackboardv2";
import { Badge, Card } from "@/components/ui";
import { SectionHeading, ErrorBanner } from "./BlackboardPanelChrome";

export function RecordPanel({ projectId, recordKey }: { projectId: string; recordKey: string }) {
  const [detail, setDetail] = useState<CurrentDetail | null>(null);
  const [snapshotRevision, setSnapshotRevision] = useState<number | null>(null);
  const [snapshotError, setSnapshotError] = useState<string | null>(null);
  const [historyItems, setHistoryItems] = useState<HistoryItem[] | null>(null);
  const [historyCursor, setHistoryCursor] = useState<string | undefined>(undefined);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  // Bumped on unmount so in-flight history for a previous mount cannot apply.
  const mountedGenRef = useRef(0);

  useEffect(() => {
    const gen = ++mountedGenRef.current;
    let cancelled = false;
    (async () => {
      try {
        const current = await readCurrentDetail(projectId, recordKey);
        if (cancelled || gen !== mountedGenRef.current) return;

        let revision: number | null = null;
        let snapErr: string | null = null;
        try {
          const snapshot = await readSnapshot(projectId);
          if (cancelled || gen !== mountedGenRef.current) return;
          revision = snapshot.revision;
        } catch (e) {
          if (cancelled || gen !== mountedGenRef.current) return;
          // Surface snapshot failure as degraded state; do not swallow silently.
          snapErr = formatBlackboardV2Error(e);
        }

        setDetail(current);
        setSnapshotRevision(revision);
        setSnapshotError(snapErr);
        setError(null);
      } catch (e) {
        if (!cancelled && gen === mountedGenRef.current) {
          setError(formatBlackboardV2Error(e));
        }
      }
    })();
    return () => {
      cancelled = true;
      // Invalidate any in-flight history tied to this mount.
      mountedGenRef.current += 1;
    };
  }, [projectId, recordKey]);

  async function loadHistory(cursor?: string) {
    const gen = mountedGenRef.current;
    setHistoryLoading(true);
    setHistoryError(null);
    try {
      const page = await readSemanticHistory(projectId, recordKey, {
        limit: 20,
        cursor,
      });
      if (gen !== mountedGenRef.current) return;
      setHistoryItems((prev) => (cursor && prev ? [...prev, ...page.items] : page.items));
      setHistoryCursor(page.next_cursor);
    } catch (e) {
      if (gen !== mountedGenRef.current) return;
      setHistoryError(formatBlackboardV2Error(e));
    } finally {
      if (gen === mountedGenRef.current) setHistoryLoading(false);
    }
  }

  if (error) return <ErrorBanner message={error} />;
  if (!detail) {
    return (
      <Card role="status" aria-label="Loading record" className="m-4 min-h-24 items-center justify-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        Loading record
      </Card>
    );
  }

  const stale =
    snapshotRevision !== null && detail.revision !== snapshotRevision
      ? { detail: detail.revision, snapshot: snapshotRevision }
      : null;

  const fieldEntries = Object.entries(detail.record).filter(
    ([, value]) => value !== undefined && value !== null && value !== "",
  );

  return (
    <section className="min-w-0 space-y-4 p-4" aria-label="Record detail">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            {detail.type}
          </p>
          <h3 className="break-words text-base font-semibold text-foreground">
            {primaryLabelForDetail(detail)}
          </h3>
          <p className="mt-1 break-all font-mono text-xs text-muted-foreground">{detail.key}</p>
        </div>
        <div className="flex flex-wrap gap-1">
          <Badge variant="outline">v{detail.version}</Badge>
          <Badge variant="outline">revision {detail.revision}</Badge>
        </div>
      </div>

      {snapshotError && (
        <p
          role="status"
          aria-label="snapshot unavailable"
          className="rounded-md border border-destructive/25 bg-destructive/5 px-3 py-2 text-sm text-foreground"
        >
          Snapshot unavailable (degraded): {snapshotError}
        </p>
      )}

      {stale && (
        <p
          role="status"
          aria-label="stale revision"
          className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-foreground"
        >
          Stale revision: detail revision {stale.detail}; snapshot revision {stale.snapshot}.
        </p>
      )}

      <section aria-label="Semantic fields">
        <SectionHeading title="Fields" />
        <dl className="grid gap-2 sm:grid-cols-2">
          {fieldEntries.map(([key, value]) => (
            <div key={key} className="rounded-md border border-border bg-background/40 px-3 py-2">
              <dt className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                {key}
              </dt>
              <dd className="mt-1 break-words text-sm text-foreground">
                {typeof value === "boolean" ? String(value) : String(value)}
              </dd>
            </div>
          ))}
        </dl>
      </section>

      <section aria-label="Relationships">
        <SectionHeading title="Relationships" detail={`${detail.relationships.length}`} />
        {detail.relationships.length === 0 ? (
          <p className="text-sm text-muted-foreground">No current relationships.</p>
        ) : (
          <ul className="divide-y divide-border border-y border-border text-sm">
            {detail.relationships.map((edge, index) => (
              <li key={`${edge.from}-${edge.relation}-${edge.to}-${index}`} className="px-3 py-2">
                <span className="font-mono text-xs">
                  <Link to={recordHref(projectId, edge.from)} className="underline-offset-4 hover:underline">
                    {edge.from}
                  </Link>
                  {" — "}
                  {edge.relation}
                  {" → "}
                  <Link to={recordHref(projectId, edge.to)} className="underline-offset-4 hover:underline">
                    {edge.to}
                  </Link>
                </span>
                {edge.reason && (
                  <p className="mt-0.5 text-muted-foreground">{edge.reason}</p>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={() => void loadHistory()}
            disabled={historyLoading}
            className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-2.5 py-1.5 text-sm font-medium shadow-sm transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
          >
            <History className="size-3.5" aria-hidden="true" />
            Semantic History
          </button>
        </div>
        {historyError && (
          <p role="alert" className="text-sm text-destructive">
            {historyError}
          </p>
        )}
        {historyItems && (
          <section aria-label="Semantic History" className="space-y-2">
            <SectionHeading title="Semantic History" detail={`${historyItems.length} ${historyItems.length === 1 ? "item" : "items"}`} />
            <ul className="divide-y divide-border border-y border-border">
              {historyItems.map((item, index) => (
                <li key={`${item.kind}-${item.version}-${index}`} className="px-3 py-2 text-sm">
                  <span className="font-mono text-xs text-muted-foreground">
                    v{item.version}
                    {item.type ? ` · ${item.type}` : ""}
                  </span>
                  {item.record && (
                    <p className="mt-0.5 text-foreground">
                      {item.record.title ??
                        item.record.name ??
                        item.record.summary ??
                        item.record.objective ??
                        item.kind}
                    </p>
                  )}
                  {item.relation && (
                    <p className="font-mono text-xs text-muted-foreground">
                      {item.from} — {item.relation} → {item.to}
                      {item.reason ? ` · ${item.reason}` : ""}
                    </p>
                  )}
                </li>
              ))}
              {historyItems.length === 0 && (
                <li className="px-3 py-4 text-sm text-muted-foreground">No history items.</li>
              )}
            </ul>
            {historyCursor && (
              <button
                type="button"
                onClick={() => void loadHistory(historyCursor)}
                disabled={historyLoading}
                className="rounded-md border border-border bg-background px-2.5 py-1.5 text-sm shadow-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
              >
                Load more
              </button>
            )}
          </section>
        )}
      </section>

      <div className="flex flex-wrap gap-3 text-sm">
        <Link
          to={blackboardHref(projectId, "work")}
          className="text-muted-foreground underline-offset-4 hover:underline"
        >
          Back to Work
        </Link>
        <Link
          to={blackboardHref(projectId, "explorer")}
          className="text-muted-foreground underline-offset-4 hover:underline"
        >
          Open in Explorer
        </Link>
      </div>
    </section>
  );
}
