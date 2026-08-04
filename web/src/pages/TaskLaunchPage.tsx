import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Rocket } from "lucide-react";
import { AttachmentPicker } from "@/components/AttachmentPicker";
import { RuntimeLaunchControls, useRuntimeLaunchControls } from "@/components/RuntimeLaunchControls";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, Label, Textarea } from "@/components/ui";
import { apiPost, apiPostForm } from "@/lib/api";

export function TaskLaunchPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const launchControls = useRuntimeLaunchControls({ projectId });
  const [goal, setGoal] = useState("");
  const [launching, setLaunching] = useState(false);
  const [attachments, setAttachments] = useState<File[]>([]);

  async function launchTask() {
    if (!projectId) return;
    setLaunching(true);
    launchControls.setError(null);
    try {
      const profileId = await launchControls.resolveRuntimeProfileId();
      const checked = await launchControls.runPreflight(`/api/projects/${projectId}/preflight`, profileId);
      if (!checked.pass) {
        launchControls.setError("preflight failed");
        return;
      }
      const payload = { goal, ...launchControls.launchPayload(profileId) };
      let created: { id: string };
      if (attachments.length > 0) {
        const body = new FormData();
        body.append("payload", JSON.stringify(payload));
        for (const file of attachments) body.append("attachments", file);
        created = await apiPostForm<{ id: string }>(`/api/projects/${projectId}/tasks`, body);
      } else {
        created = await apiPost<{ id: string }>(`/api/projects/${projectId}/tasks`, payload);
      }
      navigate(`/projects/${projectId}/tasks/${created.id}`);
    } catch (cause) {
      launchControls.setError((cause as Error).message);
    } finally {
      setLaunching(false);
    }
  }

  const hostBlocked = launchControls.form.runner === "host" && !launchControls.hostActivated;

  return (
    <ProjectPageShell
      title={
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
          <Rocket className="h-5 w-5 text-signal" /> Launch task
        </h1>
      }
      description="Define a Task goal, choose a Runtime, and launch a Task-scoped persistent Runtime."
      bodyClassName="w-full max-w-3xl space-y-4"
    >
      <div>
        <Label htmlFor="goal">Task goal</Label>
        <Textarea
          id="goal"
          name="task_goal"
          value={goal}
          onChange={(event) => setGoal(event.target.value)}
          placeholder="Enumerate example.com and assess exposure…"
          autoComplete="off"
        />
      </div>
      <AttachmentPicker
        id="attachments"
        files={attachments}
        onFilesChange={setAttachments}
        onError={launchControls.setError}
        ownerLabel="task"
      />

      <RuntimeLaunchControls controller={launchControls} ownerLabel="task" initialInput={goal} />

      <div className="flex justify-end">
        <Button onClick={launchTask} disabled={!launchControls.launchReady(goal) || launching || hostBlocked}>
          <Rocket className="h-4 w-4" /> {launching ? "Launching…" : "Launch"}
        </Button>
      </div>
    </ProjectPageShell>
  );
}
