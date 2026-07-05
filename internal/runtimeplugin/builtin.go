package runtimeplugin

func BuiltinPlugins() []Plugin {
	commonFields := []ProfileField{
		{Name: "binary_path", Type: "string", Label: "Binary path"},
		{Name: "model", Type: "string", Label: "Model"},
		{Name: "endpoint", Type: "url", Label: "Endpoint"},
		{Name: "custom_args", Type: "string_list", Label: "Custom args"},
		{Name: "env", Type: "env_map", Label: "Environment"},
		{Name: "api_keys", Type: "secret_env_map", Label: "API keys"},
		{Name: "credential_refs", Type: "string_list", Label: "Credential refs"},
		{Name: "runtime_extensions", Type: "runtime_extensions", Label: "Runtime extensions"},
		{Name: "mcp_servers", Type: "mcp_servers", Label: "MCP servers"},
		{Name: "default_runner", Type: "runner", Label: "Default runner"},
		{Name: "sandbox_image", Type: "string", Label: "Sandbox image"},
	}

	return []Plugin{
		{
			SchemaVersion: SchemaVersion,
			ID:            "fake",
			Name:          "Fake",
			Description:   "In-process fake runtime for harness and UI tests.",
			Binary:        Binary{Default: "fake"},
			Capabilities: Capabilities{
				Sandbox:             true,
				Host:                true,
				StreamingTranscript: false,
				Resume:              false,
			},
			ModelProvider:    ModelProvider{Requirement: "none"},
			ProfileSchema:    ProfileSchema{Fields: commonFields},
			ConfigProjection: ConfigProjection{Primitive: "none"},
			Launch:           LaunchTemplate{Args: []string{"{{binary}}", "{{goal}}"}},
			Transcript:       Transcript{Parser: "plain_runtime_output"},
		},
		{
			SchemaVersion: SchemaVersion,
			ID:            "codex",
			Name:          "Codex",
			Description:   "OpenAI Codex CLI runtime provider.",
			Binary:        Binary{Default: "codex", ProfileField: "binary_path"},
			Capabilities: Capabilities{
				Sandbox:             true,
				Host:                true,
				MCPConfig:           true,
				StreamingTranscript: true,
				Resume:              true,
			},
			ModelProvider: ModelProvider{
				Requirement:        "required",
				SupportedProtocols: []string{"openai_responses"},
				ProtocolPreference: []string{"openai_responses"},
			},
			ProfileSchema: commonProfileSchema(commonFields),
			ConfigProjection: ConfigProjection{
				Primitive:  "codex_home",
				ConfigPath: "runtime-home/codex/config.toml",
			},
			Launch: LaunchTemplate{
				Args: []string{"{{binary}}", "{{codex_subcommand}}", "--model", "{{model}}", "{{codex_exec_args}}", "{{custom_args}}", "{{codex_goal_prefix}}", "{{goal}}"},
			},
			NativeResume: NativeResume{
				Supported:     true,
				SessionSource: "codex_session_jsonl",
				Args:          []string{"{{binary}}", "--model", "{{model}}", "resume", "{{native_session}}", "{{resumed_message}}"},
			},
			ProcessEnv:    map[string]string{"CODEX_HOME": "{{runtime_home}}/codex"},
			CredentialEnv: []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
			Transcript:    Transcript{Parser: "codex_json"},
		},
		{
			SchemaVersion: SchemaVersion,
			ID:            "claude_code",
			Name:          "Claude Code",
			Description:   "Claude Code CLI runtime provider.",
			Binary:        Binary{Default: "claude", ProfileField: "binary_path"},
			Capabilities: Capabilities{
				Sandbox:             true,
				Host:                true,
				MCPConfig:           true,
				StreamingTranscript: true,
				Resume:              false,
			},
			ModelProvider: ModelProvider{
				Requirement:        "required",
				SupportedProtocols: []string{"anthropic_messages"},
				ProtocolPreference: []string{"anthropic_messages"},
			},
			ProfileSchema: commonProfileSchema(commonFields),
			ConfigProjection: ConfigProjection{
				Primitive:     "claude_settings",
				ConfigPath:    "runtime-home/claude/settings.json",
				MCPConfigPath: "workdir/.mcp.json",
			},
			Launch: LaunchTemplate{
				Args: []string{
					"{{binary}}",
					"--model", "{{model}}",
					"--settings", "{{config_path}}",
					"{{mcp_args}}",
					"-p",
					"--output-format", "stream-json",
					"--verbose",
					"{{custom_args}}",
					"{{claude_goal_prefix}}",
					"{{goal}}",
				},
				SingletonOptions: []SingletonOption{
					{Options: []string{"-p", "--print"}, Arity: 0},
					{Options: []string{"--output-format"}, Arity: 1},
					{Options: []string{"--verbose"}, Arity: 0},
				},
			},
			ProcessEnv:    map[string]string{"CLAUDE_HOME": "{{runtime_home}}/claude"},
			CredentialEnv: []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"},
			Transcript:    Transcript{Parser: "claude_stream_json"},
		},
		{
			SchemaVersion: SchemaVersion,
			ID:            "pi",
			Name:          "Pi",
			Description:   "Pi coding agent runtime provider.",
			Binary:        Binary{Default: "pi", ProfileField: "binary_path"},
			Capabilities: Capabilities{
				Sandbox:             true,
				Host:                true,
				MCPConfig:           true,
				StreamingTranscript: true,
				Resume:              false,
			},
			ModelProvider: ModelProvider{
				Requirement:        "required",
				SupportedProtocols: []string{"openai_chat_completions", "openai_responses", "anthropic_messages"},
				ProtocolPreference: []string{"openai_chat_completions", "openai_responses", "anthropic_messages"},
			},
			ProfileSchema: commonProfileSchema(commonFields),
			ConfigProjection: ConfigProjection{
				Primitive:     "pi_agent",
				ConfigPath:    "runtime-home/pi/agent/models.json",
				MCPConfigPath: "runtime-home/pi/agent/mcp.json",
			},
			Launch: LaunchTemplate{
				Args: []string{"{{binary}}", "{{pi_provider_args}}", "--model", "{{model}}", "{{custom_args}}", "{{goal}}"},
				SingletonOptions: []SingletonOption{
					{Options: []string{"--provider"}, Arity: 1},
				},
			},
			ProcessEnv:    map[string]string{"PI_CODING_AGENT_DIR": "{{runtime_home}}/pi/agent"},
			CredentialEnv: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
			Transcript:    Transcript{Parser: "pi_json_session"},
		},
	}
}

func commonProfileSchema(fields []ProfileField) ProfileSchema {
	out := make([]ProfileField, len(fields))
	copy(out, fields)
	return ProfileSchema{Fields: out}
}
