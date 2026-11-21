import type { BlackboardMode, BlackboardConclusionView } from "./runtimeTypes";
import type { Scope, ProjectKind } from "./api";

/** Current Runtime health, independent of durable Task lifecycle. */
export interface RuntimeActivity {
  /** live | offline | orphaned | unknown */
  liveness: string;
  /** busy | idle while live */
  turn_activity?: string;
  /** Explains unknown without inventing failure */
  warning?: string;
}

export interface Task {
  challenge_history_available?: boolean;
  blackboard_protocol?: "legacy" | "fgs";
  id: string;
  project_id: string;
  type?: ProjectKind;
  goal: string;
  status: string;
  runner: string;
  runtime_profile_id?: string;
	runtime_configuration?: RuntimeConfigurationSummary;
	run_controls: {
		host_activated?: boolean;
		sandbox_network?: string;
		sandbox_vpn_tun?: boolean;
		container_cli?: "docker" | "podman" | string;
		blackboard_mode?: BlackboardMode;
		notes?: string;
		extras?: Record<string, string>;
		policy?: TaskPolicy;
	};
  blackboard_conclusion?: BlackboardConclusionView;
  scope_snapshot: Scope;
  runtime_controls?: RuntimeControls;
  /** Current process/session health — not Task status. */
  runtime_activity?: RuntimeActivity;
  active_continuation?: TaskContinuation;
  latest_continuation?: TaskContinuation;
  created_at: string;
  updated_at: string;
}

export interface TaskPolicy {
	max_attempts?: number;
	max_wrong_submissions?: number;
	max_wall_time_seconds?: number;
	max_consecutive_failures?: number;
	max_rating_drawdown?: number;
	max_no_progress_seconds?: number;
}

export interface RuntimeTurnSelection {
  model_provider_id?: string;
  model?: string;
  reasoning_effort?: string;
}

export interface RuntimeControls {
  native_resume_available: boolean;
  native_resume_reason?: string;
  native_steer_available?: boolean;
  native_steer_mode?: "in_turn_steer" | "interrupt_then_replace" | string;
  native_steer_state?: "idle" | "requested" | "acknowledged" | "settled" | "started" | "applied" | "failed" | string;
  native_steer_request_id?: string;
  native_steer_error_code?: string;
  native_steer_error?: string;
  native_steer_reason?: string;
  resume_available: boolean;
  /** True only when Runtime Activity is live and idle. */
  finish_available?: boolean;
  queue_steer_available: boolean;
  interrupt_steer_available: boolean;
  interrupt_steer_reason?: string;
  native_session_captured: boolean;
  same_runtime_provider_only: boolean;
  runtime_provider?: string;
  /**
   * Fixed-at-launch Pi projected Model Provider IDs (ADR 0015).
   * Missing/empty → fail closed for native cross-provider (restart path).
   */
  projected_model_provider_ids?: string[];
  /** Preceding Runtime Turn Selection retained for the conversation composer. */
  turn_selection?: RuntimeTurnSelection;
  provider_permissions?: ProviderPermissionRequest[];
  recovery_state?: string;
  recovery_reason?: string;
}

export interface ProviderPermissionRequest {
  request_id?: string;
  permission_request_id: string;
  session_id?: string;
  provider_turn_id?: string;
  provider?: string;
  created_at?: string;
}

export interface TaskContinuation {
  id: string;
  task_id: string;
  number: number;
  runtime_profile_id?: string;
  runtime_provider: string;
  runner: string;
  status: string;
  container_id?: string;
  native_session_id?: string;
  native_session_path?: string;
  started_at: string;
  updated_at: string;
  ended_at?: string;
}

export interface RuntimeConfigurationSummary {
  host_browser_enabled?: boolean;
  runtime_profile_id?: string;
  runtime_profile_name?: string;
  runtime_plugin_id: string;
  runner: string;
  model_provider_id: string;
  model_provider_name?: string;
  model: string;
  requested_reasoning_effort?: string;
}

export interface TaskTranscriptEntry {
  /** Stable position inside independently paged child history. */
  position?: number;
  id: string;
  seq: number;
  continuation: number;
  kind: "message" | "reasoning" | "tool_call" | "tool_result" | "runtime_output" | "continuation" | string;
  role: "user" | "assistant" | "system" | "runtime" | "tool" | string;
  text?: string;
  tool_call_id?: string;
  tool_name?: string;
  details?: Record<string, unknown>;
  stream?: string;
  status?: string;
  incremental?: boolean;
  created_at: string;
  /** True when this row is a bounded preview of an oversized entry. */
  truncated?: boolean;
  /** Owner-authorized reference to the complete retained entry. */
  detail?: string;
}

export interface TaskTranscript {
  /** Source-window boundary for backward paging, independent of block updates. */
  before?: number;
  task_id: string;
  entries: TaskTranscriptEntry[];
  /** Maximum entry Seq; the client sends it back as ?after= for the next poll. */
  cursor?: number;
  /** True when older entries are reachable through ?before= paging. */
  has_older?: boolean;
}

export interface TaskTimelineItem {
  /** Stable item identity. Several items can share one source Event Seq. */
  id?: string;
  seq: number;
  type: "tool_use" | "tool_result" | "reasoning" | "text" | "error" | "lifecycle" | "steering" | "harness" | "subagent_activity";
  tool?: string;
  content?: string;
  input?: Record<string, unknown>;
  output?: string;
  status?: string;
  incremental?: boolean;
  created_at?: string;
  /** True when this item is a bounded preview of an oversized item. */
  truncated?: boolean;
  /** Owner-authorized reference to the complete retained item. */
  detail?: string;
}

export interface TaskTimeline {
  task_id: string;
  items: TaskTimelineItem[];
  /** Maximum item Seq; the client sends it back as ?after= for the next poll. */
  cursor?: number;
  /** True when older items are reachable through ?before= paging. */
  has_older?: boolean;
}
