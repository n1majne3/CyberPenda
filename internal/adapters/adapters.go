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

	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

// LaunchArgsRequest is the input to BuildLaunchArgs.
type LaunchArgsRequest struct {
	Provider      runtimeprofile.Provider
	Profile       runtimeprofile.Profile
	Goal          string
	ConfigPath    string
	MCPConfigPath string
	// Sandbox is true when the task runs inside the container runner.
	Sandbox bool
}

type NativeResumeArgsRequest struct {
	Provider        runtimeprofile.Provider
	Profile         runtimeprofile.Profile
	NativeSessionID string
	ResumedMessage  string
	ConfigPath      string
	MCPConfigPath   string
}

// BuildLaunchArgs constructs the command-line arguments a real runtime would
// receive, derived from the structured profile fields and task goal. It never
// embeds resolved secret values — credential references are resolved by the
// runtime through bindings at execution time, not baked into argv.
func BuildLaunchArgs(req LaunchArgsRequest) ([]string, error) {
	registry := runtimeplugin.MustBuiltinRegistry()
	plugin, ok := registry.Get(string(req.Provider))
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", req.Provider)
	}

	binary := strings.TrimSpace(req.Profile.Fields.BinaryPath)
	if binary == "" {
		binary = strings.TrimSpace(plugin.Binary.Default)
	}
	if binary == "" {
		return nil, fmt.Errorf("no binary path configured for provider %q", req.Provider)
	}

	return runtimeplugin.RenderLaunch(plugin.Launch, launchRenderContext(req, binary))
}

func BuildNativeResumeArgs(req NativeResumeArgsRequest) ([]string, error) {
	registry := runtimeplugin.MustBuiltinRegistry()
	plugin, ok := registry.Get(string(req.Provider))
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", req.Provider)
	}
	if !plugin.NativeResume.Supported {
		return nil, fmt.Errorf("native resume unsupported for provider %q", req.Provider)
	}
	if strings.TrimSpace(req.NativeSessionID) == "" {
		return nil, fmt.Errorf("native resume requires a native session id")
	}
	binary := strings.TrimSpace(req.Profile.Fields.BinaryPath)
	if binary == "" {
		binary = strings.TrimSpace(plugin.Binary.Default)
	}
	if binary == "" {
		return nil, fmt.Errorf("no binary path configured for provider %q", req.Provider)
	}
	extra := append([]string{}, req.Profile.Fields.CustomArgs...)
	extra = appendRuntimeNonInteractiveArgs(req.Provider, extra)
	lists := map[string][]string{
		"custom_args": extra,
	}
	if req.Provider == runtimeprofile.ProviderCodex && !hasCLIOption(extra, "--skip-git-repo-check") {
		lists["codex_exec_args"] = []string{"--skip-git-repo-check"}
	}
	if req.Provider == runtimeprofile.ProviderClaudeCode && strings.TrimSpace(req.ResumedMessage) != "" {
		lists["claude_goal_prefix"] = []string{"--"}
	}
	if mcpConfig := strings.TrimSpace(req.MCPConfigPath); mcpConfig != "" {
		lists["mcp_args"] = []string{"--strict-mcp-config", "--mcp-config", mcpConfig}
	}
	if piArgs := piProviderArgs(req.Profile.Fields, req.Profile.Fields.CustomArgs); len(piArgs) > 0 {
		lists["pi_provider_args"] = piArgs
	}
	return runtimeplugin.RenderLaunch(runtimeplugin.LaunchTemplate{Args: plugin.NativeResume.Args}, runtimeplugin.RenderContext{
		Scalars: map[string]string{
			"binary":          binary,
			"model":           strings.TrimSpace(req.Profile.Fields.Model),
			"config_path":     strings.TrimSpace(req.ConfigPath),
			"mcp_config_path": strings.TrimSpace(req.MCPConfigPath),
			"native_session":  strings.TrimSpace(req.NativeSessionID),
			"resumed_message": req.ResumedMessage,
		},
		Lists: lists,
	})
}

func hasCLIOption(args []string, option string) bool {
	return runtimeplugin.HasCLIOption(args, option)
}

func appendRuntimeNonInteractiveArgs(provider runtimeprofile.Provider, args []string) []string {
	switch provider {
	case runtimeprofile.ProviderCodex:
		if !hasCLIOption(args, "--dangerously-bypass-approvals-and-sandbox") {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
	case runtimeprofile.ProviderClaudeCode:
		if !hasCLIOption(args, "--dangerously-skip-permissions") {
			args = append(args, "--dangerously-skip-permissions")
		}
		if !hasCLIOption(args, "--permission-mode") {
			args = append(args, "--permission-mode", "bypassPermissions")
		}
	}
	return args
}

func defaultBinary(provider runtimeprofile.Provider) string {
	registry := runtimeplugin.MustBuiltinRegistry()
	plugin, ok := registry.Get(string(provider))
	if !ok {
		return ""
	}
	return plugin.Binary.Default
}

func launchRenderContext(req LaunchArgsRequest, binary string) runtimeplugin.RenderContext {
	extra := append([]string{}, req.Profile.Fields.CustomArgs...)
	extra = appendRuntimeNonInteractiveArgs(req.Provider, extra)
	subcommand := strings.TrimSpace(req.Profile.Fields.Env["PENTEST_CODEX_SUBCOMMAND"])
	if subcommand == "" {
		subcommand = "exec"
	}

	lists := map[string][]string{
		"custom_args": extra,
	}
	if req.Provider == runtimeprofile.ProviderCodex && subcommand == "exec" && !hasCLIOption(extra, "--skip-git-repo-check") {
		lists["codex_exec_args"] = []string{"--skip-git-repo-check"}
	}
	if req.Goal != "" && subcommand != "exec" {
		lists["codex_goal_prefix"] = []string{"--"}
	}
	if req.Goal != "" && strings.TrimSpace(req.MCPConfigPath) != "" {
		lists["claude_goal_prefix"] = []string{"--"}
	}
	if mcpConfig := strings.TrimSpace(req.MCPConfigPath); mcpConfig != "" {
		lists["mcp_args"] = []string{"--strict-mcp-config", "--mcp-config", mcpConfig}
	}
	if piArgs := piProviderArgs(req.Profile.Fields, extra); len(piArgs) > 0 {
		lists["pi_provider_args"] = piArgs
	}

	return runtimeplugin.RenderContext{
		Scalars: map[string]string{
			"binary":           binary,
			"model":            strings.TrimSpace(req.Profile.Fields.Model),
			"endpoint":         strings.TrimSpace(req.Profile.Fields.Endpoint),
			"config_path":      strings.TrimSpace(req.ConfigPath),
			"mcp_config_path":  strings.TrimSpace(req.MCPConfigPath),
			"goal":             req.Goal,
			"codex_subcommand": subcommand,
		},
		Lists: lists,
	}
}

func piProviderArgs(fields runtimeprofile.Fields, customArgs []string) []string {
	if hasCLIOption(customArgs, "--provider") {
		return nil
	}
	providerID := strings.TrimSpace(fields.Env["PI_PROVIDER_ID"])
	if providerID == "" && strings.TrimSpace(fields.Endpoint) != "" {
		providerID = "custom"
	}
	if providerID == "" {
		return nil
	}
	return []string{"--provider", providerID}
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

// minRedactableSecretLen is the shortest known secret value a Redactor will mask
// by exact match. Very short values are ignored so ordinary text (e.g. a single
// letter that happens to equal a credential) is not mangled.
const minRedactableSecretLen = 8

// Redactor masks secret values in event payloads. It always applies the
// shape-based secretPatterns; when seeded with known secret values it also masks
// those values by exact match, closing the gap for opaque tokens that lack a
// recognized prefix/shape (issue #161).
type Redactor struct {
	secrets []string
}

// shapeOnlyRedactor backs the package-level Redact: shape/regex redaction only.
var shapeOnlyRedactor = NewRedactor(nil)

// NewRedactor returns a Redactor that masks any occurrence of the given secret
// values in addition to the shape-based patterns. Empty, duplicate, and
// trivially short values are dropped to avoid over-redaction.
func NewRedactor(secrets []string) *Redactor {
	seen := make(map[string]struct{}, len(secrets))
	kept := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < minRedactableSecretLen {
			continue
		}
		if _, dup := seen[secret]; dup {
			continue
		}
		seen[secret] = struct{}{}
		kept = append(kept, secret)
	}
	return &Redactor{secrets: kept}
}

// Redact returns a copy of payload with secret-shaped values and any known secret
// values replaced by a placeholder. Variable names are retained so context is not
// lost; only the resolved secret value is stripped.
func (r *Redactor) Redact(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		out[k] = r.redactValue(v)
	}
	return out
}

func (r *Redactor) redactValue(v any) any {
	switch val := v.(type) {
	case string:
		return r.redactString(val)
	case map[string]any:
		return r.Redact(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = r.redactValue(item)
		}
		return out
	case []string:
		out := make([]string, len(val))
		for i, item := range val {
			out[i] = r.redactString(item)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(val))
		for k, item := range val {
			out[k] = r.redactString(item)
		}
		return out
	default:
		return v
	}
}

func (r *Redactor) redactString(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	for _, pat := range secretPatterns {
		s = pat.ReplaceAllStringFunc(s, func(match string) string {
			return redactMatch(match)
		})
	}
	return s
}

// Redact returns a copy of payload with secret-shaped values replaced by a
// placeholder using the shape-based patterns only. Callers that know the resolved
// secret values should prefer NewRedactor(...).Redact so opaque tokens without a
// recognized shape are masked too.
func Redact(payload map[string]any) map[string]any {
	return shapeOnlyRedactor.Redact(payload)
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
