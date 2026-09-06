package runner_test

import (
	"os"
	"path/filepath"
	"pentest/internal/blackboardv2"
	"pentest/internal/modeskill"
	"pentest/internal/owner"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"strings"
	"testing"
)

func TestFGSProjectionReplacesGeneratedLegacyChecklist(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "upgraded", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modeskill.Project(layout.SkillsRoot, modeskill.ModeWorkingGraph); err != nil {
		t.Fatal(err)
	}
	legacy := "# Blackboard workflow\n\n" + blackboardv2.CodexChecklist() + "\n"
	if err := os.WriteFile(filepath.Join(layout.Workdir, "AGENTS.md"), []byte(legacy+"Operator note: keep this.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}, runner.ProjectionRequest{
		Owner: owner.NewTaskContract("upgraded", "project", layout.Workdir), BlackboardMode: modeskill.ModeWorkingGraph, BlackboardProtocol: "fgs",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.Workdir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), legacy) || !strings.Contains(string(raw), "Operator note: keep this.") || !strings.Contains(string(raw), "## FGS work protocol") {
		t.Fatalf("upgraded instructions: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(layout.SkillsRoot, "cyberpenda-blackboard-working-graph", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("upgraded Runtime can still discover the legacy Mode Skill")
	}
}

func TestFGSProjectionPreservesUserInstructionsAcrossResume(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-fgs", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err = os.WriteFile(filepath.Join(layout.Workdir, name), []byte("User instruction: keep notes.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	req := runner.ProjectionRequest{Owner: owner.NewTaskContract("task-fgs", "project-1", layout.Workdir), BlackboardMode: modeskill.ModeWorkingGraph, BlackboardProtocol: "fgs"}
	for i := 0; i < 2; i++ {
		if _, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}, req); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		raw, err := os.ReadFile(filepath.Join(layout.Workdir, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.HasPrefix(text, "User instruction: keep notes.\n") || strings.Count(text, "<!-- cyberpenda:fgs:start -->") != 1 || !strings.Contains(text, "pentestctl working-graph emit") {
			t.Fatalf("instructions: %s", text)
		}
	}
	spec, _ := modeskill.Resolve(modeskill.ModeWorkingGraph)
	if _, err = os.Stat(filepath.Join(layout.SkillsRoot, spec.ID)); !os.IsNotExist(err) {
		t.Fatal("FGS projected legacy Mode Skill")
	}
}
