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

func TestCTFOrchestratorDeclaresDisabledOnly(t *testing.T) {
	bundle := skill.Bundle{ID: "ctf-orchestrator", Name: "ctf-orchestrator", Path: filepath.Join("..", "..", "skills", "bundles", "ctf-orchestrator")}
	if err := modeskill.ValidateBundleCompatibility(modeskill.ModeDisabled, bundle); err != nil {
		t.Fatalf("disabled compatibility: %v", err)
	}
	if err := modeskill.ValidateBundleCompatibility(modeskill.ModeWorkingGraph, bundle); err == nil {
		t.Fatal("ctf-orchestrator unexpectedly accepted Working Graph mode")
	}
}
