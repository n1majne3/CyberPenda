import { useEffect, useId, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { apiGet } from "@/lib/api";

type FGSNode = {
  key: string; type: "goal" | "step" | "fact"; version: number;
  title?: string; action?: string; summary?: string; body?: string;
  state?: string; success_criteria?: string; executor?: string; reason?: string;
};
type DeliveryStatus = { action_required: number; last_accepted_at?: string; receipts: { id: string; continuation_id: string; state: string; message?: string }[] };
type Graph = { next_cursor?: string; revision: number; nodes: FGSNode[]; edges: { from: string; to: string; relation: string }[] };
const types = ["goal", "step", "fact"] as const;
const label = (node: FGSNode) => node.title || node.action || node.summary || node.key;

export function FGSBoard({ scope, id }: { scope: "projects" | "sessions"; id: string }) {
  const [cursor, setCursor] = useState("");
  const base = `/api/v2/${scope}/${encodeURIComponent(id)}/fgs`;
  const [graph, setGraph] = useState<Graph>();
  const [error, setError] = useState("");
  const [delivery, setDelivery] = useState<DeliveryStatus>();
  const [selected, setSelected] = useState("");
  const [query, setQuery] = useState("");
  const [historyKey, setHistoryKey] = useState("");
  const [historyCursor,setHistoryCursor] = useState(0);
  const [history, setHistory] = useState<FGSNode[]>([]);
  const [historyError, setHistoryError] = useState("");
  const marker = useId().replaceAll(":", "");
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const [next, status] = await Promise.all([apiGet<Graph>(cursor ? `${base}?cursor=${encodeURIComponent(cursor)}` : base), apiGet<DeliveryStatus>(`${base}/status`)]);
        if (active) { setGraph(next); setDelivery(status); setError(""); }
      } catch (err) { if (active) setError(err instanceof Error ? err.message : "Cannot read Blackboard"); }
      finally { if (active) timer = setTimeout(refresh, 2000); }
    };
    void refresh();
    return () => { active = false; clearTimeout(timer); };
  }, [base, cursor]);
  useEffect(() => {
    if (!historyKey) return;
    let active = true;
    apiGet<FGSNode[]>(`${base}/nodes/${encodeURIComponent(historyKey)}/history${historyCursor ? `?cursor=${historyCursor}` : ""}`)
      .then((value) => { if (active) { setHistory(value); setHistoryError(""); } })
      .catch((err: unknown) => { if (active) setHistoryError(err instanceof Error ? err.message : "Cannot read history"); });
    return () => { active = false; };
  }, [base, historyKey,historyCursor]);
  const visible = useMemo(() => graph?.nodes.filter((node) => `${node.key} ${label(node)} ${node.state ?? ""}`.toLowerCase().includes(query.toLowerCase())) ?? [], [graph, query]);
  const positions = useMemo(() => {
    const out = new Map<string, { x: number; y: number }>();
    types.forEach((type, column) => visible.filter((node) => node.type === type).forEach((node, row) => out.set(node.key, { x: column * 340 + 20, y: row * 120 + 52 })));
    return out;
  }, [visible]);
  const height = Math.max(260, ...types.map((type) => visible.filter((node) => node.type === type).length * 120 + 70));
  const detail = graph?.nodes.find((node) => node.key === selected);
  const select = (key: string) => { setSelected(key); setHistoryKey(""); setHistory([]); setHistoryCursor(0); };
  return <section className="min-w-0 space-y-4" aria-label="FGS Blackboard">
    <div className="flex flex-wrap items-end justify-between gap-3">
      <div><h2 className="text-lg font-semibold">Goal · Step · Fact</h2><p className="text-sm text-muted-foreground">{graph ? `Accepted revision ${graph.revision}. ` : "Loading Blackboard. "}Last reported state; Runtime activity is shown separately.</p></div>
      <label className="text-sm">Filter nodes<input className="ml-2 rounded-md border bg-background px-3 py-2" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Key, text, or state" /></label>
    </div>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {delivery?.last_accepted_at && <p className="text-xs text-muted-foreground">Last accepted: <time dateTime={delivery.last_accepted_at}>{new Date(delivery.last_accepted_at).toLocaleString()}</time></p>}
    {!!delivery?.action_required && <div role="alert" className="space-y-2 rounded-lg border border-destructive/40 p-4 text-sm"><p>{delivery.action_required} update(s) need repair or withdrawal.</p>{delivery.receipts?.filter((receipt) => receipt.state === "action_required").map((receipt) => <p key={`${receipt.continuation_id}:${receipt.id}`}><span className="font-mono">{receipt.continuation_id}/{receipt.id}</span>: {receipt.message}</p>)}</div>}
    {graph?.nodes.length === 0 && <p className="rounded-lg border border-dashed p-8 text-sm text-muted-foreground">No accepted updates yet. Goals, Steps, and Facts appear when the Runtime publishes them.</p>}
    {!!graph?.nodes.length && <div className="overflow-auto rounded-lg border bg-card">
      <div className="relative min-w-[1040px]" style={{ height }}>
        {types.map((type, i) => <h3 key={type} className="absolute text-sm font-semibold capitalize" style={{ left: 20 + i * 340, top: 16 }}>{type}s · {visible.filter((node) => node.type === type).length}</h3>)}
        <svg className="pointer-events-none absolute inset-0 h-full w-full" aria-hidden="true">
          <defs><marker id={marker} markerWidth="7" markerHeight="7" refX="6" refY="3.5" orient="auto"><path d="M0,0 L7,3.5 L0,7" fill="currentColor" /></marker></defs>
          {graph.edges.map((edge, index) => {
            const a = positions.get(edge.from), b = positions.get(edge.to);
            if (!a || !b) return null;
            const right = a.x < b.x;
            const x1 = a.x + (right ? 290 : 0), x2 = b.x + (right ? 0 : 290);
            return <path key={index} d={`M${x1},${a.y + 42} C${(x1 + x2) / 2},${a.y + 42} ${(x1 + x2) / 2},${b.y + 42} ${x2},${b.y + 42}`} fill="none" stroke="currentColor" strokeWidth="1.2" className="text-muted-foreground/40" markerEnd={`url(#${marker})`} />;
          })}
        </svg>
        {visible.map((node) => <button key={node.key} type="button" onClick={() => select(node.key)} aria-pressed={selected === node.key} className={`absolute overflow-hidden h-[88px] w-[290px] rounded-lg border bg-background px-3 py-2 text-left shadow-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring ${selected === node.key ? "border-primary" : "border-border hover:border-primary/50"}`} style={{ left: positions.get(node.key)?.x, top: positions.get(node.key)?.y }}>
          <span className="flex justify-between gap-2 text-xs text-muted-foreground"><span className="truncate font-mono">{node.key}</span><span className="shrink-0">{node.state ?? "recorded"}</span></span>
          <span className="mt-2 line-clamp-2 break-words text-sm font-medium">{label(node)}</span>
        </button>)}
      </div>
    </div>}
    {(cursor || graph?.next_cursor) && <div className="flex gap-3 text-sm"><button className="rounded border px-3 py-1" disabled={!cursor} onClick={() => { setCursor(""); setGraph(undefined); select(""); }}>First page</button><button className="rounded border px-3 py-1" disabled={!graph?.next_cursor} onClick={() => { setCursor(graph?.next_cursor ?? ""); setGraph(undefined); select(""); }}>Next page</button><span className="text-muted-foreground">Filter applies to this page. Related nodes can be on another page.</span></div>}
    {detail && <article className="min-w-0 [overflow-wrap:anywhere] space-y-3 rounded-lg border bg-card p-5">
      <div className="flex flex-wrap items-start justify-between gap-3"><h3 className="font-semibold">{label(detail)}</h3><button className="rounded border px-3 py-1 text-sm" onClick={() => setHistoryKey(detail.key)}>History</button></div>
      <p className="font-mono text-xs text-muted-foreground">{detail.key} · {detail.type} · version {detail.version}</p>
      {detail.success_criteria && <p className="text-sm"><strong>Success criteria: </strong>{detail.success_criteria}</p>}
      {detail.body && <p className="whitespace-pre-wrap text-sm">{detail.body}</p>}
      {detail.reason && <p className="text-sm">Reason: {detail.reason}</p>}
      {detail.executor && <p className="text-sm">Executor: {detail.executor}</p>}
      <ul className="space-y-1 text-sm">{graph?.edges.filter((edge) => edge.from === detail.key || edge.to === detail.key).map((edge, i) => <li key={i}><button className="underline" onClick={() => select(edge.from)}>{edge.from}</button> → {edge.relation} → <button className="underline" onClick={() => select(edge.to)}>{edge.to}</button></li>)}</ul>
      {historyKey === detail.key && <div className="space-y-2 border-t pt-3">{historyError && <p role="alert">{historyError}</p>}{history.length>0 && history[history.length-1].version>1 && <button className="rounded border px-3 py-1 text-sm" onClick={()=>setHistoryCursor(history[history.length-1].version)}>Older versions</button>}{history.map((item) => <div key={item.version} className="rounded border p-3"><h4 className="text-sm font-medium">Version {item.version}</h4><p className="text-sm">{label(item)} · {item.state ?? "recorded"}</p>{item.reason && <p className="text-sm">{item.reason}</p>}</div>)}</div>}
    </article>}
  </section>;
}

export function FGSSessionPage() {
  const { sessionId = "" } = useParams();
  return <main className="min-w-0 space-y-5 p-6"><Link className="text-sm underline" to={`/sessions/${sessionId}`}>Back to Session</Link><h1 className="text-xl font-semibold">Blackboard</h1><FGSBoard scope="sessions" id={sessionId} /></main>;
}
