package blackboardv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestFinishContinuationRejectsOpenOwnedAttemptAndClosedDTOExtras(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, launch.Continuation.ID, "attempt:finish-open", "Truthful open work")

	for _, raw := range []string{
		`{}`,
		`{"idempotency_key":"finish-open","summary":"forbidden"}`,
		`{"idempotency_key":"finish-open","objective_outcome":{}}`,
		`{"idempotency_key":"finish-open","project_id":"smuggled"}`,
	} {
		var request blackboardv2.FinishContinuationRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Fatalf("Finish DTO accepted %s", raw)
		}
	}

	_, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-open"})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "continuation_open_attempts" {
		t.Fatalf("Finish open Attempt error = %#v", err)
	}
	if fmt.Sprint(semanticErr.Details["open_attempts"]) != "[attempt:finish-open]" {
		t.Fatalf("open Attempt details = %#v", semanticErr.Details)
	}
	continuation, err := fixture.tasks.Continuation(launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read rejected Continuation: %v", err)
	}
	if continuation.Status != task.StatusRunning {
		t.Fatalf("rejected Finish changed Continuation status to %q", continuation.Status)
	}
}

func TestFinishContinuationClosesAtExactWorkingRevisionAndDurablyReplays(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	acceptedWrite, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "finish-preserved-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:finish-preserved", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Preserved", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("write before Finish: %v", err)
	}
	workingBefore, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read Working Snapshot before Finish: %v", err)
	}
	request := blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-exact-replay"}
	finished, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request)
	if err != nil {
		t.Fatalf("Finish Continuation: %v", err)
	}
	if finished.Schema != "continuation-finish/v2" || finished.Status != "finished" || finished.Revision != acceptedWrite.Revision || finished.WorkingSnapshot.Path != ".pentest/blackboard.json" || finished.WorkingSnapshot.Revision != acceptedWrite.Revision {
		t.Fatalf("Finish result = %#v", finished)
	}
	workingAfter, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read Working Snapshot after Finish: %v", err)
	}
	if workingAfter.LastAcknowledgedRevision != workingBefore.LastAcknowledgedRevision || !bytes.Equal(workingAfter.Bytes, workingBefore.Bytes) {
		t.Fatalf("Finish changed Working Snapshot\nbefore=%#v\nafter=%#v", workingBefore, workingAfter)
	}

	for index, batch := range allLaterWriteBatches() {
		_, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, batch)
		if !isV2ErrorCode(err, "closed_continuation") {
			t.Errorf("later write %d error = %#v", index, err)
		}
	}
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "later-checkpoint", Key: "attempt:any", Version: 1, Summary: "later",
	}); !isV2ErrorCode(err, "closed_continuation") {
		t.Errorf("later checkpoint error = %#v", err)
	}
	if _, err := fixture.board.RetainEvidenceForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "later-evidence", Key: "evidence:later", Attempt: "attempt:later", SourcePath: "later.txt", ArtifactType: "text", Summary: "later",
	}); !isV2ErrorCode(err, "closed_continuation") {
		t.Errorf("later evidence error = %#v", err)
	}

	replay, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request)
	if err != nil || !bytes.Equal(mustJSON(t, replay), mustJSON(t, finished)) {
		t.Fatalf("exact Finish replay = %#v, %v; want %#v", replay, err, finished)
	}
	if _, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-altered"}); !isV2ErrorCode(err, "finish_conflict") {
		t.Fatalf("altered Finish replay error = %#v", err)
	}

	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen Store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedReplay, err := blackboardv2.NewService(reopened).FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request)
	if err != nil || !bytes.Equal(mustJSON(t, restartedReplay), mustJSON(t, finished)) {
		t.Fatalf("Finish replay after restart = %#v, %v", restartedReplay, err)
	}
}

func TestFinishContinuationBindsReplayToOwningPrincipal(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	owner := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(owner.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start owner: %v", err)
	}
	request := blackboardv2.FinishContinuationRequest{IdempotencyKey: "principal-bound-finish"}
	if _, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, owner.Continuation.ID, request); err != nil {
		t.Fatalf("Finish owner: %v", err)
	}

	peerTask, err := fixture.tasks.Create(task.CreateRequest{ProjectID: fixture.project.ID, Goal: "peer", RuntimeProfileID: fixture.profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create peer Task: %v", err)
	}
	peer, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: peerTask.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: "codex", Runner: task.RunnerSandbox, RuntimeConfig: map[string]any{"provider": "codex"},
	})
	if err != nil {
		t.Fatalf("create peer Continuation: %v", err)
	}
	if _, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, peer.Continuation.ID, request); !isV2ErrorCode(err, "authority_denied") {
		t.Fatalf("different principal replay error = %#v", err)
	}
	if _, err := fixture.board.FinishContinuation(context.Background(), "another-project", owner.Continuation.ID, request); !isV2ErrorCode(err, "authority_denied") {
		t.Fatalf("different Project replay error = %#v", err)
	}
}

func TestFinishPrecommitCrashLeavesNoReceiptOrClosedCredential(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	injected := errors.New("injected Finish crash")
	fixture.board.SetFinishFailureInjector(func(point blackboardv2.FinishFailurePoint) error {
		if point == blackboardv2.FinishFailureBeforeCommit {
			return injected
		}
		return nil
	})
	request := blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-precommit-crash"}
	if _, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request); !errors.Is(err, injected) {
		t.Fatalf("injected Finish error = %v", err)
	}
	continuation, err := fixture.tasks.Continuation(launch.Continuation.ID)
	if err != nil || continuation.Status != task.StatusRunning {
		t.Fatalf("crashed Finish closed credential: %#v, %v", continuation, err)
	}
	var receipts int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_continuation_finishes WHERE continuation_id=?`, launch.Continuation.ID).Scan(&receipts); err != nil {
		t.Fatalf("count crashed Finish receipts: %v", err)
	}
	if receipts != 0 {
		t.Fatalf("crashed Finish retained %d receipts", receipts)
	}
	fixture.board.SetFinishFailureInjector(nil)
	if _, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request); err != nil {
		t.Fatalf("retry Finish after crash: %v", err)
	}
}

func TestResumeRequiresClosedContinuationAndRevokesOldWrites(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	first := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start first Continuation: %v", err)
	}
	request := blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: fixture.task.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: "codex", Runner: task.RunnerSandbox, RuntimeConfig: map[string]any{"provider": "codex", "resume": true},
	}
	if _, err := fixture.continuity.CreateContinuation(context.Background(), request); !errors.Is(err, task.ErrActiveContinuation) {
		t.Fatalf("resume while current Continuation active = %v, want active conflict", err)
	}
	if _, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, first.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-before-resume"}); err != nil {
		t.Fatalf("Finish before resume: %v", err)
	}
	resumed, err := fixture.continuity.CreateContinuation(context.Background(), request)
	if err != nil {
		t.Fatalf("resume after Finish: %v", err)
	}
	if resumed.Continuation.ID == first.Continuation.ID || resumed.Continuation.Number != first.Continuation.Number+1 {
		t.Fatalf("resume reused closed identity: first=%#v resumed=%#v", first.Continuation, resumed.Continuation)
	}
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, first.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "old-credential-after-resume", Changes: []blackboardv2.Change{},
	}); !isV2ErrorCode(err, "closed_continuation") {
		t.Fatalf("closed credential write after resume = %#v", err)
	}
}

func TestFinishRacesWithEveryRuntimeWriteAsOneAtomicWinner(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		fixture := newContinuityFixture(t)
		launch := fixture.launch(t)
		if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
			t.Fatalf("iteration %d start: %v", iteration, err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var finishErr, writeErr error
		go func() {
			defer wg.Done()
			<-start
			_, finishErr = fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: fmt.Sprintf("finish-race-%d", iteration)})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, writeErr = fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: fmt.Sprintf("write-race-%d", iteration),
				Changes: []blackboardv2.Change{{Op: "create", Key: fmt.Sprintf("entity:race-%d", iteration), Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Race", ScopeStatus: "in_scope"}}},
			})
		}()
		close(start)
		wg.Wait()
		if finishErr != nil {
			t.Fatalf("iteration %d Finish lost to non-Attempt write: %v (write=%v)", iteration, finishErr, writeErr)
		}
		if writeErr != nil && !isV2ErrorCode(writeErr, "closed_continuation") {
			t.Fatalf("iteration %d write error = %#v", iteration, writeErr)
		}
		current, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
		if err != nil {
			t.Fatalf("iteration %d project current: %v", iteration, err)
		}
		finished, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: fmt.Sprintf("finish-race-%d", iteration)})
		if err != nil || finished.Revision != current.Snapshot.Revision {
			t.Fatalf("iteration %d atomic revision: finish=%#v current=%d err=%v", iteration, finished, current.Snapshot.Revision, err)
		}
		_ = fixture.db.Close()
	}
}

func TestFinishRacesWithAttemptTerminalizationWithoutClosingOpenWork(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		fixture := newContinuityFixture(t)
		launch := fixture.launch(t)
		if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
			t.Fatalf("iteration %d start: %v", iteration, err)
		}
		key := fmt.Sprintf("attempt:finish-terminal-race-%d", iteration)
		seedCheckpointAttempt(t, fixture.board, fixture.project.ID, launch.Continuation.ID, key, "Open before race")
		finishRequest := blackboardv2.FinishContinuationRequest{IdempotencyKey: fmt.Sprintf("finish-terminal-race-%d", iteration)}
		terminalBatch := blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: fmt.Sprintf("terminal-race-%d", iteration),
			Changes: []blackboardv2.Change{{Op: "transition", Key: key, Version: 1, Status: "failed", Summary: "Concluded before Finish"}},
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var finishErr, transitionErr error
		go func() {
			defer wg.Done()
			<-start
			_, finishErr = fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, finishRequest)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, transitionErr = fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, terminalBatch)
		}()
		close(start)
		wg.Wait()
		if transitionErr != nil {
			t.Fatalf("iteration %d terminalization error = %v", iteration, transitionErr)
		}
		if finishErr != nil && !isV2ErrorCode(finishErr, "continuation_open_attempts") {
			t.Fatalf("iteration %d Finish error = %#v", iteration, finishErr)
		}
		if _, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, finishRequest); err != nil {
			t.Fatalf("iteration %d Finish retry after terminal truth: %v", iteration, err)
		}
		_ = fixture.db.Close()
	}
}

func TestResumePinsCurrentTruthWithoutLegacyConclusionOrHandoffState(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	fixture.tasks.SetContinuationReconciler(fixture.board)
	first := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start first Continuation: %v", err)
	}
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, first.Continuation.ID, "attempt:resume-truth", "Open work before checkpoint")
	checkpoint := blackboardv2.CheckpointAttemptRequest{IdempotencyKey: "resume-checkpoint", Key: "attempt:resume-truth", Version: 1, Summary: "Truthful open work checkpoint"}
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, first.Continuation.ID, checkpoint); err != nil {
		t.Fatalf("checkpoint open work: %v", err)
	}
	if _, err := fixture.tasks.AppendEvent(fixture.task.ID, task.EventKindConversation, task.EventPayload{"message": "keep normal conversation"}); err != nil {
		t.Fatalf("append conversation: %v", err)
	}
	if _, err := fixture.tasks.AppendEvent(fixture.task.ID, task.EventKindSteering, task.EventPayload{"phase": "steering_requested", "directive": "retain unconsumed steering"}); err != nil {
		t.Fatalf("append steering: %v", err)
	}

	// An open Attempt truthfully prevents a clean Finish. Simulate the normal
	// daemon-observed interruption, which preserves its checkpoint semantics.
	if _, err := fixture.tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt first Continuation: %v", err)
	}
	currentBeforeResume, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project current before resume: %v", err)
	}
	resumed, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: fixture.task.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: "codex", Runner: task.RunnerSandbox, RuntimeConfig: map[string]any{"provider": "codex", "resume": true},
	})
	if err != nil {
		t.Fatalf("resume Continuation: %v", err)
	}
	if resumed.Continuation.ID == first.Continuation.ID || resumed.Continuation.Number != first.Continuation.Number+1 || !bytes.Equal(resumed.Snapshot, currentBeforeResume.Bytes) {
		t.Fatalf("resume did not create fresh current pin: first=%#v resumed=%#v", first.Continuation, resumed.Continuation)
	}
	history, err := fixture.board.ReadHistory(context.Background(), fixture.project.ID, checkpoint.Key, blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read checkpoint history after resume: %v", err)
	}
	var attemptVersions []blackboardv2.HistoryItem
	for _, item := range history.Items {
		if item.Kind == "record" {
			attemptVersions = append(attemptVersions, item)
		}
	}
	last := attemptVersions[len(attemptVersions)-1]
	if len(attemptVersions) != 3 || last.Record.Status != "interrupted" || last.Record.Summary != checkpoint.Summary {
		t.Fatalf("resume lost checkpoint Semantic History: %#v", history.Items)
	}
	events, err := fixture.tasks.Events(fixture.task.ID)
	if err != nil {
		t.Fatalf("read Task conversation and steering: %v", err)
	}
	if len(events) != 2 || events[0].Kind != task.EventKindConversation || events[1].Kind != task.EventKindSteering || events[1].Payload["directive"] != "retain unconsumed steering" {
		t.Fatalf("resume changed Task-local surfaces: %#v", events)
	}
	for _, table := range []string{"task_summary_versions", "blackboard_graph_mutations", "blackboard_graph_operations"} {
		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count forbidden table %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("resume/Finish v2 touched forbidden legacy table %s (%d rows)", table, count)
		}
	}
	for _, forbidden := range []string{"goal", "conclusion", "handoff", "task_summary", "objective_outcome", "mechanical_handoff"} {
		if bytes.Contains(bytes.ToLower(resumed.Snapshot), []byte(forbidden)) {
			t.Errorf("resume Snapshot copied forbidden %q state: %s", forbidden, resumed.Snapshot)
		}
	}
}

type forbiddenGoalProjector struct{ calls int }

func (projector *forbiddenGoalProjector) ProjectTaskGoal(string) error {
	projector.calls++
	return errors.New("Blackboard v2 must not project Goal records")
}

func TestBlackboardV2ContinuationLaunchNeverProjectsLegacyGoal(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	projector := &forbiddenGoalProjector{}
	fixture.tasks.SetGoalProjector(projector)
	if launch := fixture.launch(t); launch.Continuation.ID == "" {
		t.Fatal("v2 Continuation launch returned no Continuation")
	}
	if projector.calls != 0 {
		t.Fatalf("v2 Continuation launch projected Goal %d times", projector.calls)
	}
}

func TestFinishImplementationHasNoForbiddenLegacyStateDependency(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Finish test source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "finish.go"))
	if err != nil {
		t.Fatalf("read Finish implementation: %v", err)
	}
	for _, forbidden := range []string{
		"task_summary_versions", "objective_outcome", "mechanical_handoff",
		"blackboard_graph_mutations", "blackboard_graph_operations", "goal",
	} {
		if bytes.Contains(bytes.ToLower(source), []byte(forbidden)) {
			t.Errorf("Finish implementation depends on forbidden legacy state %q", forbidden)
		}
	}
}

func allLaterWriteBatches() []blackboardv2.ChangeBatch {
	return []blackboardv2.ChangeBatch{
		{Schema: "semantic-change-batch/v2", IdempotencyKey: "later-create", Changes: []blackboardv2.Change{{Op: "create", Key: "entity:later", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Later", ScopeStatus: "in_scope"}}}},
		{Schema: "semantic-change-batch/v2", IdempotencyKey: "later-update", Changes: []blackboardv2.Change{{Op: "update", Key: "entity:finish-preserved", Version: 1, Type: "entity", Record: blackboardv2.EntityPatch{Name: stringPointer("Later")}}}},
		{Schema: "semantic-change-batch/v2", IdempotencyKey: "later-relate", Changes: []blackboardv2.Change{{Op: "relate", From: "entity:finish-preserved", Relation: "part_of", To: "entity:later"}}},
		{Schema: "semantic-change-batch/v2", IdempotencyKey: "later-unrelate", Changes: []blackboardv2.Change{{Op: "unrelate", From: "entity:finish-preserved", Relation: "part_of", To: "entity:later", Version: 1}}},
		{Schema: "semantic-change-batch/v2", IdempotencyKey: "later-merge", Changes: []blackboardv2.Change{{Op: "merge", Source: "fact:a", SourceVersion: 1, Canonical: "fact:b", CanonicalVersion: 1}}},
		{Schema: "semantic-change-batch/v2", IdempotencyKey: "later-supersede", Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "entity:later", ReplacementVersion: 1, Replaced: "entity:finish-preserved", ReplacedVersion: 1}}},
	}
}

func stringPointer(value string) *string { return &value }

func isV2ErrorCode(err error, code string) bool {
	var semanticErr *blackboardv2.Error
	return errors.As(err, &semanticErr) && semanticErr.Code == code
}
