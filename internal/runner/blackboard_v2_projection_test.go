package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/owner"
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
	for _, test := range []struct {
		provider        runtimeprofile.Provider
		instructionFile string
		absent          string
	}{
		{runtimeprofile.ProviderClaudeCode, "CLAUDE.md", "AGENTS.md"},
		{runtimeprofile.ProviderPi, "AGENTS.md", "CLAUDE.md"},
		{runtimeprofile.ProviderHermes, "AGENTS.md", "CLAUDE.md"},
	} {
		t.Run(string(test.provider), func(t *testing.T) {
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-v2-"+string(test.provider), test.provider)
			if err != nil {
				t.Fatalf("prepare layout: %v", err)
			}
			if err := runner.ProjectBlackboardV2Files(layout, test.provider, header, project.Scope{Domains: []string{"example.test"}}); err != nil {
				t.Fatalf("project Blackboard v2 files: %v", err)
			}
			instructions, err := os.ReadFile(filepath.Join(layout.Workdir, test.instructionFile))
			if err != nil {
				t.Fatalf("read %s: %v", test.instructionFile, err)
			}
			if strings.Count(string(instructions), checklist) != 1 {
				t.Fatalf("checklist count = %d, want 1 in %s", strings.Count(string(instructions), checklist), test.instructionFile)
			}
			if _, err := os.Stat(filepath.Join(layout.Workdir, test.absent)); !os.IsNotExist(err) {
				t.Fatalf("expected %s absent so checklist appears once, err=%v", test.absent, err)
			}
			if _, err := os.Stat(filepath.Join(layout.Workdir, ".pentest", "context.json")); !os.IsNotExist(err) {
				t.Fatalf("legacy context.json present: %v", err)
			}
			for _, forbidden := range []string{"project_id", "task_id", "http://", "Trusted tools:", "blackboard_change"} {
				if strings.Contains(strings.ToLower(string(instructions)), strings.ToLower(forbidden)) {
					t.Fatalf("%s leaked %q: %s", test.instructionFile, forbidden, instructions)
				}
			}
		})
	}
}

func TestExternalMCPServerNamedPentestIsPreserved(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-claude-external-pentest", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model: "claude-sonnet-4",
			MCPServers: []runtimeprofile.MCPServer{{
				Name: "pentest", Mode: runtimeprofile.MCPServerExternal, URL: "http://custom.example/mcp",
			}},
		},
	}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		Owner: owner.NewTaskContract("task-claude-external-pentest", "project-1", layout.Workdir),
	}); err != nil {
		t.Fatalf("external pentest MCP should be preserved: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.Workdir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	if !strings.Contains(string(raw), "custom.example") {
		t.Fatalf("external MCP server is missing: %s", raw)
	}
	settings, err := os.ReadFile(filepath.Join(layout.ProviderHome, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if strings.Contains(string(settings), "mcp__pentest__blackboard_") {
		t.Fatalf("retired built-in MCP allowlist leaked: %s", settings)
	}
}
