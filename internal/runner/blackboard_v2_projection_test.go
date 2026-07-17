package runner_test

import (
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
