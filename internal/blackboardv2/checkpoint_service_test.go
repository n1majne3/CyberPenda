package blackboardv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestCheckpointAttemptVersionsOnlyOwnedOpenAttemptAndDurablyReplays(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, launch.Continuation.ID, "attempt:checkpoint", "Initial truthful summary")

	request := blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "checkpoint-owned-attempt",
		Key:            "attempt:checkpoint",
		Version:        1,
		Summary:        "Mapped the reachable authentication surface",
	}
	accepted, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request)
	if err != nil {
		t.Fatalf("checkpoint owned Attempt: %v", err)
	}
	assertChangeRecords(t, accepted, 4, [][]any{{"attempt:checkpoint", float64(2)}})

	current, err := fixture.board.ReadCurrent(context.Background(), fixture.project.ID, request.Key)
	if err != nil {
		t.Fatalf("read checkpointed Attempt: %v", err)
	}
	if current.Version != 2 || current.Record.Status != "open" || current.Record.Summary != request.Summary {
		t.Fatalf("checkpointed Attempt = %#v", current)
	}
	history, err := fixture.board.ReadHistory(context.Background(), fixture.project.ID, request.Key, blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read checkpoint history: %v", err)
	}
	if len(history.Items) != 1 || history.Items[0].Version != 1 || history.Items[0].Record.Summary != "Initial truthful summary" {
		t.Fatalf("checkpoint history = %#v", history.Items)
	}
	working, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read acknowledged Working Snapshot: %v", err)
	}
	if working.LastAcknowledgedRevision != accepted.Revision || !bytes.Contains(working.Bytes, []byte(request.Summary)) {
		t.Fatalf("Working Snapshot = revision %d bytes %s", working.LastAcknowledgedRevision, working.Bytes)
	}

	replay, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request)
	if err != nil {
		t.Fatalf("exact checkpoint replay: %v", err)
	}
	if !bytes.Equal(mustJSON(t, replay), mustJSON(t, accepted)) {
		t.Fatalf("checkpoint replay changed result\nfirst=%s\nreplay=%s", mustJSON(t, accepted), mustJSON(t, replay))
	}

	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen checkpoint service: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedReplay, err := blackboardv2.NewService(reopened).CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request)
	if err != nil {
		t.Fatalf("checkpoint replay after reopen: %v", err)
	}
	if !bytes.Equal(mustJSON(t, restartedReplay), mustJSON(t, accepted)) {
		t.Fatalf("restarted checkpoint replay changed result")
	}
}

func TestCheckpointAttemptRejectsClosedShapeBoundsOwnershipAndStaleState(t *testing.T) {
	for _, raw := range []string{
		`{"idempotency_key":"checkpoint","key":"attempt:a","version":1,"summary":"compact","raw_output":"forbidden"}`,
		`{"idempotency_key":"checkpoint","key":"attempt:a","version":1,"summary":"compact","provenance":{"task_id":"forbidden"}}`,
		`{"idempotency_key":"checkpoint","key":"attempt:a","version":1,"summary":"compact","project_id":"forbidden"}`,
	} {
		var request blackboardv2.CheckpointAttemptRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Fatalf("checkpoint decoded non-closed request: %s", raw)
		}
	}

	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(owner.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start owner Continuation: %v", err)
	}
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, owner.Continuation.ID, "attempt:guarded", "Initial guarded summary")

	peerTask, err := fixture.tasks.Create(task.CreateRequest{ProjectID: fixture.project.ID, Goal: "peer", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create peer Task: %v", err)
	}
	peer, err := fixture.tasks.CreateContinuation(peerTask.ID, "profile-peer", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create peer Continuation: %v", err)
	}
	request := blackboardv2.CheckpointAttemptRequest{IdempotencyKey: "guarded", Key: "attempt:guarded", Version: 1, Summary: "Compact owner checkpoint"}
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, peer.ID, request); !isSemanticCode(err, "authority_denied") {
		t.Fatalf("wrong-owner checkpoint error = %#v", err)
	}
	foreignProject, err := project.NewService(fixture.db).Create("Foreign checkpoint", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign Project: %v", err)
	}
	foreignTask, err := fixture.tasks.Create(task.CreateRequest{ProjectID: foreignProject.ID, Goal: "foreign", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreign, err := fixture.tasks.CreateContinuation(foreignTask.ID, "profile-foreign", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, foreign.ID, request); !isSemanticCode(err, "authority_denied") {
		t.Fatalf("cross-Project checkpoint error = %#v", err)
	}

	workingBeforeRejects, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read Working Snapshot before rejected checkpoints: %v", err)
	}

	stale := request
	stale.IdempotencyKey = "stale"
	stale.Version = 2
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, stale); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale checkpoint error = %#v", err)
	}
	oversized := request
	oversized.IdempotencyKey = "oversized"
	oversized.Summary = strings.Repeat("界", 342)
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, oversized); !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("oversized checkpoint error = %#v", err)
	}
	invalidUTF8 := request
	invalidUTF8.IdempotencyKey = "invalid-utf8"
	invalidUTF8.Summary = string([]byte{0xff})
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, invalidUTF8); !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("invalid UTF-8 checkpoint error = %#v", err)
	}
	workingAfterRejects, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read Working Snapshot after rejected checkpoints: %v", err)
	}
	if workingAfterRejects.LastAcknowledgedRevision != workingBeforeRejects.LastAcknowledgedRevision || !bytes.Equal(workingAfterRejects.Bytes, workingBeforeRejects.Bytes) {
		t.Fatalf("rejected checkpoint acknowledged Working Snapshot: before=%d after=%d", workingBeforeRejects.LastAcknowledgedRevision, workingAfterRejects.LastAcknowledgedRevision)
	}

	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, owner.Continuation.ID, "attempt:utf8-boundary", "Initial UTF-8 boundary summary")
	boundarySummary := strings.Repeat("界", 341) + "a"
	if len([]byte(boundarySummary)) != 1024 {
		t.Fatalf("test boundary summary bytes = %d", len([]byte(boundarySummary)))
	}
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "utf8-boundary", Key: "attempt:utf8-boundary", Version: 1, Summary: boundarySummary,
	}); err != nil {
		t.Fatalf("accept 1024-byte checkpoint: %v", err)
	}
	boundary, err := fixture.board.ReadCurrent(context.Background(), fixture.project.ID, "attempt:utf8-boundary")
	if err != nil || boundary.Record.Summary != boundarySummary {
		t.Fatalf("1024-byte checkpoint was truncated: %#v, %v", boundary, err)
	}

	accepted, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, request)
	if err != nil {
		t.Fatalf("accept checkpoint before terminal transition: %v", err)
	}
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "terminal-guarded",
		Changes: []blackboardv2.Change{{Op: "transition", Key: request.Key, Version: 2, Status: "failed", Summary: "The guarded test did not succeed"}},
	}); err != nil {
		t.Fatalf("terminalize checkpointed Attempt: %v", err)
	}
	terminalRequest := request
	terminalRequest.IdempotencyKey = "terminal-checkpoint"
	terminalRequest.Version = 3
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, terminalRequest); !isSemanticCode(err, "not_found") {
		t.Fatalf("terminal Attempt checkpoint error = %#v", err)
	}
	replay, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, request)
	if err != nil || !bytes.Equal(mustJSON(t, replay), mustJSON(t, accepted)) {
		t.Fatalf("terminal Attempt exact replay = %#v, %v", replay, err)
	}
	if _, err := fixture.tasks.UpdateContinuationStatus(owner.Continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("close checkpoint owner Continuation: %v", err)
	}
	closedReplay, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, request)
	if err != nil || !bytes.Equal(mustJSON(t, closedReplay), mustJSON(t, accepted)) {
		t.Fatalf("closed Continuation exact checkpoint replay = %#v, %v", closedReplay, err)
	}

	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, peer.ID, "attempt:closed", "Still open when the Continuation closes")
	if _, err := fixture.tasks.UpdateContinuationStatus(peer.ID, task.StatusCompleted); err != nil {
		t.Fatalf("close peer Continuation: %v", err)
	}
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, peer.ID, blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "closed-new", Key: "attempt:closed", Version: 1, Summary: "Must be rejected",
	}); !isSemanticCode(err, "closed_continuation") {
		t.Fatalf("closed Continuation checkpoint error = %#v", err)
	}
	changed := request
	changed.Summary = "Changed retry must conflict"
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, changed); !isSemanticCode(err, "idempotency_conflict") {
		t.Fatalf("changed checkpoint retry error = %#v", err)
	}
}

func TestTerminalTaskCallbackAtomicallyReconcilesOnlyUnexpectedOwnedAttempts(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	fixture.tasks.SetContinuationReconciler(fixture.board)
	if _, err := fixture.tasks.UpdateContinuationStatus(owner.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start owner Continuation: %v", err)
	}
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, owner.Continuation.ID, "attempt:checkpointed", "Initial checkpointed work")
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, owner.Continuation.ID, "attempt:fallback", "Truthful initial work summary")
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "before-interruption", Key: "attempt:checkpointed", Version: 1, Summary: "Enumerated two valid authentication paths",
	}); err != nil {
		t.Fatalf("checkpoint before interruption: %v", err)
	}

	peerTask, err := fixture.tasks.Create(task.CreateRequest{ProjectID: fixture.project.ID, Goal: "peer", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create peer Task: %v", err)
	}
	peer, err := fixture.tasks.CreateContinuation(peerTask.ID, "profile-peer", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create peer Continuation: %v", err)
	}
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, peer.ID, "attempt:peer", "Peer remains open")

	if _, err := fixture.tasks.UpdateContinuationStatus(owner.Continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("terminal callback reconciliation: %v", err)
	}
	for key, wantSummary := range map[string]string{
		"attempt:checkpointed": "Enumerated two valid authentication paths",
		"attempt:fallback":     "Truthful initial work summary",
	} {
		history, err := fixture.board.ReadHistory(context.Background(), fixture.project.ID, key, blackboardv2.HistoryOptions{})
		if err != nil {
			t.Fatalf("read reconciled %s: %v", key, err)
		}
		var terminalSummary string
		for _, item := range history.Items {
			if item.Record != nil && item.Record.Status == "interrupted" {
				terminalSummary = item.Record.Summary
			}
		}
		if terminalSummary != wantSummary || len([]byte(terminalSummary)) > 1024 {
			t.Fatalf("reconciled %s history = %#v", key, history.Items)
		}
	}
	if peerCurrent, err := fixture.board.ReadCurrent(context.Background(), fixture.project.ID, "attempt:peer"); err != nil || peerCurrent.Record.Status != "open" {
		t.Fatalf("peer Attempt changed = %#v, %v", peerCurrent, err)
	}
	marked, err := fixture.tasks.Continuation(owner.Continuation.ID)
	if err != nil {
		t.Fatalf("read reconciled Continuation: %v", err)
	}
	if marked.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("reconciliation marker = %q", marked.BlackboardReconciliationStatus)
	}
}

func TestCheckpointAndUnexpectedEndRaceConvergesWithoutDuplicateAttemptVersion(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	fixture.tasks.SetContinuationReconciler(fixture.board)
	if _, err := fixture.tasks.UpdateContinuationStatus(owner.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start racing Continuation: %v", err)
	}
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, owner.Continuation.ID, "attempt:race", "Initial race summary")

	start := make(chan struct{})
	var checkpointErr, terminalErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, checkpointErr = fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, blackboardv2.CheckpointAttemptRequest{
			IdempotencyKey: "race-checkpoint", Key: "attempt:race", Version: 1, Summary: "Checkpoint won before interruption",
		})
	}()
	go func() {
		defer wait.Done()
		<-start
		_, terminalErr = fixture.tasks.UpdateContinuationStatus(owner.Continuation.ID, task.StatusInterrupted)
	}()
	close(start)
	wait.Wait()
	if terminalErr != nil {
		t.Fatalf("terminal race failed: %v", terminalErr)
	}
	if checkpointErr != nil && !isSemanticCode(checkpointErr, "closed_continuation") {
		t.Fatalf("checkpoint race error = %#v", checkpointErr)
	}
	history, err := fixture.board.ReadHistory(context.Background(), fixture.project.ID, "attempt:race", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read raced Attempt history: %v", err)
	}
	terminalCount := 0
	recordCount := 0
	for _, item := range history.Items {
		if item.Record != nil {
			recordCount++
			if item.Record.Status == "interrupted" {
				terminalCount++
			}
		}
	}
	if terminalCount != 1 || recordCount < 2 || recordCount > 3 {
		t.Fatalf("raced Attempt history = %#v", history.Items)
	}
}

func TestRetainedEvidenceSurvivesCheckpointInterruptionBoundaryExactlyOnce(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence checkpoint boundary")
	fixture.writeSource(t, "boundary.txt", "retained proof before uncertain checkpoint\n")
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-before-checkpoint", Key: "evidence:checkpoint-boundary", Attempt: "attempt:evidence",
		SourcePath: "boundary.txt", ArtifactType: "text", Summary: "Proof retained before interruption",
	}
	retained, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
	if err != nil {
		t.Fatalf("retain Evidence before checkpoint: %v", err)
	}
	checkpointRequest := blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "checkpoint-after-evidence", Key: "attempt:evidence", Version: 1, Summary: "Evidence was retained before Runtime interruption",
	}
	checkpointed, err := fixture.service.CheckpointAttemptForContinuation(context.Background(), fixture.projectID, fixture.continuationID, checkpointRequest)
	if err != nil {
		t.Fatalf("checkpoint after Evidence: %v", err)
	}
	tasks := task.NewService(fixture.db, project.NewService(fixture.db))
	tasks.SetContinuationReconciler(fixture.service)
	if _, err := tasks.UpdateContinuationStatus(fixture.continuationID, task.StatusInterrupted); err != nil {
		t.Fatalf("reconcile Evidence-producing Continuation: %v", err)
	}

	evidence, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, request.Key)
	if err != nil {
		t.Fatalf("read retained Evidence after interruption: %v", err)
	}
	if evidence.Type != "evidence" || evidence.Version != 1 || evidence.Record.Summary != request.Summary {
		t.Fatalf("retained Evidence changed = %#v", evidence)
	}
	retainedReplay, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
	if err != nil || !bytes.Equal(mustJSON(t, retainedReplay), mustJSON(t, retained)) {
		t.Fatalf("retained Evidence replay after interruption = %#v, %v", retainedReplay, err)
	}
	checkpointReplay, err := fixture.service.CheckpointAttemptForContinuation(context.Background(), fixture.projectID, fixture.continuationID, checkpointRequest)
	if err != nil || !bytes.Equal(mustJSON(t, checkpointReplay), mustJSON(t, checkpointed)) {
		t.Fatalf("checkpoint replay after interruption = %#v, %v", checkpointReplay, err)
	}
	history, err := fixture.service.ReadHistory(context.Background(), fixture.projectID, "attempt:evidence", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read interrupted Evidence Attempt: %v", err)
	}
	produced := 0
	for _, item := range history.Items {
		if item.Relation == "produced" && item.To == request.Key {
			produced++
		}
	}
	if produced != 1 {
		t.Fatalf("produced Evidence relations = %d, history=%#v", produced, history.Items)
	}
}

func seedCheckpointAttempt(t *testing.T, service *blackboardv2.Service, projectID, continuationID, key, summary string) {
	t.Helper()
	target := "objective:" + strings.TrimPrefix(key, "attempt:")
	_, err := service.ApplyForContinuation(context.Background(), projectID, continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-" + key,
		Changes: []blackboardv2.Change{
			{Op: "create", Key: target, Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: fmt.Sprintf("Test target for %s", key)}},
			{Op: "create", Key: key, Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: summary}},
			{Op: "relate", From: key, Relation: "tests", To: target},
		},
	})
	if err != nil {
		t.Fatalf("seed checkpoint Attempt %s: %v", key, err)
	}
}
