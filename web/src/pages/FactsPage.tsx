import { useEffect, useState } from "react";
import { useParams, Link } from "react-router-dom";
import { ArrowLeft, ChevronDown, ChevronRight, FileText, Eye, EyeOff } from "lucide-react";
import { apiGet, type FactIndexEntry, type Fact } from "@/lib/api";
import { Card, Badge, Button } from "@/components/ui";

export function FactsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [facts, setFacts] = useState<FactIndexEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [openKey, setOpenKey] = useState<string | null>(null);
  const [full, setFull] = useState<Fact | null>(null);
  const [showDeprecated, setShowDeprecated] = useState(false);

  const base = `/api/projects/${projectId}`;

  async function loadIndex() {
    try {
      const d = await apiGet<{ facts: FactIndexEntry[] }>(`${base}/facts/index`);
      setFacts(d.facts ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    loadIndex();
  }, [projectId]);

  async function openFact(key: string) {
    if (openKey === key) {
      setOpenKey(null);
      return;
    }
    setOpenKey(key);
    setFull(null);
    try {
      const f = await apiGet<Fact>(`${base}/facts/${encodeURIComponent(key)}`);
      setFull(f);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  // Fact index excludes deprecated; the toggle is a placeholder for when a
  // deprecated view is added server-side.
  void showDeprecated;

  const byCategory = facts.reduce<Record<string, FactIndexEntry[]>>((acc, f) => {
    (acc[f.category] ||= []).push(f);
    return acc;
  }, {});

  return (
    <div className="p-8 max-w-4xl">
      <Link to={`/projects/${projectId}`} className="inline-flex items-center text-sm text-muted-foreground hover:text-foreground mb-4">
        <ArrowLeft className="h-4 w-4 mr-1" /> Back to dashboard
      </Link>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-semibold">Blackboard facts</h2>
        <Button size="sm" variant="ghost" onClick={() => setShowDeprecated((v) => !v)}>
          {showDeprecated ? <EyeOff className="h-4 w-4 mr-1" /> : <Eye className="h-4 w-4 mr-1" />}
          {showDeprecated ? "Hide deprecated" : "Show deprecated"}
        </Button>
      </div>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      {Object.entries(byCategory).map(([cat, items]) => (
        <div key={cat} className="mb-4">
          <h3 className="text-sm font-medium text-muted-foreground mb-2 uppercase tracking-wide">{cat}</h3>
          <div className="space-y-1">
            {items.map((f) => (
              <div key={f.fact_key}>
                <button
                  className="w-full text-left flex items-center gap-2 p-2 rounded-md hover:bg-accent/50"
                  onClick={() => openFact(f.fact_key)}
                >
                  {openKey === f.fact_key ? <ChevronDown className="h-4 w-4 shrink-0" /> : <ChevronRight className="h-4 w-4 shrink-0" />}
                  <span className="text-sm">{f.summary}</span>
                  <ConfidenceBadge confidence={f.confidence} />
                  {f.scope_status === "out_of_scope" && (
                    <Badge variant="warning">out-of-scope (non-actionable)</Badge>
                  )}
                </button>
                {openKey === f.fact_key && (
                  <Card className="ml-6 mb-2 text-sm">
                    <div className="flex items-center gap-2 mb-1">
                      <FileText className="h-3.5 w-3.5 text-muted-foreground" />
                      <code className="text-xs">{f.fact_key}</code>
                    </div>
                    {full ? (
                      <pre className="whitespace-pre-wrap text-xs text-muted-foreground">{full.body || "(no body)"}</pre>
                    ) : (
                      <p className="text-xs text-muted-foreground">Loading body…</p>
                    )}
                  </Card>
                )}
              </div>
            ))}
          </div>
        </div>
      ))}
      {facts.length === 0 && !error && <p className="text-sm text-muted-foreground">No facts recorded yet.</p>}
    </div>
  );
}

function ConfidenceBadge({ confidence }: { confidence: string }) {
  const variant = confidence === "confirmed" ? "success" : confidence === "deprecated" ? "outline" : "primary";
  return <Badge variant={variant}>{confidence}</Badge>;
}
