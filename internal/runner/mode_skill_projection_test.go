package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/modeskill"
	"pentest/internal/owner"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestProjectRuntimeConfigProjectsExactlyOneSystemModeSkill(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-mode-skill", runtimeprofile.ProviderFake)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderFake}, runner.ProjectionRequest{
		Owner: owner.NewTaskContract("task-mode-skill", "project-1", layout.Workdir), BlackboardMode: modeskill.ModeWorkingGraph,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, skillID := range []string{"cyberpenda-blackboard-interactive", "cyberpenda-blackboard-working-graph"} {
		_, statErr := os.Stat(filepath.Join(layout.SkillsRoot, skillID, "SKILL.md"))
		if skillID == "cyberpenda-blackboard-working-graph" && statErr != nil {
			t.Fatalf("selected Mode Skill missing: %v", statErr)
		}
		if skillID != "cyberpenda-blackboard-working-graph" && !os.IsNotExist(statErr) {
			t.Fatalf("unselected Mode Skill %s was projected", skillID)
		}
	}
	preview, _ := projection.Config["mode_skill"].(map[string]any)
	if preview["id"] != "cyberpenda-blackboard-working-graph" || preview["system_owned"] != true {
		t.Fatalf("Mode Skill preview = %#v", preview)
	}
}

// Disabled Blackboard Mode has no Mode Skill: the projection must succeed
// without a Mode Skill directory or a Mode Skill preview.
func TestProjectRuntimeConfigDisabledModeProjectsNoModeSkill(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-disabled-mode", runtimeprofile.ProviderFake)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderFake}, runner.ProjectionRequest{
		Owner: owner.NewTaskContract("task-disabled-mode", "project-1", layout.Workdir), BlackboardMode: modeskill.ModeDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, skillID := range []string{
		"cyberpenda-blackboard-interactive",
		"cyberpenda-blackboard-working-graph",
		"cyberpenda-blackboard-disabled",
	} {
		if _, statErr := os.Stat(filepath.Join(layout.SkillsRoot, skillID)); !os.IsNotExist(statErr) {
			t.Fatalf("Disabled projection wrote Mode Skill %s: %v", skillID, statErr)
		}
	}
	if _, has := projection.Config["mode_skill"]; has {
		t.Fatalf("Disabled projection kept a Mode Skill preview: %#v", projection.Config["mode_skill"])
	}
}
