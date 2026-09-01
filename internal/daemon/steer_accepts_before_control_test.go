package daemon

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// #194: Harness Steering must be durably accepted before a busy Harness
// control Turn completes, so the steer request returns 202 immediately and is
// applied at the next valid work boundary.

// blockedControlSession blocks the Conclude control SendTurn until released,
// while still supporting native InTurnSteer for the operator message.
type blockedControlSession struct {
	*runtime.FakeProviderSession
	controlStarted chan struct{}
	releaseControl chan struct{}
}

func (s *blockedControlSession) Capabilities() runtimeplugin.Capabilities {
	capabilities := s.FakeProviderSession.Capabilities()
	capabilities.InTurnSteer = true
	return capabilities
}

func (s *blockedControlSession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	if request.TurnKind == runtime.RuntimeTurnKindControl {
		select {
		case <-s.controlStarted:
		default:
			close(s.controlStarted)
		}
		select {
		case <-s.releaseControl:
		case <-ctx.Done():
			return runtime.ProviderSessionResult{}, ctx.Err()
		}
	}
	return s.FakeProviderSession.SendTurn(ctx, request, emit)
}

func newBlockedControlFixture(t *testing.T) (*Server, string, task.Task, *blockedControlSession) {
	t.Helper()
	return newBlockedControlFixtureAt(t, t.TempDir())
}

func newBlockedControlFixtureAt(t *testing.T, root string) (*Server, string, task.Task, *blockedControlSession) {
	t.Helper()
	var blocked *blockedControlSession
	server, projectID, profileID, fake := newAssistedConclusionFixtureAtWithDecorator(
		t, root, true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			blocked = &blockedControlSession{
				FakeProviderSession: fake,
				controlStarted:      make(chan struct{}),
				releaseControl:      make(chan struct{}),
			}
			return blocked
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, fake, 1)
	work := fake.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-194", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-194", Status: "completed"},
	} {
		if err := fake.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	// The Conclude control Turn is dispatched asynchronously; it is held inside
	// the blocked wrapper until the test releases it, so wait on the wrapper
	// signal rather than on recorded provider requests.
	select {
	case <-blocked.controlStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Conclude control Turn did not start")
	}
	return server, projectID, created, blocked
}

func TestSteerReturnsAcceptedWhileProviderControlTurnBlocked(t *testing.T) {
	server, projectID, created, blocked := newBlockedControlFixture(t)

	started := time.Now()
	steerResponse := postSteerTimed(t, server, projectID, created.ID, `{
		"request_id":"steer-while-blocked",
		"message":"Continue the inspection."
	}`, time.Second)
	elapsed := time.Since(started)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	if elapsed > time.Second {
		t.Fatalf("steer took %v behind the blocked control Turn; want immediate 202", elapsed)
	}
	// The operator message is durably accepted as pending.
	conversation := findConversationEvent(t, server, created.ID, "steer-while-blocked")
	if conversation.Payload["outcome"] != "pending" || conversation.Payload["delivery"] != "native_steer" {
		t.Fatalf("accepted steer event = %#v, want pending native_steer", conversation.Payload)
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(blocked.FakeProviderSession.LastRequests()); got != 1 {
		t.Fatalf("steer applied before the control Turn completed: requests=%d", got)
	}

	// Release the control Turn: the conclusion settles, then the accepted
	// steering is applied at the next work boundary.
	close(blocked.releaseControl)
	waitForAssistedProviderRequests(t, blocked.FakeProviderSession, 2)
	valid := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:steer-after-block","create":true,"summary":"Concluded the blocked Turn.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:steer-after-block","create_objective":{"objective":"Conclude the blocked Turn."}}],
		"produced_targets":[]
	}`)
	if err := emitAttemptResultAndComplete(t, blocked.FakeProviderSession, valid); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, blocked.FakeProviderSession, 3)
	applied := blocked.FakeProviderSession.LastRequests()[2]
	if applied.RequestID != "steer-while-blocked" || applied.Message != "Continue the inspection." {
		t.Fatalf("applied steering request = %#v", applied)
	}
	waitForSteerOutcomeEvent(t, server, created.ID, "steer-while-blocked", "applied")
}

func TestDuplicateSteerRequestDoesNotCreateSecondSteeringItem(t *testing.T) {
	server, projectID, created, blocked := newBlockedControlFixture(t)

	body := `{"request_id":"steer-duplicate","message":"Inspect the duplicate boundary."}`
	for i := 0; i < 2; i++ {
		steerResponse := postSteerTimed(t, server, projectID, created.ID, body, time.Second)
		if steerResponse.Code != http.StatusAccepted {
			t.Fatalf("steer %d status=%d body=%s", i, steerResponse.Code, steerResponse.Body.String())
		}
	}
	conversations := 0
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "steer-duplicate" {
			conversations++
		}
	}
	if conversations != 1 {
		t.Fatalf("duplicate steer created %d conversation items, want 1", conversations)
	}

	close(blocked.releaseControl)
	waitForAssistedProviderRequests(t, blocked.FakeProviderSession, 2)
	valid := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:steer-duplicate","create":true,"summary":"Concluded the duplicate Turn.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:steer-duplicate","create_objective":{"objective":"Conclude the duplicate Turn."}}],
		"produced_targets":[]
	}`)
	if err := emitAttemptResultAndComplete(t, blocked.FakeProviderSession, valid); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, blocked.FakeProviderSession, 3)
	if got := blocked.FakeProviderSession.LastRequests()[2].RequestID; got != "steer-duplicate" {
		t.Fatalf("applied steering request = %q", got)
	}
	waitForSteerOutcomeEvent(t, server, created.ID, "steer-duplicate", "applied")
	if len(blocked.FakeProviderSession.LastRequests()) != 3 {
		t.Fatalf("duplicate steer applied twice: requests=%d", len(blocked.FakeProviderSession.LastRequests()))
	}
}

func postSteerTimed(t *testing.T, server *Server, projectID, taskID, body string, budget time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	responseCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewBufferString(body))
		steer.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, steer)
		responseCh <- response
	}()
	select {
	case response := <-responseCh:
		return response
	case <-time.After(budget):
		t.Fatalf("steer did not return within %v (blocked behind control work)", budget)
		return nil
	}
}

func TestPendingSteerSurvivesConcurrentStopWithoutLosingMessage(t *testing.T) {
	server, projectID, created, blocked := newBlockedControlFixture(t)

	steerResponse := postSteerTimed(t, server, projectID, created.ID, `{
		"request_id":"steer-stop-race",
		"message":"Continue after a concurrent Stop."
	}`, time.Second)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	// Release the control Turn but never settle the conclusion, so the
	// accepted steering stays pending in the provider control queue.
	close(blocked.releaseControl)
	waitForAssistedProviderRequests(t, blocked.FakeProviderSession, 2)

	stop := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil)
	stopResponse := httptest.NewRecorder()
	server.ServeHTTP(stopResponse, stop)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("Stop status=%d body=%s", stopResponse.Code, stopResponse.Body.String())
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil || found.Status != task.StatusStopped {
		t.Fatalf("Task after Stop = %#v, err=%v", found, err)
	}
	// The operator message is not lost; the pending steering never applied.
	conversation := findConversationEvent(t, server, created.ID, "steer-stop-race")
	if conversation.Payload["outcome"] != "pending" {
		t.Fatalf("stopped steer outcome = %v, want pending", conversation.Payload["outcome"])
	}
	if got := len(blocked.FakeProviderSession.LastRequests()); got != 2 {
		t.Fatalf("pending steering applied after Stop: requests=%d", got)
	}
}

func TestAcceptedSteeringSurvivesDaemonRestart(t *testing.T) {
	root := t.TempDir()
	server, projectID, created, blocked := newBlockedControlFixtureAt(t, root)

	steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"steer-restart",
		"message":"Continue after restart."
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	_ = blocked

	server2, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server2.Close() })

	events, err := server2.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "steer-restart" {
			found = true
			if event.Payload["outcome"] != "pending" {
				t.Fatalf("restarted steer outcome = %v, want pending", event.Payload["outcome"])
			}
		}
	}
	if !found {
		t.Fatal("accepted steering was lost across daemon restart")
	}
}

func waitForSteerOutcomeEvent(t *testing.T, server *Server, taskID, requestID, outcome string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events, err := server.tasks.Events(taskID)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Payload["request_id"] == requestID && event.Payload["outcome"] == outcome {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s outcome event for steer %q", outcome, requestID)
}
