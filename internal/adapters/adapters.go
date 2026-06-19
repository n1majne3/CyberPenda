// Package adapters holds provider-specific runtime logic that can be tested
// without a real runtime binary: launch argument construction, secret redaction,
// and binary detection. Real Run execution (smoke tasks) requires the binary to
// be present and is exercised out-of-band; the pure logic here is unit-tested.
//
// The package deliberately keeps these as pure functions rather than embedding
// them in runtime.Adapter implementations, so the Slice 9 acceptance checks
// (event parsing, launch argument construction, secret redaction, steering
// mode behavior, restart/resume prompt construction) can run in CI without
// real runtimes.
package adapters

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"pentest/internal/runtimeprofile"
)

// LaunchArgsRequest is the input to BuildLaunchArgs.
type LaunchArgsRequest struct {
	Provider      runtimeprofile.Provider
	Profile       runtimeprofile.Profile
	Goal          string
	ConfigPath    string
	MCPConfigPath string
}

// BuildLaunchArgs constructs the command-line arguments a real runtime would
// receive, derived from the structured profile fields and task goal. It never
// embeds resolved secret values — credential references are resolved by the
// runtime through bindings at execution time, not baked into argv.
func BuildLaunchArgs(req LaunchArgsRequest) ([]string, error) {
	binary := strings.TrimSpace(req.Profile.Fields.BinaryPath)
	if binary == "" {
		binary = defaultBinary(req.Provider)
	}
	if binary == "" {
		return nil, fmt.Errorf("no binary path configured for provider %q", req.Provider)
	}

	extra := req.Profile.Fields.CustomArgs
	args := []string{binary}
	switch req.Provider {
	case runtimeprofile.ProviderCodex:
		// Codex reads config.toml and auth.json from CODEX_HOME; --config is for
		// one-off TOML key overrides, not a config file path.
		subcommand := "run"
		if mode := strings.TrimSpace(req.Profile.Fields.Env["PENTEST_CODEX_SUBCOMMAND"]); mode != "" {
			subcommand = mode
		}
		args = append(args, subcommand)
		if model := strings.TrimSpace(req.Profile.Fields.Model); model != "" {
			args = append(args, "--model", model)
		}
		if len(extra) > 0 {
			args = append(args, extra...)
		}
		if req.Goal != "" {
			if subcommand == "exec" {
				args = append(args, req.Goal)
			} else {
				args = append(args, "--", req.Goal)
			}
		}
	case runtimeprofile.ProviderClaudeCode:
		if model := strings.TrimSpace(req.Profile.Fields.Model); model != "" {
			args = append(args, "--model", model)
		}
		if req.ConfigPath != "" {
			args = append(args, "--settings", req.ConfigPath)
		}
		if mcpConfig := strings.TrimSpace(req.MCPConfigPath); mcpConfig != "" {
			args = append(args, "--strict-mcp-config", "--mcp-config", mcpConfig)
		}
		if !hasCLIOption(extra, "-p") && !hasCLIOption(extra, "--print") {
			args = append(args, "-p")
		}
		if !hasCLIOption(extra, "--output-format") {
			args = append(args, "--output-format", "stream-json")
		}
		if !hasCLIOption(extra, "--verbose") {
			args = append(args, "--verbose")
		}
		if len(extra) > 0 {
			args = append(args, extra...)
		}
		if req.Goal != "" {
			if strings.TrimSpace(req.MCPConfigPath) != "" {
				args = append(args, "--", req.Goal)
			} else {
				args = append(args, req.Goal)
			}
		}
	case runtimeprofile.ProviderPi:
		// Pi discovers agent/models.json and agent/auth.json from PI_CODING_AGENT_DIR.
		if !hasCLIOption(extra, "--provider") {
			providerID := strings.TrimSpace(req.Profile.Fields.Env["PI_PROVIDER_ID"])
			if providerID == "" && strings.TrimSpace(req.Profile.Fields.Endpoint) != "" {
				providerID = "custom"
			}
			if providerID != "" {
				args = append(args, "--provider", providerID)
			}
		}
		if model := strings.TrimSpace(req.Profile.Fields.Model); model != "" {
			args = append(args, "--model", model)
		}
		if len(extra) > 0 {
			args = append(args, extra...)
		}
		if req.Goal != "" {
			args = append(args, req.Goal)
		}
	default:
		return nil, fmt.Errorf("unsupported provider %q", req.Provider)
	}

	return args, nil
}

func hasCLIOption(args []string, option string) bool {
	for _, arg := range args {
		if arg == option || strings.HasPrefix(arg, option+"=") {
			return true
		}
	}
	return false
}

func defaultBinary(provider runtimeprofile.Provider) string {
	switch provider {
	case runtimeprofile.ProviderCodex:
		return "codex"
	case runtimeprofile.ProviderClaudeCode:
		return "claude"
	case runtimeprofile.ProviderPi:
		return "pi"
	default:
		return ""
	}
}

// DetectRequest is the input to DetectBinary.
type DetectRequest struct {
	Provider       runtimeprofile.Provider
	ConfiguredPath string
	// LookupPath is searched when ConfiguredPath is empty. Defaults to PATH.
	LookupPath string
}

// DetectResult is the outcome of binary detection.
type DetectResult struct {
	Path    string
	Version string
}

// DetectBinary checks that a configured or default provider binary exists and
// is executable. It does not run the binary. On failure it returns a non-nil
// error so the harness can record the failure with runtime, runner, task paths,
// and event history preserved.
func DetectBinary(req DetectRequest) (DetectResult, error) {
	candidate := strings.TrimSpace(req.ConfiguredPath)
	if candidate == "" {
		candidate = defaultBinary(req.Provider)
	}
	if candidate == "" {
		return DetectResult{}, fmt.Errorf("no binary configured or default for provider %q", req.Provider)
	}

	resolved := candidate
	if !filepath.IsAbs(candidate) {
		resolved = lookPath(candidate, req.LookupPath)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return DetectResult{}, fmt.Errorf("detect binary %q: %w", candidate, err)
	}
	if info.IsDir() {
		return DetectResult{}, fmt.Errorf("detected path %q is a directory, not a binary", resolved)
	}
	return DetectResult{Path: resolved}, nil
}

// lookPath searches LookupPath (or PATH) for an executable by name. It mirrors
// exec.LookPath semantics without importing os/exec (kept light for testability).
func lookPath(name, lookupPath string) string {
	if lookupPath == "" {
		lookupPath = os.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(lookupPath) {
		if dir == "" {
			continue
		}
		full := filepath.Join(dir, name)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			if info.Mode()&0o111 != 0 {
				return full
			}
		}
	}
	return name
}

// ErrSecretRedacted is returned when callers ask whether a payload was redacted.
var ErrSecretRedacted = errors.New("secret value redacted")

// secretPatterns matches common secret shapes so resolved values never reach
// task events. Patterns are deliberately conservative: they redact anything that
// looks like a long opaque token, while leaving short identifiers intact.
var secretPatterns = []*regexp.Regexp{
	// API keys: sk-..., key-..., ghp_..., glpat-..., xoxb-..., AKIA...
	regexp.MustCompile(`(?i)(sk|key|ghp|glpat|xoxb|AKIA|AIza)[-_]?[A-Za-z0-9]{12,}`),
	// Bearer tokens.
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{8,}`),
	// Generic long hex/base64 blobs (40+ chars) that follow '=' or ': '.
	regexp.MustCompile(`(?i)(=|:\s*)[A-Fa-f0-9]{40,}`),
	// High-entropy secrets in env-style assignments: NAME=value where value is long.
	regexp.MustCompile(`(?i)([A-Z0-9_]{4,}_KEY|_TOKEN|_SECRET|_PASSWORD)=([A-Za-z0-9/+=\-_.]{12,})`),
}

// Redact returns a copy of payload with secret-shaped values replaced by a
// placeholder. Variable names are retained so context is not lost; only the
// resolved secret value is stripped.
func Redact(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		out[k] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch val := v.(type) {
	case string:
		return redactString(val)
	case map[string]any:
		return Redact(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = redactValue(item)
		}
		return out
	case []string:
		out := make([]string, len(val))
		for i, item := range val {
			out[i] = redactString(item)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(val))
		for k, item := range val {
			out[k] = redactString(item)
		}
		return out
	default:
		return v
	}
}

func redactString(s string) string {
	for _, pat := range secretPatterns {
		s = pat.ReplaceAllStringFunc(s, func(match string) string {
			return redactMatch(match)
		})
	}
	return s
}

// redactMatch keeps human-readable prefixes (env var names, the word 'bearer')
// and replaces only the opaque secret portion.
func redactMatch(match string) string {
	// Env-style: NAME=value
	if idx := strings.Index(match, "="); idx > 0 {
		return match[:idx+1] + "[REDACTED]"
	}
	// Bearer <token>
	if strings.HasPrefix(strings.ToLower(match), "bearer ") {
		return "bearer [REDACTED]"
	}
	// Prefixed keys like sk-...: keep the prefix, redact the body.
	for _, prefix := range []string{"sk-", "key-", "ghp_", "glpat-", "xoxb-", "AKIA", "AIza"} {
		if strings.HasPrefix(match, prefix) {
			return prefix + "[REDACTED]"
		}
	}
	// Bare blob after =/: already handled above; fallback full redaction.
	return "[REDACTED]"
}

// ResumeRequest describes the context for a restart/resume continuation prompt.
// When live steering is unavailable, adapters restart the runtime with a prompt
// that carries the goal, prior task summary (if any), the fact index, and any
// pending steering directive. This is the mechanical handoff path.
type ResumeRequest struct {
	Goal              string
	TaskSummary       string
	FactIndex         []string
	SteeringDirective string
}

// BuildResumePrompt constructs the prompt a runtime receives when it is resumed
// after a restart, rather than steered live. With a task summary it produces a
// continuation prompt; without one it produces a mechanical handoff packet that
// still carries the fact index.
func BuildResumePrompt(req ResumeRequest) string {
	var b strings.Builder
	b.WriteString("Resuming task.\n\n")
	b.WriteString("Goal: ")
	b.WriteString(req.Goal)
	b.WriteString("\n\n")

	if strings.TrimSpace(req.TaskSummary) != "" {
		b.WriteString("Prior task summary:\n")
		b.WriteString(req.TaskSummary)
		b.WriteString("\n\n")
	} else {
		b.WriteString("No prior task summary available; using mechanical handoff.\n\n")
	}

	if len(req.FactIndex) > 0 {
		b.WriteString("Current fact index:\n")
		for _, fact := range req.FactIndex {
			b.WriteString("- ")
			b.WriteString(fact)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if strings.TrimSpace(req.SteeringDirective) != "" {
		b.WriteString("Steering directive for this continuation:\n")
		b.WriteString(req.SteeringDirective)
		b.WriteString("\n")
	}
	return b.String()
}
