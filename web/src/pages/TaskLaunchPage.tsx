import { useEffect, useState } from "react";
import { useNavigate, useParams, Link } from "react-router-dom";
import { ArrowLeft, Rocket, AlertTriangle, CheckCircle2, XCircle } from "lucide-react";
import { apiGet, apiPost, type PreflightResult, type Project, type RuntimeProfile } from "@/lib/api";
import { Button, Card, Label, Textarea, Badge } from "@/components/ui";

export function TaskLaunchPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [goal, setGoal] = useState("");
  const [profileId, setProfileId] = useState("");
  const [runner, setRunner] = useState("sandbox");
  const [yolo, setYolo] = useState(false);
  const [launching, setLaunching] = useState(false);
  const [preflight, setPreflight] = useState<PreflightResult | null>(null);

  useEffect(() => {
    (async () => {
      try {
        if (!projectId) return;
        const [profileData, project] = await Promise.all([
          apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles"),
          apiGet<Project>(`/api/projects/${projectId}`),
        ]);
        const loadedProfiles = profileData.profiles ?? [];
        setProfiles(loadedProfiles);
        const defaultProfileID = project.defaults.runtime_profile || loadedProfiles[0]?.id || "";
        setProfileId(defaultProfileID);
        const selectedProfile = loadedProfiles.find((p) => p.id === defaultProfileID);
        setRunner(project.defaults.runner || selectedProfile?.fields.default_runner || "sandbox");
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, [projectId]);

  async function launch() {
    if (!projectId) return;
    setLaunching(true);
    setError(null);
    try {
      const checked = await apiPost<PreflightResult>(`/api/projects/${projectId}/preflight`, {
        runtime_profile_id: profileId,
        runner,
      });
      setPreflight(checked);
      if (!checked.pass) {
        setError("preflight failed");
        return;
      }
      const created = await apiPost<{ id: string }>(`/api/projects/${projectId}/tasks`, {
        goal,
        runtime_profile_id: profileId,
        runner,
        run_controls: { yolo },
      });
      navigate(`/projects/${projectId}/tasks/${created.id}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLaunching(false);
    }
  }

  const hostWithYolo = runner === "host" || yolo;

  return (
    <div className="p-8 max-w-2xl">
      <Link to={`/projects/${projectId}`} className="inline-flex items-center text-sm text-muted-foreground hover:text-foreground mb-4">
        <ArrowLeft className="h-4 w-4 mr-1" /> Back to dashboard
      </Link>
      <h2 className="text-xl font-semibold mb-6">Launch task</h2>

      <div className="space-y-4">
        <div>
          <Label htmlFor="goal">Task goal</Label>
          <Textarea id="goal" value={goal} onChange={(e) => setGoal(e.target.value)} placeholder="Enumerate example.com and assess exposure" />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label>Runtime profile</Label>
            <select
              className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              value={profileId}
              onChange={(e) => {
                const nextProfileID = e.target.value;
                setProfileId(nextProfileID);
                setPreflight(null);
                const nextProfile = profiles.find((p) => p.id === nextProfileID);
                if (nextProfile?.fields.default_runner) setRunner(nextProfile.fields.default_runner);
              }}
            >
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} ({p.provider})
                </option>
              ))}
            </select>
          </div>
          <div>
            <Label>Runner</Label>
            <select
              className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              value={runner}
              onChange={(e) => {
                setRunner(e.target.value);
                setPreflight(null);
              }}
            >
              <option value="sandbox">sandbox</option>
              <option value="host">host</option>
            </select>
          </div>
        </div>

        {/* Safety states must be visually loud (prd.md:183). */}
        {hostWithYolo && (
          <Card className="border-warning bg-warning/10 p-3">
            <div className="flex items-center gap-2 text-warning">
              <AlertTriangle className="h-4 w-4" />
              <span className="text-sm font-medium">
                {runner === "host" && yolo ? "HOST runner + YOLO mode" : runner === "host" ? "HOST runner — runs on your machine" : "YOLO mode — approvals bypassed"}
              </span>
            </div>
          </Card>
        )}

        {preflight && (
          <Card className={preflight.pass ? "border-emerald-500/40 bg-emerald-500/5 p-3" : "border-destructive/40 bg-destructive/5 p-3"}>
            <div className="space-y-2">
              {preflight.checks.map((check) => (
                <div key={check.name} className="flex items-start gap-2 text-sm">
                  {check.status === "pass" ? (
                    <CheckCircle2 className="mt-0.5 h-4 w-4 text-emerald-400" />
                  ) : (
                    <XCircle className="mt-0.5 h-4 w-4 text-destructive" />
                  )}
                  <div>
                    <span className="font-medium">{check.name}</span>
                    {check.detail && <span className="text-muted-foreground">: {check.detail}</span>}
                  </div>
                </div>
              ))}
            </div>
          </Card>
        )}

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={yolo}
            onChange={(e) => setYolo(e.target.checked)}
            className="h-4 w-4 accent-warning"
          />
          <span className={yolo ? "text-warning font-medium" : ""}>YOLO mode (skip per-action approvals)</span>
          {yolo && <Badge variant="warning">loud</Badge>}
        </label>

        {error && <p className="text-sm text-destructive">{error}</p>}

        <Button onClick={launch} disabled={!goal.trim() || !profileId || launching}>
          <Rocket className="h-4 w-4 mr-1" /> {launching ? "Launching…" : "Launch"}
        </Button>
      </div>
    </div>
  );
}
