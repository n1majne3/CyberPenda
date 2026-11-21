package blackboardv2_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
	"pentest/internal/task"
)

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
	recoverActiveWorkingSnapshots(t, continuity)
	recovered, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json"))
	if err != nil {
		t.Fatalf("read recovered Working Snapshot: %v", err)
	}
	if !bytes.Equal(recovered, firstPin) {
		t.Fatalf("recovery regenerated rather than reused exact pin\ngot=%s\nwant=%s", recovered, firstPin)
	}

	seedCurrentEntity(t, board, fixture.project.ID, "entity:resume", "Fresh resume state")
	tasks.SetContinuationReconciler(board)
	if _, err := tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("close recovered Continuation before resume: %v", err)
	}
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

func TestRecoveryMaterializesPersistedWorkingSnapshotNotLaunchPin(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), launch.Continuation.ID); err != nil {
		t.Fatalf("materialize initial Snapshot: %v", err)
	}
	_, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "advance-before-recovery",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:advanced-working", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Mutable working state", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("advance mutable Working Snapshot: %v", err)
	}
	working, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read advanced Working Snapshot: %v", err)
	}
	if bytes.Equal(working.Bytes, launch.Snapshot) {
		t.Fatal("test did not advance mutable Working Snapshot beyond Launch Pin")
	}
	pin, err := fixture.continuity.ReadLaunchPin(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read Launch Pin before recovery: %v", err)
	}
	if !bytes.Equal(pin.Bytes, launch.Snapshot) {
		t.Fatalf("Launch Pin drifted before recovery")
	}
	path := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove Working Snapshot before recovery: %v", err)
	}
	recoverActiveWorkingSnapshots(t, fixture.continuity)
	recovered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered Snapshot: %v", err)
	}
	// Recovery must rematerialize persisted working_snapshot_bytes, not overwrite
	// with immutable Launch Pin bytes after a successful Working Snapshot advance.
	if !bytes.Equal(recovered, working.Bytes) {
		t.Fatalf("recovery did not restore exact Working Snapshot\ngot=%s\nwant=%s\npin=%s", recovered, working.Bytes, launch.Snapshot)
	}
	pinAfter, err := fixture.continuity.ReadLaunchPin(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read Launch Pin after recovery: %v", err)
	}
	if !bytes.Equal(pinAfter.Bytes, launch.Snapshot) {
		t.Fatalf("recovery mutated immutable Launch Pin")
	}
}

func TestRecoveryAfterSynchronizationRestoresExactWorkingSnapshotBytes(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:restart-after-sync", "Restart after sync")
	pending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending.Pending {
		t.Fatalf("expected pending before sync delivery: %#v, %v", pending, err)
	}
	attachment, err := fixture.board.SynchronizeContinuation(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending.FromRevision)
	if err != nil {
		t.Fatalf("SynchronizeContinuation: %v", err)
	}
	want, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read synchronized Working Snapshot: %v", err)
	}
	if want.LastAcknowledgedRevision != attachment.Revision || bytes.Equal(want.Bytes, owner.Snapshot) {
		t.Fatalf("synchronized Working Snapshot not advanced: %#v pin=%s", want, owner.Snapshot)
	}
	path := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove Working Snapshot before restart recovery: %v", err)
	}
	recoverActiveWorkingSnapshots(t, fixture.continuity)
	recovered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered post-sync Working Snapshot: %v", err)
	}
	if !bytes.Equal(recovered, want.Bytes) {
		t.Fatalf("restart recovery lost synchronized Working Snapshot\ngot=%s\nwant=%s\npin=%s", recovered, want.Bytes, owner.Snapshot)
	}
	pin, err := fixture.continuity.ReadLaunchPin(context.Background(), owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read Launch Pin after post-sync recovery: %v", err)
	}
	if !bytes.Equal(pin.Bytes, owner.Snapshot) {
		t.Fatalf("post-sync recovery mutated immutable Launch Pin")
	}
}

func TestRecoveryDoesNotProjectCorruptLaunchPinAndStillRestoresWorkingSnapshot(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	path := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json")
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), launch.Continuation.ID); err != nil {
		t.Fatalf("materialize initial Snapshot: %v", err)
	}
	working, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read Working Snapshot: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove projected Snapshot: %v", err)
	}
	if _, err := fixture.db.Exec(`DROP TRIGGER blackboard_v2_continuation_pins_no_update`); err != nil {
		t.Fatalf("drop immutable guard for corruption injection: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE blackboard_v2_continuation_pins SET snapshot_bytes=? WHERE continuation_id=?`, []byte(`{"schema":"runtime-blackboard/v2","revision":999}`), launch.Continuation.ID); err != nil {
		t.Fatalf("inject corrupt Launch Pin: %v", err)
	}
	// Recovery projects Working Snapshot state, not Launch Pin bytes. Corrupt pin
	// remains rejected by explicit pin reads while restart recovery stays available.
	recoverActiveWorkingSnapshots(t, fixture.continuity)
	recovered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered Working Snapshot: %v", err)
	}
	if !bytes.Equal(recovered, working.Bytes) {
		t.Fatalf("recovery did not restore Working Snapshot with corrupt pin present\ngot=%s\nwant=%s", recovered, working.Bytes)
	}
	if _, err := fixture.continuity.ReadLaunchPin(context.Background(), launch.Continuation.ID); !errors.Is(err, blackboardv2.ErrLaunchPinIntegrity) {
		t.Fatalf("corrupt Launch Pin read error = %v, want integrity failure", err)
	}
}

func TestClosedContinuationReplayCannotOverwriteNewerContinuationWorkingSnapshot(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	c1 := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), c1.Continuation.ID); err != nil {
		t.Fatalf("materialize C1: %v", err)
	}
	batch := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "c1-durable-replay",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:c1", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "C1 state", ScopeStatus: "in_scope"},
		}},
	}
	c1Result, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, c1.Continuation.ID, batch)
	if err != nil {
		t.Fatalf("apply C1 write: %v", err)
	}
	if _, err := fixture.tasks.UpdateContinuationStatus(c1.Continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("close C1: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:between", "State before C2")
	c2 := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), c2.Continuation.ID); err != nil {
		t.Fatalf("materialize C2: %v", err)
	}
	path := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json")
	c2Bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read C2 Working Snapshot: %v", err)
	}

	replay, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, c1.Continuation.ID, batch)
	if err != nil || replay.Revision != c1Result.Revision {
		t.Fatalf("closed C1 exact replay = %#v, %v; want durable result %#v", replay, err, c1Result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Task Working Snapshot after C1 replay: %v", err)
	}
	if !bytes.Equal(after, c2Bytes) || !bytes.Equal(after, c2.Snapshot) {
		t.Fatalf("closed C1 replay overwrote C2 Working Snapshot\nafter=%s\nC2=%s\nC1=%s", after, c2Bytes, c1.Snapshot)
	}
}

// recoverActiveWorkingSnapshots mirrors the daemon restart path
// (Server.recoverBlackboardV2ContinuationFiles): rematerialize every active
// Continuation's persisted Working Snapshot via ActiveSnapshots +
// MaterializeWorkingSnapshot.
func recoverActiveWorkingSnapshots(t *testing.T, continuity *blackboardv2.ContinuityService) {
	t.Helper()
	active, err := continuity.ActiveSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list active Snapshots: %v", err)
	}
	for _, snapshot := range active {
		if err := continuity.MaterializeWorkingSnapshot(context.Background(), snapshot.ContinuationID); err != nil {
			t.Fatalf("recover Continuation Working Snapshot: %v", err)
		}
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
