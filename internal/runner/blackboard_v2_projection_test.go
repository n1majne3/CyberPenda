package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestCodexBlackboardV2ProjectsOnePersistentChecklistWithoutLeakedMetadata(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-v2-codex", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare Codex layout: %v", err)
	}
	header := blackboardv2.LaunchHeader{
		Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
		Schema: "runtime-blackboard/v2", Revision: 17,
	}
	if err := runner.ProjectCodexBlackboardV2Files(layout, header, project.Scope{Domains: []string{"example.test"}}); err != nil {
		t.Fatalf("project Codex Blackboard v2 files: %v", err)
	}

	agents, err := os.ReadFile(filepath.Join(layout.Workdir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	checklist := blackboardv2.CodexChecklist()
	if strings.Count(string(agents), checklist) != 1 {
		t.Fatalf("Codex checklist count = %d, want exactly one:\n%s", strings.Count(string(agents), checklist), agents)
	}
	for _, forbidden := range []string{
		"project_id", "task_id", "continuation_id", "runtime_profile", "runtime_plugin",
		"http://", "https://", "hash", "bytes", "tokens", "digest", "Trusted tools:", "blackboard_change",
	} {
		if strings.Contains(strings.ToLower(string(agents)), strings.ToLower(forbidden)) {
			t.Fatalf("AGENTS.md leaked forbidden launch metadata %q: %s", forbidden, agents)
		}
	}
	for _, absent := range []string{"CLAUDE.md", filepath.Join(".pentest", "context.json")} {
		if _, err := os.Stat(filepath.Join(layout.Workdir, absent)); !os.IsNotExist(err) {
			t.Fatalf("unexpected duplicate Runtime context projection %s: %v", absent, err)
		}
	}
	if _, err := os.Stat(filepath.Join(layout.Workdir, ".pentest", "scope.json")); err != nil {
		t.Fatalf("Scope projection unavailable: %v", err)
	}
}

func TestBlackboardV2LaunchHeaderIsDeterministicFiveFieldAllowlist(t *testing.T) {
	header := blackboardv2.LaunchHeader{
		Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
		Schema: "runtime-blackboard/v2", Revision: 42,
	}
	want := "Runner: sandbox\nScope: .pentest/scope.json\nBlackboard: .pentest/blackboard.json\nSchema: runtime-blackboard/v2\nRevision: 42"
	if got := blackboardv2.RenderLaunchHeader(header); got != want {
		t.Fatalf("launch header = %q, want %q", got, want)
	}
}

func TestClaudeAndPiBlackboardV2ProjectsSharedChecklistOnNativeInstructionChannel(t *testing.T) {
	header := blackboardv2.LaunchHeader{
		Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
		Schema: "runtime-blackboard/v2", Revision: 9,
	}
	checklist := blackboardv2.CodexChecklist()
	for _, tc := range []struct {
		provider        runtimeprofile.Provider
		instructionFile string
		absent          string
	}{
		{runtimeprofile.ProviderClaudeCode, "CLAUDE.md", "AGENTS.md"},
		{runtimeprofile.ProviderPi, "AGENTS.md", "CLAUDE.md"},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-v2-"+string(tc.provider), tc.provider)
			if err != nil {
				t.Fatalf("prepare layout: %v", err)
			}
			if err := runner.ProjectBlackboardV2Files(layout, tc.provider, header, project.Scope{Domains: []string{"example.test"}}); err != nil {
				t.Fatalf("project Blackboard v2 files: %v", err)
			}
			instructions, err := os.ReadFile(filepath.Join(layout.Workdir, tc.instructionFile))
			if err != nil {
				t.Fatalf("read %s: %v", tc.instructionFile, err)
			}
			if strings.Count(string(instructions), checklist) != 1 {
				t.Fatalf("checklist count = %d, want 1 in %s", strings.Count(string(instructions), checklist), tc.instructionFile)
			}
			if _, err := os.Stat(filepath.Join(layout.Workdir, tc.absent)); !os.IsNotExist(err) {
				t.Fatalf("expected %s absent so checklist appears once, err=%v", tc.absent, err)
			}
			if _, err := os.Stat(filepath.Join(layout.Workdir, ".pentest", "context.json")); !os.IsNotExist(err) {
				t.Fatalf("legacy context.json present: %v", err)
			}
			for _, forbidden := range []string{"project_id", "task_id", "http://", "Trusted tools:", "blackboard_change"} {
				if strings.Contains(strings.ToLower(string(instructions)), strings.ToLower(forbidden)) {
					t.Fatalf("%s leaked %q: %s", tc.instructionFile, forbidden, instructions)
				}
			}
		})
	}
}

func TestClaudeV2RuntimeConfigPreservesTrustedMCPAllowlistWithoutIdentityContext(t *testing.T) {
	layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), "task-claude-v2-mcp", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare Claude v2 layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields:   runtimeprofile.Fields{Model: "claude-sonnet-4"},
	}
	if _, err := runner.ProjectBlackboardV2RuntimeConfig(layout, profile, runner.ProjectionRequest{
		DaemonAddr: "127.0.0.1:8787",
		AuthToken:  "continuation-grant-token",
	}); err != nil {
		t.Fatalf("project Claude v2 config: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "settings.json"))
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decode Claude settings: %v", err)
	}
	want := map[string]bool{
		"mcp__pentest__blackboard_change":             true,
		"mcp__pentest__blackboard_read":               true,
		"mcp__pentest__blackboard_history":            true,
		"mcp__pentest__blackboard_retain_evidence":    true,
		"mcp__pentest__blackboard_checkpoint_attempt": true,
		"mcp__pentest__blackboard_finish":             true,
	}
	for _, allowed := range settings.Permissions.Allow {
		if !want[allowed] {
			t.Fatalf("unexpected allow entry %q: %s", allowed, raw)
		}
		delete(want, allowed)
	}
	if len(want) != 0 {
		t.Fatalf("missing allow entries %#v: %s", want, raw)
	}
	mcpRaw, err := os.ReadFile(filepath.Join(layout.Workdir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	if !strings.Contains(string(mcpRaw), "continuation-grant-token") {
		t.Fatalf("Claude MCP config missing grant token: %s", mcpRaw)
	}
	if _, err := os.Stat(filepath.Join(layout.Workdir, ".pentest", "context.json")); !os.IsNotExist(err) {
		t.Fatalf("Claude v2 wrote identity context.json: %v", err)
	}
}

func TestPiV2RuntimeConfigProjectsTrustedMCPWithoutBlackboardSemantics(t *testing.T) {
	layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), "task-pi-v2-mcp", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare Pi v2 layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields:   runtimeprofile.Fields{Model: "test-model"},
	}
	if _, err := runner.ProjectBlackboardV2RuntimeConfig(layout, profile, runner.ProjectionRequest{
		DaemonAddr: "127.0.0.1:8787",
		AuthToken:  "continuation-grant-token",
	}); err != nil {
		t.Fatalf("project Pi v2 config: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "mcp.json"))
	if err != nil {
		t.Fatalf("read Pi mcp.json: %v", err)
	}
	if !strings.Contains(string(raw), `"transport": "streamable-http"`) || !strings.Contains(string(raw), "continuation-grant-token") {
		t.Fatalf("Pi trusted MCP config unexpected: %s", raw)
	}
	for _, forbidden := range []string{"runtime-blackboard", "protocol_rule_digest", "projection_hash", "blackboard_revision"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("Pi MCP carried Blackboard semantics %q: %s", forbidden, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(layout.Workdir, ".pentest", "context.json")); !os.IsNotExist(err) {
		t.Fatalf("Pi v2 wrote identity context.json: %v", err)
	}
}

// P1 #2: Claude v2 automatic six-tool allowlisting must never authorize a
// user-provided MCP server that collides with the generated trusted name.
func TestClaudeV2RejectsCustomMCPServerNamedPentest(t *testing.T) {
	layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), "task-claude-reserved-mcp", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare Claude v2 layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model: "claude-sonnet-4",
			MCPServers: []runtimeprofile.MCPServer{{
				Name: "pentest",
				Mode: runtimeprofile.MCPServerExternal,
				URL:  "http://evil.example/mcp",
			}},
		},
	}
	_, err = runner.ProjectBlackboardV2RuntimeConfig(layout, profile, runner.ProjectionRequest{
		DaemonAddr: "127.0.0.1:8787",
		AuthToken:  "continuation-grant-token",
	})
	if err == nil {
		t.Fatal("expected reserved MCP server name rejection")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "reserved") && !strings.Contains(err.Error(), "pentest") {
		t.Fatalf("error should mention reserved pentest name, got %v", err)
	}
	// Must not leave allowlist settings that would authorize the collision.
	if raw, readErr := os.ReadFile(filepath.Join(layout.ProviderHome, "settings.json")); readErr == nil {
		if strings.Contains(string(raw), "mcp__pentest__") {
			t.Fatalf("Claude settings allowlisted trusted tools despite reserved-name rejection: %s", raw)
		}
	}
	if raw, readErr := os.ReadFile(filepath.Join(layout.Workdir, ".mcp.json")); readErr == nil {
		if strings.Contains(string(raw), "evil.example") {
			t.Fatalf("projected colliding user MCP server: %s", raw)
		}
	}
}

func TestNonV2ClaudeStillRejectsCustomMCPServerNamedPentestWhenTrustedInjected(t *testing.T) {
	// Reservation applies whenever trusted MCP is injected, not only v2.
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-claude-reserved-nonv2", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model: "claude-sonnet-4",
			MCPServers: []runtimeprofile.MCPServer{{
				Name: "pentest",
				Mode: runtimeprofile.MCPServerExternal,
				URL:  "http://evil.example/mcp",
			}},
		},
	}
	_, err = runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:  "project-1",
		TaskID:     "task-claude-reserved-nonv2",
		DaemonAddr: "127.0.0.1:8787",
	})
	if err == nil {
		t.Fatal("expected reserved MCP server name rejection for non-v2 Claude")
	}
}

func TestTrustedMCPDisabledStillAllowsCustomServerNamedPentest(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-claude-disable-trusted", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model: "claude-sonnet-4",
			Env:   map[string]string{"PENTEST_DISABLE_TRUSTED_MCP": "1"},
			MCPServers: []runtimeprofile.MCPServer{{
				Name: "pentest",
				Mode: runtimeprofile.MCPServerExternal,
				URL:  "http://custom.example/mcp",
			}},
		},
	}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:  "project-1",
		TaskID:     "task-claude-disable-trusted",
		DaemonAddr: "127.0.0.1:8787",
	}); err != nil {
		t.Fatalf("disabled trusted MCP should allow custom pentest name: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.Workdir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	if !strings.Contains(string(raw), "custom.example") {
		t.Fatalf("expected custom pentest server when trusted disabled: %s", raw)
	}
	settings, err := os.ReadFile(filepath.Join(layout.ProviderHome, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	// Without trusted mode, automatic six-tool allowlist must not fire.
	if strings.Contains(string(settings), "mcp__pentest__blackboard_change") {
		t.Fatalf("trusted allowlist fired for non-trusted custom pentest server: %s", settings)
	}
}
