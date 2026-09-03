import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Rocket } from "lucide-react";
import { AttachmentPicker } from "@/components/AttachmentPicker";
import { LaunchSummaryRail, RuntimeLaunchControls, useRuntimeLaunchControls } from "@/components/RuntimeLaunchControls";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Card, CardHeader, CardTitle, Input, Label, Select, Textarea } from "@/components/ui";
import { apiPost, apiPostForm } from "@/lib/api";

export function TaskLaunchPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const launchControls = useRuntimeLaunchControls({ projectId, defaultBlackboardMode: "working_graph" });
  const [taskType, setTaskType] = useState("");
  const [goal, setGoal] = useState("");
  const [launching, setLaunching] = useState(false);
  const [attachments, setAttachments] = useState<File[]>([]);
  const [policy, setPolicy] = useState({
    maxAttempts: "0",
    maxWrongSubmissions: "0",
    maxWallTimeSeconds: "0",
    maxConsecutiveFailures: "0",
    maxRatingDrawdown: "0",
    maxNoProgressSeconds: "0",
  });
  const effectiveGoal = goal;

  async function launchTask() {
    if (!projectId) return;
    setLaunching(true);
    launchControls.setError(null);
    try {
      const checked = await launchControls.runPreflight(`/api/projects/${projectId}/preflight`);
      if (!checked.pass) {
        launchControls.setError("preflight failed");
        return;
      }
      const launch = launchControls.launchPayload();
      const payload = {
        type: taskType,
        goal: effectiveGoal,
        ...launch,
        run_controls: {
          ...launch.run_controls,
          policy: {
            max_attempts: policyValue(policy.maxAttempts),
            max_wrong_submissions: policyValue(policy.maxWrongSubmissions),
            max_wall_time_seconds: policyValue(policy.maxWallTimeSeconds),
            max_consecutive_failures: policyValue(policy.maxConsecutiveFailures),
            max_rating_drawdown: policyValue(policy.maxRatingDrawdown),
            max_no_progress_seconds: policyValue(policy.maxNoProgressSeconds),
          },
        },
      };
      const taskPath = `/api/projects/${projectId}/tasks`;
      let created: { id: string };
      if (attachments.length > 0) {
        const body = new FormData();
        body.append("payload", JSON.stringify(payload));
        for (const file of attachments) body.append("attachments", file);
        created = await apiPostForm<{ id: string }>(taskPath, body);
      } else {
        created = await apiPost<{ id: string }>(taskPath, payload);
      }
      navigate(`/projects/${projectId}/tasks/${created.id}`);
    } catch (cause) {
      launchControls.setError((cause as Error).message);
    } finally {
      setLaunching(false);
    }
  }

  const hostBlocked = launchControls.form.runner === "host" && !launchControls.hostActivated;
  const projectKind = launchControls.project?.kind === "ctf_challenge" ? "ctf_challenge" : "pentest";
  const taskTypeMatchesProject = taskType !== "" && taskType === projectKind;

  return (
    <ProjectPageShell
      title={
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
          <Rocket className="h-5 w-5 text-signal" /> Launch task
        </h1>
      }
      bodyClassName="mx-auto grid w-full max-w-[1080px] grid-cols-1 gap-6 lg:grid-cols-[1fr_300px]"
    >
      <div className="space-y-5">
          <div>
            <Label htmlFor="task-type">Task type</Label>
            <Select id="task-type" name="task_type" value={taskType} onChange={(event) => setTaskType(event.target.value)}>
              <option value="" disabled>Select task type…</option>
              <option value="pentest">Pentest</option>
              <option value="ctf_challenge">CTF Challenge</option>
            </Select>
            <p className="mt-1 text-xs text-muted-foreground">The selected type becomes an immutable Task Type Snapshot.</p>
            {taskType !== "" && !taskTypeMatchesProject && (
              <p role="alert" className="mt-2 rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-warning">
                Task Type must match this Project&apos;s kind. <Link className="underline underline-offset-2" to={`/projects/${projectId}`}>Convert the Project first.</Link>
              </p>
            )}
          </div>
        <section className="rounded-lg border border-border bg-card shadow-sm">
          <div className="p-4">
            <Label htmlFor="goal" className="text-sm font-medium">What do you want to explore?</Label>
            <Textarea
              id="goal"
              name="task_goal"
              rows={4}
              value={effectiveGoal}
              onChange={(event) => setGoal(event.target.value)}
              placeholder="Describe the goal, for example: enumerate the authenticated surface of staging.example.com…"
              autoComplete="off"
              className="mt-2 w-full resize-none rounded-lg border border-input bg-background px-3.5 py-3 text-sm leading-relaxed outline-none placeholder:text-muted-foreground focus:border-ring"
            />
            <div className="mt-3">
              <AttachmentPicker
                id="attachments"
                variant="compact"
                files={attachments}
                onFilesChange={setAttachments}
                onError={launchControls.setError}
                ownerLabel="task"
              />
            </div>
          </div>
        </section>

        <RuntimeLaunchControls controller={launchControls} ownerLabel="task" initialInput={effectiveGoal} />

        <Card as="section" className="border-border/70 bg-muted/10">
          <CardHeader>
            <CardTitle>Task Policy</CardTitle>
          </CardHeader>
          <p className="mb-3 text-xs text-muted-foreground">A value of 0 disables the limit. These values become the immutable Task Policy Snapshot.</p>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <PolicyInput id="policy-max-attempts" label="Maximum attempts" value={policy.maxAttempts} onChange={(value) => setPolicy((current) => ({ ...current, maxAttempts: value }))} />
            <PolicyInput id="policy-max-wrong" label="Maximum wrong submissions" value={policy.maxWrongSubmissions} onChange={(value) => setPolicy((current) => ({ ...current, maxWrongSubmissions: value }))} />
            <PolicyInput id="policy-max-wall" label="Maximum wall time (seconds)" value={policy.maxWallTimeSeconds} onChange={(value) => setPolicy((current) => ({ ...current, maxWallTimeSeconds: value }))} />
            <PolicyInput id="policy-max-consecutive" label="Maximum consecutive failures" value={policy.maxConsecutiveFailures} onChange={(value) => setPolicy((current) => ({ ...current, maxConsecutiveFailures: value }))} />
            <PolicyInput id="policy-max-drawdown" label="Maximum rating drawdown" value={policy.maxRatingDrawdown} onChange={(value) => setPolicy((current) => ({ ...current, maxRatingDrawdown: value }))} />
            <PolicyInput id="policy-max-no-progress" label="Maximum no-progress time (seconds)" value={policy.maxNoProgressSeconds} onChange={(value) => setPolicy((current) => ({ ...current, maxNoProgressSeconds: value }))} />
          </div>
        </Card>
      </div>

<LaunchSummaryRail
        controller={launchControls}
        disabled={!taskTypeMatchesProject || !launchControls.launchReady(effectiveGoal) || launching || hostBlocked}
        busy={launching}
        label="Launch"
        onClick={launchTask}
      />
    </ProjectPageShell>
  );
}

function PolicyInput({ id, label, value, onChange }: { id: string; label: string; value: string; onChange: (value: string) => void }) {
  return (
    <div>
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} type="number" min={0} step={1} value={value} onChange={(event) => onChange(event.target.value)} />
    </div>
  );
}

function policyValue(value: string): number {
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
}
