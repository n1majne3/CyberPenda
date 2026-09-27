package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

// assertSkillsExposure accepts a runtime skill discovery path as either a
// symlink to the Task Skills Root or, when the host denies symlink creation,
// the projected copy of its skill folders.
func assertSkillsExposure(t *testing.T, linkPath, skillsRoot string) {
	t.Helper()
	if target, err := os.Readlink(linkPath); err == nil {
		if target != skillsRoot {
			t.Fatalf("skills link %s = %q, want %q", linkPath, target, skillsRoot)
		}
		return
	}
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		t.Fatalf("read skills root: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(linkPath, entry.Name(), "SKILL.md")); err != nil {
			t.Fatalf("skill %s not exposed at %s: %v", entry.Name(), linkPath, err)
		}
	}
}

func TestProjectRuntimeConfigProjectsEnabledSkills(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "skill-source")
	if err := os.MkdirAll(filepath.Join(sourceDir, "scripts"), 0o700); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte("# Recon"), 0o600); err != nil {
		t.Fatalf("write skill doc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "scripts", "probe.sh"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write skill script: %v", err)
	}
	profile := runtimeprofile.Profile{
		ID:       "profile-1",
		Provider: runtimeprofile.ProviderCodex,
		Fields:   runtimeprofile.Fields{Model: "gpt-5"},
	}
	layout, err := runner.PrepareTaskLayout(root, "task-1", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		SkillBundles: []skill.Bundle{{
			ID:   "recon-helper",
			Name: "Recon Helper",
			Path: sourceDir,
		}},
	})
	if err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	projectedDoc := filepath.Join(layout.SkillsRoot, "recon-helper", "SKILL.md")
	if _, err := os.Stat(projectedDoc); err != nil {
		t.Fatalf("expected skill doc projected to %s: %v", projectedDoc, err)
	}
	assertSkillsExposure(t, filepath.Join(layout.Workdir, ".agents", "skills"), layout.SkillsRoot)
	assertSkillsExposure(t, filepath.Join(layout.ProviderHome, "skills"), layout.SkillsRoot)
	previews, ok := projection.Config["skills"].([]map[string]any)
	if !ok || len(previews) != 1 || previews[0]["id"] != "recon-helper" {
		t.Fatalf("expected skills preview, got %#v", projection.Config["skills"])
	}
}

func TestProjectRuntimeConfigProjectsEnabledSkillsForClaudeCode(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "skill-source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte("# Recon"), 0o600); err != nil {
		t.Fatalf("write skill doc: %v", err)
	}
	profile := runtimeprofile.Profile{
		ID:       "profile-1",
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields:   runtimeprofile.Fields{Model: "claude-sonnet-4"},
	}
	layout, err := runner.PrepareTaskLayout(root, "task-1", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		SkillBundles: []skill.Bundle{{
			ID:   "recon-helper",
			Name: "Recon Helper",
			Path: sourceDir,
		}},
	}); err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	assertSkillsExposure(t, filepath.Join(layout.Workdir, ".claude", "skills"), layout.SkillsRoot)
	if _, err := os.Lstat(filepath.Join(layout.Workdir, ".agents", "skills")); !os.IsNotExist(err) {
		t.Fatalf("expected no .agents/skills link for claude code, err=%v", err)
	}
}

func TestProjectRuntimeConfigProjectsBuiltinSkillsWithSourceFreeFolderNames(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "skill-source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte("# XSS Testing"), 0o600); err != nil {
		t.Fatalf("write skill doc: %v", err)
	}
	profile := runtimeprofile.Profile{
		ID:       "profile-1",
		Provider: runtimeprofile.ProviderCodex,
		Fields:   runtimeprofile.Fields{Model: "gpt-5"},
	}
	layout, err := runner.PrepareTaskLayout(root, "task-1", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		SkillBundles: []skill.Bundle{{
			ID:     "cyberstrikeai-tooling-nmap",
			Name:   "cyberstrikeai-tooling-nmap",
			Source: skill.SourceProvenance{Kind: "builtin"},
			Path:   sourceDir,
		}},
	})
	if err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	projectedDoc := filepath.Join(layout.SkillsRoot, "tooling-nmap", "SKILL.md")
	if _, err := os.Stat(projectedDoc); err != nil {
		t.Fatalf("expected builtin skill doc projected to source-free path %s: %v", projectedDoc, err)
	}
	if _, err := os.Stat(filepath.Join(layout.SkillsRoot, "cyberstrikeai-tooling-nmap")); !os.IsNotExist(err) {
		t.Fatalf("expected no source-prefixed projected skill folder, stat err=%v", err)
	}
	previews, ok := projection.Config["skills"].([]map[string]any)
	if !ok || len(previews) != 1 || previews[0]["id"] != "tooling-nmap" {
		t.Fatalf("expected source-free skills preview, got %#v", projection.Config["skills"])
	}
	if target, _ := previews[0]["target"].(string); filepath.Base(target) != "tooling-nmap" {
		t.Fatalf("expected source-free preview target, got %#v", previews[0])
	}
}
