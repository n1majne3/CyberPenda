package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/skill"
	"pentest/internal/store"
	"pentest/internal/task"
)

// Historical Runtime Configuration Snapshots keep the skill IDs that were
// enabled at launch. Retired builtins must not block task or session
// continuation: the resolver skips IDs that no longer resolve and keeps
// projecting the surviving skills.
func TestRuntimeSnapshotSkillBundlesSkipsRetiredSkills(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	skills := skill.NewService(db, filepath.Join(t.TempDir(), "skills"))
	if _, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "tooling-nmap", Name: "nmap"},
		Files:    map[string]string{"SKILL.md": "# nmap"},
	}); err != nil {
		t.Fatalf("publish skill: %v", err)
	}

	server := &Server{skills: skills}
	bundles, err := server.runtimeSnapshotSkillBundles(runtimeconfig.RuntimeConfigurationSnapshot{
		EnabledSkillIDs: []string{"tooling-nmap", "reverse-skill-router", "vulnerabilities-xss"},
	})
	if err != nil {
		t.Fatalf("resolve snapshot bundles: %v", err)
	}
	if len(bundles) != 1 || bundles[0].ID != "tooling-nmap" {
		t.Fatalf("expected only the surviving skill bundle, got %#v", bundles)
	}
}

func TestPrepareSessionRuntimeSkipsRetiredSnapshotSkills(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot: filepath.Join(t.TempDir(), "runs"), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	provider := createSnapshotTestModelProvider(t, server, "gpt-session")
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/true",
		ModelProviderID: provider.ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	snapshot, err := decodeRuntimeSnapshot(testSessionProfileRuntimeSnapshot(t, server, profile, session.RunnerHost))
	if err != nil {
		t.Fatalf("decode Runtime Configuration Snapshot: %v", err)
	}
	snapshot.EnabledSkillIDs = append(snapshot.EnabledSkillIDs, "retired-builtin")
	created, err := server.sessions.Create(session.CreateRequest{
		Input: "resume after Built-in Skill pruning",
		InitialRuntime: &session.CreateContinuationRequest{
			RuntimeProfileID: profile.ID, RuntimeProvider: string(profile.Provider),
			Runner: session.RunnerHost, RuntimeConfig: runtimeSnapshotMap(snapshot),
		},
	})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	previous, err := server.sessions.LatestContinuation(created.ID)
	if err != nil || previous == nil {
		t.Fatalf("read previous Continuation: %#v, %v", previous, err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(previous.ID, session.RuntimeStatusInterrupted); err != nil {
		t.Fatalf("interrupt previous Continuation: %v", err)
	}

	prepared, err := server.prepareSessionRuntime(
		context.Background(),
		session.BlackboardModeInteractive,
		sessionRuntimeInput{HostActivated: true},
		previous,
	)
	if err != nil {
		t.Fatalf("prepare Session Runtime with retired captured Skill: %v", err)
	}
	resumed, err := decodeRuntimeSnapshot(prepared.RuntimeConfig)
	if err != nil {
		t.Fatalf("decode resumed Runtime Configuration Snapshot: %v", err)
	}
	if len(resumed.EnabledSkillIDs) != len(snapshot.EnabledSkillIDs) {
		t.Fatalf("resume mutated immutable captured Skill IDs: got %v want %v", resumed.EnabledSkillIDs, snapshot.EnabledSkillIDs)
	}
}

func TestPrepareTaskResumeSkipsRetiredSnapshotSkills(t *testing.T) {
	server, seed, _ := newFinishTaskFixture(t, nil)
	versions, err := server.tasks.RuntimeConfigVersions(seed.ID)
	if err != nil || len(versions) == 0 {
		t.Fatalf("read seed Runtime Configuration Snapshot: %v", err)
	}
	snapshot, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		t.Fatalf("decode seed Runtime Configuration Snapshot: %v", err)
	}
	snapshot.EnabledSkillIDs = append(snapshot.EnabledSkillIDs, "retired-builtin")

	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: seed.ProjectID, Type: task.TypePentest,
		Goal:             "resume Task after Built-in Skill pruning",
		RuntimeProfileID: seed.RuntimeProfileID, Runner: task.RunnerSandbox,
		RunControls:   task.RunControls{BlackboardMode: task.BlackboardModeDisabled},
		RuntimeConfig: runtimeSnapshotMap(snapshot),
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt Task: %v", err)
	}
	previous, err := server.tasks.CreateContinuation(
		created.ID, created.RuntimeProfileID, snapshot.RuntimePluginID, task.RunnerSandbox,
	)
	if err != nil {
		t.Fatalf("create previous Continuation: %v", err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(previous.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt previous Continuation: %v", err)
	}

	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatalf("read interrupted Task: %v", err)
	}
	if _, _, _, err := server.prepareResumeContinuation(found, ""); err != nil {
		t.Fatalf("prepare Task Resume with retired captured Skill: %v", err)
	}
}
