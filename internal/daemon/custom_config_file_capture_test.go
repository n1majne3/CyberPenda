package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatalf("build launch plan: %v", err)
	}
	settings, _ := plan.CapturedRuntimeConfig["settings"].(map[string]any)
	overlay, ok := settings["custom_config_file"].(string)
	if !ok || overlay == "" {
		t.Fatalf("captured Snapshot must carry the custom config file overlay, got %#v", settings["custom_config_file"])
	}
	if overlay != profile.Fields.CustomConfigFile {
		t.Fatalf("captured overlay = %q, want %q", overlay, profile.Fields.CustomConfigFile)
	}
}

// Story 18: launch overlay A, then mutate the profile to B, then resume.
// The continuation projection must still use A, not the mutated B.
func TestContinuationReproducesCapturedCustomConfigFile(t *testing.T) {
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
	overlayA := `{"enabledPlugins":{"warp@claude-code-warp":true}}`
	overlayB := `{"enabledPlugins":{"other@elsewhere":true}}`
	profile, err := server.profiles.Create("Claude Overlay", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Model: "claude-test", SandboxImage: "cyberpenda:test",
		CustomConfigFile: overlayA,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	first, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatalf("first launch plan: %v", err)
	}
	firstSettings, _ := first.CapturedRuntimeConfig["settings"].(map[string]any)
	if got, _ := firstSettings["custom_config_file"].(string); got != overlayA {
		t.Fatalf("first capture = %q, want overlay A", got)
	}
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, first.CapturedRuntimeConfig); err != nil {
		t.Fatalf("record first runtime config: %v", err)
	}

	updated := profile.Fields
	updated.CustomConfigFile = overlayB
	if _, err := server.profiles.Update(profile.ID, "", "", updated, true, false); err != nil {
		t.Fatalf("mutate profile overlay: %v", err)
	}

	second, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatalf("resume launch plan: %v", err)
	}
	got, _ := second.ResolvedProfile.Fields.CustomConfigFile, true
	if got != overlayA {
		t.Fatalf("resume profile overlay = %q, want captured overlay A (not mutated B)", got)
	}
	if !strings.Contains(got, "warp@claude-code-warp") {
		t.Fatalf("resume overlay must still contain warp plugin, got %q", got)
	}
}

// Runtime Profile provenance is immutable after Owner creation.
func TestExplicitProfileSwitchIsLocked(t *testing.T) {
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
	overlayA := `{"enabledPlugins":{"warp@claude-code-warp":true}}`
	overlayB := `{"enabledPlugins":{"other@elsewhere":true}}`
	profileA, err := server.profiles.Create("Claude A", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Model: "claude-test", SandboxImage: "cyberpenda:test", CustomConfigFile: overlayA,
	})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	profileB, err := server.profiles.Create("Claude B", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Model: "claude-test", SandboxImage: "cyberpenda:test", CustomConfigFile: overlayB,
	})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect example.com",
		RuntimeProfileID: profileA.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profileA, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	first, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatalf("first launch plan: %v", err)
	}
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profileA.ID, first.CapturedRuntimeConfig); err != nil {
		t.Fatalf("record first: %v", err)
	}

	rec := httptest.NewRecorder()
	_, ok := server.recordSelectedRuntimeConfig(rec, created, "", taskContinuationSelectionInput{
		RuntimeProfileID: profileB.ID,
	})
	if ok || rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "runtime_profile_locked") {
		t.Fatalf("profile switch status=%d ok=%v body=%s", rec.Code, ok, rec.Body.String())
	}
}
