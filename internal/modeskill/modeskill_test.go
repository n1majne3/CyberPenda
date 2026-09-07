package modeskill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/modeskill"
	"pentest/internal/skill"
)

func TestModeSkillsAreExclusiveSystemBundles(t *testing.T) {
	wants := map[modeskill.Mode]string{
		modeskill.ModeInteractive:  "cyberpenda-blackboard-interactive",
		modeskill.ModeWorkingGraph: "cyberpenda-blackboard-working-graph",
	}
	for mode, wantID := range wants {
		spec, err := modeskill.Resolve(mode)
		if err != nil {
			t.Fatalf("resolve %s: %v", mode, err)
		}
		if spec.ID != wantID || spec.Mode != mode {
			t.Fatalf("mode spec = %#v", spec)
		}
		root := t.TempDir()
		bundle, err := modeskill.Project(root, mode)
		if err != nil {
			t.Fatalf("project %s: %v", mode, err)
		}
		if bundle.ID != wantID || !strings.HasPrefix(bundle.Path, root+string(os.PathSeparator)) {
			t.Fatalf("projected bundle = %#v", bundle)
		}
		if err := skill.ValidateBundle(bundle.Path, skill.Metadata{ID: bundle.ID, Name: bundle.Name}); err != nil {
			t.Fatalf("validate projected bundle: %v", err)
		}
	}
}

// Disabled Blackboard Mode has no Mode Skill. Issue #248 gives a Disabled
// launch only the state-file reminder, so Resolve and Project must refuse it.
func TestDisabledModeHasNoModeSkill(t *testing.T) {
	if _, err := modeskill.Resolve(modeskill.ModeDisabled); err == nil {
		t.Fatal("disabled Blackboard Mode unexpectedly resolved a Mode Skill")
	}
	root := t.TempDir()
	if _, err := modeskill.Project(root, modeskill.ModeDisabled); err == nil {
		t.Fatal("disabled Blackboard Mode unexpectedly projected a Mode Skill")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("disabled projection wrote %d entries into the Skills root", len(entries))
	}
}

func TestCTFOrchestratorBuiltinAcceptsDisabledAndWorkingGraph(t *testing.T) {
	bundle := skill.Bundle{ID: "ctf-orchestrator", Name: "ctf-orchestrator", Path: filepath.Join("..", "..", "internal", "skill", "builtins", "assets", "ctf-orchestrator")}
	for _, mode := range []modeskill.Mode{modeskill.ModeDisabled, modeskill.ModeWorkingGraph} {
		if err := modeskill.ValidateBundleCompatibility(mode, bundle); err != nil {
			t.Fatalf("%s compatibility: %v", mode, err)
		}
	}
	if err := modeskill.ValidateBundleCompatibility(modeskill.ModeInteractive, bundle); err == nil {
		t.Fatal("ctf-orchestrator unexpectedly accepted Interactive mode")
	}
}

func TestInjectInvocationRequiresModeSkillBeforeAdditionalSystemSkills(t *testing.T) {
	goal, err := modeskill.InjectInvocation(
		"solve every eligible challenge",
		modeskill.ModeWorkingGraph,
		"ctf-orchestrator",
		"ctf-orchestrator",
	)
	if err != nil {
		t.Fatalf("inject invocation: %v", err)
	}
	modeIndex := strings.Index(goal, "`cyberpenda-blackboard-working-graph`")
	orchestratorIndex := strings.Index(goal, "`ctf-orchestrator`")
	if modeIndex < 0 || orchestratorIndex < 0 || modeIndex >= orchestratorIndex {
		t.Fatalf("Skill invocation order is not mode-first: %s", goal)
	}
	if strings.Count(goal, "`ctf-orchestrator`") != 1 {
		t.Fatalf("duplicate system Skill invocation was not removed: %s", goal)
	}
	for _, required := range []string{
		"REQUIRED SKILL INVOCATION",
		"invoke and follow these projected Skills in order",
		"TASK GOAL:\nsolve every eligible challenge",
	} {
		if !strings.Contains(goal, required) {
			t.Fatalf("injected goal missing %q: %s", required, goal)
		}
	}
}

// Disabled mode has no Mode Skill, and the state-file reminder lives on the
// owner launch boundary. With no additional system Skills the goal is
// returned unchanged.
func TestInjectInvocationDisabledModeAddsNoSkillInvocation(t *testing.T) {
	goal, err := modeskill.InjectInvocation("inspect the standalone target", modeskill.ModeDisabled)
	if err != nil {
		t.Fatalf("inject invocation: %v", err)
	}
	if goal != "inspect the standalone target" {
		t.Fatalf("Disabled injection changed the goal: %s", goal)
	}
}

// Disabled launches still invoke additional system Skills, such as the hosted
// orchestrator, but never name a Mode Skill.
func TestInjectInvocationDisabledModeStillInvokesAdditionalSystemSkills(t *testing.T) {
	goal, err := modeskill.InjectInvocation(
		"solve every eligible challenge",
		modeskill.ModeDisabled,
		"ctf-orchestrator",
	)
	if err != nil {
		t.Fatalf("inject invocation: %v", err)
	}
	if strings.Contains(goal, "cyberpenda-blackboard-disabled") {
		t.Fatalf("Disabled injection named a Mode Skill: %s", goal)
	}
	for _, required := range []string{
		"REQUIRED SKILL INVOCATION",
		"`ctf-orchestrator`",
		"TASK GOAL:\nsolve every eligible challenge",
	} {
		if !strings.Contains(goal, required) {
			t.Fatalf("injected goal missing %q: %s", required, goal)
		}
	}
}

func TestFGSCompatibilityUnifiesEnabledModesAndKeepsDisabledSeparate(t *testing.T) {
	for _, declared := range []string{"interactive", "working_graph"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: check\nblackboard_modes: ["+declared+"]\n---\n# Check\n"), 0600); err != nil {
			t.Fatal(err)
		}
		bundle := skill.Bundle{ID: "check", Path: root}
		for _, mode := range []modeskill.Mode{modeskill.ModeInteractive, modeskill.ModeWorkingGraph} {
			if err := modeskill.ValidateFGSBundleCompatibility(mode, bundle); err != nil {
				t.Fatalf("%s / %s: %v", declared, mode, err)
			}
		}
		if err := modeskill.ValidateFGSBundleCompatibility(modeskill.ModeDisabled, bundle); err == nil {
			t.Fatal("enabled-only Skill accepted Disabled")
		}
	}
}
