package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

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
	if target, err := os.Readlink(filepath.Join(layout.Workdir, ".agents", "skills")); err != nil || target != layout.SkillsRoot {
		t.Fatalf("workdir skills link = %q, err = %v", target, err)
	}
	if target, err := os.Readlink(filepath.Join(layout.ProviderHome, "skills")); err != nil || target != layout.SkillsRoot {
		t.Fatalf("provider skills link = %q, err = %v", target, err)
	}
	previews, ok := projection.Config["skills"].([]map[string]any)
	if !ok || len(previews) != 1 || previews[0]["id"] != "recon-helper" {
		t.Fatalf("expected skills preview, got %#v", projection.Config["skills"])
	}
}

func TestProjectRuntimeConfigProjectsBuiltinSkillsWithSourceFreeFolderNames(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "skill-source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte("# API Security Testing"), 0o600); err != nil {
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
			ID:     "cyberstrikeai-api-security-testing",
			Name:   "cyberstrikeai-api-security-testing",
			Source: skill.SourceProvenance{Kind: "builtin"},
			Path:   sourceDir,
		}},
	})
	if err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	projectedDoc := filepath.Join(layout.SkillsRoot, "api-security-testing", "SKILL.md")
	if _, err := os.Stat(projectedDoc); err != nil {
		t.Fatalf("expected builtin skill doc projected to source-free path %s: %v", projectedDoc, err)
	}
	if _, err := os.Stat(filepath.Join(layout.SkillsRoot, "cyberstrikeai-api-security-testing")); !os.IsNotExist(err) {
		t.Fatalf("expected no source-prefixed projected skill folder, stat err=%v", err)
	}
	previews, ok := projection.Config["skills"].([]map[string]any)
	if !ok || len(previews) != 1 || previews[0]["id"] != "api-security-testing" {
		t.Fatalf("expected source-free skills preview, got %#v", projection.Config["skills"])
	}
	if target, _ := previews[0]["target"].(string); filepath.Base(target) != "api-security-testing" {
		t.Fatalf("expected source-free preview target, got %#v", previews[0])
	}
}
