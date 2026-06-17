import { useEffect, useState } from "react";
import { useNavigate, useParams, Link } from "react-router-dom";
import { ArrowLeft, Rocket, AlertTriangle } from "lucide-react";
import { apiGet, apiPost, type RuntimeProfile } from "@/lib/api";
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

  useEffect(() => {
    (async () => {
      try {
        const d = await apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles");
        setProfiles(d.profiles ?? []);
        if (d.profiles && d.profiles.length > 0) setProfileId(d.profiles[0].id);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, []);

  async function launch() {
    if (!projectId) return;
    setLaunching(true);
    try {
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
              onChange={(e) => setProfileId(e.target.value)}
            >
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>{p.name} ({p.provider})</option>
              ))}
            </select>
          </div>
          <div>
            <Label>Runner</Label>
            <select
              className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              value={runner}
              onChange={(e) => setRunner(e.target.value)}
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
