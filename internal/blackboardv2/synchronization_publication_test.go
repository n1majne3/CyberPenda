package blackboardv2_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func TestSynchronizeContinuationPublicationFailureIsRetrySafe(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:sync-retry", "Sync retry")
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project expected Snapshot: %v", err)
	}
	pending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending.Pending {
		t.Fatalf("expected pending before delivery: %#v, %v", pending, err)
	}

	// Simulate lost response / crash after durable acknowledgement by deleting the
	// published Working Snapshot file, then prove a retry redelivers and republishes.
	attachment, err := fixture.board.SynchronizeContinuation(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending.FromRevision)
	if err != nil {
		t.Fatalf("first SynchronizeContinuation: %v", err)
	}
	path := workingSnapshotPath(fixture.runtimeRoot, fixture.task.ID)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove Working Snapshot to simulate publication loss: %v", err)
	}
	// Pending is cleared after acknowledgement; retry of SynchronizeContinuation must
	// still be safe (replay-safe delivery of exact acknowledged bytes to disk).
	cleared, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || cleared.Pending {
		t.Fatalf("after first delivery pending should clear: %#v, %v", cleared, err)
	}
	retry, err := fixture.board.SynchronizeContinuation(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending.FromRevision)
	if err != nil {
		t.Fatalf("retry SynchronizeContinuation after publication loss: %v", err)
	}
	if retry.Revision != attachment.Revision || retry.Reason != attachment.Reason {
		t.Fatalf("retry attachment drifted: first=%#v retry=%#v", attachment, retry)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered Working Snapshot: %v", err)
	}
	if !bytes.Equal(onDisk, want.Bytes) {
		t.Fatalf("retry did not republish exact Snapshot\ngot=%s\nwant=%s", onDisk, want.Bytes)
	}
	working, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil || working.LastAcknowledgedRevision != want.Snapshot.Revision || !bytes.Equal(working.Bytes, want.Bytes) {
		t.Fatalf("acknowledged state after retry = %#v, %v", working, err)
	}
}

func TestSynchronizeContinuationRacesWithOwnWriteFinishAndIsolation(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	peer := launchPeerTask(t, fixture, "race-peer")
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), peer.Continuation.ID); err != nil {
		t.Fatalf("materialize peer Working Snapshot: %v", err)
	}

	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, peer.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "race-peer-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:race-peer", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Race peer", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	// Own write while pending absorbs current Project state and clears notice without
	// requiring a separate sync attachment for the Runtime's own acknowledgement.
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "race-owner-absorb",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:race-owner", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Race owner", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("owner write while pending: %v", err)
	}
	ownerSync, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || ownerSync.Pending {
		t.Fatalf("own write should acknowledge absorbed external state: %#v, %v", ownerSync, err)
	}
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project current: %v", err)
	}
	ownerWorking, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil || !bytes.Equal(ownerWorking.Bytes, want.Bytes) {
		t.Fatalf("owner working after absorb = %#v, %v", ownerWorking, err)
	}

	peerSync, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, peer.Continuation.TaskID, peer.Continuation.ID)
	if err != nil || !peerSync.Pending {
		t.Fatalf("peer should still be pending after owner write: %#v, %v", peerSync, err)
	}

	if _, err := fixture.tasks.UpdateContinuationStatus(peer.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start peer: %v", err)
	}
	finished, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, peer.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-pending-peer"})
	if err != nil {
		t.Fatalf("Finish pending peer: %v", err)
	}
	if finished.Revision != want.Snapshot.Revision {
		t.Fatalf("Finish revision = %d, want %d", finished.Revision, want.Snapshot.Revision)
	}
	if _, err := fixture.board.SynchronizeContinuation(context.Background(), fixture.project.ID, peer.Continuation.TaskID, peer.Continuation.ID, peerSync.FromRevision); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("SynchronizeContinuation on finished Continuation = %v", err)
	}
	authority, err := fixture.board.AuthorizeContinuationBinding(context.Background(), fixture.project.ID, peer.Continuation.TaskID, peer.Continuation.ID, false)
	if err != nil {
		t.Fatalf("authorize closed peer: %v", err)
	}
	if authority.Live || authority.Sync.Pending {
		t.Fatalf("closed peer retained live pending sync: %#v", authority)
	}

	resumed, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: peer.Continuation.TaskID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "resume": true},
	})
	if err != nil {
		t.Fatalf("resume peer: %v", err)
	}
	resumedSync, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, peer.Continuation.TaskID, resumed.Continuation.ID)
	if err != nil || resumedSync.Pending || resumedSync.Revision != want.Snapshot.Revision {
		t.Fatalf("resume sync state = %#v, %v", resumedSync, err)
	}
	if !bytes.Equal(resumed.Snapshot, want.Bytes) {
		t.Fatalf("resume Snapshot not exact current\ngot=%s\nwant=%s", resumed.Snapshot, want.Bytes)
	}
}

func TestSynchronizeContinuationRejectsForeignProjectAndNegativeFromRevision(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:auth-sync", "Auth sync")
	pending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if _, err := fixture.board.SynchronizeContinuation(context.Background(), "missing-project", owner.Continuation.TaskID, owner.Continuation.ID, pending.FromRevision); err == nil {
		t.Fatal("foreign Project accepted synchronization")
	}
	if _, err := fixture.board.SynchronizeContinuation(context.Background(), fixture.project.ID, "missing-task", owner.Continuation.ID, pending.FromRevision); err == nil {
		t.Fatal("foreign Task accepted synchronization")
	}
	if _, err := fixture.board.SynchronizeContinuation(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, -1); err == nil {
		t.Fatal("negative from_revision accepted")
	}
}
