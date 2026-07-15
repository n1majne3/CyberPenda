package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
	"pentest/internal/task"
)

type committedLaunchFixture struct {
	dbPath       string
	runtimeRoot  string
	db           *store.DB
	tasks        *task.Service
	created      task.Task
	profile      runtimeprofile.Profile
	continuation task.TaskContinuation
}

func newCommittedLaunchFixture(t *testing.T) committedLaunchFixture {
	t.Helper()
	// #99 leaves v2 Runtime launch wiring to #110, so assemble the existing
	// launch coordinator directly while still crossing its real Project
	// Interface transaction, file projection, and file-backed Store seams.
	dbPath := filepath.Join(t.TempDir(), "continuation-launch.db")
	runtimeRoot := filepath.Join(t.TempDir(), "runs")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open fresh v2 store: %v", err)
	}
	projects := project.NewService(db)
	createdProject, err := projects.Create("Committed launch", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		_ = db.Close()
		t.Fatalf("create Project: %v", err)
	}
	profiles := runtimeprofile.NewService(db)
	profile, err := profiles.Create("Fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		_ = db.Close()
		t.Fatalf("create Runtime Profile: %v", err)
	}
	graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	tasks := task.NewService(db, projects)
	tasks.SetGoalProjector(graph)
	created, err := tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "recover committed Runtime files",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		_ = db.Close()
		t.Fatalf("create Task: %v", err)
	}
	grants := projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
	tasks.SetContinuationTerminalMarker(grants)
	projectInterface := projectinterface.NewService(projectinterface.Deps{
		DB: db, Graph: graph, Grants: grants, Tasks: tasks,
	})
	server := &Server{
		db: db, projects: projects, profiles: profiles, tasks: tasks, graph: graph,
		projectInterface: projectInterface, runtimeRoot: runtimeRoot, listenAddr: "127.0.0.1:8787",
	}
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "")
	if err != nil {
		_ = db.Close()
		t.Fatalf("build Task launch plan: %v", err)
	}
	plan.RuntimeConfig["interface_token"] = "projection-only-token"
	plan.RuntimeConfig["launch_command"] = map[string]any{"program": "fake-runtime"}
	plan.RuntimeConfig["layout"] = map[string]any{"workdir": "projection-only"}
	continuation, _, err := server.prepareGraphNativeContinuationLaunch(created, plan, created.Goal)
	if err != nil {
		_ = db.Close()
		t.Fatalf("commit graph-native Continuation launch: %v", err)
	}
	return committedLaunchFixture{
		dbPath: dbPath, runtimeRoot: runtimeRoot, db: db,
		tasks: tasks, created: created, profile: profile, continuation: continuation,
	}
}

func TestCommittedContinuationLaunchPersistsOnlyCapturedRuntimeConfiguration(t *testing.T) {
	fixture := newCommittedLaunchFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })

	versions, err := fixture.tasks.RuntimeConfigVersions(fixture.created.ID)
	if err != nil {
		t.Fatalf("read captured Task Runtime Configuration: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("captured Task Runtime Configuration versions = %d, want 1", len(versions))
	}
	captured := versions[0].Config
	if captured["runtime_profile_id"] != fixture.profile.ID || captured["runtime_plugin_id"] != string(runtimeprofile.ProviderFake) {
		t.Fatalf("captured Task Runtime Configuration identity = %#v", captured)
	}
	for _, projectionOnly := range []string{"interface_token", "launch_command", "layout"} {
		if _, persisted := captured[projectionOnly]; persisted {
			t.Fatalf("captured Task Runtime Configuration persisted projection-only %s", projectionOnly)
		}
	}
	raw, err := json.Marshal(captured)
	if err != nil {
		t.Fatalf("encode captured Task Runtime Configuration: %v", err)
	}
	if bytes.Contains(raw, []byte("projection-only-token")) {
		t.Fatal("captured Task Runtime Configuration persisted the Continuation Interface Grant token")
	}
}

func TestCrashRecoveryRegeneratesCommittedRuntimeFilesWithoutProfileLookupOrRepin(t *testing.T) {
	fixture := newCommittedLaunchFixture(t)
	beforeContinuations, err := fixture.tasks.ActivePinnedContinuations()
	if err != nil {
		_ = fixture.db.Close()
		t.Fatalf("read committed Continuation before crash: %v", err)
	}
	beforeConfigs, err := fixture.tasks.RuntimeConfigVersions(fixture.created.ID)
	if err != nil {
		_ = fixture.db.Close()
		t.Fatalf("read committed Runtime configuration before crash: %v", err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close crashed daemon Store: %v", err)
	}

	workdir := filepath.Join(fixture.runtimeRoot, fixture.created.ID, "workdir")
	if err := os.RemoveAll(filepath.Join(workdir, ".pentest")); err != nil {
		t.Fatalf("remove projected Runtime files: %v", err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err := os.Remove(filepath.Join(workdir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove %s: %v", name, err)
		}
	}

	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen v2 Store after crash: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	profiles := runtimeprofile.NewService(reopened)
	if err := profiles.Delete(fixture.profile.ID); err != nil {
		t.Fatalf("delete live Runtime Profile before recovery: %v", err)
	}
	if _, err := profiles.Get(fixture.profile.ID); !errors.Is(err, runtimeprofile.ErrNotFound) {
		t.Fatalf("deleted Runtime Profile lookup = %v, want not found", err)
	}

	projects := project.NewService(reopened)
	tasks := task.NewService(reopened, projects)
	graph := blackboard.NewGraphService(reopened, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	restarted := &Server{
		db: reopened, projects: projects, tasks: tasks, graph: graph,
		runtimeRoot: fixture.runtimeRoot, listenAddr: "127.0.0.1:8787",
	}
	if err := restarted.recoverPinnedContinuationFiles(); err != nil {
		t.Fatalf("recover committed Continuation files: %v", err)
	}

	ctx, pin := restarted.runtimeBlackboardContext(fixture.created, fixture.continuation)
	if err := blackboard.VerifyCanonicalMainGraphSnapshot(pin, filepath.Join(workdir, filepath.FromSlash(ctx.BlackboardPath))); err != nil {
		t.Fatalf("verify regenerated Launch Blackboard Pin: %v", err)
	}
	contextRaw, err := os.ReadFile(filepath.Join(workdir, ".pentest", "context.json"))
	if err != nil {
		t.Fatalf("read regenerated Runtime context: %v", err)
	}
	var regenerated projectinterface.RuntimeBlackboardContextV1
	if err := json.Unmarshal(contextRaw, &regenerated); err != nil {
		t.Fatalf("decode regenerated Runtime context: %v", err)
	}
	if regenerated.ContinuationID != fixture.continuation.ID || regenerated.RuntimeConfigVersionID != fixture.continuation.RuntimeConfigVersionID || regenerated.RuntimeProfileID != fixture.profile.ID {
		t.Fatalf("regenerated Runtime context drifted from committed Continuation: %#v", regenerated)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		instructions, err := os.ReadFile(filepath.Join(workdir, name))
		if err != nil {
			t.Fatalf("read regenerated %s: %v", name, err)
		}
		text := string(instructions)
		if !strings.Contains(text, fixture.continuation.ID) || !strings.Contains(text, fixture.continuation.BlackboardProjectionHash) {
			t.Fatalf("regenerated %s does not describe committed pin: %s", name, text)
		}
	}

	afterContinuations, err := tasks.ActivePinnedContinuations()
	if err != nil {
		t.Fatalf("read committed Continuation after recovery: %v", err)
	}
	afterConfigs, err := tasks.RuntimeConfigVersions(fixture.created.ID)
	if err != nil {
		t.Fatalf("read Runtime configuration after recovery: %v", err)
	}
	if len(beforeContinuations) != 1 || len(afterContinuations) != 1 || afterContinuations[0].ID != beforeContinuations[0].ID {
		t.Fatalf("recovery repinned Continuation: before=%#v after=%#v", beforeContinuations, afterContinuations)
	}
	if len(beforeConfigs) != 1 || len(afterConfigs) != 1 || afterConfigs[0].ID != beforeConfigs[0].ID {
		t.Fatalf("recovery recaptured Runtime configuration: before=%#v after=%#v", beforeConfigs, afterConfigs)
	}
}
