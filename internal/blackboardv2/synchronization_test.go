package blackboardv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// Issue #117 — parallel Task synchronization through trusted responses.
// These tests pin the service-level notice, delivery, acknowledgement, and
// isolation contracts that HTTP/MCP/CLI adapters reuse.

func TestPeerWriteCreatesCoalescedPendingNoticeWithoutTaskIdentityOrContent(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	peer := launchPeerTask(t, fixture, "peer-creates-notice")

	before, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil {
		t.Fatalf("inspect before peer write: %v", err)
	}
	if before.Pending {
		t.Fatalf("owner should not start pending: %#v", before)
	}
	ownerWorkingBefore, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read owner working before: %v", err)
	}
	ownerDiskBefore, err := os.ReadFile(workingSnapshotPath(fixture.runtimeRoot, fixture.task.ID))
	if err != nil {
		t.Fatalf("read owner disk before: %v", err)
	}

	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, peer.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "peer-notice-1",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:peer-notice", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Peer notice", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("peer write: %v", err)
	}

	// Notice is coalesced revision state only — no Task identity, no changed content.
	after, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil {
		t.Fatalf("inspect after peer write: %v", err)
	}
	if !after.Pending || after.FromRevision != before.Revision || after.Revision <= after.FromRevision {
		t.Fatalf("pending notice = %#v, want pending from %d", after, before.Revision)
	}
	noticeJSON, err := json.Marshal(after)
	if err != nil {
		t.Fatalf("marshal notice: %v", err)
	}
	for _, leak := range []string{
		fixture.task.ID, owner.Continuation.ID, peer.Continuation.ID, peer.Continuation.TaskID,
		"entity:peer-notice", "Peer notice", `"task_id"`, `"continuation_id"`, `"changes"`,
	} {
		if leak != "" && bytes.Contains(noticeJSON, []byte(leak)) {
			t.Fatalf("pending notice leaked %q: %s", leak, noticeJSON)
		}
	}

	// No asynchronous graph injection into Working Snapshot or model-visible file.
	ownerWorkingAfter, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read owner working after: %v", err)
	}
	if ownerWorkingAfter.LastAcknowledgedRevision != ownerWorkingBefore.LastAcknowledgedRevision || !bytes.Equal(ownerWorkingAfter.Bytes, ownerWorkingBefore.Bytes) {
		t.Fatalf("peer write advanced owner Working Snapshot without trusted delivery")
	}
	ownerDiskAfter, err := os.ReadFile(workingSnapshotPath(fixture.runtimeRoot, fixture.task.ID))
	if err != nil {
		t.Fatalf("read owner disk after: %v", err)
	}
	if !bytes.Equal(ownerDiskAfter, ownerDiskBefore) {
		t.Fatalf("peer write asynchronously rewrote owner Working Snapshot file")
	}
}

func TestOwnWriteAndForeignProjectDoNotCreatePendingNotice(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	peer := launchPeerTask(t, fixture, "peer-unaffected-by-own-write")

	foreignProject, err := project.NewService(fixture.db).Create("Foreign sync isolation", "", project.Scope{Domains: []string{"foreign.test"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign Project: %v", err)
	}
	foreignTask, err := fixture.tasks.Create(task.CreateRequest{
		ProjectID: foreignProject.ID, Goal: "foreign work", RuntimeProfileID: fixture.profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreignLaunch, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: foreignProject.ID, TaskID: foreignTask.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex"},
	})
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}

	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "owner-self-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:owner-self", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Owner self", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("owner write: %v", err)
	}
	ownerSync, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil {
		t.Fatalf("inspect owner after own write: %v", err)
	}
	if ownerSync.Pending {
		t.Fatalf("Runtime own write created pending notice for itself: %#v", ownerSync)
	}
	// Peer's notice advances only for same-Project external writes (owner is another Task).
	peerSync, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, peer.Continuation.TaskID, peer.Continuation.ID)
	if err != nil {
		t.Fatalf("inspect peer after owner write: %v", err)
	}
	if !peerSync.Pending {
		t.Fatalf("same-Project peer Task should observe pending notice: %#v", peerSync)
	}

	if _, err := fixture.board.ApplyForContinuation(context.Background(), foreignProject.ID, foreignLaunch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "foreign-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:foreign-only", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Foreign", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("foreign write: %v", err)
	}
	// Foreign Project must not affect owner Project pending state (owner already current).
	ownerAfterForeign, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil {
		t.Fatalf("inspect owner after foreign write: %v", err)
	}
	if ownerAfterForeign.Pending || ownerAfterForeign.Revision != ownerSync.Revision {
		t.Fatalf("foreign Project mutated owner sync state: before=%#v after=%#v", ownerSync, ownerAfterForeign)
	}
	// Owner of the foreign Project is unaffected by same-repo other Project activity.
	foreignSync, err := fixture.board.InspectContinuationSynchronization(context.Background(), foreignProject.ID, foreignTask.ID, foreignLaunch.Continuation.ID)
	if err != nil {
		t.Fatalf("inspect foreign after own write: %v", err)
	}
	if foreignSync.Pending {
		t.Fatalf("foreign Runtime own write created pending notice: %#v", foreignSync)
	}
}

func TestExternalWritesCoalesceIntoOnePendingNotice(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	from := owner.Revision

	for i, key := range []string{"entity:coalesce-a", "entity:coalesce-b", "entity:coalesce-c"} {
		if _, err := fixture.board.Apply(context.Background(), fixture.project.ID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "coalesce-" + key,
			Changes: []blackboardv2.Change{{
				Op: "create", Key: key, Type: "entity",
				Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Coalesce", ScopeStatus: "in_scope"},
			}},
		}); err != nil {
			t.Fatalf("external write %d: %v", i, err)
		}
		sync, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
		if err != nil {
			t.Fatalf("inspect after write %d: %v", i, err)
		}
		// At most one pending notice; from stays at last acknowledged, current advances.
		if !sync.Pending || sync.FromRevision != from || sync.Revision <= from {
			t.Fatalf("coalesced notice after write %d = %#v, want from=%d pending", i, sync, from)
		}
	}
}

func TestSynchronizeContinuationDeliversExactSnapshotAcknowledgesAndClearsNotice(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:sync-deliver", "Sync deliver")
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project expected Snapshot: %v", err)
	}
	pending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending.Pending {
		t.Fatalf("expected pending before delivery: %#v, %v", pending, err)
	}

	attachment, err := fixture.board.SynchronizeContinuation(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending.FromRevision)
	if err != nil {
		t.Fatalf("SynchronizeContinuation: %v", err)
	}
	if attachment.Reason != "another_task_changed_shared_project_knowledge" {
		t.Fatalf("sync reason = %q", attachment.Reason)
	}
	if attachment.FromRevision != pending.FromRevision || attachment.Revision != want.Snapshot.Revision {
		t.Fatalf("sync revisions = from %d revision %d, want from %d revision %d", attachment.FromRevision, attachment.Revision, pending.FromRevision, want.Snapshot.Revision)
	}
	gotSnapshot, err := json.Marshal(attachment.Snapshot)
	if err != nil {
		t.Fatalf("marshal attachment snapshot: %v", err)
	}
	if !bytes.Equal(gotSnapshot, want.Bytes) {
		t.Fatalf("sync Snapshot is not exact canonical bytes\ngot=%s\nwant=%s", gotSnapshot, want.Bytes)
	}
	attachmentJSON, err := json.Marshal(attachment)
	if err != nil {
		t.Fatalf("marshal attachment: %v", err)
	}
	for _, leak := range []string{fixture.task.ID, owner.Continuation.ID, fixture.project.ID, `"task_id"`, `"continuation_id"`, `"project_id"`} {
		if leak != "" && bytes.Contains(attachmentJSON, []byte(leak)) {
			t.Fatalf("sync attachment leaked %q: %s", leak, attachmentJSON)
		}
	}

	working, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read Working Snapshot after delivery: %v", err)
	}
	if working.LastAcknowledgedRevision != want.Snapshot.Revision || !bytes.Equal(working.Bytes, want.Bytes) {
		t.Fatalf("Working Snapshot not replaced/acknowledged: %#v", working)
	}
	onDisk, err := os.ReadFile(workingSnapshotPath(fixture.runtimeRoot, fixture.task.ID))
	if err != nil {
		t.Fatalf("read delivered Working Snapshot file: %v", err)
	}
	if !bytes.Equal(onDisk, want.Bytes) {
		t.Fatalf("disk Working Snapshot is not exact delivered bytes\ngot=%s\nwant=%s", onDisk, want.Bytes)
	}

	cleared, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || cleared.Pending || cleared.FromRevision != cleared.Revision || cleared.Revision != want.Snapshot.Revision {
		t.Fatalf("notice not cleared after delivery: %#v, %v", cleared, err)
	}

	// Later trusted response path sees ordinary state only (no pending piggyback).
	result, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "post-sync-delta",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:post-sync", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Post sync", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("post-sync own write: %v", err)
	}
	afterWrite, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || afterWrite.Pending {
		t.Fatalf("post-sync write should not leave pending notice: %#v, %v", afterWrite, err)
	}
	if result.WorkingSnapshot.Revision != result.Revision || result.WorkingSnapshot.Path != ".pentest/blackboard.json" {
		t.Fatalf("ordinary delta missing working snapshot pointer: %#v", result)
	}
}

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

	// External advance leaves owner pending.
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

	// Peer remains pending for owner's write (external to peer).
	peerSync, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, peer.Continuation.TaskID, peer.Continuation.ID)
	if err != nil || !peerSync.Pending {
		t.Fatalf("peer should still be pending after owner write: %#v, %v", peerSync, err)
	}

	// Finish while pending applies current Project Snapshot and closes without leaving live pending.
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
	// Closed Continuations lose live synchronization authority.
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

	// Resume pins fresh current state and starts with no pending notice.
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

func launchPeerTask(t *testing.T, fixture continuityFixture, goal string) blackboardv2.ContinuationLaunch {
	t.Helper()
	peerTask, err := fixture.tasks.Create(task.CreateRequest{
		ProjectID: fixture.project.ID, Goal: goal, RuntimeProfileID: fixture.profile.ID, Runner: task.RunnerSandbox,
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
	return peer
}

func workingSnapshotPath(runtimeRoot, taskID string) string {
	return filepath.Join(runtimeRoot, taskID, "workdir", ".pentest", "blackboard.json")
}
