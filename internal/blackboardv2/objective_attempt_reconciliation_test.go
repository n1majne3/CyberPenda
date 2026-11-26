package blackboardv2_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"path/filepath"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestServerReconcilesOnlyClosedContinuationsOwnedAttempts(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Attempt Reconciliation", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	foreignProject, err := projects.Create("Foreign Reconciliation", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	tasks := task.NewService(db, projects)
	ownerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Owner", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create owner Task: %v", err)
	}
	owner, err := tasks.CreateContinuation(ownerTask.ID, "profile-owner", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create owner Continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(owner.ID, task.StatusRunning); err != nil {
		t.Fatalf("run owner Continuation: %v", err)
	}
	peerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Peer", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create peer Task: %v", err)
	}
	peer, err := tasks.CreateContinuation(peerTask.ID, "profile-peer", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create peer Continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(peer.ID, task.StatusRunning); err != nil {
		t.Fatalf("run peer Continuation: %v", err)
	}
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: foreignProject.ID, Type: task.TypePentest, Goal: "Foreign", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreign, err := tasks.CreateContinuation(foreignTask.ID, "profile-foreign", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(foreign.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt foreign Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)

	ownerCreate := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-reconciliation-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:owner", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Exercise owner targets"}},
			{Op: "create", Key: "attempt:owner-a", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Owner attempt A checkpoint"}},
			{Op: "create", Key: "attempt:owner-b", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Owner attempt B checkpoint"}},
			{Op: "relate", From: "attempt:owner-a", Relation: "tests", To: "objective:owner"},
			{Op: "relate", From: "attempt:owner-b", Relation: "tests", To: "objective:owner"},
		},
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, ownerCreate); err != nil {
		t.Fatalf("create owner reconciliation work: %v", err)
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, peer.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "peer-reconciliation-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:peer", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Peer attempt checkpoint"}},
			{Op: "relate", From: "attempt:peer", Relation: "tests", To: "objective:owner"},
		},
	}); err != nil {
		t.Fatalf("create peer reconciliation work: %v", err)
	}

	_, err = service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "runtime-self-interrupt",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "attempt:owner-a", Version: 1, Status: "interrupted", Summary: "Runtime cannot choose interrupted",
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("Runtime self-interruption error = %#v, want semantic_validation", err)
	}

	_, err = service.ReconcileContinuationAttempts(ctx, createdProject.ID, owner.ID)
	if !isSemanticCode(err, "continuation_not_closed") {
		t.Fatalf("running reconciliation error = %#v, want continuation_not_closed", err)
	}
	if _, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, foreign.ID); !isSemanticCode(err, "authority_denied") {
		t.Fatalf("cross-project reconciliation error = %#v, want authority_denied", err)
	}

	if _, err := tasks.UpdateContinuationStatus(owner.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt owner Continuation: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "closed-owner-write",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "attempt:owner-a", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Closed owner write")},
		}},
	})
	if !isSemanticCode(err, "closed_continuation") {
		t.Fatalf("closed Continuation write error = %#v, want closed_continuation", err)
	}
	if replay, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, ownerCreate); err != nil || replay.Revision != 5 {
		t.Fatalf("closed Continuation exact replay = %#v, %v", replay, err)
	}

	reconciled, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, owner.ID)
	if err != nil {
		t.Fatalf("reconcile interrupted owner: %v", err)
	}
	assertChangeRecords(t, reconciled, 9, [][]any{{"attempt:owner-a", float64(2)}, {"attempt:owner-b", float64(2)}})
	for _, tt := range []struct {
		key         string
		historySize int
	}{
		{key: "attempt:owner-a", historySize: 3},
		{key: "attempt:owner-b", historySize: 3},
	} {
		key := tt.key
		if _, err := service.ReadCurrent(ctx, createdProject.ID, key); !isSemanticCode(err, "not_found") {
			t.Fatalf("reconciled Attempt %s current read = %#v, want not_found", key, err)
		}
		history, err := service.ReadHistory(ctx, createdProject.ID, key, blackboardv2.HistoryOptions{})
		if err != nil {
			t.Fatalf("read reconciled history %s: %v", key, err)
		}
		if len(history.Items) != tt.historySize || history.Items[1].Record.Status != "interrupted" || strings.TrimSpace(history.Items[1].Record.Summary) == "" {
			t.Fatalf("reconciled history %s = %#v", key, history.Items)
		}
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:peer"); err != nil {
		t.Fatalf("peer-owned Attempt was reconciled: %v", err)
	}

	replayReconciliation, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, owner.ID)
	if err != nil {
		t.Fatalf("replay reconciliation: %v", err)
	}
	assertChangeRecords(t, replayReconciliation, 9, [][]any{})

	if _, err := tasks.UpdateContinuationStatus(peer.ID, task.StatusCompleted); err != nil {
		t.Fatalf("complete peer Continuation: %v", err)
	}
	completed, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, peer.ID)
	if err != nil {
		t.Fatalf("reconcile completed peer: %v", err)
	}
	assertChangeRecords(t, completed, 9, [][]any{})
	if peerAttempt, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:peer"); err != nil || peerAttempt.Record.Status != "open" {
		t.Fatalf("clean completion rewrote peer Attempt = %#v, %v", peerAttempt, err)
	}
}

func TestReconciliationReportsNeverLinkedOwnedAttemptsWithoutForeignHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Invalid Legacy Attempt", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	foreignProject, err := projects.Create("Foreign Attempt History", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Diagnose invalid Attempt", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-invalid-attempt", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)
	if _, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-invalid-legacy-attempt",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "attempt:legacy-never-linked", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Legacy Attempt never linked to a tested target"},
		}},
	}); err != nil {
		t.Fatalf("create invalid legacy Attempt: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO blackboard_v2_attempt_origins (project_id, key, continuation_id, created_at)
		VALUES (?, ?, ?, ?)`, createdProject.ID, "attempt:legacy-never-linked", continuation.ID, "2026-07-16T00:00:00Z"); err != nil {
		t.Fatalf("bind invalid legacy Attempt origin: %v", err)
	}
	if _, err := service.Apply(ctx, foreignProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-foreign-tests-history",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:legacy-never-linked", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Foreign same-key Attempt has a valid target"}},
			{Op: "create", Key: "entity:foreign-tested-target", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Foreign tested endpoint", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:legacy-never-linked", Relation: "tests", To: "entity:foreign-tested-target"},
			{Op: "transition", Key: "entity:foreign-tested-target", Version: 1, Status: "retired", ResolutionSummary: "Archive only the foreign Project tests relationship"},
		},
	}); err != nil {
		t.Fatalf("create foreign tests history: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		_, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, continuation.ID)
		var semanticErr *blackboardv2.Error
		if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Message != "Attempt reconciliation found owned Attempts without tested targets" {
			t.Fatalf("invalid reconciliation %d error = %#v", attempt, err)
		}
		if semanticErr.Details["reason"] != "missing_tested_target" || !reflect.DeepEqual(semanticErr.Details["attempts"], []string{"attempt:legacy-never-linked"}) || semanticErr.Details["next_action"] != "repair_invalid_attempt" {
			t.Fatalf("invalid reconciliation %d details = %#v", attempt, semanticErr.Details)
		}
	}
	current, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:legacy-never-linked")
	if err != nil || current.Record.Status != "open" || len(current.Relationships) != 0 {
		t.Fatalf("invalid legacy Attempt changed during reconciliation = %#v, %v", current, err)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "attempt:legacy-never-linked", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read invalid legacy Attempt history: %v", err)
	}
	if len(history.Items) != 0 {
		t.Fatalf("invalid reconciliation invented Semantic History: %#v", history.Items)
	}
}

func TestTrustedAttemptWritesRejectEveryTerminalContinuationStatus(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Closed Continuations", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	service := blackboardv2.NewService(db)

	for _, status := range []task.Status{task.StatusCompleted, task.StatusFailed, task.StatusStopped, task.StatusInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Closed " + string(status), Runner: task.RunnerSandbox})
			if err != nil {
				t.Fatalf("create Task: %v", err)
			}
			continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-"+string(status), "codex", task.RunnerSandbox)
			if err != nil {
				t.Fatalf("create Continuation: %v", err)
			}
			if _, err := tasks.UpdateContinuationStatus(continuation.ID, status); err != nil {
				t.Fatalf("set Continuation status: %v", err)
			}
			_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "closed-attempt-write-" + string(status),
				Changes: []blackboardv2.Change{{
					Op: "create", Key: "attempt:closed-" + string(status), Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Must not be created by a closed Continuation"},
				}},
			})
			if !isSemanticCode(err, "closed_continuation") {
				t.Fatalf("closed %s write error = %#v, want closed_continuation", status, err)
			}
			if _, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:closed-"+string(status)); !isSemanticCode(err, "not_found") {
				t.Fatalf("closed %s Continuation created Attempt: %#v", status, err)
			}
		})
	}
}
