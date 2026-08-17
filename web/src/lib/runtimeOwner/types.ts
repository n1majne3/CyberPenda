import type {
  BlackboardConclusionMode,
  BlackboardConclusionView,
  FinishReadiness,
  RuntimeActivity,
  RuntimeControls,
  Session,
  TaskTimelineItem,
  TaskTranscriptEntry,
} from "@/lib/api";

/** Runtime Owner kinds sharing one workspace: a Project Task and a Non-Project Session. */
export type RuntimeOwnerKind = "task" | "session";

export type RuntimeOwnerCapabilities = {
  projectChrome: boolean;
  rename: boolean;
  archive: boolean;
  restore: boolean;
  delete: boolean;
  finish: boolean;
  resumeWithoutMessage: boolean;
  attachments: boolean;
};

/** The owner-neutral workspace view both owner kinds project into. */
export type RuntimeOwnerView = {
  kind: RuntimeOwnerKind;
  id: string;
  title: string;
  status: string;
  lifecycle?: Session["lifecycle"];
  runner: string;
  runtimeProfileID: string;
  blackboardConclusionMode?: BlackboardConclusionMode;
  blackboardConclusion?: BlackboardConclusionView;
  runtimeControls?: RuntimeControls;
  runtimeActivity?: RuntimeActivity;
  activeContinuation?: RuntimeOwnerContinuation;
  latestContinuation?: RuntimeOwnerContinuation;
  createdAt: string;
  updatedAt: string;
  capabilities: RuntimeOwnerCapabilities;
};

export type RuntimeOwnerContinuation = {
  id: string;
  number: number;
  runtimeProfileID: string;
  runtimeProvider: string;
  runner: string;
  status: string;
  nativeSessionID?: string;
};

/** The complete resolved Runtime Turn Selection sent with every write. */
export type ConversationSelection = {
  model_provider_id?: string;
  model?: string;
  model_override?: string;
  reasoning_effort: string;
};

/** OwnerHistory is the owner-local Runtime Owner History Window state (#202). */
export type OwnerHistory = {
  timeline: TaskTimelineItem[];
  transcript: TaskTranscriptEntry[];
  timelineCursor: number;
  transcriptCursor: number;
  timelineHasOlder: boolean;
  transcriptHasOlder: boolean;
  timelineUnseen: number;
  transcriptUnseen: number;
  loadingOlder: "timeline" | "transcript" | null;
};

export function emptyHistory(): OwnerHistory {
  return {
    timeline: [], transcript: [], timelineCursor: 0, transcriptCursor: 0,
    timelineHasOlder: false, transcriptHasOlder: false,
    timelineUnseen: 0, transcriptUnseen: 0, loadingOlder: null,
  };
}

export type RuntimeWorkspaceLoad = {
  owner: RuntimeOwnerView;
  finishReadiness?: FinishReadiness;
  timeline: TaskTimelineItem[];
  transcript: TaskTranscriptEntry[];
  timelineCursor: number;
  transcriptCursor: number;
  timelineHasOlder: boolean;
  transcriptHasOlder: boolean;
};

export type ConversationSendMode = "native" | "interrupt" | "queue" | "resume" | "unavailable";
