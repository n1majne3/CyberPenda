package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/steering"
	"pentest/internal/task"
)

// #200: Accepted Steering is a durable Runtime Harness responsibility for both
// Task and Session owners. 202 Accepted follows durable queue creation,
// requests dispatch FIFO per owner with a durable send-start fence, duplicate
// submissions are idempotent, restart resumes pre-fence work while post-fence
// ambiguity becomes action_required, and Stop/Task Finish settle queued work
// with truthful terminal outcomes.

// blockingSteerSession holds the native steer operation until released so the
// durable send-start fence can be observed mid-dispatch.
type blockingSteerSession struct {
	*runtime.FakeProviderSession
	steerStarted chan struct{}
	releaseSteer chan struct{}
}

type pendingHermesResponse struct {
	done     chan struct{}
	once     sync.Once
	response runtime.SandboxBridgeResponse
	err      error
}

func newPendingHermesResponse() *pendingHermesResponse {
	return &pendingHermesResponse{done: make(chan struct{})}
}

func (response *pendingHermesResponse) Wait(ctx context.Context) (runtime.SandboxBridgeResponse, error) {
	select {
	case <-response.done:
		return response.response, response.err
	case <-ctx.Done():
		return runtime.SandboxBridgeResponse{}, ctx.Err()
	}
}

func (response *pendingHermesResponse) complete(result runtime.SandboxBridgeResponse, err error) {
	response.once.Do(func() {
		response.response = result
		response.err = err
		close(response.done)
	})
}

type longRunningHermesTransport struct {
	provider      *runtime.HermesProviderSession
	promptStarted chan runtime.SandboxBridgeRequest
	promptResult  *pendingHermesResponse
	closeOnce     sync.Once
}

func newLongRunningHermesTransport() *longRunningHermesTransport {
	return &longRunningHermesTransport{
		promptStarted: make(chan runtime.SandboxBridgeRequest, 1),
		promptResult:  newPendingHermesResponse(),
	}
}

func (transport *longRunningHermesTransport) Send(_ context.Context, request runtime.SandboxBridgeRequest) (runtime.SandboxBridgeResponse, error) {
	if request.Method == "session/cancel" {
		transport.provider.HandleEvent(runtime.SandboxBridgeEvent{
			Method: "session/update",
			Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"turn_ended","stopReason":"cancelled"}}`),
		}, nil)
	}
	return runtime.SandboxBridgeResponse{
		ID: request.ID, Result: json.RawMessage(`{"sessionId":"hermes-session"}`),
	}, nil
}

func (transport *longRunningHermesTransport) BeginSend(request runtime.SandboxBridgeRequest) (runtime.ProviderSessionResponseWaiter, error) {
	transport.promptStarted <- request
	return transport.promptResult, nil
}

func (transport *longRunningHermesTransport) Close(context.Context) error {
	transport.closeOnce.Do(func() {
		transport.promptResult.complete(runtime.SandboxBridgeResponse{}, runtime.ErrProviderSessionClosed)
	})
	return nil
}

func (s *blockingSteerSession) Capabilities() runtimeplugin.Capabilities {
	capabilities := s.FakeProviderSession.Capabilities()
	capabilities.InTurnSteer = true
	return capabilities
}

func (s *blockingSteerSession) SteerInTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	select {
	case <-s.steerStarted:
	default:
		close(s.steerStarted)
	}
	select {
	case <-s.releaseSteer:
	case <-ctx.Done():
		return runtime.ProviderSessionResult{}, ctx.Err()
	}
	state := s.FakeProviderSession.TurnState()
	if request.ProviderTurnID != "" && request.ProviderTurnID != state.ActiveTurnID {
		return runtime.ProviderSessionResult{}, &runtime.ProviderSessionOperationError{
			Mode:  runtime.ProviderSessionModeInTurnSteer,
			Cause: &runtime.ProviderTurnChangedError{ExpectedTurnID: request.ProviderTurnID, ActiveTurnID: state.ActiveTurnID},
		}
	}
	return s.FakeProviderSession.SteerInTurn(ctx, request, emit)
}

// newBlockingSteerFixture creates an interactive Task with a bound provider
// session whose native steer is held until released.
func newBlockingSteerFixture(t *testing.T) (*Server, string, task.Task, *blockingSteerSession) {
	t.Helper()
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	project, err := server.projects.Create("Steer", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	fake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "blocking-steer-session", ActiveTurnID: "turn-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	blocked := &blockingSteerSession{
		FakeProviderSession: fake,
		steerStarted:        make(chan struct{}),
		releaseSteer:        make(chan struct{}),
	}
	if err := server.BindProviderSession(created.ID, blocked); err != nil {
		t.Fatal(err)
	}
	return server, project.ID, created, blocked
}

func TestAcceptedSteeringSendsInFIFOOrderPerOwner(t *testing.T) {
	server, projectID, created, blocked := newBlockedControlFixture(t)

	for _, requestID := range []string{"fifo-1", "fifo-2"} {
		response := postSteerTimed(t, server, projectID, created.ID, `{
			"request_id":"`+requestID+`",
			"message":"FIFO steer `+requestID+`."
		}`, time.Second)
		if response.Code != http.StatusAccepted {
			t.Fatalf("steer %s status=%d body=%s", requestID, response.Code, response.Body.String())
		}
	}
	// Both requests are durably queued while the Harness control Turn blocks.
	first, err := server.steering.ByRequestID(owner.KindTask, created.ID, "fifo-1")
	if err != nil || first.State != owner.SteeringPending {
		t.Fatalf("fifo-1 record = %#v err=%v, want pending", first, err)
	}
	second, err := server.steering.ByRequestID(owner.KindTask, created.ID, "fifo-2")
	if err != nil || second.State != owner.SteeringPending {
		t.Fatalf("fifo-2 record = %#v err=%v, want pending", second, err)
	}
	if first.QueueOrder >= second.QueueOrder {
		t.Fatalf("queue order = %d then %d, want strictly increasing", first.QueueOrder, second.QueueOrder)
	}

	close(blocked.releaseControl)
	waitForAssistedProviderRequests(t, blocked.FakeProviderSession, 2)
	valid := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:fifo","create":true,"summary":"Concluded the FIFO Turn.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:fifo","create_objective":{"objective":"Conclude the FIFO Turn."}}],
		"produced_targets":[]
	}`)
	if err := emitAttemptResultAndComplete(t, blocked.FakeProviderSession, valid); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, blocked.FakeProviderSession, 4)
	requests := blocked.FakeProviderSession.LastRequests()
	if requests[2].RequestID != "fifo-1" || requests[3].RequestID != "fifo-2" {
		t.Fatalf("provider dispatch order = %q, %q; want fifo-1 then fifo-2",
			requests[2].RequestID, requests[3].RequestID)
	}
	waitForSteerOutcomeEvent(t, server, created.ID, "fifo-1", "applied")
	waitForSteerOutcomeEvent(t, server, created.ID, "fifo-2", "applied")
	applied, err := server.steering.ByRequestID(owner.KindTask, created.ID, "fifo-1")
	if err != nil || applied.State != owner.SteeringApplied {
		t.Fatalf("fifo-1 final record = %#v err=%v, want applied", applied, err)
	}
}

func TestAcceptedSteeringIsOwnerIsolated(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	project, err := server.projects.Create("Isolation", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile := createTestRuntimeProfile(t, server)
	newTask := func(goal string) task.Task {
		created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: goal, RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
			t.Fatal(err)
		}
		return created
	}
	taskA := newTask("isolation A")
	taskB := newTask("isolation B")
	sessionA := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "isolation-session-a",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	sessionB := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "isolation-session-b",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	if err := server.BindProviderSession(taskA.ID, sessionA); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(taskB.ID, sessionB); err != nil {
		t.Fatal(err)
	}

	steer := func(taskID, requestID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+taskID+"/steer",
			bytes.NewBufferString(`{"request_id":"`+requestID+`","message":"Isolation steer `+requestID+`."}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	// Interleave the two owners so any cross-owner queue would surface.
	for _, response := range []*httptest.ResponseRecorder{
		steer(taskA.ID, "a-1"), steer(taskB.ID, "b-1"), steer(taskA.ID, "a-2"), steer(taskB.ID, "b-2"),
	} {
		if response.Code != http.StatusAccepted {
			t.Fatalf("steer status=%d body=%s", response.Code, response.Body.String())
		}
	}
	waitForProviderRequests(t, sessionA, 2)
	waitForProviderRequests(t, sessionB, 2)
	requestsA := sessionA.LastRequests()
	requestsB := sessionB.LastRequests()
	for index, want := range []string{"a-1", "a-2"} {
		if requestsA[index].RequestID != want {
			t.Fatalf("owner A request %d = %q, want %q", index, requestsA[index].RequestID, want)
		}
	}
	for index, want := range []string{"b-1", "b-2"} {
		if requestsB[index].RequestID != want {
			t.Fatalf("owner B request %d = %q, want %q", index, requestsB[index].RequestID, want)
		}
	}
	waitForSteerOutcomeEvent(t, server, taskA.ID, "a-2", "applied")
	waitForSteerOutcomeEvent(t, server, taskB.ID, "b-2", "applied")
}

func TestAcceptedSteeringRecordsDurableSendStartFence(t *testing.T) {
	server, projectID, created, blocked := newBlockingSteerFixture(t)

	response := postSteerTimed(t, server, projectID, created.ID, `{
		"request_id":"fence-1",
		"message":"Observe the send-start fence."
	}`, time.Second)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-blocked.steerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted steering did not start dispatch")
	}
	// The durable send-start fence is recorded before the provider delivery
	// completes: the request cannot be sent twice after a crash.
	fenced, err := server.steering.ByRequestID(owner.KindTask, created.ID, "fence-1")
	if err != nil || fenced.State != owner.SteeringDispatchStarted {
		t.Fatalf("fenced record = %#v err=%v, want dispatch_started", fenced, err)
	}
	if fenced.SendStartedAt == nil || fenced.SendStartedAt.IsZero() {
		t.Fatalf("fenced record has no send_started_at: %#v", fenced)
	}
	if fenced.ExpectedProviderTurnID != "turn-1" {
		t.Fatalf("fenced expected provider Turn = %q, want turn-1", fenced.ExpectedProviderTurnID)
	}

	close(blocked.releaseSteer)
	waitForSteerOutcomeEvent(t, server, created.ID, "fence-1", "applied")
	applied, err := server.steering.ByRequestID(owner.KindTask, created.ID, "fence-1")
	if err != nil || applied.State != owner.SteeringApplied {
		t.Fatalf("applied record = %#v err=%v, want applied", applied, err)
	}
	if len(applied.Result) == 0 {
		t.Fatalf("applied record carries no delivery evidence: %#v", applied)
	}
	requests := blocked.LastRequests()
	if len(requests) != 1 || requests[0].ProviderTurnID != "turn-1" {
		t.Fatalf("provider steer requests = %#v, want one request for turn-1", requests)
	}
}

func TestAcceptedSameTurnSteeringDoesNotRetargetAChangedProviderTurn(t *testing.T) {
	server, projectID, created, blocked := newBlockingSteerFixture(t)

	response := postSteerTimed(t, server, projectID, created.ID, `{"request_id":"turn-race","message":"Stay on the accepted Turn."}`, time.Second)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-blocked.steerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted steering did not reach provider dispatch")
	}
	if err := blocked.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, SessionID: blocked.SessionID(),
		ProviderTurnID: "turn-1", RequestID: "work-1", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.SendTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "later-work", Message: "new work"}, nil); err != nil {
		t.Fatal(err)
	}
	if state := blocked.TurnState(); state.ActiveTurnID == "turn-1" || state.ActiveTurnID == "" {
		t.Fatalf("replacement provider Turn state = %#v", state)
	}
	close(blocked.releaseSteer)
	waitForSteerOutcomeEvent(t, server, created.ID, "turn-race", "action_required")
	record, err := server.steering.ByRequestID(owner.KindTask, created.ID, "turn-race")
	if err != nil {
		t.Fatal(err)
	}
	if record.ErrorCode != owner.SteeringReasonTargetTurnChanged {
		t.Fatalf("settlement = %#v, want target_turn_changed", record)
	}
}

func TestAcceptedSteeringPostFenceRestartBecomesActionRequiredWithoutReplay(t *testing.T) {
	root := t.TempDir()
	server, projectID, created, blocked := newBlockingSteerFixtureAt(t, root)

	response := postSteerTimed(t, server, projectID, created.ID, `{
		"request_id":"post-fence-restart",
		"message":"Crashed after the fence."
	}`, time.Second)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-blocked.steerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted steering did not start dispatch")
	}
	// Crash window: the fence is durable but the provider delivery never
	// settles. Simulate process death at this exact boundary; the daemon Close
	// below cancels the in-flight op but the durable record state is what
	// restart recovery must resolve.
	fenced, err := server.steering.ByRequestID(owner.KindTask, created.ID, "post-fence-restart")
	if err != nil || fenced.State != owner.SteeringDispatchStarted {
		t.Fatalf("fenced record = %#v err=%v, want dispatch_started", fenced, err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart without any provider session: the post-fence request cannot be
	// proven delivered or undelivered, so it becomes action_required and is
	// never replayed automatically.
	server2, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server2.Close() })
	recovered, err := server2.steering.ByRequestID(owner.KindTask, created.ID, "post-fence-restart")
	if err != nil || recovered.State != owner.SteeringActionRequired {
		t.Fatalf("recovered record = %#v err=%v, want action_required", recovered, err)
	}
	if recovered.ErrorCode != owner.SteeringReasonDeliveryAmbiguous {
		t.Fatalf("recovered reason = %q, want delivery_ambiguous", recovered.ErrorCode)
	}
	events, err := server2.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawActionRequired, sawApplied bool
	for _, event := range events {
		if event.Payload["request_id"] != "post-fence-restart" {
			continue
		}
		if event.Payload["outcome"] == "action_required" {
			sawActionRequired = true
		}
		if event.Payload["phase"] == "steering_applied" {
			sawApplied = true
		}
	}
	if !sawActionRequired {
		t.Fatalf("no action_required projection after restart: %#v", events)
	}
	if sawApplied {
		t.Fatalf("post-fence request was replayed as applied: %#v", events)
	}
	detail := httptest.NewRecorder()
	server2.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID, nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("recovered Task detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var recoveredTask task.Task
	if err := json.Unmarshal(detail.Body.Bytes(), &recoveredTask); err != nil {
		t.Fatal(err)
	}
	if recoveredTask.RuntimeControls.NativeSteerState != "action_required" ||
		recoveredTask.RuntimeControls.NativeSteerErrorCode != string(owner.SteeringReasonDeliveryAmbiguous) {
		t.Fatalf("recovered Task Runtime Controls = %#v", recoveredTask.RuntimeControls)
	}
	beforeReplayEvents := len(events)
	replay := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"post-fence-restart",
		"message":"Crashed after the fence."
	}`))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Idempotency-Key", "post-fence-restart")
	replayResponse := httptest.NewRecorder()
	server2.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusAccepted {
		t.Fatalf("recovered Task replay status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	var replayOutcome struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &replayOutcome); err != nil || replayOutcome.Outcome != "action_required" {
		t.Fatalf("recovered Task replay outcome=%#v err=%v body=%s", replayOutcome, err, replayResponse.Body.String())
	}
	afterReplayEvents, err := server2.tasks.Events(created.ID)
	if err != nil || len(afterReplayEvents) != beforeReplayEvents {
		t.Fatalf("recovered Task replay events=%d err=%v, want %d", len(afterReplayEvents), err, beforeReplayEvents)
	}
}

func TestAcceptedSteeringPreFenceRestartResumesDispatchAfterResume(t *testing.T) {
	root := t.TempDir()
	server, projectID, created, blocked := newBlockedControlFixtureAt(t, root)

	response := postSteerTimed(t, server, projectID, created.ID, `{
		"request_id":"pre-fence-restart",
		"message":"Continue after restart."
	}`, time.Second)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", response.Code, response.Body.String())
	}
	// The request is still pre-fence: the dispatch waits behind the blocked
	// Harness control Turn and never reached the provider.
	pending, err := server.steering.ByRequestID(owner.KindTask, created.ID, "pre-fence-restart")
	if err != nil || pending.State != owner.SteeringPending {
		t.Fatalf("pending record = %#v err=%v, want pending", pending, err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	_ = blocked

	// A fresh daemon with a fresh provider session recovers the owner. The
	// pre-fence request is safe to send once the Runtime is live again.
	freshFake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "pre-fence-restart-session", ActiveTurnID: "work-turn-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true, AssistedConclusion: true,
		},
	})
	var freshSession runtime.ProviderSession = freshFake
	adapter := runtime.NewProviderSessionRunAdapter(freshSession, make(chan struct{}))
	factory := &assistedConclusionSessionFactory{session: freshSession, adapter: adapter, support: true}
	server2, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true, ProviderSessionFactory: factory,
		ContainerCLI: filepath.Join(root, "fake-docker"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server2.Close() })

	// Recovery keeps the pre-fence request queued for the resumable owner.
	queued, err := server2.steering.ByRequestID(owner.KindTask, created.ID, "pre-fence-restart")
	if err != nil || queued.State != owner.SteeringPending {
		t.Fatalf("queued record after restart = %#v err=%v, want pending", queued, err)
	}

	// Resuming the interrupted Task binds a fresh provider session, which
	// triggers the FIFO dispatcher: the pre-fence request is delivered on the
	// new live Runtime and never duplicated.
	resume := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/resume", nil)
	resumeResponse := httptest.NewRecorder()
	server2.ServeHTTP(resumeResponse, resume)
	if resumeResponse.Code != http.StatusOK && resumeResponse.Code != http.StatusAccepted {
		t.Fatalf("resume status=%d body=%s", resumeResponse.Code, resumeResponse.Body.String())
	}
	// The resumed launch sends its goal turn first; the recovered Accepted
	// Steering is the next provider request on the fresh live Runtime.
	waitForAssistedProviderRequests(t, freshFake, 2)
	requests := freshFake.LastRequests()
	if got := requests[1].RequestID; got != "pre-fence-restart" {
		t.Fatalf("resumed provider request = %q, want pre-fence-restart", got)
	}
	waitForSteerOutcomeEvent(t, server2, created.ID, "pre-fence-restart", "applied")
	applied, err := server2.steering.ByRequestID(owner.KindTask, created.ID, "pre-fence-restart")
	if err != nil || applied.State != owner.SteeringApplied {
		t.Fatalf("resumed record = %#v err=%v, want applied", applied, err)
	}
}

func TestAcceptedSteeringReplayReturnsDurableOutcomeAndRejectsConflict(t *testing.T) {
	server, projectID, created, blocked := newBlockingSteerFixture(t)
	body := `{"request_id":"replay-1","message":"Replay the durable outcome.","model":"gpt-test"}`
	first := postSteerTimed(t, server, projectID, created.ID, body, time.Second)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first steer status=%d body=%s", first.Code, first.Body.String())
	}
	close(blocked.releaseSteer)
	waitForSteerOutcomeEvent(t, server, created.ID, "replay-1", "applied")
	// A repeated request with the same identity returns the durable current
	// outcome and never creates a second queue item.
	replayed := postSteerTimed(t, server, projectID, created.ID, body, time.Second)
	if replayed.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	if !bytes.Contains(replayed.Body.Bytes(), []byte(`"outcome":"applied"`)) {
		t.Fatalf("replay body = %s, want durable outcome applied", replayed.Body.String())
	}
	record, err := server.steering.ByRequestID(owner.KindTask, created.ID, "replay-1")
	if err != nil || record.State != owner.SteeringApplied {
		t.Fatalf("replay record = %#v err=%v, want applied", record, err)
	}
	conversations := 0
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "replay-1" {
			conversations++
		}
	}
	if conversations != 1 {
		t.Fatalf("replay created %d conversation items, want 1", conversations)
	}

	// Omitting an explicitly supplied client Turn Selection changes the
	// idempotency identity even when the resolved provider selection is equal.
	omittedSelection := postSteerTimed(t, server, projectID, created.ID, `{
		"request_id":"replay-1",
		"message":"Replay the durable outcome."
	}`, time.Second)
	if omittedSelection.Code != http.StatusConflict {
		t.Fatalf("omitted selection replay status=%d body=%s, want 409", omittedSelection.Code, omittedSelection.Body.String())
	}

	// Conflicting content under the same request identity is a conflict.
	conflict := postSteerTimed(t, server, projectID, created.ID, `{
		"request_id":"replay-1",
		"message":"A different message under the same identity."
	}`, time.Second)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting replay status=%d body=%s, want 409", conflict.Code, conflict.Body.String())
	}
}

func TestAcceptedSteeringStopSettlesQueuedWithTruthfulOutcome(t *testing.T) {
	server, projectID, created, blocked := newBlockedControlFixture(t)

	response := postSteerTimed(t, server, projectID, created.ID, `{
		"request_id":"steer-stop-settle",
		"message":"Settle me at Stop."
	}`, time.Second)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", response.Code, response.Body.String())
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
	settled, err := server.steering.ByRequestID(owner.KindTask, created.ID, "steer-stop-settle")
	if err != nil || settled.State != owner.SteeringFailed {
		t.Fatalf("settled record = %#v err=%v, want failed", settled, err)
	}
	if settled.ErrorCode != owner.SteeringReasonOwnerStopped {
		t.Fatalf("settled reason = %q, want owner_stopped", settled.ErrorCode)
	}
	waitForSteerOutcomeEvent(t, server, created.ID, "steer-stop-settle", "failed")
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawOwnerStopped bool
	for _, event := range events {
		if event.Payload["request_id"] == "steer-stop-settle" && event.Payload["error_code"] == string(owner.SteeringReasonOwnerStopped) {
			sawOwnerStopped = true
		}
	}
	if !sawOwnerStopped {
		t.Fatalf("Stop did not project the truthful owner_stopped reason: %#v", events)
	}
}

func TestAcceptedSteeringTaskFinishSettlesQueuedWithTruthfulOutcome(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	project, err := server.projects.Create("Finish", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "finish steer", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "finish-steer-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}
	// A pre-fence queued request that never dispatched.
	if _, err := server.steering.Accept(context.Background(), owner.KindTask, created.ID, steering.AcceptRequest{
		RequestID: "finish-queued-1", Message: "Queued at Finish.", Mode: owner.SteeringModeInTurnSteer,
	}, nil); err != nil {
		t.Fatal(err)
	}

	finish := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/finish", nil)
	finishResponse := httptest.NewRecorder()
	server.ServeHTTP(finishResponse, finish)
	if finishResponse.Code != http.StatusOK {
		t.Fatalf("Finish status=%d body=%s", finishResponse.Code, finishResponse.Body.String())
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil || found.Status != task.StatusCompleted {
		t.Fatalf("Task after Finish = %#v err=%v, want completed", found, err)
	}
	settled, err := server.steering.ByRequestID(owner.KindTask, created.ID, "finish-queued-1")
	if err != nil || settled.State != owner.SteeringFailed {
		t.Fatalf("settled record = %#v err=%v, want failed", settled, err)
	}
	if settled.ErrorCode != owner.SteeringReasonOwnerFinished {
		t.Fatalf("settled reason = %q, want owner_finished", settled.ErrorCode)
	}
	waitForSteerOutcomeEvent(t, server, created.ID, "finish-queued-1", "failed")
}

// newBlockingSteerFixtureAt mirrors newBlockingSteerFixture at a fixed DB root
// so restart tests can reopen the store.
func newBlockingSteerFixtureAt(t *testing.T, root string) (*Server, string, task.Task, *blockingSteerSession) {
	t.Helper()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	project, err := server.projects.Create("Steer", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	fake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "blocking-steer-session", ActiveTurnID: "turn-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	blocked := &blockingSteerSession{
		FakeProviderSession: fake,
		steerStarted:        make(chan struct{}),
		releaseSteer:        make(chan struct{}),
	}
	if err := server.BindProviderSession(created.ID, blocked); err != nil {
		t.Fatal(err)
	}
	return server, project.ID, created, blocked
}

// newSessionSteerFixture creates an open Session with a bound persistent
// provider session that supports native in-turn steering.
func newSessionSteerFixture(t *testing.T) (*Server, string, *runtime.FakeProviderSession) {
	t.Helper()
	fake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-steer-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true,
		},
	})
	factory := &recordingProviderSessionFactory{session: fake, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", Model: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"input":"session steer target","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, bound := server.sessionProviderSessions.get(created.ID); bound && server.sessionHarness.IsActive(created.ID) {
			return server, created.ID, fake
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Session %s never bound an active provider session", created.ID)
	return nil, "", nil
}

func TestAcceptedSteeringWithoutAttachmentsAdvancesSessionActivityAtomically(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile := createTestRuntimeProfile(t, server)
	created, err := server.sessions.Create(session.CreateRequest{Input: "Session activity"})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.sessions.CreateContinuation(created.ID, profile.ID, string(runtimeprofile.ProviderCodex), session.RunnerHost)
	if err != nil {
		t.Fatal(err)
	}
	active := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-activity-provider", ActiveTurnID: "turn-session-activity",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	if err := server.BindSessionProviderSession(created.ID, active); err != nil {
		t.Fatal(err)
	}
	releaseHarness := make(chan struct{})
	t.Cleanup(func() { close(releaseHarness) })
	go func() {
		_ = server.sessionHarness.Launch(context.Background(), runtime.SessionLaunchRequest{
			SessionID: created.ID, Goal: "Session activity", Adapter: holdAdapter{release: releaseHarness}, ContinuationID: continuation.ID,
		})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		events, eventsErr := server.sessions.Events(created.ID)
		if eventsErr != nil {
			t.Fatal(eventsErr)
		}
		started := false
		for _, event := range events {
			if event.Payload["phase"] == "hold_started" {
				started = true
				break
			}
		}
		if started && server.sessionHarness.IsActive(created.ID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("controlled Session Harness did not start: %#v", events)
		}
		time.Sleep(5 * time.Millisecond)
	}
	sessionID := created.ID
	controlStarted := make(chan struct{})
	releaseControl := make(chan struct{})
	if !server.enqueueProviderTaskControl(sessionID, func(ctx context.Context) {
		close(controlStarted)
		select {
		case <-releaseControl:
		case <-ctx.Done():
		}
	}) {
		t.Fatal("reserve Session provider control")
	}
	select {
	case <-controlStarted:
	case <-time.After(time.Second):
		t.Fatal("Session provider control did not start")
	}
	defer close(releaseControl)

	stale := time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC)
	stamp := stale.Format(time.RFC3339Nano)
	if _, err := server.db.Exec(`UPDATE sessions SET updated_at=?,last_activity_at=? WHERE id=?`, stamp, stamp, sessionID); err != nil {
		t.Fatal(err)
	}
	before, err := server.sessions.Get(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !before.LastActivityAt.Equal(stale) {
		t.Fatalf("Session activity fixture = %s, want stale %s", before.LastActivityAt, stale)
	}

	steer := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/steer", bytes.NewBufferString(`{
		"request_id":"session-activity-1",
		"message":"Advance Session activity before provider settlement."
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steer.Header.Set("Idempotency-Key", "session-activity-1")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, steer)
	if response.Code != http.StatusAccepted {
		t.Fatalf("Session steer status=%d body=%s", response.Code, response.Body.String())
	}
	record, err := server.steering.ByRequestID(owner.KindSession, sessionID, "session-activity-1")
	if err != nil || record.State != owner.SteeringPending {
		t.Fatalf("accepted Session steer = %#v err=%v, want pending behind provider control", record, err)
	}
	if requests := active.LastRequests(); len(requests) != 0 {
		t.Fatalf("queued Session steer reached provider before activity assertion: %#v", requests)
	}

	found, err := server.sessions.Get(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !found.LastActivityAt.After(stale) {
		t.Fatalf("Session last activity = %s, want after accepted steering at %s", found.LastActivityAt, stale)
	}
}

func TestAcceptedSteeringSessionOwnerDispatchesDurably(t *testing.T) {
	server, sessionID, fake := newSessionSteerFixture(t)

	steer := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/steer", bytes.NewBufferString(`{
		"request_id":"session-steer-1",
		"message":"Focus the Session on the login flow."
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steer.Header.Set("Idempotency-Key", "session-steer-1")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("Session steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	// 202 follows the durable record: the owner-neutral queue owns settlement.
	// Dispatch is asynchronous, so the record may already have advanced past
	// pending; the durable existence and owner kind are the contract here.
	record, err := server.steering.ByRequestID(owner.KindSession, sessionID, "session-steer-1")
	if err != nil || record == nil {
		t.Fatalf("Session record = %#v err=%v, want a durable record", record, err)
	}
	if record.OwnerKind != owner.KindSession {
		t.Fatalf("Session record owner kind = %q", record.OwnerKind)
	}
	waitForProviderRequests(t, fake, 1)
	if got := fake.LastRequests()[0].RequestID; got != "session-steer-1" {
		t.Fatalf("Session provider request = %q, want session-steer-1", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		record, err = server.steering.ByRequestID(owner.KindSession, sessionID, "session-steer-1")
		if err == nil && record.State == owner.SteeringApplied {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if record.State != owner.SteeringApplied {
		t.Fatalf("Session record = %#v, want applied", record)
	}

	// A repeated request with the same identity never creates a second item.
	replay := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/steer", bytes.NewBufferString(`{
		"request_id":"session-steer-1",
		"message":"Focus the Session on the login flow."
	}`))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Idempotency-Key", "session-steer-1")
	replayResponse := httptest.NewRecorder()
	server.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusAccepted {
		t.Fatalf("Session steer replay status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	events, err := server.sessions.Events(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	conversations := 0
	for _, event := range events {
		if event.Kind == session.EventKindConversation && event.Payload["request_id"] == "session-steer-1" {
			conversations++
		}
	}
	if conversations != 1 {
		t.Fatalf("Session replay created %d conversation items, want 1", conversations)
	}
	if len(fake.LastRequests()) != 1 {
		t.Fatalf("Session replay dispatched %d provider requests, want 1", len(fake.LastRequests()))
	}
}

func TestHermesAcceptedSteeringStaysAppliedAndBusyWhileReplacementPromptRuns(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.steeringDispatchTimeout = 500 * time.Millisecond
	profile := createTestRuntimeProfile(t, server)
	created, err := server.sessions.Create(session.CreateRequest{Input: "Hermes long-running replacement"})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.sessions.CreateContinuation(created.ID, profile.ID, string(runtimeprofile.ProviderHermes), session.RunnerHost)
	if err != nil {
		t.Fatal(err)
	}
	releaseRuntime := make(chan struct{})
	var releaseRuntimeOnce sync.Once
	t.Cleanup(func() { releaseRuntimeOnce.Do(func() { close(releaseRuntime) }) })
	go func() {
		_ = server.sessionHarness.Launch(context.Background(), runtime.SessionLaunchRequest{
			SessionID: created.ID, Goal: "Hermes long-running replacement", Adapter: holdAdapter{release: releaseRuntime}, ContinuationID: continuation.ID,
		})
	}()
	deadline := time.Now().Add(time.Second)
	for !server.sessionHarness.IsActive(created.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !server.sessionHarness.IsActive(created.ID) {
		t.Fatal("Hermes Session Harness did not become active")
	}

	transport := newLongRunningHermesTransport()
	provider := runtime.NewHermesProviderSession(runtime.HermesProviderSessionConfig{
		Transport:    transport,
		SessionID:    "hermes-session",
		ActiveTurnID: "hermes-old-turn",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession:    true,
			SendTurn:             true,
			InterruptThenReplace: true,
		},
	})
	transport.provider = provider
	if err := server.BindSessionProviderSession(created.ID, provider); err != nil {
		t.Fatal(err)
	}

	steer := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"hermes-long-steer",
		"message":"Continue the Session."
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steer.Header.Set("Idempotency-Key", "hermes-long-steer")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("Hermes Session steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}

	var prompt runtime.SandboxBridgeRequest
	select {
	case prompt = <-transport.promptStarted:
		if prompt.Method != "session/prompt" {
			t.Fatalf("Hermes replacement method = %q, want session/prompt", prompt.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("Hermes replacement prompt did not start")
	}
	record, err := server.steering.ByRequestID(owner.KindSession, created.ID, "hermes-long-steer")
	if err != nil || record.State != owner.SteeringDispatchStarted {
		t.Fatalf("Hermes Accepted Steering before provider evidence = %#v err=%v, want dispatch_started", record, err)
	}
	provider.HandleEvent(runtime.SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"still working"}}}`),
	}, nil)

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		record, err = server.steering.ByRequestID(owner.KindSession, created.ID, "hermes-long-steer")
		if err == nil && record.State == owner.SteeringApplied {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if record == nil || record.State != owner.SteeringApplied {
		t.Fatalf("Hermes Accepted Steering = %#v err=%v, want applied while prompt response is pending", record, err)
	}
	select {
	case <-transport.promptResult.done:
		t.Fatal("Hermes prompt response completed before the active Runtime Turn assertion")
	default:
	}
	<-time.After(2 * server.steeringDispatchTimeout)
	record, err = server.steering.ByRequestID(owner.KindSession, created.ID, "hermes-long-steer")
	if err != nil || record.State != owner.SteeringApplied {
		t.Fatalf("Hermes Accepted Steering after the delivery bound = %#v err=%v, want applied", record, err)
	}
	select {
	case <-transport.promptResult.done:
		t.Fatal("Hermes replacement prompt completed before the post-bound assertion")
	default:
	}
	detail := httptest.NewRecorder()
	server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID, nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("Hermes Session detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var running session.Session
	if err := json.Unmarshal(detail.Body.Bytes(), &running); err != nil {
		t.Fatal(err)
	}
	if running.RuntimeActivity.Liveness != runtimeLivenessLive || running.RuntimeActivity.TurnActivity != runtimeTurnBusy {
		t.Fatalf("Hermes replacement Runtime Activity = %#v, want live/busy", running.RuntimeActivity)
	}
	if running.RuntimeControls.NativeSteerState != string(owner.SteeringApplied) {
		t.Fatalf("Hermes active Runtime Turn Controls = %#v, want applied steering", running.RuntimeControls)
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("Hermes Session Stop status=%d body=%s", stop.Code, stop.Body.String())
	}
	select {
	case <-transport.promptResult.done:
	default:
		t.Fatal("Hermes Session Stop did not close the pending replacement prompt")
	}
	if server.sessionHarness.IsActive(created.ID) {
		t.Fatal("Hermes Session Stop left the Session Harness active")
	}
	if _, bound := server.sessionProviderSessions.get(created.ID); bound {
		t.Fatal("Hermes Session Stop left the Provider Session bound")
	}
	if state := provider.TurnState(); state.TurnBusy() {
		t.Fatalf("Hermes Session Stop left Runtime Turn busy: %#v", state)
	}
}

func TestAcceptedSteeringSessionStopSettlesQueuedWithTruthfulOutcome(t *testing.T) {
	server, sessionID, _ := newSessionSteerFixture(t)

	// A pre-fence queued request that never dispatched.
	if _, err := server.steering.Accept(context.Background(), owner.KindSession, sessionID, steering.AcceptRequest{
		RequestID: "session-stop-queued", Message: "Queued before Session Stop.", Mode: owner.SteeringModeInTurnSteer,
	}, nil); err != nil {
		t.Fatal(err)
	}
	stop := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/stop", nil)
	stopResponse := httptest.NewRecorder()
	server.ServeHTTP(stopResponse, stop)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("Session Stop status=%d body=%s", stopResponse.Code, stopResponse.Body.String())
	}
	settled, err := server.steering.ByRequestID(owner.KindSession, sessionID, "session-stop-queued")
	if err != nil || settled.State != owner.SteeringFailed {
		t.Fatalf("Session settled record = %#v err=%v, want failed", settled, err)
	}
	if settled.ErrorCode != owner.SteeringReasonOwnerStopped {
		t.Fatalf("Session settled reason = %q, want owner_stopped", settled.ErrorCode)
	}
	events, err := server.sessions.Events(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var sawSettlement bool
	for _, event := range events {
		if event.Payload["request_id"] == "session-stop-queued" && event.Payload["error_code"] == string(owner.SteeringReasonOwnerStopped) {
			sawSettlement = true
		}
	}
	if !sawSettlement {
		t.Fatalf("Session Stop did not project the truthful reason: %#v", events)
	}
}

// TestAcceptedSteeringSessionPreFenceRestartResumesDispatchAfterMessage proves
// the Session owner uses the same restart rules as the Task owner: a pre-fence
// request stays queued through restart and is delivered when the Session
// Runtime binds again.
func TestAcceptedSteeringSessionPreFenceRestartResumesDispatchAfterMessage(t *testing.T) {
	root := t.TempDir()
	server, sessionID, _ := newSessionSteerFixtureAt(t, root)
	// A pre-fence queued request that never dispatched before the restart.
	if _, err := server.steering.Accept(context.Background(), owner.KindSession, sessionID, steering.AcceptRequest{
		RequestID: "session-pre-fence", Message: "Survive the Session restart.", Mode: owner.SteeringModeSendTurn,
	}, nil); err != nil {
		t.Fatal(err)
	}
	pending, err := server.steering.ByRequestID(owner.KindSession, sessionID, "session-pre-fence")
	if err != nil || pending.State != owner.SteeringPending {
		t.Fatalf("pending Session record = %#v err=%v", pending, err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	freshFake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-pre-fence-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true,
		},
	})
	factory := &recordingProviderSessionFactory{session: freshFake, adapter: &persistentTestAdapter{}}
	server2, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server2.Close() })

	// Recovery keeps the pre-fence request queued for the open Session.
	queued, err := server2.steering.ByRequestID(owner.KindSession, sessionID, "session-pre-fence")
	if err != nil || queued.State != owner.SteeringPending {
		t.Fatalf("queued Session record after restart = %#v err=%v, want pending", queued, err)
	}

	// The next operator message binds a fresh Session Runtime; the queued
	// request is delivered on it and never duplicated.
	latest, err := server2.sessions.LatestContinuation(sessionID)
	if err != nil || latest == nil {
		t.Fatalf("latest Session continuation = %#v err=%v", latest, err)
	}
	message := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{
		"message":"Resume the Session runtime.",
		"runtime_profile_id":"`+latest.RuntimeProfileID+`",
		"runner":"host",
		"host_activated":true
	}`))
	message.Header.Set("Content-Type", "application/json")
	messageResponse := httptest.NewRecorder()
	server2.ServeHTTP(messageResponse, message)
	if messageResponse.Code != http.StatusAccepted {
		t.Fatalf("Session message status=%d body=%s", messageResponse.Code, messageResponse.Body.String())
	}
	// The Session relaunch binds the Runtime without a launch turn; the first
	// provider request on the fresh session is the recovered Accepted Steering.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		requests := freshFake.LastRequests()
		if len(requests) == 1 && requests[0].RequestID == "session-pre-fence" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	requests := freshFake.LastRequests()
	if len(requests) != 1 || requests[0].RequestID != "session-pre-fence" {
		t.Fatalf("Session provider requests = %#v, want exactly session-pre-fence", requests)
	}
	var applied *owner.SteeringRecord
	for time.Now().Before(deadline) {
		applied, err = server2.steering.ByRequestID(owner.KindSession, sessionID, "session-pre-fence")
		if err == nil && applied.State == owner.SteeringApplied {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if applied == nil || applied.State != owner.SteeringApplied {
		t.Fatalf("applied Session record = %#v err=%v, want applied", applied, err)
	}
}

// newSessionSteerFixtureAt mirrors newSessionSteerFixture at a fixed DB root so
// restart tests can reopen the store.
func newSessionSteerFixtureAt(t *testing.T, root string) (*Server, string, *runtime.FakeProviderSession) {
	t.Helper()
	fake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-steer-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true,
		},
	})
	factory := &recordingProviderSessionFactory{session: fake, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", Model: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"input":"session steer target","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, bound := server.sessionProviderSessions.get(created.ID); bound && server.sessionHarness.IsActive(created.ID) {
			return server, created.ID, fake
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Session %s never bound an active provider session", created.ID)
	return nil, "", nil
}

// TestAcceptedSteeringSessionPostFenceRestartBecomesActionRequiredWithoutReplay
// proves the Session owner gets the same post-fence restart treatment as the
// Task owner: a fenced request with no durable outcome becomes action_required
// and is never replayed.
func TestAcceptedSteeringSessionPostFenceRestartBecomesActionRequiredWithoutReplay(t *testing.T) {
	root := t.TempDir()
	server, sessionID, _ := newSessionSteerFixtureAt(t, root)
	record, err := server.steering.Accept(context.Background(), owner.KindSession, sessionID, steering.AcceptRequest{
		RequestID: "session-post-fence", Message: "Crashed after the Session fence.", Mode: owner.SteeringModeInTurnSteer,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Crash window: the durable fence is recorded but the provider delivery
	// never settles.
	if _, err := server.steering.MarkDispatchStarted(context.Background(), record.ID, "", record.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	server2, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server2.Close() })
	recovered, err := server2.steering.ByRequestID(owner.KindSession, sessionID, "session-post-fence")
	if err != nil || recovered.State != owner.SteeringActionRequired {
		t.Fatalf("recovered Session record = %#v err=%v, want action_required", recovered, err)
	}
	if recovered.ErrorCode != owner.SteeringReasonDeliveryAmbiguous {
		t.Fatalf("recovered Session reason = %q, want delivery_ambiguous", recovered.ErrorCode)
	}
	events, err := server2.sessions.Events(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var sawActionRequired, sawApplied bool
	for _, event := range events {
		if event.Payload["request_id"] != "session-post-fence" {
			continue
		}
		if event.Payload["outcome"] == "action_required" {
			sawActionRequired = true
		}
		if event.Payload["outcome"] == "applied" {
			sawApplied = true
		}
	}
	if !sawActionRequired || sawApplied {
		t.Fatalf("Session post-fence projection = action_required:%v applied:%v events=%#v", sawActionRequired, sawApplied, events)
	}
	detail := httptest.NewRecorder()
	server2.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID, nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("recovered Session detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var recoveredSession session.Session
	if err := json.Unmarshal(detail.Body.Bytes(), &recoveredSession); err != nil {
		t.Fatal(err)
	}
	if recoveredSession.RuntimeControls.NativeSteerState != "action_required" ||
		recoveredSession.RuntimeControls.NativeSteerErrorCode != string(owner.SteeringReasonDeliveryAmbiguous) {
		t.Fatalf("recovered Session Runtime Controls = %#v", recoveredSession.RuntimeControls)
	}
	beforeReplayEvents := len(events)
	replay := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/steer", bytes.NewBufferString(`{
		"request_id":"session-post-fence",
		"message":"Crashed after the Session fence."
	}`))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Idempotency-Key", "session-post-fence")
	replayResponse := httptest.NewRecorder()
	server2.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusAccepted {
		t.Fatalf("recovered Session replay status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	var replayed session.Session
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &replayed); err != nil || replayed.RuntimeControls.NativeSteerState != "action_required" {
		t.Fatalf("recovered Session replay state=%#v err=%v body=%s", replayed.RuntimeControls, err, replayResponse.Body.String())
	}
	afterReplayEvents, err := server2.sessions.Events(sessionID)
	if err != nil || len(afterReplayEvents) != beforeReplayEvents {
		t.Fatalf("recovered Session replay events=%d err=%v, want %d", len(afterReplayEvents), err, beforeReplayEvents)
	}
}

// TestAcceptedSteeringTaskDeletionSettlesQueuedWithTruthfulOutcome proves Task
// Deletion settles queued Accepted Steering instead of orphaning it with the
// deleted owner.
func TestAcceptedSteeringTaskDeletionSettlesQueuedWithTruthfulOutcome(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	project, err := server.projects.Create("Delete", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "delete steer", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.steering.Accept(context.Background(), owner.KindTask, created.ID, steering.AcceptRequest{
		RequestID: "delete-queued-1", Message: "Queued before deletion.", Mode: owner.SteeringModeInTurnSteer,
	}, nil); err != nil {
		t.Fatal(err)
	}
	// Task Deletion is legal only for terminal Tasks.
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	del := httptest.NewRequest(http.MethodDelete, "/api/projects/"+project.ID+"/tasks/"+created.ID, nil)
	delResponse := httptest.NewRecorder()
	server.ServeHTTP(delResponse, del)
	if delResponse.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", delResponse.Code, delResponse.Body.String())
	}
	settled, err := server.steering.ByRequestID(owner.KindTask, created.ID, "delete-queued-1")
	if err != nil || settled.State != owner.SteeringFailed {
		t.Fatalf("settled record = %#v err=%v, want failed", settled, err)
	}
	if settled.ErrorCode != owner.SteeringReasonOwnerStateChanged {
		t.Fatalf("settled reason = %q, want owner_state_changed", settled.ErrorCode)
	}
}
