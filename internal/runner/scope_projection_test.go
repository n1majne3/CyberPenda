package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestProjectRuntimeConfigWritesScopeSnapshot(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-scope", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	snapshot := project.Scope{
		Domains:       []string{"example.com"},
		URLs:          []string{"https://example.com/admin"},
		TestingLimits: []string{"no destructive payloads"},
		Notes:         "business hours only",
	}

	_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields:   runtimeprofile.Fields{Model: "claude-sonnet-4"},
	}, runner.ProjectionRequest{
		ProjectID:     "project-1",
		TaskID:        "task-scope",
		DaemonAddr:    "127.0.0.1:8787",
		Sandbox:       true,
		ScopeSnapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	scopePath := filepath.Join(layout.Workdir, ".pentest", "scope.json")
	raw, err := os.ReadFile(scopePath)
	if err != nil {
		t.Fatalf("read scope.json: %v", err)
	}
	var got project.Scope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode scope.json: %v", err)
	}
	if len(got.Domains) != 1 || got.Domains[0] != "example.com" {
		t.Fatalf("unexpected domains: %#v", got.Domains)
	}
	if got.Notes != "business hours only" {
		t.Fatalf("unexpected notes: %q", got.Notes)
	}

	agents, err := os.ReadFile(filepath.Join(layout.Workdir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "scope.json") {
		t.Fatalf("expected AGENTS.md to mention scope.json, got:\n%s", agents)
	}
}