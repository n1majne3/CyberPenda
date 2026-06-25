package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestPrepareSandboxSkillsLinksWorkdirAndProviderHome(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-1", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if err := runner.PrepareSandboxSkills(layout, runtimeprofile.ProviderClaudeCode); err != nil {
		t.Fatalf("prepare sandbox skills: %v", err)
	}

	workdirLink := filepath.Join(layout.Workdir, ".agents", "skills")
	if target, err := os.Readlink(workdirLink); err != nil || target != "/opt/pentest/skills" {
		t.Fatalf("workdir skills link = %q, err = %v", target, err)
	}
	providerLink := filepath.Join(layout.ProviderHome, "skills")
	if target, err := os.Readlink(providerLink); err != nil || target != "/opt/pentest/skills" {
		t.Fatalf("provider skills link = %q, err = %v", target, err)
	}
}
