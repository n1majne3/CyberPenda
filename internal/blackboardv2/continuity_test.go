package blackboardv2_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
	"pentest/internal/task"
)

type continuityFixture struct {
	db          *store.DB
	dbPath      string
	runtimeRoot string
	board       *blackboardv2.Service
	continuity  *blackboardv2.ContinuityService
	tasks       *task.Service
	project     project.Project
	task        task.Task
	profile     runtimeprofile.Profile
}

func newContinuityFixture(t *testing.T) continuityFixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "continuity.db")
	runtimeRoot := filepath.Join(root, "runs")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	projects := project.NewService(db)
	createdProject, err := projects.Create("Launch continuity", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{})
	if err != nil {
		_ = db.Close()
		t.Fatalf("create Project: %v", err)
	}
	profiles := runtimeprofile.NewService(db)
	profile, err := profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		_ = db.Close()
		t.Fatalf("create Runtime Profile: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "inspect example.test",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		_ = db.Close()
		t.Fatalf("create Task: %v", err)
	}
	board := blackboardv2.NewService(db)
	continuity := blackboardv2.NewContinuityService(db, board, tasks, runtimeRoot)
	return continuityFixture{
		db: db, dbPath: dbPath, runtimeRoot: runtimeRoot, board: board,
		continuity: continuity, tasks: tasks, project: createdProject, task: createdTask, profile: profile,
	}
}

func (f continuityFixture) launch(t *testing.T) blackboardv2.ContinuationLaunch {
	t.Helper()
	launch, err := f.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: f.project.ID, TaskID: f.task.ID, RuntimeProfileID: f.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	return launch
}

func TestContinuationCreationAtomicallyBindsExactCanonicalPinAndAcknowledgedWorkingState(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:launch", "Launch state")
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project expected Snapshot: %v", err)
	}

	launch := fixture.launch(t)
	if !bytes.Equal(launch.Snapshot, want.Bytes) || launch.Schema != "runtime-blackboard/v2" || launch.Revision != want.Snapshot.Revision {
		t.Fatalf("launch Snapshot drifted from exact canonical bytes\ngot=%s\nwant=%s", launch.Snapshot, want.Bytes)
	}
	pin, err := fixture.continuity.ReadLaunchPin(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read Launch Pin: %v", err)
	}
	working, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read Working Snapshot: %v", err)
	}
	if !bytes.Equal(pin.Bytes, want.Bytes) || !bytes.Equal(working.Bytes, pin.Bytes) || working.LastAcknowledgedRevision != pin.Revision {
		t.Fatalf("pin/working state mismatch: pin=%#v working=%#v", pin, working)
	}

	for _, table := range []string{"task_runtime_config_versions", "task_continuations", "blackboard_v2_continuation_pins", "blackboard_v2_continuation_state"} {
		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s rows = %d, want one atomic launch row", table, count)
		}
	}
}

func TestContinuationCreationRollsBackEveryBindingOnInjectedPrecommitCrash(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	fixture.continuity.SetFailureInjector(func(point blackboardv2.ContinuityFailurePoint) error {
		if point == blackboardv2.ContinuityFailureBeforeCommit {
			return errors.New("simulated launch crash")
		}
		return nil
	})

	_, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: fixture.task.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex"},
	})
	if err == nil || !strings.Contains(err.Error(), "simulated launch crash") {
		t.Fatalf("launch crash error = %v", err)
	}
	for _, table := range []string{"task_runtime_config_versions", "task_continuations", "blackboard_v2_continuation_pins", "blackboard_v2_continuation_state"} {
		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d partial rows after crash", table, count)
		}
	}
}

func TestRuntimeAcknowledgedWriteAtomicallyAdvancesOnlyOwningWorkingSnapshot(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}

	peerTask, err := fixture.tasks.Create(task.CreateRequest{
		ProjectID: fixture.project.ID, Goal: "peer task", RuntimeProfileID: fixture.profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create peer Task: %v", err)
	}
	peer, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: peerTask.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex"},
	})
	if err != nil {
		t.Fatalf("create peer Continuation: %v", err)
	}
	peerBefore, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), peer.Continuation.ID)
	if err != nil {
		t.Fatalf("read peer Working Snapshot: %v", err)
	}

	result, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "owner-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:owner-write", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Owner write", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("apply owner write: %v", err)
	}
	ownerWorking, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read owner Working Snapshot: %v", err)
	}
	current, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project current Snapshot: %v", err)
	}
	if result.Revision != current.Snapshot.Revision || ownerWorking.LastAcknowledgedRevision != result.Revision || !bytes.Equal(ownerWorking.Bytes, current.Bytes) {
		t.Fatalf("owner acknowledgement mismatch: result=%#v working=%#v", result, ownerWorking)
	}
	workingPath := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json")
	onDisk, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("read owner Working Snapshot file: %v", err)
	}
	if !bytes.Equal(onDisk, current.Bytes) {
		t.Fatalf("owner Working Snapshot file is not exact acknowledged bytes\ngot=%s\nwant=%s", onDisk, current.Bytes)
	}
	peerAfter, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), peer.Continuation.ID)
	if err != nil {
		t.Fatalf("read peer Working Snapshot after owner write: %v", err)
	}
	if peerAfter.LastAcknowledgedRevision != peerBefore.LastAcknowledgedRevision || !bytes.Equal(peerAfter.Bytes, peerBefore.Bytes) {
		t.Fatalf("owner write advanced peer state: before=%#v after=%#v", peerBefore, peerAfter)
	}

	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "owner-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:owner-write", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Owner write", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("replay owner write: %v", err)
	}
}

func TestLaunchPinIsImmutableAndIntegrityCheckedInternally(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.db.Exec(`UPDATE blackboard_v2_continuation_pins SET snapshot_bytes='tampered' WHERE continuation_id=?`, launch.Continuation.ID); err == nil {
		t.Fatal("immutable Launch Pin accepted an UPDATE")
	}
	if _, err := fixture.db.Exec(`DROP TRIGGER blackboard_v2_continuation_pins_no_update`); err != nil {
		t.Fatalf("drop integrity test guard: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE blackboard_v2_continuation_pins SET snapshot_bytes='tampered' WHERE continuation_id=?`, launch.Continuation.ID); err != nil {
		t.Fatalf("inject persisted corruption: %v", err)
	}
	if _, err := fixture.continuity.ReadLaunchPin(context.Background(), launch.Continuation.ID); !errors.Is(err, blackboardv2.ErrLaunchPinIntegrity) {
		t.Fatalf("corrupt Launch Pin error = %v, want integrity failure", err)
	}
}

func TestCrashRecoveryReusesExactPinWhileResumePinsFreshCurrentState(t *testing.T) {
	fixture := newContinuityFixture(t)
	first := fixture.launch(t)
	firstPin := append([]byte(nil), first.Snapshot...)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), first.Continuation.ID); err != nil {
		_ = fixture.db.Close()
		t.Fatalf("materialize first Working Snapshot: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest")); err != nil {
		_ = fixture.db.Close()
		t.Fatalf("remove projected files for crash: %v", err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close crashed Store: %v", err)
	}

	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen Store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	projects := project.NewService(reopened)
	tasks := task.NewService(reopened, projects)
	board := blackboardv2.NewService(reopened)
	continuity := blackboardv2.NewContinuityService(reopened, board, tasks, fixture.runtimeRoot)
	if err := continuity.RecoverActiveWorkingSnapshots(context.Background()); err != nil {
		t.Fatalf("recover active Working Snapshots: %v", err)
	}
	recovered, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json"))
	if err != nil {
		t.Fatalf("read recovered Working Snapshot: %v", err)
	}
	if !bytes.Equal(recovered, firstPin) {
		t.Fatalf("recovery regenerated rather than reused exact pin\ngot=%s\nwant=%s", recovered, firstPin)
	}

	seedCurrentEntity(t, board, fixture.project.ID, "entity:resume", "Fresh resume state")
	resumed, err := continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: fixture.task.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "resume": true},
	})
	if err != nil {
		t.Fatalf("create resumed Continuation: %v", err)
	}
	current, err := board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project current resume Snapshot: %v", err)
	}
	if bytes.Equal(resumed.Snapshot, firstPin) || !bytes.Equal(resumed.Snapshot, current.Bytes) {
		t.Fatalf("resume did not pin fresh current state\nresume=%s\nfirst=%s\ncurrent=%s", resumed.Snapshot, firstPin, current.Bytes)
	}
}

func seedCurrentEntity(t *testing.T, board *blackboardv2.Service, projectID, key, name string) {
	t.Helper()
	_, err := board.Apply(context.Background(), projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-" + key,
		Changes: []blackboardv2.Change{{
			Op: "create", Key: key, Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: name, ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}
