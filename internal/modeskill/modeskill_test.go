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
		modeskill.ModeDisabled:     "cyberpenda-blackboard-disabled",
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
		modeskill.ModeDisabled,
		"ctf-orchestrator",
		"ctf-orchestrator",
	)
	if err != nil {
		t.Fatalf("inject invocation: %v", err)
	}
	modeIndex := strings.Index(goal, "`cyberpenda-blackboard-disabled`")
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
