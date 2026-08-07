package blackboardv2_test

import (
	"bytes"
	"context"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/task"
)

// ADR 0022: blackboard_finish records a Blackboard Finish Intent during a Work
// Runtime Turn and does not close the Continuation while that Turn can still
// produce work. Later source work invalidates the intent; settlement closes the
// Continuation only when the intent is still valid and the open work is
// terminal.

func TestRecordFinishIntentReturnsIntentRecordedAndKeepsContinuationWritable(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	intent, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID,
		blackboardv2.FinishContinuationRequest{IdempotencyKey: "intent-1"},
		blackboardv2.FinishIntentProvenance{SourceTurnID: "work-turn-1", SourceWorkWatermark: 1})
	if err != nil {
		t.Fatalf("record Finish Intent: %v", err)
	}
	if intent.Schema != "continuation-finish/v2" || intent.Status != "intent_recorded" {
		t.Fatalf("intent result = %#v, want status intent_recorded", intent)
	}
	// The Continuation stays writable after an intent is recorded.
	write, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "intent-write-1",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:intent-write", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Intent write", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("write after Finish Intent: %v", err)
	}
	if write.Revision <= intent.Revision {
		t.Fatalf("write advanced Blackboard: write=%d intent=%d", write.Revision, intent.Revision)
	}
	recorded, found, err := fixture.board.FinishIntentForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID)
	if err != nil || !found || !recorded.Valid {
		t.Fatalf("recorded intent = found=%v %#v err=%v, want valid", found, recorded, err)
	}
}

func TestRecordFinishIntentExactReplayReturnsSameIntent(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	request := blackboardv2.FinishContinuationRequest{IdempotencyKey: "intent-replay"}
	provenance := blackboardv2.FinishIntentProvenance{SourceTurnID: "work-turn-replay", SourceWorkWatermark: 0}
	first, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID, request, provenance)
	if err != nil {
		t.Fatalf("record Finish Intent: %v", err)
	}
	replay, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID, request, provenance)
	if err != nil || !bytes.Equal(mustJSON(t, replay), mustJSON(t, first)) {
		t.Fatalf("replay = %#v %v, want exact first %#v", replay, err, first)
	}
	// A different idempotency key on the same continuation is a conflict.
	if _, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID,
		blackboardv2.FinishContinuationRequest{IdempotencyKey: "intent-different"}, provenance); !isV2ErrorCode(err, "finish_conflict") {
		t.Fatalf("conflicting intent error = %#v, want finish_conflict", err)
	}
}

func TestInvalidateFinishIntentForcesNewIntentAndAdvancesWatermark(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if _, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID,
		blackboardv2.FinishContinuationRequest{IdempotencyKey: "intent-invalidate-1"},
		blackboardv2.FinishIntentProvenance{SourceTurnID: "work-turn-invalidate", SourceWorkWatermark: 1}); err != nil {
		t.Fatalf("record Finish Intent: %v", err)
	}
	if err := fixture.board.InvalidateFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID); err != nil {
		t.Fatalf("invalidate Finish Intent: %v", err)
	}
	recorded, found, err := fixture.board.FinishIntentForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID)
	if err != nil || !found || recorded.Valid {
		t.Fatalf("intent after invalidation = %#v found=%v err=%v, want invalid", recorded, found, err)
	}
	// Invalidation is idempotent: invalidating again is a safe no-op.
	if err := fixture.board.InvalidateFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID); err != nil {
		t.Fatalf("idempotent invalidate: %v", err)
	}
}

func TestSettleFinishIntentClosesOnlyWhenValidAndOpenWorkTerminal(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if _, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID,
		blackboardv2.FinishContinuationRequest{IdempotencyKey: "intent-settle"},
		blackboardv2.FinishIntentProvenance{SourceTurnID: "work-turn-settle", SourceWorkWatermark: 0}); err != nil {
		t.Fatalf("record Finish Intent: %v", err)
	}
	settled, err := fixture.board.SettleFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID)
	if err != nil || !settled {
		t.Fatalf("settle valid intent = settled=%v err=%v", settled, err)
	}
	// After settlement the Continuation is closed.
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "post-settle-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:post-settle", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Post settle", ScopeStatus: "in_scope"},
		}},
	}); !isV2ErrorCode(err, "closed_continuation") {
		t.Fatalf("post-settle write error = %#v, want closed_continuation", err)
	}
}

func TestSettleFinishIntentIsNoOpWhenIntentInvalidated(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if _, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID,
		blackboardv2.FinishContinuationRequest{IdempotencyKey: "intent-settle-invalid"},
		blackboardv2.FinishIntentProvenance{SourceTurnID: "work-turn-settle-invalid", SourceWorkWatermark: 1}); err != nil {
		t.Fatalf("record Finish Intent: %v", err)
	}
	if err := fixture.board.InvalidateFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID); err != nil {
		t.Fatalf("invalidate Finish Intent: %v", err)
	}
	settled, err := fixture.board.SettleFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID)
	if err != nil {
		t.Fatalf("settle after invalidation: %v", err)
	}
	if settled {
		t.Fatalf("invalidated intent settled, want no-op")
	}
	// The Continuation stays writable because settlement did not close it.
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "still-writable",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:still-writable", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Still writable", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("write after invalidation settlement no-op: %v", err)
	}
}

func TestSettleFinishIntentIsNoOpWhenNoIntentRecorded(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	settled, err := fixture.board.SettleFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID)
	if err != nil || settled {
		t.Fatalf("settle without intent = settled=%v err=%v, want no-op", settled, err)
	}
}

func TestRecordFinishIntentAgainstClosedContinuationReplaysFinishReceipt(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	request := blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-then-intent"}
	// Interactive finish closes the Continuation and writes the receipt.
	finished, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request)
	if err != nil {
		t.Fatalf("Finish Continuation: %v", err)
	}
	// A finish intent call against the closed Continuation with the same key
	// replays the finish receipt instead of recording an intent.
	replay, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID, request,
		blackboardv2.FinishIntentProvenance{SourceTurnID: "work-turn-after-close", SourceWorkWatermark: 0})
	if err != nil || !bytes.Equal(mustJSON(t, replay), mustJSON(t, finished)) {
		t.Fatalf("intent against closed Continuation = %#v %v, want finish receipt %#v", replay, err, finished)
	}
	// A different key reports closed authority truthfully.
	if _, err := fixture.board.RecordFinishIntent(context.Background(), fixture.project.ID, launch.Continuation.ID,
		blackboardv2.FinishContinuationRequest{IdempotencyKey: "intent-after-close-other"},
		blackboardv2.FinishIntentProvenance{SourceTurnID: "work-turn-after-close", SourceWorkWatermark: 0}); !isV2ErrorCode(err, "closed_continuation") {
		t.Fatalf("intent against closed Continuation other key = %#v, want closed_continuation", err)
	}
}
