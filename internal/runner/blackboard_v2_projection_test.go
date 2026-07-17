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
