import { useEffect, useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { Boxes, CheckCircle2, CircleDashed, Globe2, Import, Search, ShieldAlert, X } from "lucide-react";
import { apiGet, apiPost, type AssetBatchResult, type AssetCoverage, type Project, type ProjectAsset } from "@/lib/api";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Badge, Button, Card, CardDescription, CardTitle, Select } from "@/components/ui";
import { RichEmptyState, SectionLabel, SettingsAlert } from "@/components/shared";
import { formatDateTime, formatRelativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

const typeLabels: Record<string, string> = {
  root_domain: "Root domain",
  subdomain: "Subdomain",
  ip: "IP",
  service: "Service",
  endpoint: "Endpoint",
};
const scopeLabels: Record<string, string> = {
  in_scope: "In scope",
  out_of_scope: "Out of scope",
  excluded: "Excluded",
};

function scopeVariant(status: string): "success" | "warning" | "destructive" {
  if (status === "in_scope") return "success";
  if (status === "excluded") return "destructive";
  return "warning";
}

function assetParentName(asset: ProjectAsset, byID: Map<string, ProjectAsset>): string {
  if (!asset.parent_id) return "—";
  return byID.get(asset.parent_id)?.normalized_value ?? "Linked asset";
}

async function fetchAssetPage(projectId: string): Promise<[Project, ProjectAsset[], AssetCoverage]> {
  const [nextProject, nextAssets, nextCoverage] = await Promise.all([
    apiGet<Project>(`/api/projects/${projectId}`),
    apiGet<{ assets: ProjectAsset[] }>(`/api/projects/${projectId}/assets`),
    apiGet<AssetCoverage>(`/api/projects/${projectId}/assets/coverage`),
  ]);
  return [nextProject, nextAssets.assets ?? [], nextCoverage];
}

export function AssetsPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  const [project, setProject] = useState<Project>();
  const [assets, setAssets] = useState<ProjectAsset[]>([]);
  const [coverage, setCoverage] = useState<AssetCoverage>();
  const [projects, setProjects] = useState<Project[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("all");
  const [scopeFilter, setScopeFilter] = useState("all");
  const [importSource, setImportSource] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const [importBusy, setImportBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const [nextProject, nextAssets, nextCoverage] = await fetchAssetPage(projectId);
        if (cancelled) return;
        setProject(nextProject);
        setAssets(nextAssets);
        setCoverage(nextCoverage);
        setError("");
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : "Cannot load assets");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [projectId]);

  useEffect(() => {
    if (!importOpen) return;
    let active = true;
    apiGet<{ projects: Project[] }>("/api/projects")
      .then((result) => {
        if (!active) return;
        const candidates = (result.projects ?? []).filter((item) => item.id !== projectId);
        setProjects(candidates);
        setImportSource((current) => current || candidates[0]?.id || "");
      })
      .catch(() => { if (active) setProjects([]); });
    return () => { active = false; };
  }, [importOpen, projectId]);

  const byID = useMemo(() => new Map(assets.map((asset) => [asset.id, asset])), [assets]);
  const selected = byID.get(selectedID);
  const filtered = useMemo(() => {
    const text = query.trim().toLowerCase();
    return assets.filter((asset) => {
      if (typeFilter !== "all" && asset.type !== typeFilter) return false;
      if (scopeFilter !== "all" && asset.scope_status !== scopeFilter) return false;
      if (!text) return true;
      return [asset.value, asset.normalized_value, asset.host, asset.path, asset.scheme]
        .filter(Boolean).join(" ").toLowerCase().includes(text);
    });
  }, [assets, query, scopeFilter, typeFilter]);

  const percent = coverage?.coverage_percent ?? 0;
  const importAssets = async () => {
    if (!importSource) return;
    setImportBusy(true);
    setNotice("");
    try {
      const result = await apiPost<AssetBatchResult>(`/api/projects/${projectId}/assets/import`, {
        source_project_id: importSource,
      });
      const [nextProject, nextAssets, nextCoverage] = await fetchAssetPage(projectId);
      setProject(nextProject);
      setAssets(nextAssets);
      setCoverage(nextCoverage);
      setNotice(`Imported ${result.created} new asset${result.created === 1 ? "" : "s"}; ${result.updated} already existed.`);
      setImportOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Asset import failed");
    } finally {
      setImportBusy(false);
    }
  };

  const statCards = [
    { label: "Inventory", value: coverage?.total_assets ?? assets.length, detail: `${coverage?.out_of_scope_assets ?? 0} out of scope · ${coverage?.excluded_assets ?? 0} excluded` },
    { label: "In-scope coverage", value: `${Math.round(percent)}%`, detail: `${coverage?.covered_in_scope_assets ?? 0}/${coverage?.in_scope_assets ?? 0} assets have Fact anchors` },
    { label: "Uncovered", value: coverage?.uncovered_in_scope_asset_ids.length ?? 0, detail: "In-scope assets still waiting for an accepted Fact" },
  ];

  return (
    <ProjectPageShell
      title={project?.name ?? "Assets"}
      description="Project-scoped asset inventory and asset coverage. Inventory records observations; Scope remains the authorization boundary."
      actions={
        <Button type="button" variant="outline" onClick={() => setImportOpen((open) => !open)}>
          <Import className="size-3.5" />Import assets
        </Button>
      }
    >
      {error && <SettingsAlert>{error}</SettingsAlert>}
      {notice && <p role="status" className="mb-4 rounded-lg border border-success/25 bg-success/10 px-3 py-2 text-sm text-success">{notice}</p>}

      {importOpen && (
        <Card className="mb-6 border-signal/25 bg-signal/5" size="compact">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
            <div className="min-w-0 flex-1">
              <CardTitle className="text-sm">Import assets from another Project</CardTitle>
              <CardDescription className="mt-1">
                Copies inventory rows only. FGS anchors, Facts, and Scope authorization do not move across Projects.
              </CardDescription>
            </div>
            <div className="flex flex-wrap items-end gap-2">
              <label className="min-w-64 text-xs font-medium text-muted-foreground">
                Source Project
                <Select className="mt-1" value={importSource} onChange={(event) => setImportSource(event.target.value)}>
                  {projects.length === 0 && <option value="">No other Projects</option>}
                  {projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
                </Select>
              </label>
              <Button type="button" disabled={!importSource || importBusy} onClick={importAssets}>
                {importBusy ? "Importing…" : "Import all assets"}
              </Button>
              <Button type="button" variant="ghost" onClick={() => setImportOpen(false)}><X className="size-3.5" />Close</Button>
            </div>
          </div>
        </Card>
      )}

      <div className="mb-6 grid gap-3 md:grid-cols-3">
        {statCards.map((stat) => (
          <Card key={stat.label} size="compact">
            <SectionLabel>{stat.label}</SectionLabel>
            <p className="mt-2 text-3xl font-semibold tracking-tight">{stat.value}</p>
            <p className="mt-1 text-xs leading-5 text-muted-foreground">{stat.detail}</p>
          </Card>
        ))}
      </div>

      {coverage && coverage.in_scope_assets > 0 && (
        <div className="mb-6 h-2 overflow-hidden rounded-full bg-muted" role="progressbar" aria-label="In-scope asset coverage" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(percent)}>
          <div className="h-full rounded-full bg-signal transition-all" style={{ width: `${percent}%` }} />
        </div>
      )}

      <Card className="overflow-hidden" size="compact">
        <div className="flex flex-col gap-3 border-b border-border pb-4 lg:flex-row lg:items-end lg:justify-between">
          <div>
            <CardTitle>Asset inventory</CardTitle>
            <CardDescription className="mt-1">Select an asset to inspect its durable FGS anchors.</CardDescription>
          </div>
          <div className="flex flex-wrap items-end gap-2">
            <label className="text-xs font-medium text-muted-foreground">
              Search
              <span className="relative mt-1 block">
                <Search className="pointer-events-none absolute left-2.5 top-2.5 size-3.5 text-muted-foreground" />
                <input className="h-9 w-56 rounded-md border bg-background pl-8 pr-3 text-sm text-foreground" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Host, service, or path" />
              </span>
            </label>
            <label className="text-xs font-medium text-muted-foreground">Type
              <Select className="mt-1 w-36" value={typeFilter} onChange={(event) => setTypeFilter(event.target.value)}>
                <option value="all">All types</option>
                {Object.entries(typeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
              </Select>
            </label>
            <label className="text-xs font-medium text-muted-foreground">Scope
              <Select className="mt-1 w-36" value={scopeFilter} onChange={(event) => setScopeFilter(event.target.value)}>
                <option value="all">All statuses</option>
                {Object.entries(scopeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
              </Select>
            </label>
          </div>
        </div>

        {loading ? (
          <p role="status" className="py-12 text-center text-sm text-muted-foreground">Loading assets</p>
        ) : filtered.length === 0 ? (
          <RichEmptyState
            icon={<Boxes className="size-5" />}
            title={assets.length === 0 ? "No assets observed yet" : "No assets match the filters"}
            description={assets.length === 0 ? "Runtime asset observations and explicit Project imports will appear here." : "Adjust the search, type, or Scope filter."}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[920px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs uppercase tracking-[0.12em] text-muted-foreground">
                  <th className="px-2 py-3 font-medium">Asset</th>
                  <th className="px-2 py-3 font-medium">Type</th>
                  <th className="px-2 py-3 font-medium">Parent</th>
                  <th className="px-2 py-3 font-medium">Scope status</th>
                  <th className="px-2 py-3 font-medium">Coverage</th>
                  <th className="px-2 py-3 font-medium">Anchors</th>
                  <th className="px-2 py-3 font-medium">Last seen</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((asset) => (
                  <tr
                    key={asset.id}
                    className={cn("cursor-pointer border-b border-border/70 align-top transition-colors hover:bg-muted/45", selectedID === asset.id && "bg-signal/5")}
                    onClick={() => setSelectedID(asset.id)}
                  >
                    <td className="max-w-[330px] px-2 py-3">
                      <div className="flex items-start gap-2">
                        <Globe2 className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                        <div className="min-w-0">
                          <button
                            type="button"
                            className="break-all text-left font-medium text-foreground underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            onClick={(event) => {
                              event.stopPropagation();
                              setSelectedID(asset.id);
                            }}
                          >
                            {asset.normalized_value}
                          </button>
                          {asset.imported_from_project_id && <p className="mt-1 text-xs text-muted-foreground">Imported from another Project</p>}
                        </div>
                      </div>
                    </td>
                    <td className="px-2 py-3"><Badge variant="outline">{typeLabels[asset.type] ?? asset.type}</Badge></td>
                    <td className="max-w-[180px] break-all px-2 py-3 text-muted-foreground">{assetParentName(asset, byID)}</td>
                    <td className="px-2 py-3"><Badge variant={scopeVariant(asset.scope_status)}>{scopeLabels[asset.scope_status] ?? asset.scope_status}</Badge></td>
                    <td className="px-2 py-3">
                      <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", asset.covered ? "text-success" : "text-muted-foreground")}>
                        {asset.covered ? <CheckCircle2 className="size-3.5" /> : <CircleDashed className="size-3.5" />}
                        {asset.covered ? "Covered" : "Uncovered"}
                      </span>
                    </td>
                    <td className="px-2 py-3 text-xs text-muted-foreground">
                      {asset.anchors.length === 0 ? "—" : `${asset.fact_count} Facts · ${asset.step_count} Steps · ${asset.goal_count} Goals`}
                    </td>
                    <td className="whitespace-nowrap px-2 py-3 text-xs text-muted-foreground" title={formatDateTime(asset.last_seen_at)}>{formatRelativeTime(asset.last_seen_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {selected && (
        <Card className="mt-6" size="default">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <SectionLabel>Asset detail</SectionLabel>
              <CardTitle className="mt-2 break-all text-lg">{selected.normalized_value}</CardTitle>
              <CardDescription className="mt-1">
                {typeLabels[selected.type] ?? selected.type} · first seen {formatDateTime(selected.first_seen_at)} · last seen {formatDateTime(selected.last_seen_at)}
              </CardDescription>
            </div>
            <Button type="button" variant="ghost" onClick={() => setSelectedID("")}><X className="size-3.5" />Close</Button>
          </div>
          <div className="mt-5 grid gap-4 md:grid-cols-3">
            <div className="rounded-lg border bg-muted/30 p-3"><SectionLabel>Scope</SectionLabel><p className="mt-2 text-sm font-medium">{scopeLabels[selected.scope_status]}</p><p className="mt-1 text-xs text-muted-foreground">Derived from current Project Scope on every read.</p></div>
            <div className="rounded-lg border bg-muted/30 p-3"><SectionLabel>Hierarchy</SectionLabel><p className="mt-2 break-all text-sm font-medium">{assetParentName(selected, byID)}</p><p className="mt-1 text-xs text-muted-foreground">Closest durable parent asset.</p></div>
            <div className="rounded-lg border bg-muted/30 p-3"><SectionLabel>Coverage rule</SectionLabel><p className="mt-2 text-sm font-medium">{selected.covered ? "Covered by Fact evidence" : "No Fact anchor yet"}</p><p className="mt-1 text-xs text-muted-foreground">Goal and Step anchors help navigation but do not count as coverage.</p></div>
          </div>
          <div className="mt-6">
            <SectionLabel>Durable FGS anchors</SectionLabel>
            {selected.anchors.length === 0 ? (
              <p className="mt-3 rounded-lg border border-dashed p-4 text-sm text-muted-foreground">No anchors yet. A Runtime can attach accepted Goal, Step, or Fact keys with `pentestctl assets anchor`.</p>
            ) : (
              <div className="mt-3 space-y-2">
                {selected.anchors.map((anchor) => (
                  <div key={`${anchor.asset_id}:${anchor.node_key}`} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3 text-sm">
                    <div className="min-w-0">
                      <p className="break-all font-mono text-xs font-medium">{anchor.node_key}</p>
                      <p className="mt-1 text-xs text-muted-foreground">{anchor.node_type} · {anchor.actor_id || "Runtime"} · {formatDateTime(anchor.created_at)}</p>
                    </div>
                    <Badge variant={anchor.node_type === "fact" ? "success" : "outline"}>{anchor.node_type}</Badge>
                  </div>
                ))}
              </div>
            )}
          </div>
        </Card>
      )}

      <div className="mt-6 flex items-start gap-2 rounded-lg border border-dashed p-4 text-xs leading-5 text-muted-foreground">
        <ShieldAlert className="mt-0.5 size-4 shrink-0" />
        <p>Assets are inventory observations, not authorization. Out-of-scope and excluded assets remain visible so operators can audit reconnaissance without expanding Scope.</p>
      </div>
    </ProjectPageShell>
  );
}
