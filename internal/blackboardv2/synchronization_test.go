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

func TestCaptureTrustedSynchronizationRedeliversExactFingerprintAndNotLaterRequests(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:capture-sync", "Capture sync")
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project expected Snapshot: %v", err)
	}
	pending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending.Pending {
		t.Fatalf("expected pending before capture: %#v, %v", pending, err)
	}
	fingerprint := blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", "capture-key")
	claimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fingerprint, pending)
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	first, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending, true, fingerprint)
	if err != nil || first == nil {
		t.Fatalf("first capture: %#v, %v", first, err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first capture: %v", err)
	}
	gotSnapshot, err := json.Marshal(first.Snapshot)
	if err != nil || !bytes.Equal(gotSnapshot, want.Bytes) {
		t.Fatalf("capture Snapshot drifted\ngot=%s\nwant=%s err=%v", gotSnapshot, want.Bytes, err)
	}
	// Pending is cleared; exact fingerprint retry still redelivers the same attachment.
	cleared, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || cleared.Pending {
		t.Fatalf("pending should clear after capture: %#v, %v", cleared, err)
	}
	retry, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, cleared, true, fingerprint)
	if err != nil || retry == nil {
		t.Fatalf("fingerprint retry: %#v, %v", retry, err)
	}
	retryJSON, err := json.Marshal(retry)
	if err != nil || !bytes.Equal(firstJSON, retryJSON) {
		t.Fatalf("fingerprint retry drifted\nfirst=%s\nretry=%s err=%v", firstJSON, retryJSON, err)
	}
	// Ordinary later request (different fingerprint, no Pending) stays clean.
	later, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, cleared, true, blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", "other-key"))
	if err != nil {
		t.Fatalf("later capture: %v", err)
	}
	if later != nil {
		t.Fatalf("later capture reattached sync: %#v", later)
	}
}

// Crash after claim (and optional action) before finalize must recover on exact retry.
func TestSyncDeliveryCrashBetweenClaimActionFinalizeRecoversOnExactRetry(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:crash-window", "Crash window")
	pending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending.Pending {
		t.Fatalf("expected pending: %#v, %v", pending, err)
	}
	fingerprint := blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", "crash-window")
	claimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fingerprint, pending)
	if err != nil || !claimed {
		t.Fatalf("claim before crash: claimed=%v err=%v", claimed, err)
	}
	// Crash: process dies after claim (and before/after action) without finalize.
	// Exact retry reclaims and finalizes.
	reclaimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fingerprint, pending)
	if err != nil || !reclaimed {
		t.Fatalf("reclaim after crash: claimed=%v err=%v", reclaimed, err)
	}
	// Pending may still be true (no finalize yet).
	stillPending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil {
		t.Fatalf("inspect after claim-only crash: %v", err)
	}
	if !stillPending.Pending {
		t.Fatalf("claim must not acknowledge: %#v", stillPending)
	}
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project Snapshot: %v", err)
	}
	recovered, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, stillPending, true, fingerprint)
	if err != nil || recovered == nil {
		t.Fatalf("finalize after crash: %#v, %v", recovered, err)
	}
	got, err := json.Marshal(recovered.Snapshot)
	if err != nil || !bytes.Equal(got, want.Bytes) {
		t.Fatalf("recovered Snapshot drifted\ngot=%s\nwant=%s err=%v", got, want.Bytes, err)
	}
	// Second exact retry redelivers byte-identical attachment.
	cleared, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || cleared.Pending {
		t.Fatalf("pending after finalize: %#v, %v", cleared, err)
	}
	retry, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, cleared, true, fingerprint)
	if err != nil || retry == nil {
		t.Fatalf("exact retry after finalize: %#v, %v", retry, err)
	}
	firstJSON, _ := json.Marshal(recovered)
	retryJSON, _ := json.Marshal(retry)
	if !bytes.Equal(firstJSON, retryJSON) {
		t.Fatalf("exact retry drifted\nfirst=%s\nretry=%s", firstJSON, retryJSON)
	}
}

// Two concurrent different fingerprints: exactly one claims and receives sync.
func TestSyncDeliveryConcurrentDifferentFingerprintsOnlyOneReceives(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:concurrent-claim", "Concurrent claim")
	pending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending.Pending {
		t.Fatalf("expected pending: %#v, %v", pending, err)
	}
	fpA := blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", "concurrent-a")
	fpB := blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", "concurrent-b")
	type claimResult struct {
		fp      string
		claimed bool
		err     error
	}
	results := make(chan claimResult, 2)
	go func() {
		claimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fpA, pending)
		results <- claimResult{fp: fpA, claimed: claimed, err: err}
	}()
	go func() {
		claimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fpB, pending)
		results <- claimResult{fp: fpB, claimed: claimed, err: err}
	}()
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("claim errors: %#v %#v", first, second)
	}
	winners := 0
	var winnerFP, loserFP string
	for _, r := range []claimResult{first, second} {
		if r.claimed {
			winners++
			winnerFP = r.fp
		} else {
			loserFP = r.fp
		}
	}
	if winners != 1 || winnerFP == "" || loserFP == "" {
		t.Fatalf("want exactly one claim winner, got first=%#v second=%#v", first, second)
	}
	winnerAttach, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending, true, winnerFP)
	if err != nil || winnerAttach == nil {
		t.Fatalf("winner capture: %#v, %v", winnerAttach, err)
	}
	loserAttach, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending, true, loserFP)
	if err != nil {
		t.Fatalf("loser capture err: %v", err)
	}
	if loserAttach != nil {
		t.Fatalf("loser must not receive sync: %#v", loserAttach)
	}
	cleared, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || cleared.Pending {
		t.Fatalf("pending after winner finalize: %#v, %v", cleared, err)
	}
}

// Older fingerprint replay survives a later sync delivery on a different key.
func TestSyncDeliveryOldFingerprintReplayAfterLaterDelivery(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:old-fp-1", "Old fingerprint first")
	pending1, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending1.Pending {
		t.Fatalf("expected pending: %#v, %v", pending1, err)
	}
	fp1 := blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", "old-fp-1")
	if claimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fp1, pending1); err != nil || !claimed {
		t.Fatalf("claim fp1: %v %v", claimed, err)
	}
	first, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending1, true, fp1)
	if err != nil || first == nil {
		t.Fatalf("first capture: %#v, %v", first, err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	// Later peer write creates a new pending notice; a different fingerprint delivers it.
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:old-fp-2", "Old fingerprint second")
	pending2, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending2.Pending {
		t.Fatalf("expected second pending: %#v, %v", pending2, err)
	}
	fp2 := blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", "old-fp-2")
	if claimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fp2, pending2); err != nil || !claimed {
		t.Fatalf("claim fp2: %v %v", claimed, err)
	}
	second, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending2, true, fp2)
	if err != nil || second == nil {
		t.Fatalf("second capture: %#v, %v", second, err)
	}
	if second.Revision <= first.Revision {
		t.Fatalf("second delivery did not advance: first=%d second=%d", first.Revision, second.Revision)
	}
	// Exact replay of the older fingerprint still returns the original attachment.
	cleared, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || cleared.Pending {
		t.Fatalf("pending after second delivery: %#v, %v", cleared, err)
	}
	oldReplay, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, cleared, true, fp1)
	if err != nil || oldReplay == nil {
		t.Fatalf("old fingerprint replay: %#v, %v", oldReplay, err)
	}
	oldJSON, err := json.Marshal(oldReplay)
	if err != nil || !bytes.Equal(firstJSON, oldJSON) {
		t.Fatalf("old fingerprint replay drifted\nfirst=%s\nreplay=%s err=%v", firstJSON, oldJSON, err)
	}
}

// Finish claim → commit → crash before finalize; exact Finish retry recovers sync.
func TestSyncDeliveryFinishCrashBeforeFinalizeRecoversOnExactRetry(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), owner.Continuation.ID); err != nil {
		t.Fatalf("materialize owner Working Snapshot: %v", err)
	}
	// Open then terminal attempt so Finish is allowed; external write creates pending.
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "finish-crash-open",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:finish-crash", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Finish crash"}},
			{Op: "create", Key: "attempt:finish-crash", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Work"}},
			{Op: "relate", From: "attempt:finish-crash", Relation: "tests", To: "objective:finish-crash"},
		},
	}); err != nil {
		t.Fatalf("open work: %v", err)
	}
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "finish-crash-terminal",
		Changes: []blackboardv2.Change{
			{Op: "transition", Key: "attempt:finish-crash", Version: 1, Status: "failed", Summary: "Done"},
		},
	}); err != nil {
		t.Fatalf("terminal attempt: %v", err)
	}
	seedCurrentEntity(t, fixture.board, fixture.project.ID, "entity:finish-crash", "Finish crash")
	pending, err := fixture.board.InspectContinuationSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID)
	if err != nil || !pending.Pending {
		t.Fatalf("expected pending before finish: %#v, %v", pending, err)
	}
	fingerprint := blackboardv2.SynchronizationDeliveryFingerprint("finish", "finish-crash-key")
	claimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fingerprint, pending)
	if err != nil || !claimed {
		t.Fatalf("claim finish: claimed=%v err=%v", claimed, err)
	}
	finished, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-crash-key"})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	// Crash before Capture: claim open, Finish committed, no finalized receipt.
	// Exact retry: Finish replays, Capture finalizes from closed Working Snapshot.
	authority, err := fixture.board.AuthorizeContinuationBinding(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, false)
	if err != nil {
		t.Fatalf("authorize closed: %v", err)
	}
	if authority.Live {
		t.Fatalf("continuation should be closed after finish")
	}
	reclaimed, err := fixture.board.ClaimTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, fingerprint, pending)
	if err != nil || !reclaimed {
		t.Fatalf("reclaim after finish crash: claimed=%v err=%v", reclaimed, err)
	}
	replayFinish, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-crash-key"})
	if err != nil || replayFinish.Revision != finished.Revision {
		t.Fatalf("finish replay: %#v, %v", replayFinish, err)
	}
	attachment, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending, false, fingerprint)
	if err != nil || attachment == nil {
		t.Fatalf("finalize after finish crash: %#v, %v", attachment, err)
	}
	if attachment.Revision != finished.Revision {
		t.Fatalf("finish sync revision=%d want %d", attachment.Revision, finished.Revision)
	}
	// Second exact Capture redelivers identical attachment.
	again, err := fixture.board.CaptureTrustedSynchronization(context.Background(), fixture.project.ID, owner.Continuation.TaskID, owner.Continuation.ID, pending, false, fingerprint)
	if err != nil || again == nil {
		t.Fatalf("second finish sync replay: %#v, %v", again, err)
	}
	a, _ := json.Marshal(attachment)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Fatalf("finish sync replay drifted\nfirst=%s\nsecond=%s", a, b)
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
