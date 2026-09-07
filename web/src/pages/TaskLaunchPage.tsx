import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Rocket } from "lucide-react";
import { AttachmentPicker } from "@/components/AttachmentPicker";
import { LaunchSummaryRail, RuntimeLaunchControls, useRuntimeLaunchControls } from "@/components/RuntimeLaunchControls";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Label, Select, Textarea } from "@/components/ui";
import { apiPost, apiPostForm } from "@/lib/api";

export function TaskLaunchPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const launchControls = useRuntimeLaunchControls({ projectId, defaultBlackboardMode: "working_graph" });
  const [taskType, setTaskType] = useState("");
  const [goal, setGoal] = useState("");
  const [launching, setLaunching] = useState(false);
  const [attachments, setAttachments] = useState<File[]>([]);
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
