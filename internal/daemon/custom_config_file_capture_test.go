package daemon

import (
	"path/filepath"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// Story 18 of issue #226: the imported overlay must be captured into the
// Task Runtime Configuration at launch so continuations reproduce the same
// effective config.
func TestLaunchCapturesCustomConfigFileOverlay(t *testing.T) {
	factory := &effortProviderSessionFactory{}
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	profile, err := server.profiles.Create("Claude Overlay", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Model: "claude-test", SandboxImage: "cyberpenda:test",
		CustomConfigFile: `{"enabledPlugins":{"warp@claude-code-warp":true}}`,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatalf("build launch plan: %v", err)
	}
	overlay, ok := plan.CapturedRuntimeConfig["custom_config_file"].(string)
	if !ok || overlay == "" {
		t.Fatalf("captured runtime config must carry the custom config file overlay, got %#v", plan.CapturedRuntimeConfig["custom_config_file"])
	}
	if overlay != profile.Fields.CustomConfigFile {
		t.Fatalf("captured overlay = %q, want %q", overlay, profile.Fields.CustomConfigFile)
	}
}
