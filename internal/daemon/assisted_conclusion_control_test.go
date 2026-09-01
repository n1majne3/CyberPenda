package daemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

type assistedSteerSession struct {
	*runtime.FakeProviderSession
	steerStarted chan struct{}
}

func (session *assistedSteerSession) Capabilities() runtimeplugin.Capabilities {
	capabilities := session.FakeProviderSession.Capabilities()
	capabilities.InTurnSteer = true
	return capabilities
}

func (session *assistedSteerSession) SteerInTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	select {
	case <-session.steerStarted:
	default:
		close(session.steerStarted)
	}
	return session.FakeProviderSession.SendTurn(ctx, request, emit)
}

func TestOperatorMessageWaitsUntilAssistedConclusionSettles(t *testing.T) {
	var steering *assistedSteerSession
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			steering = &assistedSteerSession{FakeProviderSession: fake, steerStarted: make(chan struct{})}
			return steering
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-message-order", ToolCallID: "tool-message", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-message-order", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)

	steered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"after-conclusion","message":"continue only after persistence"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		steered <- response
	}()
	select {
	case <-steering.steerStarted:
		t.Fatalf("operator message raced conclusion: requests=%#v", session.LastRequests())
	case <-time.After(50 * time.Millisecond):
	}

	valid := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:message-order","create":true,"summary":"Persist before operator message.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:message-order","create_objective":{"objective":"Serialize operator input after conclusion."}}],
		"produced_targets":[]
	}`)
	if err := emitAttemptResultAndComplete(t, session, valid); err != nil {
		t.Fatal(err)
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	var response *httptest.ResponseRecorder
	select {
	case response = <-steered:
	case <-time.After(2 * time.Second):
		t.Fatal("operator message did not run after conclusion")
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("operator message status=%d body=%s", response.Code, response.Body.String())
	}
	waitForAssistedProviderRequests(t, session, 3)
	if got := session.LastRequests()[2].RequestID; got != "after-conclusion" {
		t.Fatalf("queued operator request ID=%q", got)
	}
}

type blockingConclusionSession struct {
	*runtime.FakeProviderSession
	started  chan struct{}
	canceled chan struct{}
}

func (session *blockingConclusionSession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	if request.TurnKind != runtime.RuntimeTurnKindControl {
		return session.FakeProviderSession.SendTurn(ctx, request, emit)
	}
	select {
	case <-session.started:
	default:
		close(session.started)
	}
	<-ctx.Done()
	select {
	case <-session.canceled:
	default:
		close(session.canceled)
	}
	return runtime.ProviderSessionResult{}, ctx.Err()
}

func TestStopPreemptsActiveAssistedConcludeTurn(t *testing.T) {
	var blocking *blockingConclusionSession
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			blocking = &blockingConclusionSession{FakeProviderSession: fake, started: make(chan struct{}), canceled: make(chan struct{})}
			return blocking
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-stop-conclude", ToolCallID: "tool-stop", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-stop-conclude", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Conclude SendTurn did not start")
	}

	stop := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil)
	stopResponse := httptest.NewRecorder()
	server.ServeHTTP(stopResponse, stop)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("Stop during Conclude status=%d body=%s", stopResponse.Code, stopResponse.Body.String())
	}
	select {
	case <-blocking.canceled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel active Conclude Turn")
	}
	stopped := getConclusionTask(t, server, projectID, created.ID)
	if stopped.Status != task.StatusStopped {
		t.Fatalf("Stop during Conclude Task = %#v", stopped)
	}
	if stopped.BlackboardConclusion.State != task.BlackboardConclusionStateActionRequired ||
		stopped.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("Stop left assisted receipt stranded: %#v", stopped.BlackboardConclusion)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "unexpected-after-stop", Status: "completed",
	}); !errors.Is(err, runtime.ErrProviderSessionClosed) {
		t.Fatalf("provider session after Stop error=%v, want closed", err)
	}
}

func TestTaskFinishDrainsPendingAssistedConclusionBeforeCompleting(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-finish-drain", ToolCallID: "tool-drain", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-finish-drain", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)

	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/finish", nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		finished <- response
	}()
	select {
	case response := <-finished:
		t.Fatalf("Finish returned before semantic conclusion settled: status=%d body=%s", response.Code, response.Body.String())
	case <-time.After(50 * time.Millisecond):
	}

	valid := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:finish-drain","create":true,"summary":"Persist before operator Finish.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:finish-drain","create_objective":{"objective":"Drain assisted semantic work before Finish."}}],
		"produced_targets":[]
	}`)
	if err := emitAttemptResultAndComplete(t, session, valid); err != nil {
		t.Fatal(err)
	}
	var response *httptest.ResponseRecorder
	select {
	case response = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Finish did not resume after assisted conclusion applied")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("drained Finish status=%d body=%s", response.Code, response.Body.String())
	}
	completed := getConclusionTask(t, server, projectID, created.ID)
	if completed.Status != task.StatusCompleted || completed.BlackboardConclusion.State != task.BlackboardConclusionStateClean || completed.BlackboardConclusion.AppliedRevision == nil {
		t.Fatalf("drained Finish Task = %#v", completed)
	}
}

func TestTaskFinishRejectsActionRequiredAssistedConclusionAndLeavesStopAvailable(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-finish-action", ToolCallID: "tool-finish", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-finish-action", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	invalid := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`)
	if err := emitAttemptResultAndComplete(t, session, invalid); err == nil {
		t.Fatal("invalid Conclude result unexpectedly decoded")
	}
	waitForAssistedProviderRequests(t, session, 3)
	if err := emitAttemptResultAndComplete(t, session, invalid); err == nil {
		t.Fatal("invalid repair result unexpectedly decoded")
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)

	finish := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/finish", nil)
	finishResponse := httptest.NewRecorder()
	server.ServeHTTP(finishResponse, finish)
	if finishResponse.Code != http.StatusConflict || !bytes.Contains(finishResponse.Body.Bytes(), []byte("semantic_conclusion_action_required")) {
		t.Fatalf("finish action-required status=%d body=%s", finishResponse.Code, finishResponse.Body.String())
	}
	stillRunning := getConclusionTask(t, server, projectID, created.ID)
	if stillRunning.Status != task.StatusRunning || stillRunning.BlackboardConclusion.State != task.BlackboardConclusionStateActionRequired {
		t.Fatalf("rejected Finish changed Task = %#v", stillRunning)
	}

	stop := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil)
	stopResponse := httptest.NewRecorder()
	server.ServeHTTP(stopResponse, stop)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("Stop after rejected Finish status=%d body=%s", stopResponse.Code, stopResponse.Body.String())
	}
}
