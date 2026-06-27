package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestSkillsWorkdirRelPath(t *testing.T) {
	if got := runner.SkillsWorkdirRelPath(runtimeprofile.ProviderClaudeCode); got != ".claude/skills" {
		t.Fatalf("claude skills path = %q", got)
	}
	if got := runner.SkillsWorkdirRelPath(runtimeprofile.ProviderCodex); got != ".agents/skills" {
		t.Fatalf("codex skills path = %q", got)
	}
}

func TestPrepareSandboxSkillsLinksClaudeWorkdirAndProviderHome(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-1", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if err := runner.PrepareSandboxSkills(layout, runtimeprofile.ProviderClaudeCode); err != nil {
		t.Fatalf("prepare sandbox skills: %v", err)
	}

	workdirLink := filepath.Join(layout.Workdir, ".claude", "skills")
	if target, err := os.Readlink(workdirLink); err != nil || target != "/opt/pentest/skills" {
		t.Fatalf("claude workdir skills link = %q, err = %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Workdir, ".agents", "skills")); !os.IsNotExist(err) {
		t.Fatalf("expected no .agents/skills link for claude code, err=%v", err)
	}
	providerLink := filepath.Join(layout.ProviderHome, "skills")
	if target, err := os.Readlink(providerLink); err != nil || target != "/opt/pentest/skills" {
		t.Fatalf("provider skills link = %q, err = %v", target, err)
	}
}

func TestPrepareSandboxSkillsLinksCodexWorkdirAndProviderHome(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-1", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if err := runner.PrepareSandboxSkills(layout, runtimeprofile.ProviderCodex); err != nil {
		t.Fatalf("prepare sandbox skills: %v", err)
	}

	workdirLink := filepath.Join(layout.Workdir, ".agents", "skills")
	if target, err := os.Readlink(workdirLink); err != nil || target != "/opt/pentest/skills" {
		t.Fatalf("codex workdir skills link = %q, err = %v", target, err)
	}
}
