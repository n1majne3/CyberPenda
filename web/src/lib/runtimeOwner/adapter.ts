import {
  apiDelete,
  apiGet,
  apiPatch,
  apiPost,
  apiPostForm,
  type FinishReadiness,
  type Session,
  type Task,
  type TaskTimeline,
  type TaskTranscript,
} from "@/lib/api";
import { newPermissionRequestID } from "./ids";
import { sessionContinuationStatus } from "./status";
import type {
  ConversationSelection,
  OwnerHistory,
  RuntimeOwnerKind,
  RuntimeOwnerView,
  RuntimeWorkspaceLoad,
} from "./types";

/**
 * RuntimeOwnerAdapter is the behavioral seam of the Runtime Owner Workspace.
 * One adapter per owner kind owns the endpoints, payload shapes, and
 * lifecycle writes; the workspace renders and orchestrates, and the
 * conversation kernel decides which verb sequence one operator message takes.
 */
export type RuntimeOwnerAdapter = {
  readonly kind: RuntimeOwnerKind;
  /** One endpoint truth per owner: the owner API base every write extends. */
  readonly base: string;
  /** Loads the bounded workspace page (owner, history window, finish readiness). */
  loadWorkspace(signal: AbortSignal, mode: "initial" | "poll", current: OwnerHistory): Promise<RuntimeWorkspaceLoad>;
  loadOlderTranscript(before: number): Promise<TaskTranscript>;
  loadOlderTimeline(before: number): Promise<TaskTimeline>;
  /** Sends a fresh message that starts a new continuation. */
  sendMessage(text: string, selection: ConversationSelection, attachments: File[]): Promise<RuntimeOwnerView>;
  /** Steers the live runtime; native picks the provider-native request shape. */
  steer(input: { text: string; selection: ConversationSelection; requestID: string; native: boolean; forceReplace?: boolean; attachments?: File[] }): Promise<unknown>;
  /** Queues an accepted steering request for the harness to deliver in order. */
  queueSteer(text: string, selection: ConversationSelection, attachments: File[]): Promise<unknown>;
  stop(): Promise<unknown>;
  finish(): Promise<unknown>;
  resume(selection: ConversationSelection): Promise<unknown>;
  respondPermission(permissionRequestID: string, decision: "allow" | "deny"): Promise<unknown>;
  rename(title: string): Promise<RuntimeOwnerView>;
  changeLifecycle(action: "archive" | "restore"): Promise<RuntimeOwnerView>;
  remove(): Promise<unknown>;
};

// The first load reads the initial bounded history window (no cursor); refresh
// polls always send the committed cursor, including an explicit after=0 after
// an empty initial read so synthetic seq-0 content is never resent.
function timelineURL(base: string, mode: "initial" | "poll", cursor: number): string {
  return mode === "initial" ? `${base}/timeline` : `${base}/timeline?after=${cursor}`;
}

function transcriptURL(base: string, mode: "initial" | "poll", cursor: number): string {
  return mode === "initial" ? `${base}/transcript` : `${base}/transcript?after=${cursor}`;
}

export function taskRuntimeOwnerAdapter(base: string): RuntimeOwnerAdapter {
  return {
    kind: "task",
    base,
    async loadWorkspace(signal, mode, current) {
      const [task, timeline, transcript, finishReadiness] = await Promise.all([
        apiGet<Task>(base, { signal }),
        apiGet<TaskTimeline>(timelineURL(base, mode, current.timelineCursor), { signal }),
        apiGet<TaskTranscript>(transcriptURL(base, mode, current.transcriptCursor), { signal }),
        apiGet<FinishReadiness>(`${base}/finish-readiness`, { signal }),
      ]);
      return {
        owner: taskAsRuntimeOwner(task),
        finishReadiness: typeof finishReadiness.ready_to_finish === "boolean" && Array.isArray(finishReadiness.blockers)
          ? finishReadiness
          : undefined,
        timeline: timeline.items ?? [],
        transcript: transcript.entries ?? [],
        timelineCursor: timeline.cursor ?? current.timelineCursor,
        transcriptCursor: transcript.cursor ?? current.transcriptCursor,
        timelineHasOlder: timeline.has_older === true,
        transcriptHasOlder: transcript.has_older === true,
      };
    },
    loadOlderTranscript: (before) => apiGet<TaskTranscript>(`${base}/transcript?before=${before}`),
    loadOlderTimeline: (before) => apiGet<TaskTimeline>(`${base}/timeline?before=${before}`),
    async sendMessage(text, selection) {
      return taskAsRuntimeOwner(await apiPost<Task>(`${base}/messages`, { message: text, ...selection }));
    },
    steer: ({ text, selection, requestID, native, forceReplace }) =>
      apiPost(
        `${base}/steer`,
        native
          ? { request_id: requestID, message: text, ...(forceReplace ? { force_replace: true } : {}), ...selection }
          : { directive: text, ...selection },
      ),
    queueSteer: (text, selection) => apiPost(`${base}/steer/queue`, { directive: text, ...selection }),
    stop: () => apiPost(`${base}/stop`, {}),
    finish: () => apiPost(`${base}/finish`, {}),
    resume: (selection) => apiPost(`${base}/resume`, selection),
    respondPermission: (permissionRequestID, decision) =>
      apiPost(`${base}/permissions/${encodeURIComponent(permissionRequestID)}/respond`, {
        request_id: newPermissionRequestID(),
        decision,
      }),
    async rename() {
      throw new Error("Tasks cannot be renamed");
    },
    async changeLifecycle() {
      throw new Error("Tasks cannot be archived");
    },
    remove: () => apiDelete(base),
  };
}

export function sessionRuntimeOwnerAdapter(base: string): RuntimeOwnerAdapter {
  return {
    kind: "session",
    base,
    async loadWorkspace(signal, mode, current) {
      const [session, timeline, transcript] = await Promise.all([
        apiGet<Session>(base, { signal }),
        apiGet<TaskTimeline>(timelineURL(base, mode, current.timelineCursor), { signal }),
        apiGet<TaskTranscript>(transcriptURL(base, mode, current.transcriptCursor), { signal }),
      ]);
      return {
        owner: sessionAsRuntimeOwner(session),
        // Session timelines and transcripts are built by the same daemon pipeline
        // as task ones, so the rendered shapes are identical.
        timeline: timeline.items ?? [],
        transcript: transcript.entries ?? [],
        timelineCursor: timeline.cursor ?? current.timelineCursor,
        transcriptCursor: transcript.cursor ?? current.transcriptCursor,
        timelineHasOlder: timeline.has_older === true,
        transcriptHasOlder: transcript.has_older === true,
      };
    },
    loadOlderTranscript: (before) => apiGet<TaskTranscript>(`${base}/transcript?before=${before}`),
    loadOlderTimeline: (before) => apiGet<TaskTimeline>(`${base}/timeline?before=${before}`),
    sendMessage: (text, selection, attachments) => postSessionRuntimeMessage(`${base}/messages`, text, selection, attachments).then(sessionAsRuntimeOwner),
    steer: ({ text, selection, requestID, attachments, forceReplace }) => postSessionRuntimeMessage(
      `${base}/steer`, text, selection, attachments ?? [], forceReplace ? { force_replace: true } : {}, requestID,
    ),
    queueSteer: (text, selection, attachments) => postSessionRuntimeMessage(`${base}/steer/queue`, text, selection, attachments),
    stop: () => apiPost(`${base}/stop`, {}),
    finish: () => apiPost(`${base}/finish`, {}),
    resume: (selection) => apiPost(`${base}/resume`, selection),
    respondPermission: (permissionRequestID, decision) =>
      apiPost(`${base}/permissions/${encodeURIComponent(permissionRequestID)}/respond`, {
        request_id: newPermissionRequestID(),
        decision,
      }),
    async rename(title) {
      return sessionAsRuntimeOwner(await apiPatch<Session>(base, { title }));
    },
    changeLifecycle: async (action) => sessionAsRuntimeOwner(await apiPost<Session>(`${base}/${action}`, {})),
    remove: () => apiDelete(base),
  };
}

async function postSessionRuntimeMessage(
  path: string,
  message: string,
  selection: ConversationSelection,
  attachments: File[],
  extra: Record<string, unknown> = {},
  idempotencyKey = "",
): Promise<Session> {
  const init = idempotencyKey ? { headers: { "Idempotency-Key": idempotencyKey } } : undefined;
  if (attachments.length === 0) {
    return apiPost<Session>(path, { message, ...selection, ...extra }, init);
  }
  const form = new FormData();
  form.append("payload", JSON.stringify({ message, ...selection, ...extra }));
  attachments.forEach((attachment) => form.append("attachments", attachment, attachment.name));
  return apiPostForm<Session>(path, form, init);
}

const DELETABLE = new Set(["completed", "failed", "stopped", "interrupted"]);

function displayRunnerLabel(runner: string, containerCLI?: string): string {
  if (runner === "sandbox") {
    const engine = containerCLI?.trim().toLowerCase();
    if (engine === "podman" || engine === "docker") return engine;
    return "docker";
  }
  return runner;
}

export function taskAsRuntimeOwner(task: Task): RuntimeOwnerView {
  return {
    kind: "task",
    id: task.id,
    title: task.goal,
    status: task.status,
    runner: displayRunnerLabel(task.runner, task.run_controls?.container_cli),
    runtimeProfileID: task.runtime_profile_id ?? "",
    runtimeConfiguration: task.runtime_configuration,
    blackboardMode: task.run_controls.blackboard_mode,
    blackboardConclusion: task.blackboard_conclusion,
    runtimeControls: task.runtime_controls,
    runtimeActivity: task.runtime_activity,
    activeContinuation: task.active_continuation ? taskContinuationAsRuntimeOwner(task.active_continuation) : undefined,
    latestContinuation: task.latest_continuation ? taskContinuationAsRuntimeOwner(task.latest_continuation) : undefined,
    createdAt: task.created_at,
    updatedAt: task.updated_at,
    capabilities: {
      projectChrome: true,
      rename: false,
      archive: false,
      restore: false,
      delete: DELETABLE.has(task.status),
      finish: true,
      resumeWithoutMessage: true,
      attachments: false,
    },
  };
}

function taskContinuationAsRuntimeOwner(continuation: Task["active_continuation"]): RuntimeOwnerView["activeContinuation"] {
  if (!continuation) return undefined;
  return {
    id: continuation.id,
    number: continuation.number,
    runtimeProfileID: continuation.runtime_profile_id ?? "",
    runtimeProvider: continuation.runtime_provider,
    runner: continuation.runner,
    status: continuation.status,
    nativeSessionID: continuation.native_session_id,
  };
}

export function sessionAsRuntimeOwner(session: Session): RuntimeOwnerView {
  const active = session.active_continuation ? sessionContinuationAsRuntimeOwner(session.active_continuation) : undefined;
  const latest = session.latest_continuation ? sessionContinuationAsRuntimeOwner(session.latest_continuation) : active;
  const continuation = active ?? latest;
  const status = session.lifecycle === "archived" ? "archived" : sessionContinuationStatus(session) ?? "stopped";
  const controls = session.runtime_controls;
  return {
    kind: "session",
    id: session.id,
    title: session.title,
    status,
    lifecycle: session.lifecycle,
    runner: continuation?.runner ?? "host",
    runtimeProfileID: continuation?.runtimeProfileID ?? "",
    runtimeConfiguration: session.runtime_configuration,
    blackboardMode: session.run_controls?.blackboard_mode,
    blackboardConclusion: session.blackboard_conclusion,
    runtimeActivity: session.runtime_activity,
    activeContinuation: active,
    latestContinuation: latest,
    runtimeControls: controls ? {
      native_resume_available: controls.native_resume_available,
      resume_available: session.lifecycle === "open",
      native_steer_available: controls.native_steer_available,
      native_steer_mode: controls.native_steer_mode,
      native_steer_state: controls.native_steer_state,
      native_steer_request_id: controls.native_steer_request_id,
      native_steer_error_code: controls.native_steer_error_code,
      native_steer_error: controls.native_steer_error,
      queue_steer_available: controls.queue_steer_available,
      interrupt_steer_available: controls.interrupt_steer_available,
      native_session_captured: controls.native_session_captured,
      same_runtime_provider_only: true,
      runtime_provider: controls.runtime_provider,
      turn_selection: controls.turn_selection,
      provider_permissions: controls.provider_permissions,
      recovery_state: controls.recovery_state,
      recovery_reason: controls.recovery_reason,
      finish_available: false,
    } : {
      native_resume_available: false,
      resume_available: session.lifecycle === "open",
      queue_steer_available: true,
      interrupt_steer_available: false,
      native_session_captured: false,
      same_runtime_provider_only: true,
      finish_available: false,
    },
    createdAt: session.created_at,
    updatedAt: session.updated_at,
    capabilities: {
      projectChrome: false,
      rename: true,
      archive: session.lifecycle === "open",
      restore: session.lifecycle === "archived",
      delete: session.lifecycle === "archived",
      finish: false,
      resumeWithoutMessage: false,
      attachments: true,
    },
  };
}

function sessionContinuationAsRuntimeOwner(continuation: NonNullable<Session["active_continuation"]>): NonNullable<RuntimeOwnerView["activeContinuation"]> {
  return {
    id: continuation.id,
    number: continuation.number,
    runtimeProfileID: continuation.runtime_profile_id ?? "",
    runtimeProvider: continuation.runtime_provider,
    runner: continuation.runner,
    status: continuation.status,
    nativeSessionID: continuation.native_session_id,
  };
}

// ---- Test double ---------------------------------------------------------

export type RecordedOwnerCall = { op: string } & Record<string, unknown>;

export type RecordingOwnerAdapter = RuntimeOwnerAdapter & { __recorded: RecordedOwnerCall[] };

/** recordedCalls returns the verb sequence a fake adapter has run. */
export function recordedCalls(adapter: RuntimeOwnerAdapter): RecordedOwnerCall[] {
  return (adapter as RecordingOwnerAdapter).__recorded ?? [];
}

/** fakeRuntimeOwnerAdapter is the kernel's test surface: it records verbs. */
export function fakeRuntimeOwnerAdapter(options: { kind: RuntimeOwnerKind }): RuntimeOwnerAdapter {
  const adapter: RecordingOwnerAdapter = {
    kind: options.kind,
    base: `/api/fake/${options.kind}`,
    __recorded: [],
    async loadWorkspace() {
      throw new Error("fake adapter does not load");
    },
    async loadOlderTranscript() {
      throw new Error("fake adapter does not page");
    },
    async loadOlderTimeline() {
      throw new Error("fake adapter does not page");
    },
    async sendMessage(text) {
      adapter.__recorded.push({ op: "sendMessage", text });
      return undefined as unknown as RuntimeOwnerView;
    },
    async steer({ text, requestID, native }) {
      adapter.__recorded.push({ op: "steer", text, native, requestID });
    },
    async queueSteer(text) {
      adapter.__recorded.push({ op: "queueSteer", text });
    },
    async stop() {
      adapter.__recorded.push({ op: "stop" });
    },
    async finish() {
      adapter.__recorded.push({ op: "finish" });
    },
    async resume() {
      adapter.__recorded.push({ op: "resume" });
    },
    async respondPermission(permissionRequestID, decision) {
      adapter.__recorded.push({ op: "respondPermission", permissionRequestID, decision });
    },
    async rename(title) {
      adapter.__recorded.push({ op: "rename", title });
      return undefined as unknown as RuntimeOwnerView;
    },
    async changeLifecycle(action) {
      adapter.__recorded.push({ op: "changeLifecycle", action });
      return undefined as unknown as RuntimeOwnerView;
    },
    async remove() {
      adapter.__recorded.push({ op: "remove" });
    },
  };
  return adapter;
}
