package adapters_test

import (
	"reflect"
	"strings"
	"testing"

	"pentest/internal/adapters"
	"pentest/internal/runtimeprofile"
)

// TestBuildCodexLaunchArgsFromRuntimeConfig proves the Slice 9 acceptance:
// adapters build launch args from task runtime configuration. The args must
// reflect binary path, model, and projected config without leaking secrets.
func TestBuildCodexLaunchArgsFromRuntimeConfig(t *testing.T) {
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			BinaryPath: "/usr/local/bin/codex",
			Model:      "gpt-5",
			CustomArgs: []string{"--json"},
			// Credential refs are pointers; resolved secrets must NOT appear in args.
			CredentialRefs: []string{"codex-api-key"},
		},
	}

	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderCodex,
		Profile:  profile,
		Goal:     "enumerate example.com",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}

	joined := strings.Join(args, " ")
	// Binary path leads the command.
	if args[0] != "/usr/local/bin/codex" {
		t.Fatalf("expected binary path first, got %q", args[0])
	}
	// Model is passed through.
	if !strings.Contains(joined, "gpt-5") {
		t.Fatalf("expected model in args, got %q", joined)
	}
	// Codex discovers config from CODEX_HOME; argv must not carry a config file path.
	if strings.Contains(joined, "--config") {
		t.Fatalf("codex launch args must not include --config file path, got %q", joined)
	}
	// Goal is supplied.
	if !strings.Contains(joined, "enumerate example.com") {
		t.Fatalf("expected goal in args, got %q", joined)
	}
	// Custom args pass through.
	if !strings.Contains(joined, "--json") {
		t.Fatalf("expected custom args in args, got %q", joined)
	}
	// Secrets must not leak: the credential reference name appears only as a ref,
	// never a resolved value. The raw ref name is acceptable; a resolved secret
	// would be a value like sk-... .
	for _, a := range args {
		if strings.Contains(a, "sk-") {
			t.Fatalf("secret value leaked into args: %q", a)
		}
	}
}

func TestBuildCodexLaunchArgsDefaultsToExecForPrompt(t *testing.T) {
	goal := "rest2\nA seemingly simple interactive function can have a serious impact.\nhttp://example.test/archive.zip"
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderCodex,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				Model: "gpt-5.5",
			},
		},
		Goal: goal,
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}

	want := []string{"codex", "exec", "--model", "gpt-5.5", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", goal}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestBuildCodexLaunchArgsDoesNotDuplicateExplicitSkipGitRepoCheck(t *testing.T) {
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderCodex,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				Model:      "gpt-5.5",
				CustomArgs: []string{"--skip-git-repo-check", "--json"},
			},
		},
		Goal: "scan target",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}

	if strings.Count(strings.Join(args, " "), "--skip-git-repo-check") != 1 {
		t.Fatalf("expected one --skip-git-repo-check, got %#v", args)
	}
}

func TestBuildCodexLaunchArgsDoesNotDuplicateExplicitNonInteractiveBypass(t *testing.T) {
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderCodex,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				Model:      "gpt-5.5",
				CustomArgs: []string{"--dangerously-bypass-approvals-and-sandbox"},
			},
		},
		Goal: "scan target",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}

	if strings.Count(strings.Join(args, " "), "--dangerously-bypass-approvals-and-sandbox") != 1 {
		t.Fatalf("expected one permission bypass flag, got %#v", args)
	}
}

func TestBuildNativeResumeArgsUsesRuntimePluginContract(t *testing.T) {
	args, err := adapters.BuildNativeResumeArgs(adapters.NativeResumeArgsRequest{
		Provider: runtimeprofile.ProviderCodex,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				BinaryPath: "/usr/local/bin/codex",
				Model:      "gpt-5",
			},
		},
		NativeSessionID: "sess-123",
		ResumedMessage:  "focus admin",
	})
	if err != nil {
		t.Fatalf("build native resume args: %v", err)
	}
	want := []string{"/usr/local/bin/codex", "exec", "--model", "gpt-5", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "resume", "sess-123", "focus admin"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected native resume args:\nwant %#v\ngot  %#v", want, args)
	}
}

func TestBuildNativeResumeArgsUsesClaudeCodeRuntimePluginContract(t *testing.T) {
	args, err := adapters.BuildNativeResumeArgs(adapters.NativeResumeArgsRequest{
		Provider: runtimeprofile.ProviderClaudeCode,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
			Fields: runtimeprofile.Fields{
				BinaryPath: "/usr/local/bin/claude",
				Model:      "claude-sonnet-4",
				CustomArgs: []string{"--permission-mode", "bypassPermissions"},
			},
		},
		NativeSessionID: "sess-123",
		ResumedMessage:  "focus admin",
		ConfigPath:      "/task/runtime-home/claude/settings.json",
		MCPConfigPath:   "/task/workdir/.mcp.json",
	})
	if err != nil {
		t.Fatalf("build native resume args: %v", err)
	}
	want := []string{
		"/usr/local/bin/claude",
		"--resume", "sess-123",
		"--model", "claude-sonnet-4",
		"--settings", "/task/runtime-home/claude/settings.json",
		"--strict-mcp-config", "--mcp-config", "/task/workdir/.mcp.json",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
		"--dangerously-skip-permissions",
		"--",
		"focus admin",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected claude native resume args:\nwant %#v\ngot  %#v", want, args)
	}
}

func TestBuildNativeResumeArgsUsesClaudeNonInteractiveDefaultsOnHost(t *testing.T) {
	args, err := adapters.BuildNativeResumeArgs(adapters.NativeResumeArgsRequest{
		Provider: runtimeprofile.ProviderClaudeCode,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
			Fields:   runtimeprofile.Fields{Model: "claude-sonnet-4"},
		},
		NativeSessionID: "sess-123",
		ResumedMessage:  "focus admin",
	})
	if err != nil {
		t.Fatalf("build native resume args: %v", err)
	}
	if !strings.Contains(strings.Join(args, " "), "--dangerously-skip-permissions") {
		t.Fatalf("expected host resume to include bypass args, got %#v", args)
	}
}

func TestBuildNativeResumeArgsUsesPiRuntimePluginContract(t *testing.T) {
	args, err := adapters.BuildNativeResumeArgs(adapters.NativeResumeArgsRequest{
		Provider: runtimeprofile.ProviderPi,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				BinaryPath: "/usr/local/bin/pi",
				Model:      "DeepSeek-V4-Pro",
				Endpoint:   "https://api.edgefn.net/v1",
				// Non-conflicting advanced options only; --thinking/--model/--provider
				// are structured-field aliases and rejected at Profile validation.
				CustomArgs: []string{"--debug", "--strict-mode"},
			},
		},
		NativeSessionID: "sess-pi",
		ResumedMessage:  "focus admin",
	})
	if err != nil {
		t.Fatalf("build native resume args: %v", err)
	}
	want := []string{
		"/usr/local/bin/pi",
		"--provider", "custom",
		"--model", "DeepSeek-V4-Pro",
		"--mode", "json",
		"--session", "sess-pi",
		"--debug",
		"--strict-mode",
		"focus admin",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected pi native resume args:\nwant %#v\ngot  %#v", want, args)
	}
}

// TestBuildClaudeCodeLaunchArgs proves the same contract for Claude Code.
func TestBuildClaudeCodeLaunchArgs(t *testing.T) {
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			BinaryPath: "/usr/local/bin/claude",
			Model:      "claude-sonnet-4",
		},
	}

	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider:   runtimeprofile.ProviderClaudeCode,
		Profile:    profile,
		Goal:       "find vulns",
		ConfigPath: "/task/runtime-home/claude/config.json",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	if args[0] != "/usr/local/bin/claude" {
		t.Fatalf("expected binary path first, got %q", args[0])
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "claude-sonnet-4") {
		t.Fatalf("expected model in args, got %q", joined)
	}
	if !strings.Contains(joined, "--settings /task/runtime-home/claude/config.json") {
		t.Fatalf("expected --settings path in args, got %q", joined)
	}
	for _, want := range []string{"-p", "--output-format stream-json", "--verbose"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected Claude transcript arg %q in args, got %q", want, joined)
		}
	}
}

func TestBuildClaudeCodeLaunchArgsKeepsExplicitOutputFormat(t *testing.T) {
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderClaudeCode,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
			Fields: runtimeprofile.Fields{
				Model:      "claude-sonnet-4",
				CustomArgs: []string{"--print", "--output-format", "text", "--verbose"},
			},
		},
		Goal: "find vulns",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "stream-json") {
		t.Fatalf("explicit output format should not be replaced, got %q", joined)
	}
	if strings.Count(joined, "--output-format") != 1 {
		t.Fatalf("expected one output format option, got %q", joined)
	}
	if !strings.Contains(joined, "--print --output-format text --verbose") {
		t.Fatalf("expected explicit Claude args to pass through, got %q", joined)
	}
}

func TestBuildClaudeCodeLaunchArgsUsesNonInteractiveDefaultsInSandbox(t *testing.T) {
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderClaudeCode,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
			Fields:   runtimeprofile.Fields{Model: "deepseek-v4-flash"},
		},
		Goal:    "enumerate example.com",
		Sandbox: true,
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--dangerously-skip-permissions", "--permission-mode bypassPermissions"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected bypass arg %q in %q", want, joined)
		}
	}
}

func TestBuildClaudeCodeLaunchArgsUsesNonInteractiveDefaultsOnHost(t *testing.T) {
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderClaudeCode,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
			Fields:   runtimeprofile.Fields{Model: "deepseek-v4-flash"},
		},
		Goal:    "enumerate example.com",
		Sandbox: false,
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Fatalf("expected host runner to include bypass args, got %q", joined)
	}
}

func TestBuildClaudeCodeLaunchArgsUsesStrictMCPConfig(t *testing.T) {
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderClaudeCode,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
			Fields: runtimeprofile.Fields{
				Model: "glm-5.2",
			},
		},
		Goal:          "call trusted mcp",
		ConfigPath:    "/task/runtime-home/claude/settings.json",
		MCPConfigPath: "/task/workdir/.mcp.json",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--strict-mcp-config",
		"--mcp-config /task/workdir/.mcp.json",
		"-- call trusted mcp",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in args, got %q", want, joined)
		}
	}
}

func TestBuildPiLaunchArgsSelectsGeneratedCustomProvider(t *testing.T) {
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderPi,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				Model:    "DeepSeek-V4-Pro",
				Endpoint: "https://api.edgefn.net/v1",
			},
		},
		Goal: "test authorized target",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--provider custom") {
		t.Fatalf("expected generated custom provider selection, got %q", joined)
	}
	if !strings.Contains(joined, "--model DeepSeek-V4-Pro") {
		t.Fatalf("expected configured model, got %q", joined)
	}
	if !strings.Contains(joined, "--mode json") {
		t.Fatalf("expected Pi JSON event stream mode, got %q", joined)
	}
}

func TestBuildPiLaunchArgsUsesPIProviderIDForStructuredProvider(t *testing.T) {
	// Structured Model Provider / PI_PROVIDER_ID is authoritative. --provider in
	// Custom Args is a conflict rejected at Profile validation/Preflight; launch
	// assembly projects provider only from structured fields.
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderPi,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				Model:      "mimo-v2.5-pro",
				Endpoint:   "https://api.example.test/v1",
				CustomArgs: []string{"--debug", "--strict-mode"},
				Env:        map[string]string{"PI_PROVIDER_ID": "xiaomi-token-plan-cn"},
			},
		},
		Goal: "test authorized target",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}

	joined := strings.Join(args, " ")
	if strings.Count(joined, "--provider") != 1 {
		t.Fatalf("expected one structured provider argument, got %q", joined)
	}
	if !strings.Contains(joined, "--provider xiaomi-token-plan-cn") {
		t.Fatalf("expected PI_PROVIDER_ID projection, got %q", joined)
	}
	if !strings.Contains(joined, "--debug") || !strings.Contains(joined, "--strict-mode") {
		t.Fatalf("expected non-conflicting custom args preserved, got %q", joined)
	}
}

func TestBuildPiLaunchArgsDoesNotDoubleInjectWhenLegacyProviderCustomArgPresent(t *testing.T) {
	// Defensive only: legacy/invalid profiles that somehow still carry
	// --provider in Custom Args must not get a second injected --provider.
	// Domain validation rejects this at Create/Update/Preflight.
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderPi,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				Model:      "mimo-v2.5-pro",
				Endpoint:   "https://api.example.test/v1",
				CustomArgs: []string{"--provider", "legacy-explicit"},
				Env:        map[string]string{"PI_PROVIDER_ID": "custom"},
			},
		},
		Goal: "test authorized target",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Count(joined, "--provider") != 1 {
		t.Fatalf("expected no double-inject of --provider, got %q", joined)
	}
	if !strings.Contains(joined, "--provider legacy-explicit") {
		t.Fatalf("expected legacy custom --provider token preserved as-is, got %q", joined)
	}
}

// TestRedactSecretsFromEventPayload proves adapters redact resolved secret
// values before they reach task events.
func TestRedactSecretsFromEventPayload(t *testing.T) {
	payload := map[string]any{
		"text":  "using key sk-1234567890abcdef for OPENAI_API_KEY",
		"token": "Bearer abcDEFghiJKL",
		"safe":  "model gpt-5",
		"args":  []string{"--api-key", "sk-1234567890abcdef"},
		"env":   map[string]string{"OPENAI_API_KEY": "sk-1234567890abcdef"},
	}

	redacted := adapters.Redact(payload)

	text, _ := redacted["text"].(string)
	if strings.Contains(text, "sk-1234567890abcdef") {
		t.Fatalf("expected api key redacted, got %q", text)
	}
	if !strings.Contains(text, "OPENAI_API_KEY") {
		t.Fatalf("expected env var name retained, got %q", text)
	}
	token, _ := redacted["token"].(string)
	if strings.Contains(token, "abcDEFghiJKL") {
		t.Fatalf("expected bearer token redacted, got %q", token)
	}
	safe, _ := redacted["safe"].(string)
	if safe != "model gpt-5" {
		t.Fatalf("expected safe content untouched, got %q", safe)
	}
	args, _ := redacted["args"].([]string)
	if len(args) != 2 || strings.Contains(args[1], "sk-1234567890abcdef") {
		t.Fatalf("expected string slice secret redacted, got %#v", args)
	}
	env, _ := redacted["env"].(map[string]string)
	if strings.Contains(env["OPENAI_API_KEY"], "sk-1234567890abcdef") {
		t.Fatalf("expected env map secret redacted, got %#v", env)
	}
}

// TestDetectBinaryReportsMissingWhenAbsent proves binary detection fails
// cleanly when the configured binary does not exist, preserving the error for
// the harness (Slice 9: adapter failure preserves error).
func TestDetectBinaryReportsMissingWhenAbsent(t *testing.T) {
	_, err := adapters.DetectBinary(adapters.DetectRequest{
		Provider:       runtimeprofile.ProviderCodex,
		ConfiguredPath: "/definitely/not/installed/codex",
	})
	if err == nil {
		t.Fatal("expected detection error for missing binary")
	}
}

func TestBuildLaunchArgsPreservesNonConflictingCustomArgsOrder(t *testing.T) {
	custom := []string{"--strict", "--flag=value", "keep-me"}
	args, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: runtimeprofile.ProviderCodex,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				Model:      "gpt-5",
				CustomArgs: append([]string(nil), custom...),
			},
		},
		Goal: "goal",
	})
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	// Non-conflicting custom args must appear byte/order-equivalently before the goal.
	// Runtime non-interactive defaults may append after them.
	joined := strings.Join(args, "\x00")
	wantSeq := strings.Join(custom, "\x00")
	if !strings.Contains(joined, wantSeq) {
		t.Fatalf("custom args order not preserved:\nargs=%#v\nwant contiguous %#v", args, custom)
	}
}

func TestBuildNativeResumeArgsPreservesNonConflictingCustomArgsOrder(t *testing.T) {
	custom := []string{"--strict", "--flag=value"}
	args, err := adapters.BuildNativeResumeArgs(adapters.NativeResumeArgsRequest{
		Provider: runtimeprofile.ProviderCodex,
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				Model:      "gpt-5",
				CustomArgs: append([]string(nil), custom...),
			},
		},
		NativeSessionID: "sess-1",
		ResumedMessage:  "continue",
	})
	if err != nil {
		t.Fatalf("build native resume: %v", err)
	}
	joined := strings.Join(args, "\x00")
	wantSeq := strings.Join(custom, "\x00")
	if !strings.Contains(joined, wantSeq) {
		t.Fatalf("custom args order not preserved on resume:\nargs=%#v\nwant contiguous %#v", args, custom)
	}
}
