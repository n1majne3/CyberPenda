import { useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Paperclip, Rocket, UploadCloud, X } from "lucide-react";
import { RuntimeLaunchControls, useRuntimeLaunchControls } from "@/components/RuntimeLaunchControls";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { Button, Label, Textarea } from "@/components/ui";
import { apiPost, apiPostForm } from "@/lib/api";

// Attachment limits mirror the daemon's create-task ceilings (ADR 0019).
const MAX_ATTACHMENT_COUNT = 25;
const MAX_ATTACHMENT_MB = 100;
const MAX_ATTACHMENT_BYTES = MAX_ATTACHMENT_MB * 1024 * 1024;

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function TaskLaunchPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const launchControls = useRuntimeLaunchControls({ projectId });
  const [goal, setGoal] = useState("");
  const [launching, setLaunching] = useState(false);
  const [attachments, setAttachments] = useState<File[]>([]);
  const [dragOver, setDragOver] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  function addFiles(incoming: FileList | File[]) {
    launchControls.setError(null);
    setAttachments((current) => {
      const next = [...current];
      for (const file of Array.from(incoming)) {
        if (file.size > MAX_ATTACHMENT_BYTES) {
          launchControls.setError(`${file.name} exceeds the ${MAX_ATTACHMENT_MB} MB limit`);
          continue;
        }
        if (next.length >= MAX_ATTACHMENT_COUNT) {
          launchControls.setError(`At most ${MAX_ATTACHMENT_COUNT} attachments per task`);
          break;
        }
        if (next.some((staged) => staged.name === file.name && staged.size === file.size)) continue;
        next.push(file);
      }
      return next;
    });
  }

  function removeFile(index: number) {
    setAttachments((current) => current.filter((_, position) => position !== index));
  }

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
    <ProjectPageShell title="Launch task" bodyClassName="w-full max-w-3xl space-y-4">
      <div className="space-y-4">
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
        <div>
          <Label htmlFor="attachments">Attachments</Label>
          <div
            data-testid="attachment-dropzone"
            role="button"
            tabIndex={0}
            onClick={() => fileInputRef.current?.click()}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                fileInputRef.current?.click();
              }
            }}
            onDragOver={(event) => {
              event.preventDefault();
              setDragOver(true);
            }}
            onDragLeave={() => setDragOver(false)}
            onDrop={(event) => {
              event.preventDefault();
              setDragOver(false);
              if (event.dataTransfer.files.length > 0) addFiles(event.dataTransfer.files);
            }}
            className={`mt-1 flex cursor-pointer flex-col items-center justify-center gap-1 rounded-lg border border-dashed p-4 text-sm transition-colors ${
              dragOver ? "border-primary bg-primary/10" : "border-border bg-background/50"
            }`}
          >
            <UploadCloud className="h-5 w-5 text-muted-foreground" />
            <span className="text-muted-foreground">Drag &amp; drop files here, or click to browse</span>
            <span className="text-xs text-muted-foreground">Up to {MAX_ATTACHMENT_COUNT} files, {MAX_ATTACHMENT_MB} MB each. Projected into the task workdir.</span>
            <input
              ref={fileInputRef}
              id="attachments"
              type="file"
              name="attachments"
              multiple
              className="hidden"
              onChange={(event) => {
                if (event.target.files && event.target.files.length > 0) addFiles(event.target.files);
                event.target.value = "";
              }}
            />
          </div>
          {attachments.length > 0 && (
            <ul className="mt-2 space-y-1">
              {attachments.map((file, index) => (
                <li key={`${file.name}-${file.size}`} className="flex items-center justify-between gap-2 rounded-md border border-border/60 bg-background/50 px-2 py-1 text-sm">
                  <span className="flex min-w-0 items-center gap-2">
                    <Paperclip className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <span className="truncate">{file.name}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">{formatBytes(file.size)}</span>
                  </span>
                  <button type="button" aria-label={`Remove ${file.name}`} onClick={() => removeFile(index)} className="shrink-0 text-muted-foreground hover:text-destructive">
                    <X className="h-4 w-4" />
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <RuntimeLaunchControls controller={launchControls} ownerLabel="task" initialInput={goal} />

        <Button onClick={launchTask} disabled={!launchControls.launchReady(goal) || launching || hostBlocked}>
          <Rocket className="h-4 w-4 mr-1" /> {launching ? "Launching…" : "Launch"}
        </Button>
      </div>
    </ProjectPageShell>
  );
}
