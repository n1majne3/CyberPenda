package blackboardv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"pentest/internal/blackboardv2"
)

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
