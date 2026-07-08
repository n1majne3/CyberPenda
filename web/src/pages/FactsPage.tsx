import { useEffect, useState } from "react";
import { useParams, Link } from "react-router-dom";
import {
  ChevronDown,
  ChevronRight,
  FileText,
  Eye,
  EyeOff,
  GitBranch,
  GitMerge,
  History,
  ScrollText,
} from "lucide-react";
import {
  apiGet,
  apiPost,
  type FactIndexEntry,
  type Fact,
  type FactVersion,
  type FactRelation,
  type Task,
  type TaskSummaryResponse,
} from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { BackLink, PageContainer } from "@/components/shared";
import { Card, Badge, Button, Select } from "@/components/ui";

export function FactsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [facts, setFacts] = useState<FactIndexEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [openKey, setOpenKey] = useState<string | null>(null);
  const [showDeprecated, setShowDeprecated] = useState(false);

  const base = `/api/projects/${projectId}`;

  async function loadIndex(includeDeprecated: boolean) {
    try {
      const qs = includeDeprecated ? "?include_deprecated=1" : "";
      const d = await apiGet<{ facts: FactIndexEntry[] }>(`${base}/facts/index${qs}`);
      setFacts(d.facts ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    // Initial load, re-fetched when the deprecated toggle changes.
    // loadIndex() is reused by event handlers.
    loadIndex(showDeprecated);
  }, [projectId, showDeprecated]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  async function openFact(key: string) {
    setOpenKey((cur) => (cur === key ? null : key));
  }

  const byCategory = facts.reduce<Record<string, FactIndexEntry[]>>((acc, f) => {
    (acc[f.category] ||= []).push(f);
    return acc;
  }, {});

  return (
    <PageContainer className="max-w-4xl">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>
      <ProjectNav />
      <h2 className="text-xl font-semibold mb-2">Blackboard</h2>

      {/* Task summaries belong to project memory (CONTEXT.md groups them with the
          blackboard), so they surface here rather than on a separate route. */}
      <TaskSummaries base={base} />

      <div className="flex items-center justify-between mb-6 mt-6">
        <h3 className="text-sm font-medium text-muted-foreground uppercase tracking-wide">Facts</h3>
        <Button size="sm" variant="ghost" onClick={() => setShowDeprecated((v) => !v)}>
          {showDeprecated ? <EyeOff className="h-4 w-4 mr-1" /> : <Eye className="h-4 w-4 mr-1" />}
          {showDeprecated ? "Hide deprecated" : "Show deprecated"}
        </Button>
      </div>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      {Object.entries(byCategory).map(([cat, items]) => (
        <div key={cat} className="mb-4">
          <h4 className="text-xs font-medium text-muted-foreground mb-2 uppercase tracking-wide">{cat}</h4>
          <div className="space-y-1">
            {items.map((f) => (
              <FactRow
                key={f.fact_key}
                entry={f}
                projectId={projectId!}
                base={base}
                open={openKey === f.fact_key}
                allFactKeys={facts.map((x) => x.fact_key).filter((k) => k !== f.fact_key)}
                onToggle={() => openFact(f.fact_key)}
                onMerged={() => loadIndex(showDeprecated)}
              />
            ))}
          </div>
        </div>
      ))}
      {facts.length === 0 && !error && <p className="text-sm text-muted-foreground">No facts recorded yet.</p>}
    </PageContainer>
  );
}

function FactRow({
  entry,
  projectId,
  base,
  open,
  allFactKeys,
  onToggle,
  onMerged,
}: {
  entry: FactIndexEntry;
  projectId: string;
  base: string;
  open: boolean;
  allFactKeys: string[];
  onToggle: () => void;
  onMerged: () => void;
}) {
  const [full, setFull] = useState<Fact | null>(null);
  const [versions, setVersions] = useState<FactVersion[] | null>(null);
  const [relations, setRelations] = useState<FactRelation[] | null>(null);
  const [mergeOpen, setMergeOpen] = useState(false);
  const [mergeTarget, setMergeTarget] = useState("");
  const [mergeError, setMergeError] = useState<string | null>(null);
  const [mergeBusy, setMergeBusy] = useState(false);

  // Fetch body + versions + relations lazily when expanded.
  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    if (!open) {
      // Reset stale state synchronously when the row collapses. This is an
      // intentional synchronous reset, not a cascading render.
      setFull(null);
      setVersions(null);
      setRelations(null);
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const [f, v, r] = await Promise.all([
          apiGet<Fact>(`${base}/facts/${encodeURIComponent(entry.fact_key)}`),
          apiGet<{ versions: FactVersion[] }>(`${base}/facts/${encodeURIComponent(entry.fact_key)}/versions`),
          apiGet<{ relations: FactRelation[] }>(`${base}/facts/${encodeURIComponent(entry.fact_key)}/relations`),
        ]);
        if (cancelled) return;
        setFull(f);
        setVersions(v.versions ?? []);
        setRelations(r.relations ?? []);
      } catch {
        if (!cancelled) setFull(null);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [open, base, entry.fact_key]);
  /* eslint-enable react-hooks/set-state-in-effect */

  const deprecated = entry.confidence === "deprecated";

  async function confirmMerge() {
    if (!mergeTarget) return;
    setMergeBusy(true);
    setMergeError(null);
    try {
      await apiPost(`${base}/facts/merge`, {
        source_fact_key: entry.fact_key,
        canonical_fact_key: mergeTarget,
      });
      setMergeOpen(false);
      setMergeTarget("");
      onMerged();
    } catch (e) {
      setMergeError((e as Error).message);
    } finally {
      setMergeBusy(false);
    }
  }

  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        className="w-full text-left flex items-center gap-2 p-2 rounded-md hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        onClick={onToggle}
      >
        {open ? <ChevronDown className="h-4 w-4 shrink-0" /> : <ChevronRight className="h-4 w-4 shrink-0" />}
        <span className={`text-sm ${deprecated ? "line-through text-muted-foreground" : ""}`}>{entry.summary}</span>
        <ConfidenceBadge confidence={entry.confidence} />
        {entry.scope_status === "out_of_scope" && (
          <Badge variant="warning">out-of-scope (non-actionable)</Badge>
        )}
      </button>
      {open && (
        <Card className="ml-6 mb-2 text-sm space-y-3">
          <div className="flex items-center gap-2">
            <FileText className="h-3.5 w-3.5 text-muted-foreground" />
            <code className="text-xs">{entry.fact_key}</code>
          </div>
          <div>
            {full ? (
              <pre className="whitespace-pre-wrap text-xs text-muted-foreground">{full.body || "(no body)"}</pre>
            ) : (
              <p className="text-xs text-muted-foreground">Loading body…</p>
            )}
          </div>

          {/* Versions — historical revisions of this fact key. */}
          <div>
            <p className="text-xs font-medium text-muted-foreground flex items-center gap-1 mb-1">
              <History className="h-3.5 w-3.5" /> Versions {versions ? `(${versions.length})` : ""}
            </p>
            {versions === null ? (
              <p className="text-xs text-muted-foreground">Loading…</p>
            ) : versions.length === 0 ? (
              <p className="text-xs text-muted-foreground">No versions.</p>
            ) : (
              <ol className="space-y-1">
                {[...versions].reverse().map((v) => (
                  <li key={v.id} className="text-xs flex flex-wrap items-center gap-2">
                    <code className="text-muted-foreground">v{v.version}</code>
                    <ConfidenceBadge confidence={v.confidence} />
                    {v.scope_status === "out_of_scope" && <Badge variant="outline">out-of-scope</Badge>}
                    <span className="text-muted-foreground">{v.summary}</span>
                  </li>
                ))}
              </ol>
            )}
          </div>

          {/* Relations — typed links to other facts. */}
          <div>
            <p className="text-xs font-medium text-muted-foreground flex items-center gap-1 mb-1">
              <GitBranch className="h-3.5 w-3.5" /> Relations {relations ? `(${relations.length})` : ""}
            </p>
            {relations === null ? (
              <p className="text-xs text-muted-foreground">Loading…</p>
            ) : relations.length === 0 ? (
              <p className="text-xs text-muted-foreground">No relations.</p>
            ) : (
              <ul className="space-y-1">
                {relations.map((r) => (
                  <li key={r.id} className="text-xs flex flex-wrap items-center gap-2">
                    <Badge variant="primary">{r.relation}</Badge>
                    <Link
                      to={`/projects/${projectId}/facts`}
                      className="text-foreground hover:underline"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <code>{r.target_fact_key}</code>
                    </Link>
                    {r.summary && <span className="text-muted-foreground">{r.summary}</span>}
                  </li>
                ))}
              </ul>
            )}
          </div>

          {/* Merge — consolidate this fact key into a canonical key (governed cleanup). */}
          {!deprecated && allFactKeys.length > 0 && (
            <div className="border-t border-border pt-3">
              {!mergeOpen ? (
                <Button size="sm" variant="outline" onClick={() => setMergeOpen(true)}>
                  <GitMerge className="h-3.5 w-3.5 mr-1" /> Merge into…
                </Button>
              ) : (
                <div className="space-y-2">
                  <p className="text-xs text-muted-foreground">
                    Merge <code>{entry.fact_key}</code> into the canonical fact. The old key becomes an alias; history is preserved.
                  </p>
	                  <Select
	                    aria-label="Canonical fact key"
	                    name="canonical_fact_key"
	                    className="max-w-md text-xs"
	                    value={mergeTarget}
	                    onChange={(e) => setMergeTarget(e.target.value)}
	                  >
                    <option value="">Choose canonical fact key</option>
                    {allFactKeys.map((k) => (
                      <option key={k} value={k}>
                        {k}
                      </option>
                    ))}
                  </Select>
                  {mergeError && <p className="text-xs text-destructive">{mergeError}</p>}
                  <div className="flex gap-2">
                    <Button size="sm" onClick={confirmMerge} disabled={!mergeTarget || mergeBusy}>
                      Confirm merge
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => {
                        setMergeOpen(false);
                        setMergeTarget("");
                        setMergeError(null);
                      }}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              )}
            </div>
          )}
        </Card>
      )}
    </div>
  );
}

// TaskSummaries shows the latest accepted task summary for the most recently
// active tasks. It is blackboard memory, surfaced so a user can inspect the
// runtime's compact handoff view without replaying task events.
function TaskSummaries({ base }: { base: string }) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const d = await apiGet<{ tasks: Task[] }>(`${base}/tasks`);
        if (cancelled) return;
        // Most recently updated first — those are the ones with fresh memory.
        const sorted = [...(d.tasks ?? [])].sort((a, b) => b.updated_at.localeCompare(a.updated_at));
        setTasks(sorted.slice(0, 3));
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      } finally {
        if (!cancelled) setLoaded(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [base]);

  if (!loaded) return null;
  return (
    <Card className="space-y-2">
      <p className="text-xs font-medium text-muted-foreground flex items-center gap-1">
        <ScrollText className="h-3.5 w-3.5" /> Task summaries
      </p>
      {error && <p className="text-xs text-destructive">{error}</p>}
      {tasks.length === 0 && !error && (
        <p className="text-xs text-muted-foreground">No tasks yet.</p>
      )}
      <div className="space-y-2">
        {tasks.map((t) => (
          <TaskSummaryLine key={t.id} base={base} task={t} />
        ))}
      </div>
    </Card>
  );
}

function TaskSummaryLine({ base, task }: { base: string; task: Task }) {
  const [resp, setResp] = useState<TaskSummaryResponse | null>(null);

  useEffect(() => {
    let cancelled = false;
    apiGet<TaskSummaryResponse>(`${base}/tasks/${task.id}/summary`)
      .then((d) => !cancelled && setResp(d))
      .catch(() => !cancelled && setResp(null));
    return () => {
      cancelled = true;
    };
  }, [base, task.id]);

  return (
    <div className="text-xs">
      <Link to={`/projects/${task.project_id}/tasks/${task.id}`} className="text-foreground hover:underline">
        {task.goal}
      </Link>
      {resp?.summary ? (
        <div className="mt-0.5">
          <Badge variant="outline">v{resp.summary.version}</Badge>
          <span className="text-muted-foreground ml-2 line-clamp-2">{resp.summary.summary}</span>
        </div>
      ) : (
        <p className="text-muted-foreground mt-0.5">No summary accepted yet.</p>
      )}
    </div>
  );
}

function ConfidenceBadge({ confidence }: { confidence: string }) {
  const variant =
    confidence === "confirmed" ? "success" : confidence === "deprecated" ? "outline" : "primary";
  return <Badge variant={variant}>{confidence}</Badge>;
}
