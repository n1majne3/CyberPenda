export interface RuntimeProfile {
  id: string;
  name: string;
  provider: string;
  fields: {
    binary_path?: string;
    model?: string;
    endpoint?: string;
    model_provider_id?: string;
    model_provider_protocol?: string;
    model_override?: string;
    reasoning_effort?: string;
    custom_args?: string[];
    env?: Record<string, string>;
    api_keys?: Record<string, string>;
    credential_refs?: string[];
    runtime_extensions?: { id: string; enabled?: boolean; config?: Record<string, string> }[];
    mcp_servers?: { name?: string; mode?: string; command?: string; url?: string; args?: string[]; env?: Record<string, string> }[];
    default_runner?: string;
    sandbox_image?: string;
    /** Codex-only in-turn multi-agent tools control; unset inherits the Codex default. */
    codex_multi_agent?: {
      enabled?: boolean;
      max_concurrent_threads_per_session?: number;
      max_depth?: number;
    };
    /** Custom Config File: provider-bound raw overlay text, deep-merged at projection. */
    custom_config_file?: string;
  };
  created_at: string;
  updated_at: string;
}

export interface ModelProviderEndpoint {
  protocol: string;
  base_url: string;
}

export interface ModelProvider {
  id: string;
  name: string;
  base_url: string;
  protocols?: string[];
  endpoints?: ModelProviderEndpoint[];
  api_key_env: string;
  catalog?: {
    manual?: string[];
    refreshed?: string[];
    default_model?: string;
    limits?: Record<string, { context_window?: number; max_output_tokens?: number }>;
  };
  created_at?: string;
  updated_at?: string;
}

export interface ModelProviderMigrationPreview {
  profile_id: string;
  profile_name: string;
  runtime_provider: string;
  eligible: boolean;
  reason?: string;
  proposed: {
    name: string;
    base_url: string;
    model?: string;
    protocols?: string[];
    endpoints?: ModelProviderEndpoint[];
    suggested_protocol?: string;
    api_key_env?: string;
  };
  matches: { provider: ModelProvider }[];
  api_key_sources: {
    kind: string;
    credential_ref?: string;
    env_var?: string;
    configured?: boolean;
  }[];
}

export interface RuntimePluginCapabilities {
  sandbox: boolean;
  host: boolean;
  mcp_config: boolean;
  streaming_transcript: boolean;
  resume: boolean;
}

export type BlackboardMode = "interactive" | "working_graph" | "disabled";

export type BlackboardConclusionState = "clean" | "pending" | "concluding" | "action_required";

export interface BlackboardConclusionView {
  mode: BlackboardMode;
  state: BlackboardConclusionState;
  source_turn_id?: string;
  applied_revision?: number;
  error_code?: string;
  validation_reason?: string;
  validation_field_path?: string;
  validation_expected?: string;
  retry_available?: boolean;
  next_eligible_at?: string;
}

export interface RuntimePluginProfileField {
  name: string;
  type: string;
  label: string;
}

export interface RuntimePluginProfileSchema {
  fields: RuntimePluginProfileField[];
}

export interface RuntimePlugin {
  schema_version: number;
  id: string;
  name: string;
  description?: string;
  binary: {
    default: string;
    profile_field?: string;
  };
  capabilities: RuntimePluginCapabilities;
  model_provider?: {
    requirement: string;
    supported_protocols?: string[];
    protocol_preference?: string[];
  };
  profile_schema: RuntimePluginProfileSchema;
  config_projection: {
    primitive: string;
    config_path?: string;
    mcp_config_path?: string;
  };
  launch: {
    args: string[];
    singleton_options?: { options: string[]; arity: number }[];
  };
  process_env?: Record<string, string>;
  credential_env?: string[];
  transcript: {
    parser: string;
  };
}

export interface RuntimeExtension {
  schema_version: number;
  id: string;
  name: string;
  description?: string;
  compatible_runtime_plugins: string[];
  source: {
    type: "local_dir" | "local_file" | string;
    path: string;
  };
  projection: {
    location: "provider_home" | "runtime_home" | "workdir" | string;
    path: string;
  };
  config?: Record<string, string>;
}

export interface CredentialBinding {
  id: string;
  credential_ref: string;
  scope: string;
  scope_id?: string;
  source: { kind: string; value?: string; destination_env?: string };
  disabled?: boolean;
  created_at: string;
  updated_at: string;
}

export interface Skill {
  id: string;
  name: string;
  description?: string;
  source_provenance?: {
    kind?: string;
    package?: string;
    ref?: string;
    source_url?: string;
    last_imported_at?: string;
    local_modified?: boolean;
  };
  files?: Record<string, string>;
  enabled: boolean;
  globally_opted_out?: boolean;
  profile_opted_out?: boolean;
  created_at: string;
  updated_at: string;
}

export interface PreflightCheck {
  name: string;
  status: "pass" | "fail";
  detail?: string;
}

export interface PreflightSkill {
  id: string;
  name: string;
}

export interface PreflightModelProvider {
  model_provider_id?: string;
  model_provider_name?: string;
  endpoint_base_url?: string;
  base_url?: string;
  protocol?: string;
  model?: string;
  api_key_env?: string;
  api_key_source?: string;
  projection_target?: string;
}

export interface PreflightRuntimeExtension {
  id: string;
  name?: string;
  source: "registry" | "catalog" | string;
  install_ref?: string;
  registry?: string;
}

export interface PreflightResult {
  pass: boolean;
  checks: PreflightCheck[];
  skills?: PreflightSkill[];
  runtime_extensions?: PreflightRuntimeExtension[];
  model_provider?: PreflightModelProvider;
  /**
   * Codex-only multi-agent tools projection state. "inherit" projects no
   * keys so Codex's own feature default applies; "on" and "off" project
   * the explicit keys.
   */
  codex_multi_agent?: {
    state: "inherit" | "on" | "off";
    max_concurrent_threads_per_session?: number;
    max_depth?: number;
  };
}
