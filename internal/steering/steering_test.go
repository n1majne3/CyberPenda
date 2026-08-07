package steering

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db)
}

func TestAcceptAssignsFIFOQueueOrderPerOwner(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	input := AcceptRequest{RequestID: "req-1", Message: "first", Mode: owner.SteeringModeInTurnSteer}
	first, err := service.Accept(ctx, owner.KindTask, "task-1", input, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Accept(ctx, owner.KindTask, "task-1", AcceptRequest{RequestID: "req-2", Message: "second", Mode: owner.SteeringModeInTurnSteer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessionFirst, err := service.Accept(ctx, owner.KindSession, "session-1", input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != owner.SteeringPending || first.QueueOrder != 1 || second.QueueOrder != 2 {
		t.Fatalf("task queue order = %d, %d (states %s, %s), want 1 then 2", first.QueueOrder, second.QueueOrder, first.State, second.State)
	}
	if sessionFirst.QueueOrder != 1 {
		t.Fatalf("session queue order = %d, want owner-local order 1", sessionFirst.QueueOrder)
	}
	oldest, err := service.OldestPending(owner.KindTask, "task-1")
	if err != nil || oldest == nil || oldest.RequestID != "req-1" {
		t.Fatalf("oldest pending = %#v err=%v, want req-1", oldest, err)
	}
}

func TestDuplicateRequestIdentityIsRejected(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	input := AcceptRequest{RequestID: "dup", Message: "same", Mode: owner.SteeringModeInTurnSteer}
	if _, err := service.Accept(ctx, owner.KindTask, "task-1", input, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Accept(ctx, owner.KindTask, "task-1", input, nil); !errors.Is(err, ErrDuplicateRequest) {
		t.Fatalf("duplicate accept error = %v, want ErrDuplicateRequest", err)
	}
}

func TestFenceTransitionIsExactlyOnce(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	record, err := service.Accept(ctx, owner.KindTask, "task-1", AcceptRequest{
		RequestID: "fence", Message: "fence me", Mode: owner.SteeringModeInterruptThenReplace,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := service.MarkDispatchStarted(ctx, record.ID, "continuation-1", record.CreatedAt)
	if err != nil || fenced.State != owner.SteeringDispatchStarted {
		t.Fatalf("fence = %#v err=%v, want dispatch_started", fenced, err)
	}
	if fenced.ContinuationID != "continuation-1" || fenced.SendStartedAt == nil {
		t.Fatalf("fence evidence = %#v", fenced)
	}
	// A second fence is an idempotent replay of the durable state.
	again, err := service.MarkDispatchStarted(ctx, record.ID, "continuation-2", record.CreatedAt)
	if err != nil || again.State != owner.SteeringDispatchStarted || again.ContinuationID != "continuation-1" {
		t.Fatalf("second fence = %#v err=%v, must not rewrite the fence", again, err)
	}
}

func TestClosedReasonVocabularyRejectsArbitraryReasons(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	record, err := service.Accept(ctx, owner.KindTask, "task-1", AcceptRequest{
		RequestID: "reason", Message: "reject bad reason", Mode: owner.SteeringModeInTurnSteer,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.MarkFailed(ctx, record.ID, "not-a-closed-reason", "freeform"); !errors.Is(err, ErrInvalidReason) {
		t.Fatalf("arbitrary reason error = %v, want ErrInvalidReason", err)
	}
	if _, err := service.SettleOwner(ctx, owner.KindTask, "task-1", "freeform", "settle"); !errors.Is(err, ErrInvalidReason) {
		t.Fatalf("arbitrary settle reason error = %v, want ErrInvalidReason", err)
	}
	if _, err := service.MarkActionRequired(ctx, record.ID, owner.SteeringReasonDeliveryAmbiguous, "ambiguous"); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.ByRequestID(owner.KindTask, "task-1", "reason")
	if err != nil || terminal.State != owner.SteeringActionRequired || terminal.ErrorCode != owner.SteeringReasonDeliveryAmbiguous {
		t.Fatalf("action_required record = %#v err=%v", terminal, err)
	}
}

func TestSettleOwnerDistinguishesPreFenceAndPostFence(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	pending, err := service.Accept(ctx, owner.KindTask, "task-1", AcceptRequest{
		RequestID: "pre-fence", Message: "never sent", Mode: owner.SteeringModeInTurnSteer,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = pending
	fenced, err := service.Accept(ctx, owner.KindTask, "task-1", AcceptRequest{
		RequestID: "post-fence", Message: "may have been sent", Mode: owner.SteeringModeInTurnSteer,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.MarkDispatchStarted(ctx, fenced.ID, "", fenced.CreatedAt); err != nil {
		t.Fatal(err)
	}
	settled, err := service.SettleOwner(ctx, owner.KindTask, "task-1", owner.SteeringReasonOwnerStopped, "stopped")
	if err != nil {
		t.Fatal(err)
	}
	byRequest := func(requestID string) *owner.SteeringRecord {
		record, err := service.ByRequestID(owner.KindTask, "task-1", requestID)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	pre := byRequest("pre-fence")
	if pre.State != owner.SteeringFailed || pre.ErrorCode != owner.SteeringReasonOwnerStopped {
		t.Fatalf("pre-fence settlement = %#v, want failed(owner_stopped)", pre)
	}
	post := byRequest("post-fence")
	if post.State != owner.SteeringActionRequired || post.ErrorCode != owner.SteeringReasonDeliveryAmbiguous {
		t.Fatalf("post-fence settlement = %#v, want action_required(delivery_ambiguous)", post)
	}
	if len(settled) != 2 {
		t.Fatalf("settled records = %d, want 2", len(settled))
	}
}
