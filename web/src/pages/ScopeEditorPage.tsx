import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { AlertTriangle, Save } from "lucide-react";
import { apiGet, apiPatch, type Project, type RuntimeProfile, type Scope } from "@/lib/api";
import { isManualRuntimeProfile } from "@/pages/runtimeProfileKind";
import { ProjectNav } from "@/components/ProjectNav";
import { BackLink, PageContainer } from "@/components/shared";
import { Button, Card, CardTitle, CardHeader, Label, Textarea, Badge, Select } from "@/components/ui";

// Each list field is edited as newline-separated text.
type ScopeDraft = {
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

  useEffect(() => {
    if (!projectId) return;
    (async () => {
      try {
        const [p, profileData] = await Promise.all([
          apiGet<Project>(`/api/projects/${projectId}`),
          apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles"),
        ]);
        setProject(p);
        setDraft(toDraft(p.scope));
        setProfiles(profileData.profiles ?? []);
        setDefaultProfile(p.defaults.runtime_profile ?? "");
        setDefaultRunner(p.defaults.runner || "sandbox");
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

  if (error) return <PageContainer className="text-destructive">{error}</PageContainer>;
  if (!project || !draft) return <PageContainer className="text-muted-foreground">Loading…</PageContainer>;

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
    <PageContainer className="max-w-4xl space-y-6">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>
      <ProjectNav />
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">Scope & defaults — {project.name}</h2>
        </div>
        <Button size="sm" onClick={save} disabled={saving}>
          <Save className="h-4 w-4 mr-1" /> {saving ? "Saving…" : "Save"}
        </Button>
      </div>

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

      <section className="grid grid-cols-1 gap-4 lg:grid-cols-2">
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
    </PageContainer>
  );
}
