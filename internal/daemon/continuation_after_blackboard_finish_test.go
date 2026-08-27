package daemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/runtime"
	"pentest/internal/task"
)

// #190: after Blackboard Finish closes the current Continuation, new operator
// work on the same live Runtime must start with a fresh writable Continuation
// instead of reusing the closed grant authority.

func TestNewUserMessageAfterBlackboardFinishCreatesWritableContinuation(t *testing.T) {
	session, adapter := newFinishSessionPair("post-finish-work")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	if server.blackboardV2 == nil || server.blackboardV2Continuity == nil {
		t.Fatal("fixture has no Blackboard v2 continuity services")
	}
	finish, err := server.blackboardV2.FinishContinuation(context.Background(), created.ProjectID, cont.ID, blackboardv2.FinishContinuationRequest{
		IdempotencyKey: "bb-finish-then-work",
	})
	if err != nil {
		t.Fatalf("Blackboard Finish: %v", err)
	}
	if active, err := server.tasks.ActiveContinuation(created.ID); err != nil || active != nil {
		t.Fatalf("continuation still active after Blackboard Finish: %v %#v", err, active)
	}

	steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"post-finish-1",
		"message":"Continue the inspection after Blackboard Finish."
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer after Blackboard Finish status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	server.providerControlWG.Wait()

	next := waitForActiveContinuation(t, server, created.ID)
	if next.Number != cont.Number+1 || next.Status != task.StatusRunning {
		t.Fatalf("fresh continuation = %#v, want number %d running", next, cont.Number+1)
	}
	if factory.openCount() != 1 {
		t.Fatalf("post-finish steer opened %d provider sessions, want 1 (same live Runtime)", factory.openCount())
	}
	// The operator message stays the durable conversation projection; the
	// writable Continuation is resolved by the Accepted Steering dispatch.
	conversation := findConversationEvent(t, server, created.ID, "post-finish-1")
	if conversation.Payload["outcome"] != "pending" || conversation.Payload["delivery"] != "native_steer" {
		t.Fatalf("post-finish conversation projection = %#v", conversation.Payload)
	}
	// Timeline boundary: the prior terminal Continuation and the fresh writable
	// Continuation are both visible as lifecycle rows.
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawPriorCompleted, sawFreshStarted bool
	for _, event := range events {
		if event.Kind != task.EventKindLifecycle {
			continue
		}
		if event.Payload["phase"] == "completed" && event.ContinuationID == cont.ID && event.Payload["reason"] == "superseded_by_writable_continuation" {
			sawPriorCompleted = true
		}
		if event.Payload["phase"] == "started" && event.ContinuationID == next.ID && event.Payload["reason"] == "writable_after_terminal_continuation" {
			sawFreshStarted = true
		}
	}
	if !sawPriorCompleted || !sawFreshStarted {
		t.Fatalf("timeline boundary events missing: priorCompleted=%v freshStarted=%v", sawPriorCompleted, sawFreshStarted)
	}

	// A Blackboard write in the new Turn succeeds with the new authority.
	batch := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "post-finish-write-1",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:post-finish", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Post-finish target", ScopeStatus: "in_scope"},
		}},
	}
	if _, err := server.blackboardV2.ApplyForContinuationAtRevision(context.Background(), created.ProjectID, next.ID, finish.Revision, batch); err != nil {
		t.Fatalf("Blackboard write with fresh authority: %v", err)
	}
	// Direct replay through the old authority still returns closed_continuation.
	oldBatch := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "post-finish-old-authority-replay",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:post-finish-old", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Old authority replay", ScopeStatus: "in_scope"},
		}},
	}
	_, oldErr := server.blackboardV2.ApplyForContinuationAtRevision(context.Background(), created.ProjectID, cont.ID, finish.Revision, oldBatch)
	var boardErr *blackboardv2.Error
	if !errors.As(oldErr, &boardErr) || boardErr.Code != "closed_continuation" {
		t.Fatalf("old authority replay error = %v, want closed_continuation", oldErr)
	}
}

func TestPostFinishSteerFailsExplicitlyWhenSessionCannotBindFreshContinuation(t *testing.T) {
	inner, adapter := newFinishSessionPair("post-finish-unbindable")
	session := &unbindableFinishSession{ProviderSession: inner}
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	if _, err := server.blackboardV2.FinishContinuation(context.Background(), created.ProjectID, cont.ID, blackboardv2.FinishContinuationRequest{
		IdempotencyKey: "bb-finish-unbindable",
	}); err != nil {
		t.Fatalf("Blackboard Finish: %v", err)
	}

	// The Accepted Steering contract returns 202 after the request is durable;
	// the unbindable fresh Continuation settles the dispatch as failed instead
	// of a synchronous 409.
	steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"post-finish-unbindable-1",
		"message":"Continue after Finish without a bindable session."
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("unbindable post-finish steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	server.providerControlWG.Wait()
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Kind == task.EventKindSteering && event.Payload["request_id"] == "post-finish-unbindable-1" &&
				event.Payload["outcome"] == "failed" && event.Payload["error_code"] == "continuation_unavailable" {
				return true
			}
		}
		return false
	})
	if next, err := server.tasks.ActiveContinuation(created.ID); err != nil || next != nil {
		t.Fatalf("failed bind left a writable continuation behind: %v %#v", err, next)
	}
}

// unbindableFinishSession refuses Continuation rebinding so the explicit
// fallback path can be tested.
type unbindableFinishSession struct {
	runtime.ProviderSession
}

func (s *unbindableFinishSession) BindContinuation(continuationID string) error {
	return errors.New("binding rejected")
}

func (s *unbindableFinishSession) TurnState() runtime.ProviderSessionTurnState {
	reporter, ok := s.ProviderSession.(runtime.ProviderSessionTurnStateReporter)
	if !ok {
		return runtime.ProviderSessionTurnState{SessionID: s.SessionID()}
	}
	return reporter.TurnState()
}

func (s *unbindableFinishSession) TurnBusy() bool {
	return s.TurnState().TurnBusy()
}

func (s *unbindableFinishSession) EmitObservation(observation runtime.ProviderSessionObservation) error {
	emitter, ok := s.ProviderSession.(interface {
		EmitObservation(runtime.ProviderSessionObservation) error
	})
	if !ok {
		return errors.New("provider session cannot emit observations")
	}
	return emitter.EmitObservation(observation)
}

func TestPostFinishSteerRaceWithBlackboardFinishSettlesToWritableContinuation(t *testing.T) {
	session, adapter := newFinishSessionPair("post-finish-race")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	raceResult := make(chan error, 2)
	go func() {
		_, err := server.blackboardV2.FinishContinuation(context.Background(), created.ProjectID, cont.ID, blackboardv2.FinishContinuationRequest{
			IdempotencyKey: "bb-finish-race",
		})
		raceResult <- err
	}()
	steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"post-finish-race-1",
		"message":"New work racing the Finish call."
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	raceResult <- nil
	for i := 0; i < 2; i++ {
		if err := <-raceResult; err != nil {
			t.Fatalf("racing operation failed: %v", err)
		}
	}
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("racing steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	server.providerControlWG.Wait()

	// Whatever the interleaving, the next user message must run on a writable
	// Continuation whose Blackboard authority is open.
	second := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"post-finish-race-2",
		"message":"Continue after the Finish race settled."
	}`))
	second.Header.Set("Content-Type", "application/json")
	secondResponse := httptest.NewRecorder()
	server.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusAccepted {
		t.Fatalf("second steer status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	active := waitForActiveContinuation(t, server, created.ID)
	if active.Status != task.StatusRunning {
		t.Fatalf("post-race writable continuation = %#v", active)
	}
	snapshot, err := server.blackboardV2.RuntimeSnapshot(context.Background(), created.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	batch := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "post-finish-race-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:post-finish-race", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Post-race target", ScopeStatus: "in_scope"},
		}},
	}
	if _, err := server.blackboardV2.ApplyForContinuationAtRevision(context.Background(), created.ProjectID, active.ID, snapshot.Revision, batch); err != nil {
		t.Fatalf("post-race Blackboard write: %v", err)
	}
	if factory.openCount() != 1 {
		t.Fatalf("race reopened provider sessions: opens=%d", factory.openCount())
	}
}

func TestDaemonRestartAfterPostFinishContinuationKeepsWritableAuthorityWithoutSecondRuntime(t *testing.T) {
	root := t.TempDir()
	session, adapter := newFinishSessionPair("post-finish-restart")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixtureAt(t, root, factory)
	launchFinishTask(t, server, created)

	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	if _, err := server.blackboardV2.FinishContinuation(context.Background(), created.ProjectID, cont.ID, blackboardv2.FinishContinuationRequest{
		IdempotencyKey: "bb-finish-restart",
	}); err != nil {
		t.Fatalf("Blackboard Finish: %v", err)
	}
	steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"post-finish-restart-1",
		"message":"Create the writable continuation before the restart."
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	server.providerControlWG.Wait()
	next := waitForActiveContinuation(t, server, created.ID)
	if next.Number != cont.Number+1 {
		t.Fatalf("writable continuation = %#v, want number %d", next, cont.Number+1)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	restartSession, restartAdapter := newFinishSessionPair("post-finish-restart-2")
	restartFactory := &finishSessionFactory{session: restartSession, adapter: restartAdapter}
	server2, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, ProviderSessionFactory: restartFactory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server2.Close() })

	found, err := server2.tasks.Get(created.ID)
	if err != nil || found.Status != task.StatusInterrupted {
		t.Fatalf("Task after restart = status %q, err=%v (want interrupted orphan, never auto-resumed)", found.Status, err)
	}
	if restartFactory.openCount() != 0 {
		t.Fatalf("restart launched a second Runtime without ownership proof: opens=%d", restartFactory.openCount())
	}
	latest, err := server2.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil || latest.ID != next.ID || latest.Status != task.StatusInterrupted {
		t.Fatalf("restart latest continuation = %#v, err=%v", latest, err)
	}
}

func waitForActiveContinuation(t *testing.T, server *Server, taskID string) task.TaskContinuation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		active, err := server.tasks.ActiveContinuation(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if active != nil {
			return *active
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no writable continuation appeared for Task %s", taskID)
	return task.TaskContinuation{}
}

func findConversationEvent(t *testing.T, server *Server, taskID, requestID string) task.Event {
	t.Helper()
	events, err := server.tasks.Events(taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == requestID {
			return event
		}
	}
	t.Fatalf("no conversation event with request_id %q in %d events", requestID, len(events))
	return task.Event{}
}
