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
	for _, mode := range []modeskill.Mode{modeskill.ModeInteractive, modeskill.ModeWorkingGraph, modeskill.ModeDisabled} {
		spec, _ := modeskill.Resolve(mode)
		_, statErr := os.Stat(filepath.Join(layout.SkillsRoot, spec.ID, "SKILL.md"))
		if mode == modeskill.ModeWorkingGraph && statErr != nil {
			t.Fatalf("selected Mode Skill missing: %v", statErr)
		}
		if mode != modeskill.ModeWorkingGraph && !os.IsNotExist(statErr) {
			t.Fatalf("unselected Mode Skill %s was projected", spec.ID)
		}
	}
	preview, _ := projection.Config["mode_skill"].(map[string]any)
	if preview["id"] != "cyberpenda-blackboard-working-graph" || preview["system_owned"] != true {
		t.Fatalf("Mode Skill preview = %#v", preview)
	}
}
