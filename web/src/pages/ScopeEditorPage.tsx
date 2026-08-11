import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { AlertTriangle, Plus, Save } from "lucide-react";
import { apiGet, apiPatch, apiPost, type Project, type RuntimeProfile, type Scope, type ScopeExpansion } from "@/lib/api";
import { isManualRuntimeProfile } from "@/pages/runtimeProfileKind";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, Card, CardTitle, CardHeader, Input, Label, Textarea, Badge, Select } from "@/components/ui";
import { ErrorState, LoadingState } from "@/components/shared";

// Each list field is edited as newline-separated text.
type ScopeDraft = {
  capabilities: string;
  domains: string;
  ips: string;
  cidrs: string;
  urls: string;
  ports: string;
  excluded: string;
  testing_limits: string;
  notes: string;
};

function toDraft(scope: Scope): ScopeDraft {
  const j = (a?: string[]) => (a ?? []).join("\n");
  return {
	capabilities: j(scope.capabilities),
    domains: j(scope.domains),
    ips: j(scope.ips),
    cidrs: j(scope.cidrs),
    urls: j(scope.urls),
    ports: j(scope.ports),
    excluded: j(scope.excluded),
    testing_limits: j(scope.testing_limits),
    notes: scope.notes ?? "",
  };
}

function fromDraft(d: ScopeDraft): Scope {
  const split = (s: string) =>
    s
      .split("\n")
      .map((x) => x.trim())
      .filter(Boolean);
  return {
    capabilities: split(d.capabilities),
    domains: split(d.domains),
    ips: split(d.ips),
    cidrs: split(d.cidrs),
    urls: split(d.urls),
    ports: split(d.ports),
    excluded: split(d.excluded),
    testing_limits: split(d.testing_limits),
    notes: d.notes.trim(),
  };
}

export function ScopeEditorPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const [project, setProject] = useState<Project | null>(null);
  const [draft, setDraft] = useState<ScopeDraft | null>(null);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [defaultProfile, setDefaultProfile] = useState("");
  const [defaultRunner, setDefaultRunner] = useState("sandbox");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [expansions, setExpansions] = useState<ScopeExpansion[]>([]);
  const [expansionField, setExpansionField] = useState<Exclude<keyof Scope, "notes">>("domains");
  const [expansionValue, setExpansionValue] = useState("");
  const [discoverySource, setDiscoverySource] = useState("");
  const [expansionReason, setExpansionReason] = useState("");
  const [expansionRisk, setExpansionRisk] = useState("");
  const [proposing, setProposing] = useState(false);
  const [deciding, setDeciding] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId) return;
    (async () => {
      try {
        const [p, profileData, expansionData] = await Promise.all([
          apiGet<Project>(`/api/projects/${projectId}`),
          apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles"),
          apiGet<{ expansions?: ScopeExpansion[] }>(`/api/projects/${projectId}/scope-expansions`),
        ]);
        setProject(p);
        setDraft(toDraft(p.scope));
        setProfiles(profileData.profiles ?? []);
        setDefaultProfile(p.defaults.runtime_profile ?? "");
        setDefaultRunner(p.defaults.runner || "sandbox");
        setExpansions(expansionData.expansions ?? []);
        setError(null);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, [projectId]);

  async function save() {
    if (!draft || !projectId) return;
    setSaving(true);
    try {
      await apiPatch(`/api/projects/${projectId}`, {
        scope: fromDraft(draft),
        defaults: {
          runtime_profile: defaultProfile || undefined,
          runner: defaultRunner || undefined,
        },
      });
      setError(null);
      navigate(`/projects/${projectId}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function proposeExpansion() {
    if (!projectId || !expansionValue.trim() || !discoverySource.trim() || !expansionReason.trim() || !expansionRisk.trim()) return;
    setProposing(true);
    try {
      const proposal = await apiPost<ScopeExpansion>(`/api/projects/${projectId}/scope-expansions`, {
        addition: { [expansionField]: [expansionValue.trim()] },
        discovery_source: discoverySource.trim(),
        reason: expansionReason.trim(),
        risk: expansionRisk.trim(),
      });
      setExpansions((current) => [...current, proposal]);
      setExpansionValue("");
      setDiscoverySource("");
      setExpansionReason("");
      setExpansionRisk("");
      setError(null);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setProposing(false);
    }
  }

  async function decideExpansion(expansionId: string, decision: "approve" | "reject") {
    if (!projectId) return;
    setDeciding(expansionId);
    try {
      const result = await apiPost<{ expansion: ScopeExpansion; project: Project }>(
        `/api/projects/${projectId}/scope-expansions/${expansionId}/${decision}`,
      );
      setExpansions((current) => current.map((item) => item.id === expansionId ? result.expansion : item));
      setProject(result.project);
      setDraft(toDraft(result.project.scope));
      setError(null);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setDeciding(null);
    }
  }

  if (error) {
    return (
      <ProjectPageShell>
        <ErrorState error={error} title="Couldn't load scope" className="max-w-2xl" />
      </ProjectPageShell>
    );
  }
  if (!project || !draft) {
    return (
      <ProjectPageShell>
        <LoadingState label="Loading scope" className="max-w-2xl" />
      </ProjectPageShell>
    );
  }

  const field = (key: keyof ScopeDraft, label: string, placeholder: string, warning = false) => (
    <div className="space-y-2">
      <Label htmlFor={`scope-${key}`} className={warning ? "flex items-center gap-2 text-warning" : undefined}>
        {warning && <AlertTriangle className="h-3.5 w-3.5" />}
        {label}
        {warning && <Badge variant="warning">safety limit</Badge>}
      </Label>
      <Textarea
        id={`scope-${key}`}
        name={key}
        value={draft[key]}
        onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
        placeholder={`${placeholder}…`}
        autoComplete="off"
        spellCheck={false}
      />
    </div>
  );

  return (
    <ProjectPageShell
      title={`Scope & defaults — ${project.name}`}
      actions={
        <Button size="sm" onClick={save} disabled={saving}>
          <Save className="h-4 w-4 mr-1" /> {saving ? "Saving…" : "Save"}
        </Button>
      }
      bodyClassName="space-y-6"
    >
      <Card as="section">
        <CardHeader>
          <CardTitle>Project defaults</CardTitle>
        </CardHeader>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <Label htmlFor="scope-default-profile">Default runtime profile</Label>
            <Select
              id="scope-default-profile"
              name="default_runtime_profile"
              value={defaultProfile}
              onChange={(e) => setDefaultProfile(e.target.value)}
            >
              <option value="">(none)</option>
              {profiles.filter(isManualRuntimeProfile).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} ({p.provider})
                </option>
              ))}
            </Select>
          </div>
          <div>
            <Label htmlFor="scope-default-runner">Default runner</Label>
            <Select
              id="scope-default-runner"
              name="default_runner"
              value={defaultRunner}
              onChange={(e) => setDefaultRunner(e.target.value)}
            >
              <option value="sandbox">sandbox</option>
              <option value="host">host</option>
            </Select>
          </div>
        </div>
      </Card>

      <Card as="section" aria-label="Scope Expansion proposals">
        <CardHeader>
          <CardTitle>Scope Expansion proposals</CardTitle>
        </CardHeader>
        <p className="mb-4 text-sm text-muted-foreground">
          Propose a discovered addition. It does not become authorized Scope until an operator approves it.
        </p>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <Label htmlFor="expansion-field">Expansion field</Label>
            <Select id="expansion-field" value={expansionField} onChange={(event) => setExpansionField(event.target.value as Exclude<keyof Scope, "notes">)}>
              <option value="capabilities">Authorized capabilities</option>
              <option value="domains">Domains</option>
              <option value="ips">IP addresses</option>
              <option value="cidrs">CIDRs</option>
              <option value="urls">URLs</option>
              <option value="ports">Ports</option>
              <option value="excluded">Exclusions</option>
              <option value="testing_limits">Testing limits</option>
            </Select>
          </div>
          <div>
            <Label htmlFor="expansion-value">Proposed addition</Label>
            <Input id="expansion-value" value={expansionValue} onChange={(event) => setExpansionValue(event.target.value)} autoComplete="off" />
          </div>
          <div>
            <Label htmlFor="expansion-source">Discovery source</Label>
            <Input id="expansion-source" value={discoverySource} onChange={(event) => setDiscoverySource(event.target.value)} autoComplete="off" />
          </div>
          <div>
            <Label htmlFor="expansion-risk">Expansion risk</Label>
            <Input id="expansion-risk" value={expansionRisk} onChange={(event) => setExpansionRisk(event.target.value)} autoComplete="off" />
          </div>
          <div className="sm:col-span-2">
            <Label htmlFor="expansion-reason">Expansion reason</Label>
            <Textarea id="expansion-reason" value={expansionReason} onChange={(event) => setExpansionReason(event.target.value)} autoComplete="off" />
          </div>
        </div>
        <div className="mt-3 flex justify-end">
          <Button onClick={proposeExpansion} disabled={proposing || !expansionValue.trim() || !discoverySource.trim() || !expansionReason.trim() || !expansionRisk.trim()}>
            <Plus className="mr-1 h-4 w-4" /> {proposing ? "Proposing…" : "Propose Scope Expansion"}
          </Button>
        </div>
        {expansions.length > 0 && (
          <ul className="mt-4 divide-y divide-border border-y border-border">
            {expansions.map((expansion) => (
              <li key={expansion.id} className="space-y-2 py-3">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div>
                    <p className="text-sm font-medium">{scopeAdditionLabel(expansion.addition)}</p>
                    <p className="text-xs text-muted-foreground">{expansion.discovery_source} · {expansion.reason} · Risk: {expansion.risk}</p>
                  </div>
                  <Badge variant="outline">{expansion.status}</Badge>
                </div>
                {expansion.status === "proposed" && (
                  <div className="flex gap-2">
                    <Button size="sm" onClick={() => decideExpansion(expansion.id, "approve")} disabled={deciding === expansion.id}>Approve</Button>
                    <Button size="sm" variant="outline" onClick={() => decideExpansion(expansion.id, "reject")} disabled={deciding === expansion.id}>Reject</Button>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </Card>

      <section className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {field("capabilities", "Authorized capabilities", "challenge_platform\nnetwork_access")}
        {field("domains", "Domains", "example.com\napi.example.com")}
        {field("ips", "IP addresses", "203.0.113.5")}
        {field("cidrs", "CIDRs", "203.0.113.0/24")}
        {field("urls", "URLs", "https://example.com/admin")}
        {field("ports", "Ports", "443\n8443")}
      </section>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card as="section" className="border-warning/25 bg-warning/5">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-warning">
              <AlertTriangle className="h-4 w-4" /> Exclusions
              <Badge variant="warning">non-actionable</Badge>
            </CardTitle>
          </CardHeader>
          <div>{field("excluded", "Out-of-scope assets", "admin.example.com\nmail.example.com")}</div>
        </Card>
        <Card as="section" className="border-warning/25 bg-warning/5">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-warning">
              <AlertTriangle className="h-4 w-4" /> Testing limits
            </CardTitle>
          </CardHeader>
          <div>{field("testing_limits", "Authorized limits", "No destructive payloads\nBusiness hours only", true)}</div>
        </Card>
      </div>

      <section className="space-y-2">
          <Label htmlFor="scope-notes">Scope notes</Label>
          <Textarea
            id="scope-notes"
            name="notes"
            value={draft.notes}
            onChange={(e) => setDraft({ ...draft, notes: e.target.value })}
            placeholder="Free-form context for the runtime…"
            autoComplete="off"
          />
      </section>
    </ProjectPageShell>
  );
}

function scopeAdditionLabel(addition: Scope): string {
  const values = Object.entries(addition)
    .filter(([, value]) => Array.isArray(value))
    .flatMap(([field, value]) => (value as string[]).map((item) => `${field}: ${item}`));
  return values.join(", ");
}
