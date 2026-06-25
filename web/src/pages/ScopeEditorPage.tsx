import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Save } from "lucide-react";
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
    <div>
      <Label className={warning ? "text-warning" : undefined}>
        {label}
        {warning && <Badge variant="warning" className="ml-2">safety</Badge>}
      </Label>
      <Textarea
        value={draft[key]}
        onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
        placeholder={placeholder}
      />
    </div>
  );

  return (
    <PageContainer className="max-w-3xl">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>
      <ProjectNav />
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-semibold">Scope & defaults — {project.name}</h2>
        <Button size="sm" onClick={save} disabled={saving}>
          <Save className="h-4 w-4 mr-1" /> {saving ? "Saving…" : "Save"}
        </Button>
      </div>

      <Card className="mb-6">
        <CardHeader>
          <CardTitle>Project defaults</CardTitle>
        </CardHeader>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label>Default runtime profile</Label>
            <Select
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
            <Label>Default runner</Label>
            <Select
              value={defaultRunner}
              onChange={(e) => setDefaultRunner(e.target.value)}
            >
              <option value="sandbox">sandbox</option>
              <option value="host">host</option>
            </Select>
          </div>
        </div>
      </Card>

      <div className="space-y-4">
        {field("domains", "Domains", "example.com\napi.example.com")}
        {field("ips", "IP addresses", "203.0.113.5")}
        {field("cidrs", "CIDRs", "203.0.113.0/24")}
        {field("urls", "URLs", "https://example.com/admin")}
        {field("ports", "Ports", "443\n8443")}
        <Card className="border-warning/50">
          <CardHeader>
            <CardTitle className="text-warning">Exclusions (out of scope)</CardTitle>
          </CardHeader>
          <div>{field("excluded", "Excluded assets", "admin.example.com\nmail.example.com")}</div>
        </Card>
        <Card className="border-warning/50">
          <CardHeader>
            <CardTitle className="text-warning">Testing limits</CardTitle>
          </CardHeader>
          <div>{field("testing_limits", "Authorized limits", "No destructive payloads\nBusiness hours only", true)}</div>
        </Card>
        <div>
          <Label>Scope notes</Label>
          <Textarea
            value={draft.notes}
            onChange={(e) => setDraft({ ...draft, notes: e.target.value })}
            placeholder="Free-form context for the runtime…"
          />
        </div>
      </div>
    </PageContainer>
  );
}
