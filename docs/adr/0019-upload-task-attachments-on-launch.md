# Upload task attachments on launch

The Launch Task request carries operator-supplied attachment files alongside the goal. `POST /api/projects/{id}/tasks` accepts `multipart/form-data` in addition to its existing JSON body: the launch configuration travels as a JSON `payload` form field and each attachment travels as an `attachments` file part. Because task creation and launch are one synchronous request, and the task workdir only exists after the task ID is assigned, attachments are written into the workdir root after `buildTaskLaunchPlan` prepares the layout and before the background launch starts. The resulting paths are appended to the launch goal — not the stored goal — as a trailing `ATTACHED FILES:` list, using the runner-appropriate path form: `/task/workdir/<name>` for the Sandbox runner and the absolute `<runtimeRoot>/<taskID>/workdir/<name>` for the Host runner.

## Consequences

- The create endpoint branches on `Content-Type`: `multipart/form-data` parses the `payload` field and file parts; any other content type keeps the existing JSON decode, so JSON-only clients are unaffected.
- Attachments land in the workdir root (not a subdirectory), so the runtime sees them beside the files it generates; a future run that generates a same-named file can overwrite an attachment.
- Filenames are reduced to a sanitized basename (path separators and `..` traversal stripped) and de-duplicated with a numeric suffix, so an operator-chosen name can never write outside the workdir.
- Limits are 100 MB per file and 25 files per launch; exceeding either returns `400` and no task is created.
- The stored Task goal stays exactly as typed; only the launch goal handed to the runtime carries the appended `ATTACHED FILES:` section, so the dashboard shows the operator's original text.
- Attachment write happens after layout preparation but before launch; a write failure aborts the launch and surfaces as a launch error rather than leaving a task running without its inputs.
