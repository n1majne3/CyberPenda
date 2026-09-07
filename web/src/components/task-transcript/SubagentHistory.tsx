import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { apiGet, type TaskTranscriptEntry } from "@/lib/api";
import { Button } from "@/components/ui";

interface ChildPage {
  entries: TaskTranscriptEntry[];
  cursor: number;
  before: number;
  has_older: boolean;
  has_newer: boolean;
}

// Keep one bounded child window. A stable item update replaces its earlier
// version in place; new items enter at the tail. Older pages replace the view.
function appendChildItems(previous: TaskTranscriptEntry[], updates: TaskTranscriptEntry[]) {
  const items = new Map(previous.map((item) => [item.id, item]));
  for (const item of updates) {
    const old = items.get(item.id);
    if (!old || item.seq >= old.seq) items.set(item.id, item);
  }
  const bounded: TaskTranscriptEntry[] = [];
  let bytes = 0;
  const encoder = new TextEncoder();
  for (const item of [...items.values()].sort((a, b) => (a.position ?? a.seq) - (b.position ?? b.seq)).reverse()) {
    const size = encoder.encode(JSON.stringify(item)).length;
    if (bounded.length >= 200 || (bytes + size > 256 * 1024 && bounded.length > 0)) break;
    bytes += size;
    bounded.push(item);
  }
  return bounded.reverse();
}

interface Props {
  history: string;
  renderItems: (items: TaskTranscriptEntry[]) => ReactNode;
}

export function SubagentHistory(props: Props) {
  return <ChildHistoryWindow key={props.history} {...props} />;
}

function ChildHistoryWindow({ history, renderItems }: Props) {
  const [page, setPage] = useState<ChildPage>();
  const [tail, setTail] = useState(true);
  const [unseen, setUnseen] = useState(false);
  const [retries, setRetries] = useState(0);
  const [full, setFull] = useState<TaskTranscriptEntry>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const generation = useRef(0);
  const active = useRef<AbortController | null>(null);
  const viewport = useRef<HTMLDivElement>(null);
  const following = useRef(true);

  const load = useCallback((before?: number) => {
    const token = ++generation.current;
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    return apiGet<ChildPage>(before ? `${history}?before=${before}` : history, { signal: controller.signal }).then((result) => {
      if (token !== generation.current) return;
      setPage(result);
      setTail(!before);
      following.current = !before;
      setUnseen(false);
    }).catch((err: unknown) => {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "Could not read child activity.");
    }).finally(() => {
      if (token === generation.current) { active.current = null; setLoading(false); }
    });
  }, [history]);

  function browse(before?: number) {
    setLoading(true); setFull(undefined); setError("");
    void load(before);
  }

  const cancel = useCallback(() => { generation.current++; active.current?.abort(); active.current = null; }, []);
  useEffect(() => {
    void load();
    return cancel;
  }, [load, cancel]);

  useEffect(() => {
    if (!page || loading) return;
    const timer = setTimeout(async () => {
      if (active.current) return;
      const token = generation.current;
      const controller = new AbortController();
      active.current = controller;
      try {
        const delta = await apiGet<ChildPage>(`${history}?after=${page.cursor}`, { signal: controller.signal });
        if (token !== generation.current) return;
        if (tail && following.current && !unseen) {
          setPage((old) => {
            if (!old) return old;
            const entries = appendChildItems(old.entries, delta.entries);
            return { ...old, entries, cursor: delta.cursor, before: entries[0]?.position ?? old.before,
              has_older: old.has_older || (entries[0]?.position ?? 0) > (old.entries[0]?.position ?? 0), has_newer: delta.has_newer };
          });
        } else {
          if (delta.entries.length) setUnseen(true);
          // Keep the user's page intact. Returning to latest performs a fresh read.
          setPage((old) => old ? { ...old, cursor: delta.cursor, has_newer: delta.has_newer } : old);
        }
        setError("");
      } catch (err) {
        if (!controller.signal.aborted) { setError(err instanceof Error ? err.message : "Could not refresh child activity."); setRetries((value) => value + 1); }
      } finally {
        if (token === generation.current) active.current = null;
      }
    }, page.has_newer ? 0 : 3000);
    return () => clearTimeout(timer);
  }, [history, page, tail, loading, retries, unseen]);

  useEffect(() => {
    if (tail && following.current && viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight;
  }, [page, tail]);

  return <div className="space-y-2" data-testid="child-history">
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" variant="outline" disabled={loading || !page?.has_older} onClick={() => browse(page?.before)}>Older child activity</Button>
      <Button size="sm" variant="outline" disabled={loading} onClick={() => browse()}>Latest child activity{unseen ? " · new activity" : ""}</Button>
      {loading && <span role="status" className="text-xs text-muted-foreground">Loading child activity…</span>}
    </div>
    {error && <p role="alert" className="text-xs text-destructive">{error} <button type="button" onClick={() => browse()}>Retry</button></p>}
    <div ref={viewport} className="max-h-[32rem] overflow-y-auto overscroll-contain" onScroll={() => {
      const node = viewport.current;
      if (node) following.current = node.scrollHeight - node.scrollTop - node.clientHeight < 24;
    }}>
      {page && renderItems(page.entries)}
      {page?.entries.filter((entry) => entry.truncated && entry.detail).map((entry) => <button className="block text-xs underline" type="button" key={entry.id} onClick={async () => {
        const token = generation.current;
        try {
          const result = await apiGet<TaskTranscriptEntry>(entry.detail!);
          if (token !== generation.current) return;
          if (result.id !== entry.id || result.seq !== entry.seq || result.truncated) throw new Error("Child detail does not match its preview.");
          setFull(result);
        } catch (err) { if (token === generation.current) setError(err instanceof Error ? err.message : "Could not read full child entry."); }
      }}>Read full child entry · {entry.tool_name ?? entry.kind} #{entry.seq}</button>)}
      {full && <section aria-label="Full child entry" className="mt-3 border-t pt-2">
        <button type="button" className="text-xs underline" onClick={() => setFull(undefined)}>Close full entry</button>
        {renderItems([full])}
      </section>}
      {page && page.entries.length === 0 && <p className="text-xs text-muted-foreground">No child activity yet.</p>}
    </div>
  </div>;
}
