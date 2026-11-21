import type { BlackboardMode, BlackboardConclusionView } from "./runtimeTypes";
import type { RuntimeActivity, RuntimeConfigurationSummary } from "./taskTypes";

export type SessionLifecycle = "open" | "archived";

export interface Session {
  blackboard_protocol?: "legacy" | "fgs";
  id: string;
  title: string;
  lifecycle: SessionLifecycle;
  run_controls?: { blackboard_mode?: BlackboardMode };
  blackboard_conclusion?: BlackboardConclusionView;
  runtime_controls?: SessionRuntimeControls;
  runtime_activity?: RuntimeActivity;
  runtime_configuration?: RuntimeConfigurationSummary;
  active_continuation?: SessionContinuation;
  latest_continuation?: SessionContinuation;
  created_at: string;
  updated_at: string;
  last_activity_at: string;
}

export type SessionEventKind = "conversation" | "attachment" | "lifecycle" | string;

export interface SessionEvent {
  id: string;
  session_id: string;
  seq: number;
  kind: SessionEventKind;
  payload: Record<string, unknown>;
  created_at: string;
}

export interface SessionContinuation {
  id: string;
  session_id: string;
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

export interface SessionRuntimeSelection {
  model_provider_id?: string;
  model?: string;
  reasoning_effort?: string;
}

export interface SessionPermission {
  request_id: string;
  permission_request_id: string;
  session_id?: string;
  provider_turn_id?: string;
  provider?: string;
  created_at: string;
}

export interface SessionRuntimeControls {
  native_resume_available: boolean;
  native_steer_available: boolean;
  native_steer_mode?: string;
  native_steer_state?: string;
  native_steer_request_id?: string;
  native_steer_error_code?: string;
  native_steer_error?: string;
  queue_steer_available: boolean;
  interrupt_steer_available: boolean;
  native_session_captured: boolean;
  runtime_provider?: string;
  turn_selection?: SessionRuntimeSelection;
  provider_permissions?: SessionPermission[];
  recovery_state?: string;
  recovery_reason?: string;
}

export interface SessionLaunchOptions {
  host_browser_enabled?: boolean;
  runtime_profile_id?: string;
  runtime_plugin_id?: string;
  provider?: string;
  runtime_provider?: string;
  model_provider_id?: string;
  model?: string;
  model_override?: string;
  reasoning_effort?: string;
  runner?: string;
  host_activated?: boolean;
  blackboard_mode?: BlackboardMode;
  run_controls?: {
    blackboard_mode?: BlackboardMode;
    container_cli?: "docker" | "podman" | string;
    sandbox_network?: string;
    sandbox_vpn_tun?: boolean;
  };
}
