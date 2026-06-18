package adapters_test

import (
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
		Provider:   runtimeprofile.ProviderCodex,
		Profile:    profile,
		Goal:       "enumerate example.com",
		ConfigPath: "/task/runtime-home/codex/config.json",
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
	// Config path is projected.
	if !strings.Contains(joined, "/task/runtime-home/codex/config.json") {
		t.Fatalf("expected config path in args, got %q", joined)
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

// TestBuildResumePromptIncludesFactIndexAndTaskSummary proves the Slice 9
// restart/resume contract: when live steering is unavailable, the adapter falls
// back to a resume continuation whose prompt carries the fact index and the
// latest task summary, enabling mechanical handoff.
func TestBuildResumePromptIncludesFactIndexAndTaskSummary(t *testing.T) {
	prompt := adapters.BuildResumePrompt(adapters.ResumeRequest{
		Goal:        "enumerate example.com",
		TaskSummary: "Completed DNS enumeration; 3 subdomains found.",
		FactIndex: []string{
			"recon:subdomains — Found 3 subdomains",
			"dns:example.com — resolves to 1.2.3.4",
		},
		SteeringDirective: "Focus on admin.example.com next.",
	})

	if !strings.Contains(prompt, "enumerate example.com") {
		t.Fatalf("expected goal in resume prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "3 subdomains found") {
		t.Fatalf("expected task summary in resume prompt, got %q", prompt)
	}
	for _, fact := range []string{"recon:subdomains", "dns:example.com"} {
		if !strings.Contains(prompt, fact) {
			t.Fatalf("expected fact %q in resume prompt, got %q", fact, prompt)
		}
	}
	if !strings.Contains(prompt, "admin.example.com") {
		t.Fatalf("expected steering directive in resume prompt, got %q", prompt)
	}
}

// TestBuildResumePromptWorksWithoutTaskSummary proves the mechanical handoff
// packet path: when no task summary exists, the prompt still carries the fact
// index so the continuation has context.
func TestBuildResumePromptWorksWithoutTaskSummary(t *testing.T) {
	prompt := adapters.BuildResumePrompt(adapters.ResumeRequest{
		Goal:      "x",
		FactIndex: []string{"fact:one — summary one"},
	})
	if !strings.Contains(prompt, "fact:one") {
		t.Fatalf("expected fact index in mechanical handoff, got %q", prompt)
	}
	if !strings.Contains(prompt, "mechanical handoff") {
		t.Fatalf("expected mechanical handoff marker when no summary, got %q", prompt)
	}
}
