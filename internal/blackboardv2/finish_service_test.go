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
	"strings"
	"sync"
	"testing"

	"pentest/internal/adapters"
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

func TestFinishContinuationSynchronizesExactCurrentProjectRuntimeSnapshot(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}

	operatorWrite, err := fixture.board.Apply(context.Background(), fixture.project.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "operator-current-before-finish",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:operator-current", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Operator current", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("operator write before Finish: %v", err)
	}
	stale, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read pre-Finish acknowledged Snapshot: %v", err)
	}
	if stale.LastAcknowledgedRevision == operatorWrite.Revision {
		t.Fatalf("test requires operator write beyond Continuation acknowledgement: %#v", stale)
	}
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project exact current Snapshot: %v", err)
	}

	finished, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-current-project"})
	if err != nil {
		t.Fatalf("Finish current Project state: %v", err)
	}
	working, err := fixture.continuity.ReadWorkingSnapshot(context.Background(), launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read synchronized closed Snapshot: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json"))
	if err != nil {
		t.Fatalf("read final Working Snapshot: %v", err)
	}
	if finished.Revision != want.Snapshot.Revision || finished.WorkingSnapshot.Revision != want.Snapshot.Revision || working.LastAcknowledgedRevision != want.Snapshot.Revision || !bytes.Equal(working.Bytes, want.Bytes) || !bytes.Equal(onDisk, want.Bytes) {
		t.Fatalf("Finish did not close exact current Project bytes: finish=%#v working=%#v want_revision=%d\ndisk=%s\nwant=%s", finished, working, want.Snapshot.Revision, onDisk, want.Bytes)
	}
}

func TestOldFinishReplayNeverPublishesOverNewerContinuationWorkingSnapshot(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	first := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start first Continuation: %v", err)
	}
	request := blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-first-before-new-owner"}
	finished, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, first.Continuation.ID, request)
	if err != nil {
		t.Fatalf("Finish first Continuation: %v", err)
	}
	second, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: fixture.task.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: "codex", Runner: task.RunnerSandbox, RuntimeConfig: map[string]any{"provider": "codex", "resume": true},
	})
	if err != nil {
		t.Fatalf("create second Continuation: %v", err)
	}
	if _, err := fixture.tasks.UpdateContinuationStatus(second.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start second Continuation: %v", err)
	}
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, second.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "second-owner-current-bytes",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:second-owner", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Second owner", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("advance second Working Snapshot: %v", err)
	}
	workingPath := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json")
	before, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("read second Working Snapshot: %v", err)
	}

	replay, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, first.Continuation.ID, request)
	if err != nil || !bytes.Equal(mustJSON(t, replay), mustJSON(t, finished)) {
		t.Fatalf("replay first Finish = %#v, %v; want %#v", replay, err, finished)
	}
	after, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("reread second Working Snapshot: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("old Finish replay replaced newer owner bytes\nbefore=%s\nafter=%s", before, after)
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

func TestFinishPublishesExactCommittedWorkingSnapshotBeforeClosing(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	launch := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if err := fixture.continuity.MaterializeWorkingSnapshot(context.Background(), launch.Continuation.ID); err != nil {
		t.Fatalf("materialize initial Working Snapshot: %v", err)
	}
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fixture.board.SetSnapshotPublicationInjector(func(point blackboardv2.SnapshotPublicationPoint, continuationID string) error {
		if point == blackboardv2.SnapshotPublicationAfterCommit && continuationID == launch.Continuation.ID {
			once.Do(func() { close(reached) })
			<-release
		}
		return nil
	})
	writeDone := make(chan error, 1)
	go func() {
		_, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "pending-publication-write",
			Changes: []blackboardv2.Change{{Op: "create", Key: "entity:pending-publication", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Committed current bytes", ScopeStatus: "in_scope"}}},
		})
		writeDone <- err
	}()
	<-reached
	finished, err := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-pending-publication"})
	if err != nil {
		close(release)
		t.Fatalf("Finish while publication pending: %v", err)
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("committed writer publication: %v", err)
	}
	current, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project current Snapshot: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", ".pentest", "blackboard.json"))
	if err != nil {
		t.Fatalf("read closed Working Snapshot: %v", err)
	}
	if finished.Revision != current.Snapshot.Revision || finished.WorkingSnapshot.Revision != current.Snapshot.Revision || !bytes.Equal(onDisk, current.Bytes) {
		t.Fatalf("Finish closed stale publication: finish=%#v current=%d\ndisk=%s\ncurrent=%s", finished, current.Snapshot.Revision, onDisk, current.Bytes)
	}
}

type finishEvidenceBarrier struct {
	point   blackboardv2.EvidenceFailurePoint
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (barrier *finishEvidenceBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != barrier.point {
		return nil
	}
	barrier.once.Do(func() { close(barrier.reached) })
	<-barrier.release
	return nil
}

func TestFinishAndEvidenceReservationHaveOneAtomicWinner(t *testing.T) {
	for _, test := range []struct {
		name  string
		point blackboardv2.EvidenceFailurePoint
	}{
		{name: "Finish wins before reservation", point: blackboardv2.EvidenceFailureBeforeReservation},
		{name: "reservation wins before Finish", point: blackboardv2.EvidenceFailureBeforeFilePublish},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newContinuityFixture(t)
			t.Cleanup(func() { _ = fixture.db.Close() })
			barrier := &finishEvidenceBarrier{point: test.point, reached: make(chan struct{}), release: make(chan struct{})}
			artifactRoot := filepath.Join(filepath.Dir(fixture.runtimeRoot), "retained")
			if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
				t.Fatalf("create Artifact Root: %v", err)
			}
			fixture.board = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: artifactRoot, Failures: barrier})
			fixture.continuity = blackboardv2.NewContinuityService(fixture.db, fixture.board, fixture.tasks, fixture.runtimeRoot)
			launch := fixture.launch(t)
			if _, err := fixture.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
				t.Fatalf("start Continuation: %v", err)
			}
			seedCheckpointAttempt(t, fixture.board, fixture.project.ID, launch.Continuation.ID, "attempt:evidence-finish-race", "Retaining proof")
			workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
			if err := os.MkdirAll(workdir, 0o700); err != nil {
				t.Fatalf("create workdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("race proof"), 0o600); err != nil {
				t.Fatalf("write proof: %v", err)
			}
			request := blackboardv2.RetainEvidenceRequest{IdempotencyKey: "finish-evidence-race", Key: "evidence:finish-race", Attempt: "attempt:evidence-finish-race", SourcePath: "proof.txt", ArtifactType: "text", Summary: "Retained race proof"}
			retainDone := make(chan struct {
				result blackboardv2.ChangeResult
				err    error
			}, 1)
			go func() {
				result, err := fixture.board.RetainEvidenceForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, request)
				retainDone <- struct {
					result blackboardv2.ChangeResult
					err    error
				}{result: result, err: err}
			}()
			select {
			case <-barrier.reached:
			case retained := <-retainDone:
				t.Fatalf("retain exited before %s barrier: %v", test.point, retained.err)
			}
			terminal, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-evidence-finish-race",
				Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:evidence-finish-race", Version: 1, Status: "failed", Summary: "Retention race concluded"}},
			})
			if err != nil {
				close(barrier.release)
				t.Fatalf("terminalize producing Attempt: %v", err)
			}
			finish, finishErr := fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-evidence-atomic"})
			if test.point == blackboardv2.EvidenceFailureBeforeReservation {
				if finishErr != nil || finish.Revision != terminal.Revision {
					close(barrier.release)
					t.Fatalf("Finish winner = %#v, %v; terminal revision=%d", finish, finishErr, terminal.Revision)
				}
				close(barrier.release)
				retained := <-retainDone
				if !isV2ErrorCode(retained.err, "closed_continuation") {
					t.Fatalf("paused retain after Finish = %#v", retained.err)
				}
				var reservations int
				if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE continuation_id=?`, launch.Continuation.ID).Scan(&reservations); err != nil || reservations != 0 {
					t.Fatalf("retain reserved after Finish: count=%d err=%v", reservations, err)
				}
				return
			}
			if !isV2ErrorCode(finishErr, "continuation_pending_writes") {
				close(barrier.release)
				t.Fatalf("Finish with reserved Evidence = %#v", finishErr)
			}
			close(barrier.release)
			retained := <-retainDone
			if retained.err != nil {
				t.Fatalf("reserved Evidence completion: %v", retained.err)
			}
			finish, err = fixture.board.FinishContinuation(context.Background(), fixture.project.ID, launch.Continuation.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-evidence-atomic"})
			if err != nil || finish.Revision != retained.result.Revision {
				t.Fatalf("Finish after Evidence completion = %#v, %v; Evidence revision=%d", finish, err, retained.result.Revision)
			}
		})
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
	for _, table := range []string{"blackboard_graph_mutations", "blackboard_graph_operations"} {
		var exists int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatalf("inspect forbidden table %s: %v", table, err)
		}
		if exists != 0 {
			t.Errorf("resume/Finish v2 retained forbidden legacy table %s", table)
		}
	}
	for _, forbidden := range []string{"goal", "conclusion", "handoff", "task_summary", "objective_outcome", "mechanical_handoff"} {
		if bytes.Contains(bytes.ToLower(resumed.Snapshot), []byte(forbidden)) {
			t.Errorf("resume Snapshot copied forbidden %q state: %s", forbidden, resumed.Snapshot)
		}
	}
}

type failingResumeReconciler struct{ err error }

func (reconciler failingResumeReconciler) ReconcileTerminalContinuation(context.Context, string, string) error {
	return reconciler.err
}

func TestResumeRequiresDurableReconciliationAndExposesBoundedInterruptedCheckpoints(t *testing.T) {
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	first := fixture.launch(t)
	if _, err := fixture.tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start first Continuation: %v", err)
	}
	seedCheckpointAttempt(t, fixture.board, fixture.project.ID, first.Continuation.ID, "attempt:interrupted-resume", "Initial interrupted work")
	checkpointSummary := "Mapped two reachable paths before interruption"
	if _, err := fixture.board.CheckpointAttemptForContinuation(context.Background(), fixture.project.ID, first.Continuation.ID, blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "checkpoint-before-failed-reconciliation", Key: "attempt:interrupted-resume", Version: 1, Summary: checkpointSummary,
	}); err != nil {
		t.Fatalf("checkpoint before interruption: %v", err)
	}
	reconcileFailure := errors.New("injected reconciliation failure")
	fixture.tasks.SetContinuationReconciler(failingResumeReconciler{err: reconcileFailure})
	if _, err := fixture.tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusInterrupted); !errors.Is(err, reconcileFailure) {
		t.Fatalf("interruption reconciliation error = %v", err)
	}
	request := blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: fixture.task.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: "codex", Runner: task.RunnerSandbox, RuntimeConfig: map[string]any{"provider": "codex", "resume": true},
	}
	if _, err := fixture.continuity.CreateContinuation(context.Background(), request); !errors.Is(err, task.ErrContinuationReconciliationIncomplete) {
		t.Fatalf("resume before reconciliation = %v, want prerequisite error", err)
	}
	var continuationCount int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM task_continuations WHERE task_id=?`, fixture.task.ID).Scan(&continuationCount); err != nil || continuationCount != 1 {
		t.Fatalf("failed prerequisite created Continuation: count=%d err=%v", continuationCount, err)
	}

	fixture.tasks.SetContinuationReconciler(fixture.board)
	if _, err := fixture.tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("retry interruption reconciliation: %v", err)
	}
	checkpoints, err := fixture.board.InterruptedAttemptCheckpoints(context.Background(), fixture.project.ID, first.Continuation.ID)
	if err != nil {
		t.Fatalf("read interrupted checkpoints: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].Key != "attempt:interrupted-resume" || checkpoints[0].Summary != checkpointSummary {
		t.Fatalf("interrupted checkpoint DTOs = %#v", checkpoints)
	}
	encoded, err := json.Marshal(checkpoints[0])
	if err != nil || string(encoded) != `{"key":"attempt:interrupted-resume","summary":"Mapped two reachable paths before interruption"}` {
		t.Fatalf("closed checkpoint DTO = %s, %v", encoded, err)
	}
	current, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project fresh current Snapshot: %v", err)
	}
	var currentSnapshot blackboardv2.RuntimeSnapshot
	if err := json.Unmarshal(current.Bytes, &currentSnapshot); err != nil {
		t.Fatalf("decode fresh current Snapshot: %v", err)
	}
	if len(currentSnapshot.Work.Attempts) != 0 || bytes.Contains(current.Bytes, []byte(checkpointSummary)) {
		t.Fatalf("fresh current Snapshot included terminal Attempt: %s", current.Bytes)
	}
	prompt := adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{
		TaskGoal: fixture.task.Goal, InterruptedAttempts: checkpoints,
	})
	for _, required := range []string{"attempt:interrupted-resume", checkpointSummary} {
		if !strings.Contains(prompt, required) {
			t.Errorf("resume prompt omitted %q: %s", required, prompt)
		}
	}
	for _, forbidden := range []string{"provenance", "continuation_id", "raw", "mechanical handoff", "objective outcome"} {
		if strings.Contains(strings.ToLower(prompt), forbidden) {
			t.Errorf("resume prompt leaked forbidden %q: %s", forbidden, prompt)
		}
	}
	if strings.Count(prompt, fixture.task.Goal) != 1 {
		t.Fatalf("Task Goal duplicated in resume prompt: %s", prompt)
	}

	resumed, err := fixture.continuity.CreateContinuation(context.Background(), request)
	if err != nil {
		t.Fatalf("resume after reconciliation: %v", err)
	}
	if _, err := fixture.board.ApplyForContinuation(context.Background(), fixture.project.ID, resumed.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "recreate-interrupted-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:interrupted-resume-continued", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: checkpointSummary}},
			{Op: "relate", From: "attempt:interrupted-resume-continued", Relation: "tests", To: "objective:interrupted-resume"},
		},
	}); err != nil {
		t.Fatalf("new principal recreates interrupted work: %v", err)
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
