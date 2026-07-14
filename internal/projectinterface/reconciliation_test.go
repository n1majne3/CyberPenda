package projectinterface_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/projectinterface"
	"pentest/internal/task"
)

func TestUnexpectedContinuationInterruptsOnlyItsMatchingOpenAttempts(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()

	firstPrincipal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate first Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, firstPrincipal, "first-task")

	secondTask, err := fixture.tasks.Create(task.CreateRequest{
		ProjectID: fixture.project.ID, Goal: "Inspect the second surface", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create second Task: %v", err)
	}
	secondConfig, err := fixture.tasks.RecordRuntimeConfig(secondTask.ID, fixture.runtimeProfile, map[string]any{"model": "test-model"})
	if err != nil {
		t.Fatalf("record second Runtime configuration: %v", err)
	}
	secondContinuation, err := fixture.tasks.CreateContinuation(secondTask.ID, fixture.runtimeProfile, fixture.runtimePlugin, task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create second Continuation: %v", err)
	}
	secondToken, _, err := fixture.grants.Issue(ctx, projectinterface.IssueGrantRequest{
		ProjectID: fixture.project.ID, TaskID: secondTask.ID, ContinuationID: secondContinuation.ID,
		RuntimeConfigVersionID: secondConfig.ID, RuntimeProfileID: fixture.runtimeProfile,
		RuntimePluginID: fixture.runtimePlugin, Runner: fixture.runner,
	})
	if err != nil {
		t.Fatalf("issue second Continuation grant: %v", err)
	}
	secondPrincipal, err := fixture.service.Authenticate(ctx, secondToken, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate second Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, secondPrincipal, "second-task")

	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("mark first Continuation unexpectedly terminal: %v", err)
	}
	if _, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "forced_stop"); err != nil {
		t.Fatalf("reconcile unexpected Continuation: %v", err)
	}

	firstAttempt, err := fixture.graph.ReadNode(ctx, blackboard.ReadNodeRequest{
		ProjectID: fixture.project.ID, NodeType: blackboard.NodeTypeAttempt, Key: "attempt:first-task",
	})
	if err != nil {
		t.Fatalf("read first Attempt: %v", err)
	}
	if got := firstAttempt.Node.PropertyMap["status"]; got != "interrupted" {
		t.Fatalf("first Attempt status = %v, want interrupted", got)
	}
	wantFallback := fmt.Sprintf("Continuation %s ended before this Attempt was concluded (forced_stop).", fixture.continuation.ID)
	if got := firstAttempt.Node.PropertyMap["summary"]; got != wantFallback {
		t.Fatalf("first Attempt summary = %v, want %q", got, wantFallback)
	}
	secondAttempt, err := fixture.graph.ReadNode(ctx, blackboard.ReadNodeRequest{
		ProjectID: fixture.project.ID, NodeType: blackboard.NodeTypeAttempt, Key: "attempt:second-task",
	})
	if err != nil {
		t.Fatalf("read second Attempt: %v", err)
	}
	if got := secondAttempt.Node.PropertyMap["status"]; got != "open" {
		t.Fatalf("second Attempt status = %v, want open", got)
	}
	firstContinuation, err := fixture.tasks.LatestContinuation(fixture.task.ID)
	if err != nil {
		t.Fatalf("read reconciled Continuation: %v", err)
	}
	if firstContinuation.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("reconciliation status = %q, want %q", firstContinuation.BlackboardReconciliationStatus, task.ReconciliationCompleted)
	}
	if currentTask, err := fixture.tasks.Get(fixture.task.ID); err != nil {
		t.Fatalf("read owning Task: %v", err)
	} else if currentTask.Status != fixture.task.Status {
		t.Fatalf("reconciliation changed Task status from %q to %q", fixture.task.Status, currentTask.Status)
	}
}

type failReconciliationOnce struct {
	fired bool
}

func (f *failReconciliationOnce) FailAfter(point projectinterface.ReconciliationFailurePoint) error {
	if f.fired || point != projectinterface.ReconciliationFailureAfterApply {
		return nil
	}
	f.fired = true
	return errors.New("injected reconciliation marker failure")
}

func TestReconciliationDiscoversCommittedMutationAfterLostMarker(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "lost-marker")
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("mark Continuation unexpectedly terminal: %v", err)
	}

	failing := projectinterface.NewService(projectinterface.Deps{
		DB: fixture.db, Graph: fixture.graph, Grants: fixture.grants, Tasks: fixture.tasks,
		ReconciliationFailures: &failReconciliationOnce{},
	})
	if _, err := failing.ReconcileContinuation(ctx, fixture.continuation.ID, "forced_stop"); err == nil {
		t.Fatal("expected injected failure after reconciliation Apply")
	}
	interrupted := mustReadAttempt(t, fixture, "attempt:lost-marker")
	markerMissing, err := fixture.tasks.LatestContinuation(fixture.task.ID)
	if err != nil {
		t.Fatalf("read Continuation after lost marker: %v", err)
	}
	if markerMissing.BlackboardReconciliationStatus != task.ReconciliationPending {
		t.Fatalf("status after lost marker = %q, want pending", markerMissing.BlackboardReconciliationStatus)
	}

	recovered, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "daemon_restart")
	if err != nil {
		t.Fatalf("recover lost reconciliation marker: %v", err)
	}
	if recovered.MutationID == "" {
		t.Fatal("recovery did not discover the committed reconciliation mutation")
	}
	after := mustReadAttempt(t, fixture, "attempt:lost-marker")
	if after.ObservedGraphRevision != interrupted.ObservedGraphRevision || after.Node.Version != interrupted.Node.Version {
		t.Fatalf("recovery created another graph change: before revision/version %d/%d, after %d/%d",
			interrupted.ObservedGraphRevision, interrupted.Node.Version, after.ObservedGraphRevision, after.Node.Version)
	}
}

func TestUnexpectedContinuationUsesAttemptSpecificSummariesAndBoundedOrderedEvents(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "parallel-a")
	prepareRetainEvidenceAttempt(t, fixture, principal, "parallel-b")

	started, err := fixture.tasks.AppendContinuationEvent(fixture.task.ID, fixture.continuation.ID, task.EventKindLifecycle, task.EventPayload{"phase": "started"})
	if err != nil {
		t.Fatalf("append Continuation start Event: %v", err)
	}
	first := mustReadAttempt(t, fixture, "attempt:parallel-a")
	firstCheckpoint, err := fixture.service.CheckpointAttempt(ctx, principal, projectinterface.CheckpointAttemptRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "checkpoint-parallel-a",
		Attempt: blackboard.NodeRef{ID: first.Node.ID}, ExpectedVersion: first.Node.Version,
		Summary: "Mapped the first attack surface",
	})
	if err != nil {
		t.Fatalf("checkpoint first Attempt: %v", err)
	}
	second := mustReadAttempt(t, fixture, "attempt:parallel-b")
	secondCheckpoint, err := fixture.service.CheckpointAttempt(ctx, principal, projectinterface.CheckpointAttemptRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "checkpoint-parallel-b",
		Attempt: blackboard.NodeRef{ID: second.Node.ID}, ExpectedVersion: second.Node.Version,
		Summary: "Enumerated the second attack surface",
	})
	if err != nil {
		t.Fatalf("checkpoint second Attempt: %v", err)
	}
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("mark Continuation unexpectedly terminal: %v", err)
	}
	terminal, err := fixture.tasks.AppendContinuationEvent(fixture.task.ID, fixture.continuation.ID, task.EventKindLifecycle, task.EventPayload{"phase": "interrupted", "reason": "timeout"})
	if err != nil {
		t.Fatalf("append terminal Event: %v", err)
	}
	if _, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "timeout"); err != nil {
		t.Fatalf("reconcile unexpected Continuation: %v", err)
	}

	first = mustReadAttempt(t, fixture, "attempt:parallel-a")
	second = mustReadAttempt(t, fixture, "attempt:parallel-b")
	if got := first.Node.PropertyMap["summary"]; got != "Mapped the first attack surface" {
		t.Fatalf("first Attempt summary = %v", got)
	}
	if got := second.Node.PropertyMap["summary"]; got != "Enumerated the second attack surface" {
		t.Fatalf("second Attempt summary = %v", got)
	}

	projection, err := fixture.graph.CanonicalMainGraph(ctx, fixture.project.ID, first.ObservedGraphRevision)
	if err != nil {
		t.Fatalf("read canonical graph: %v", err)
	}
	firstEvents := projectionSourceEvents(t, projection.Bytes, "attempt:parallel-a")
	secondEvents := projectionSourceEvents(t, projection.Bytes, "attempt:parallel-b")
	wantFirst := []string{started.ID, firstCheckpoint.Result.Event.ID, terminal.ID}
	wantSecond := []string{started.ID, secondCheckpoint.Result.Event.ID, terminal.ID}
	if !reflect.DeepEqual(firstEvents, wantFirst) {
		t.Fatalf("first Attempt source Events = %v, want %v", firstEvents, wantFirst)
	}
	if !reflect.DeepEqual(secondEvents, wantSecond) {
		t.Fatalf("second Attempt source Events = %v, want %v", secondEvents, wantSecond)
	}
}

func TestUnexpectedContinuationIgnoresNonterminalLifecycleMessageForSummary(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "nonterminal-message")
	if _, err := fixture.tasks.AppendContinuationEvent(fixture.task.ID, fixture.continuation.ID, task.EventKindLifecycle, task.EventPayload{
		"phase": "running", "message": "ordinary progress message",
	}); err != nil {
		t.Fatalf("append nonterminal lifecycle Event: %v", err)
	}
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("mark Continuation interrupted: %v", err)
	}
	if _, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "timeout"); err != nil {
		t.Fatalf("reconcile Continuation: %v", err)
	}
	attempt := mustReadAttempt(t, fixture, "attempt:nonterminal-message")
	want := fmt.Sprintf("Continuation %s ended before this Attempt was concluded (timeout).", fixture.continuation.ID)
	if got := attempt.Node.PropertyMap["summary"]; got != want {
		t.Fatalf("Attempt summary = %v, want fallback %q", got, want)
	}
}

func TestCleanCompletionWithOpenAttemptReportsProtocolGapWithoutGuessingOutcome(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "clean-gap-b")
	prepareRetainEvidenceAttempt(t, fixture, principal, "clean-gap-a")
	first := mustReadAttempt(t, fixture, "attempt:clean-gap-a")
	second := mustReadAttempt(t, fixture, "attempt:clean-gap-b")
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("mark Continuation cleanly completed: %v", err)
	}
	reconciled, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "clean_exit")
	if err != nil {
		t.Fatalf("audit clean completion: %v", err)
	}
	if reconciled.Continuation.BlackboardReconciliationStatus != task.ReconciliationFailed {
		t.Fatalf("reconciliation status = %q, want failed", reconciled.Continuation.BlackboardReconciliationStatus)
	}
	attempt := mustReadAttempt(t, fixture, "attempt:clean-gap-a")
	if got := attempt.Node.PropertyMap["status"]; got != "open" {
		t.Fatalf("clean audit invented Attempt status %v", got)
	}
	events, err := fixture.tasks.Events(fixture.task.ID)
	if err != nil {
		t.Fatalf("read reconciliation Task Events: %v", err)
	}
	wantAttempts := []any{first.Node.ID, second.Node.ID}
	var persistedAttempts []any
	for _, event := range events {
		if event.ContinuationID == fixture.continuation.ID && event.Payload["phase"] == "reconciliation_failed" {
			persistedAttempts, _ = event.Payload["attempt_node_ids"].([]any)
		}
	}
	if !reflect.DeepEqual(persistedAttempts, wantAttempts) {
		t.Fatalf("persisted failed reconciliation Attempts = %v, want %v", persistedAttempts, wantAttempts)
	}
	health, err := fixture.graph.RunHealth(ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("run Blackboard Health: %v", err)
	}
	for _, result := range health.Results {
		if result.Code == "completion_protocol_gap" {
			return
		}
	}
	t.Fatalf("Health results do not report completion_protocol_gap: %#v", health.Results)
}

func TestHealthWaitsForNormalReconciliationBeforeReportingCompletionProtocolGap(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "pending-clean-audit")
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("mark Continuation completed: %v", err)
	}
	health, err := fixture.graph.RunHealth(ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("run Blackboard Health: %v", err)
	}
	for _, result := range health.Results {
		if result.Code == "completion_protocol_gap" || result.Code == "completion_protocol_stuck" {
			t.Fatalf("Health reported %s before normal reconciliation ran", result.Code)
		}
	}
}

func TestRepeatedFailedNormalReconciliationIsNoOp(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "repeat-clean-gap")
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("mark Continuation completed: %v", err)
	}
	first, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "clean_exit")
	if err != nil {
		t.Fatalf("run first normal reconciliation: %v", err)
	}
	beforeEvents, err := fixture.tasks.Events(fixture.task.ID)
	if err != nil {
		t.Fatalf("read first reconciliation Events: %v", err)
	}
	second, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "daemon_restart")
	if err != nil {
		t.Fatalf("repeat normal reconciliation: %v", err)
	}
	afterEvents, err := fixture.tasks.Events(fixture.task.ID)
	if err != nil {
		t.Fatalf("read repeated reconciliation Events: %v", err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("repeated normal reconciliation added Events: before=%d after=%d", len(beforeEvents), len(afterEvents))
	}
	if first.Continuation.BlackboardReconciledAt == nil || second.Continuation.BlackboardReconciledAt == nil ||
		!first.Continuation.BlackboardReconciledAt.Equal(*second.Continuation.BlackboardReconciledAt) {
		t.Fatalf("repeated normal reconciliation rewrote reconciled_at: first=%v second=%v",
			first.Continuation.BlackboardReconciledAt, second.Continuation.BlackboardReconciledAt)
	}
}

func TestRuntimeTerminalTransitionWinsRaceWithoutBeingOverwritten(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "runtime-wins")
	open := mustReadAttempt(t, fixture, "attempt:runtime-wins")
	if _, err := fixture.service.Apply(ctx, principal, projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "runtime-wins-terminal",
			Operations: []blackboard.Operation{{
				OpID: "failed", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: open.Node.ID},
				Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "failed", Summary: "Runtime recorded the negative result"},
			}},
		},
	}); err != nil {
		t.Fatalf("commit Runtime terminal transition: %v", err)
	}
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("mark Continuation unexpectedly terminal: %v", err)
	}
	reconciled, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "forced_stop")
	if err != nil {
		t.Fatalf("reconcile after Runtime terminal transition: %v", err)
	}
	if reconciled.MutationID != "" {
		t.Fatalf("reconciliation created mutation %q after Runtime already concluded the Attempt", reconciled.MutationID)
	}
	terminal := mustReadAttempt(t, fixture, "attempt:runtime-wins")
	if got := terminal.Node.PropertyMap["status"]; got != "failed" {
		t.Fatalf("Attempt status = %v, want failed", got)
	}
	if got := terminal.Node.PropertyMap["summary"]; got != "Runtime recorded the negative result" {
		t.Fatalf("Attempt summary = %v", got)
	}
}

func TestReconciliationWinsRaceAndStaleRuntimeGetsVersionConflict(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "reconciler-wins")
	stale := mustReadAttempt(t, fixture, "attempt:reconciler-wins")
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("mark Continuation unexpectedly terminal: %v", err)
	}
	if _, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "timeout"); err != nil {
		t.Fatalf("reconcile unexpected Continuation: %v", err)
	}

	_, err = fixture.graph.Apply(ctx, blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "stale-runtime-terminal",
		Context: blackboard.ExecutionContext{
			ProjectID: fixture.project.ID, ProjectKind: fixture.project.Kind,
			ActorType: blackboard.ActorTypeRuntime, ActorID: fixture.grant.ActorID,
			TaskID: fixture.task.ID, ContinuationID: fixture.continuation.ID,
			RuntimeProfileID: fixture.runtimeProfile, Runner: fixture.runner,
		},
		Operations: []blackboard.Operation{{
			OpID: "late-failure", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: stale.Node.ID},
			Transition: blackboard.TransitionNodeInput{ExpectedVersion: stale.Node.Version, Status: "failed", Summary: "late Runtime conclusion"},
		}},
	})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeVersionConflict {
		t.Fatalf("stale Runtime transition error = %v, want version_conflict", err)
	}
}

func TestCleanCompletionAfterFinishCreatesNoReconciliationMutation(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "finished-cleanly")
	open := mustReadAttempt(t, fixture, "attempt:finished-cleanly")
	if _, err := fixture.service.Apply(ctx, principal, projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "finish-clean-terminal",
			Operations: []blackboard.Operation{{
				OpID: "failed", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: open.Node.ID},
				Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "failed", Summary: "The Runtime concluded the Attempt"},
			}},
		},
	}); err != nil {
		t.Fatalf("conclude Attempt: %v", err)
	}
	if _, err := fixture.service.FinishContinuation(ctx, principal, projectinterface.FinishContinuationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "finish-clean",
		Summary: "The Runtime completed its protocol.",
	}); err != nil {
		t.Fatalf("Finish Continuation: %v", err)
	}
	before := mustReadAttempt(t, fixture, "attempt:finished-cleanly")
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("mark Continuation completed: %v", err)
	}
	reconciled, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "clean_exit")
	if err != nil {
		t.Fatalf("audit clean Finish: %v", err)
	}
	if reconciled.MutationID != "" || reconciled.Continuation.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("clean Finish reconciliation = %#v", reconciled)
	}
	after := mustReadAttempt(t, fixture, "attempt:finished-cleanly")
	if after.ObservedGraphRevision != before.ObservedGraphRevision {
		t.Fatalf("clean Finish changed graph revision from %d to %d", before.ObservedGraphRevision, after.ObservedGraphRevision)
	}
}

func TestCleanFinishAuditRejectsMismatchedRecordedGraphPosition(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(serviceFixture) error
	}{
		{
			name: "graph revision",
			corrupt: func(fixture serviceFixture) error {
				_, err := fixture.db.Exec(`UPDATE task_continuations SET blackboard_finish_graph_revision=blackboard_finish_graph_revision+1 WHERE id=?`, fixture.continuation.ID)
				return err
			},
		},
		{
			name: "Runtime mutation sequence",
			corrupt: func(fixture serviceFixture) error {
				_, err := fixture.db.Exec(`UPDATE task_continuations SET blackboard_finish_mutation_sequence=blackboard_finish_mutation_sequence+1 WHERE id=?`, fixture.continuation.ID)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newServiceFixture(t)
			ctx := context.Background()
			principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
			if err != nil {
				t.Fatalf("authenticate Continuation: %v", err)
			}
			finishContinuationForReconciliationAudit(t, fixture, principal)
			if err := test.corrupt(fixture); err != nil {
				t.Fatalf("inject Finish marker mismatch: %v", err)
			}
			if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusCompleted); err != nil {
				t.Fatalf("mark Continuation completed: %v", err)
			}
			if _, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "clean_exit"); err == nil {
				t.Fatal("expected clean Finish audit to reject mismatched recorded position")
			}
			continuation, err := fixture.tasks.Continuation(fixture.continuation.ID)
			if err != nil {
				t.Fatalf("read Continuation after rejected audit: %v", err)
			}
			if continuation.BlackboardReconciliationStatus != task.ReconciliationPending {
				t.Fatalf("reconciliation status = %q, want pending", continuation.BlackboardReconciliationStatus)
			}
		})
	}
}

func TestUnexpectedReconciliationBoundsEventsAndKeepsLifecycleAnchors(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "bounded-events")
	started, err := fixture.tasks.AppendContinuationEvent(fixture.task.ID, fixture.continuation.ID, task.EventKindLifecycle, task.EventPayload{"phase": "started"})
	if err != nil {
		t.Fatalf("append start Event: %v", err)
	}
	checkpointIDs := make([]string, 0, 10)
	for index := 0; index < 10; index++ {
		attempt := mustReadAttempt(t, fixture, "attempt:bounded-events")
		checkpoint, err := fixture.service.CheckpointAttempt(ctx, principal, projectinterface.CheckpointAttemptRequest{
			ProtocolVersion: projectinterface.RuntimeProtocolVersion,
			IdempotencyKey:  fmt.Sprintf("bounded-checkpoint-%02d", index),
			Attempt:         blackboard.NodeRef{ID: attempt.Node.ID}, ExpectedVersion: attempt.Node.Version,
			Summary: fmt.Sprintf("checkpoint %02d", index),
		})
		if err != nil {
			t.Fatalf("checkpoint %d: %v", index, err)
		}
		checkpointIDs = append(checkpointIDs, checkpoint.Result.Event.ID)
	}
	if _, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("mark Continuation interrupted: %v", err)
	}
	terminal, err := fixture.tasks.AppendContinuationEvent(fixture.task.ID, fixture.continuation.ID, task.EventKindLifecycle, task.EventPayload{"phase": "interrupted"})
	if err != nil {
		t.Fatalf("append terminal Event: %v", err)
	}
	if _, err := fixture.service.ReconcileContinuation(ctx, fixture.continuation.ID, "timeout"); err != nil {
		t.Fatalf("reconcile Continuation: %v", err)
	}
	attempt := mustReadAttempt(t, fixture, "attempt:bounded-events")
	projection, err := fixture.graph.CanonicalMainGraph(ctx, fixture.project.ID, attempt.ObservedGraphRevision)
	if err != nil {
		t.Fatalf("read canonical graph: %v", err)
	}
	want := append([]string{started.ID}, checkpointIDs[4:]...)
	want = append(want, terminal.ID)
	got := projectionSourceEvents(t, projection.Bytes, "attempt:bounded-events")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bounded source Events = %v, want %v", got, want)
	}
}

func TestTerminalContinuationRunsReconciliationThroughTaskPublicInterface(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Continuation: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "terminal-hook")
	fixture.tasks.SetContinuationReconciler(fixture.service)
	updated, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusInterrupted)
	if err != nil {
		t.Fatalf("make Continuation terminal: %v", err)
	}
	if updated.Status != task.StatusInterrupted {
		t.Fatalf("Continuation status = %q", updated.Status)
	}
	attempt := mustReadAttempt(t, fixture, "attempt:terminal-hook")
	if got := attempt.Node.PropertyMap["status"]; got != "interrupted" {
		t.Fatalf("Attempt status = %v, want interrupted", got)
	}
	continuation, err := fixture.tasks.Continuation(fixture.continuation.ID)
	if err != nil {
		t.Fatalf("read reconciled Continuation: %v", err)
	}
	if continuation.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("reconciliation status = %q", continuation.BlackboardReconciliationStatus)
	}
}

func mustReadAttempt(t *testing.T, fixture serviceFixture, stableKey string) blackboard.ReadNodeResult {
	t.Helper()
	result, err := fixture.graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: fixture.project.ID, NodeType: blackboard.NodeTypeAttempt, Key: stableKey,
	})
	if err != nil {
		t.Fatalf("read Attempt %s: %v", stableKey, err)
	}
	return result
}

func finishContinuationForReconciliationAudit(t *testing.T, fixture serviceFixture, principal projectinterface.Principal) {
	t.Helper()
	ctx := context.Background()
	prepareRetainEvidenceAttempt(t, fixture, principal, "finish-audit")
	open := mustReadAttempt(t, fixture, "attempt:finish-audit")
	if _, err := fixture.service.Apply(ctx, principal, projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "finish-audit-terminal",
			Operations: []blackboard.Operation{{
				OpID: "failed", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: open.Node.ID},
				Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "failed", Summary: "Audit fixture concluded the Attempt"},
			}},
		},
	}); err != nil {
		t.Fatalf("conclude Attempt: %v", err)
	}
	if _, err := fixture.service.FinishContinuation(ctx, principal, projectinterface.FinishContinuationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "finish-audit",
		Summary: "The Runtime completed its protocol.",
	}); err != nil {
		t.Fatalf("Finish Continuation: %v", err)
	}
}

func projectionSourceEvents(t *testing.T, projection []byte, stableKey string) []string {
	t.Helper()
	var document struct {
		Nodes []struct {
			StableKey         string `json:"stable_key"`
			UpdatedProvenance struct {
				SourceEventIDs []string `json:"source_event_ids"`
			} `json:"updated_provenance"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(projection, &document); err != nil {
		t.Fatalf("decode canonical graph: %v", err)
	}
	for _, node := range document.Nodes {
		if node.StableKey == stableKey {
			return node.UpdatedProvenance.SourceEventIDs
		}
	}
	t.Fatalf("canonical graph is missing %s", stableKey)
	return nil
}
