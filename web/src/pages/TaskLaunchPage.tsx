import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Rocket, AlertTriangle, CheckCircle2, XCircle } from "lucide-react";
import { apiGet, apiPost, type PreflightResult, type Project, type RuntimeProfile } from "@/lib/api";
import { Button, Card, Label, Textarea, Badge, Select } from "@/components/ui";
import { BackLink, PageContainer } from "@/components/shared";

export function TaskLaunchPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [goal, setGoal] = useState("");
  const [profileId, setProfileId] = useState("");
  const [runner, setRunner] = useState("sandbox");
  const [yolo, setYolo] = useState(false);
  const [hostActivated, setHostActivated] = useState(false);
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
        run_controls: { yolo, host_activated: hostActivated },
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
        run_controls: { yolo, host_activated: hostActivated },
      });
      navigate(`/projects/${projectId}/tasks/${created.id}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLaunching(false);
    }
  }

  const hostRunner = runner === "host";
  const hostWithYolo = hostRunner || yolo;
  const hostBlocked = hostRunner && !hostActivated && !yolo;

  return (
    <PageContainer className="max-w-2xl">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>
      <h2 className="text-xl font-semibold mb-6">Launch task</h2>

      <div className="space-y-4">
        <div>
          <Label htmlFor="goal">Task goal</Label>
          <Textarea id="goal" value={goal} onChange={(e) => setGoal(e.target.value)} placeholder="Enumerate example.com and assess exposure" />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label>Runtime profile</Label>
            <Select
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
            </Select>
          </div>
          <div>
            <Label>Runner</Label>
            <Select
              value={runner}
              onChange={(e) => {
                setRunner(e.target.value);
                setPreflight(null);
              }}
            >
              <option value="sandbox">sandbox</option>
              <option value="host">host</option>
            </Select>
          </div>
        </div>

        {/* Safety states must be visually loud (prd.md:183). */}
        {hostWithYolo && (
          <Card className="border-warning bg-warning/10 p-3 space-y-2">
            <div className="flex items-center gap-2 text-warning">
              <AlertTriangle className="h-4 w-4" />
              <span className="text-sm font-medium">
                {hostRunner && yolo ? "HOST runner + YOLO mode" : hostRunner ? "HOST runner — runs on your machine" : "YOLO mode — approvals bypassed"}
              </span>
            </div>
            {hostRunner && !yolo && (
              <label className="flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={hostActivated}
                  onChange={(e) => {
                    setHostActivated(e.target.checked);
                    setPreflight(null);
                  }}
                  className="mt-0.5 h-4 w-4 accent-warning"
                />
                <span>
                  I explicitly activate the host runner for this task. Commands execute on this machine outside the sandbox.
                </span>
              </label>
            )}
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
            {preflight.skills && preflight.skills.length > 0 && (
              <div className="mt-3 border-t border-border/60 pt-3">
                <p className="mb-2 text-sm font-medium">Enabled Skills</p>
                <div className="space-y-2">
                  {preflight.skills.map((skill) => (
                    <div key={skill.id} className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm">
                      <div className="font-medium">{skill.name || skill.id}</div>
                      <div className="font-mono text-xs text-muted-foreground">{skill.id}</div>
                    </div>
                  ))}
                </div>
              </div>
            )}
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

        <Button onClick={launch} disabled={!goal.trim() || !profileId || launching || hostBlocked}>
          <Rocket className="h-4 w-4 mr-1" /> {launching ? "Launching…" : "Launch"}
        </Button>
      </div>
    </PageContainer>
  );
}
