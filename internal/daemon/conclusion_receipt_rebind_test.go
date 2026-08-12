package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// #203 / ADR 0021: the steer path's assisted-conclusion recovery helper must
// create a NEW Conclusion Dispatch bound to the replacement Continuation +
// live provider session so a later retry delivers its control turn against the
// live replacement session instead of looping on the dead pre-steer session.
// Historical dispatch identity is never rewritten. This exercises the daemon
// wrapper used by both steer call sites (advanceNativeSteerContinuation and
// createWritableContinuationForLiveSession).

func newAssistedConclusionRecoveryServer(t *testing.T) (*Server, task.Task, task.TaskContinuation) {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectRecord, err := server.projects.Create("RecoveryDispatch", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "recover assisted conclusion", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	return server, created, continuation
}

// #197 regression after #203: a dispatch_failed Pending Blackboard
// Conclusion can become action_required immediately before an
// interrupt_then_replace transition creates the live replacement Runtime
// Continuation. Retry must create a NEW dispatch bound to that proven-live
// replacement. It must not inherit the obsolete dispatch binding and fail
// again with "source provider session is not live".
func TestRetryActionRequiredConclusionUsesLiveReplacementRuntime(t *testing.T) {
	server, created, original := newAssistedConclusionRecoveryServer(t)

	obligation, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-action-required", "session-original", "turn-action-required",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 3, SemanticPersistence: 1},
	)
	if err != nil {
		t.Fatalf("record checkpoint: %v", err)
	}
	initial, won, err := server.tasks.ClaimBlackboardConclusionDispatch(obligation.ID, 0)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch: %#v won=%v err=%v", initial, won, err)
	}
	if _, changed, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequired(
		initial.DispatchRequestID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0,
	); err != nil || !changed {
		t.Fatalf("mark dispatch_failed action_required: changed=%v err=%v", changed, err)
	}

	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	const replacementSessionID = "session-replacement"
	if _, err := server.tasks.UpdateContinuationRuntimeMetadata(replacement.ID, "", replacementSessionID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(original.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(replacement.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	replacementSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: replacementSessionID,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	if err := server.providerSessions.bind(created.ID, replacementSession); err != nil {
		t.Fatalf("bind replacement session: %v", err)
	}

	retryURL := "/api/projects/" + created.ProjectID + "/tasks/" + created.ID + "/blackboard-conclusion/retry"
	for attempt, wantStatus := range []int{http.StatusAccepted, http.StatusOK} {
		retry := httptest.NewRequest(http.MethodPost, retryURL, bytes.NewBufferString(`{}`))
		retry.Header.Set("Idempotency-Key", "retry-on-live-replacement")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, retry)
		if response.Code != wantStatus {
			t.Fatalf("retry %d status=%d body=%s, want=%d", attempt+1, response.Code, response.Body.String(), wantStatus)
		}
	}
	server.providerControlWG.Wait()

	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest conclusion: %#v err=%v", latest, err)
	}
	if latest.InternalState != task.BlackboardConclusionReceiptAwaitingResult {
		t.Fatalf("retry state=%s recovery_reason=%s, want awaiting_result", latest.InternalState, latest.RecoveryReason)
	}
	if latest.ContinuationID != replacement.ID || latest.SourceSessionID != replacementSessionID {
		t.Fatalf("retry binding=(%s,%s), want replacement=(%s,%s)", latest.ContinuationID, latest.SourceSessionID, replacement.ID, replacementSessionID)
	}
	if latest.BaseRevision == nil || *latest.BaseRevision != 0 {
		t.Fatalf("replacement retry base_revision=%v, want current Project revision 0", latest.BaseRevision)
	}
	requests := replacementSession.LastRequests()
	if len(requests) != 1 || requests[0].RequestID != latest.DispatchRequestID || requests[0].TurnKind != runtime.RuntimeTurnKindControl {
		t.Fatalf("replacement Runtime requests=%#v, want one control Turn for %q", requests, latest.DispatchRequestID)
	}

	history, err := server.tasks.ConclusionDispatches(obligation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("dispatch history=%d rows, want initial + retry", len(history))
	}
	for i := range history {
		if history[i].DispatchRequestID == initial.DispatchRequestID &&
			(history[i].ContinuationID != original.ID || history[i].SourceSessionID != "session-original") {
			t.Fatalf("historical dispatch binding changed: %#v", history[i])
		}
	}
}

func TestRetryUndispatchedActionRequiredConclusionUsesLiveReplacementRuntime(t *testing.T) {
	server, created, original := newAssistedConclusionRecoveryServer(t)

	obligation, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-undispatched", "session-original", "turn-undispatched",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 2, SemanticPersistence: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
		obligation.ID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0,
	); err != nil || !changed {
		t.Fatalf("mark undispatched action_required: changed=%v err=%v", changed, err)
	}

	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	const replacementSessionID = "session-replacement-undispatched"
	if _, err := server.tasks.UpdateContinuationRuntimeMetadata(replacement.ID, "", replacementSessionID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(original.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(replacement.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	replacementSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: replacementSessionID,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	if err := server.providerSessions.bind(created.ID, replacementSession); err != nil {
		t.Fatal(err)
	}

	retry := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry",
		bytes.NewBufferString(`{}`))
	retry.Header.Set("Idempotency-Key", "retry-undispatched-on-live-replacement")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, retry)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}
	server.providerControlWG.Wait()

	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest conclusion: %#v err=%v", latest, err)
	}
	if latest.InternalState != task.BlackboardConclusionReceiptAwaitingResult ||
		latest.ContinuationID != replacement.ID || latest.SourceSessionID != replacementSessionID {
		t.Fatalf("undispatched retry=%#v, want awaiting_result on replacement Runtime", latest)
	}
	if latest.BaseRevision == nil || *latest.BaseRevision != 0 {
		t.Fatalf("undispatched replacement base_revision=%v, want current Project revision 0", latest.BaseRevision)
	}
	if requests := replacementSession.LastRequests(); len(requests) != 1 || requests[0].RequestID != latest.DispatchRequestID {
		t.Fatalf("replacement Runtime requests=%#v", requests)
	}
	history, err := server.tasks.ConclusionDispatches(obligation.ID)
	if err != nil || len(history) != 1 || history[0].Kind != task.ConclusionDispatchKindRetry {
		t.Fatalf("undispatched retry history=%#v err=%v", history, err)
	}
}

func TestRetryDispatchFailedConclusionWithoutProvenLiveRuntimeFailsClosed(t *testing.T) {
	server, created, original := newAssistedConclusionRecoveryServer(t)
	obligation, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-no-live-runtime", "session-original", "turn-no-live-runtime",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, _, err := server.tasks.ClaimBlackboardConclusionDispatch(obligation.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequired(
		initial.DispatchRequestID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0,
	); err != nil {
		t.Fatal(err)
	}

	retry := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry",
		bytes.NewBufferString(`{}`))
	retry.Header.Set("Idempotency-Key", "retry-without-live-runtime")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, retry)
	if response.Code != http.StatusConflict {
		t.Fatalf("retry status=%d body=%s, want conflict", response.Code, response.Body.String())
	}
	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil || latest.InternalState != task.BlackboardConclusionReceiptActionRequired || latest.ExplicitRetryCount != 0 {
		t.Fatalf("failed-closed conclusion=%#v err=%v", latest, err)
	}
	if history, err := server.tasks.ConclusionDispatches(obligation.ID); err != nil || len(history) != 1 {
		t.Fatalf("failed-closed dispatch history=%#v err=%v", history, err)
	}
}

func TestRecoveryConclusionDispatchBindsReplacementContinuation(t *testing.T) {
	server, created, original := newAssistedConclusionRecoveryServer(t)

	// Simulate the stuck-live-task condition: an in-flight obligation with an
	// active dispatch bound to the original continuation + session.
	obligation, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-1", "session-original", "turn-1",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 3, SemanticPersistence: 1},
	)
	if err != nil {
		t.Fatalf("record checkpoint: %v", err)
	}
	claimed, won, err := server.tasks.ClaimBlackboardConclusionDispatch(obligation.ID, 7)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch: %#v won=%v err=%v", claimed, won, err)
	}

	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	const replacementSessionID = "session-replacement"

	// Bind the live replacement provider session so the recovered pre-send
	// dispatch is actually delivered against it. Without a bound session the
	// async delivery fails closed and the test races the control queue.
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    replacementSessionID,
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	if err := server.providerSessions.bind(created.ID, session); err != nil {
		t.Fatalf("bind replacement session: %v", err)
	}

	// This is the wrapper invoked by both steer call sites.
	server.recoverConclusionObligationsForReplacedContinuation(created.ID, original.ID, replacement.ID, replacementSessionID)
	// Settle the async recovery dispatch before asserting, so the assertions
	// never race the provider control queue.
	server.providerControlWG.Wait()

	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest obligation after recovery: %v %#v", err, latest)
	}
	if latest.ID != obligation.ID {
		t.Fatalf("recovery changed the obligation identity: %s != %s", latest.ID, obligation.ID)
	}
	if latest.ContinuationID != replacement.ID {
		t.Fatalf("recovery dispatch continuation_id = %s, want %s", latest.ContinuationID, replacement.ID)
	}
	if latest.SourceSessionID != replacementSessionID {
		t.Fatalf("recovery dispatch source_session_id = %s, want %s", latest.SourceSessionID, replacementSessionID)
	}
	if latest.DispatchRequestID == claimed.DispatchRequestID {
		t.Fatalf("recovery reused the old dispatch request id %s", latest.DispatchRequestID)
	}
	if latest.InternalState != task.BlackboardConclusionReceiptAwaitingResult {
		t.Fatalf("recovery dispatch state = %s, want awaiting_result after delivery on the replacement session", latest.InternalState)
	}
	requests := session.LastRequests()
	if len(requests) != 1 || requests[0].RequestID != latest.DispatchRequestID {
		t.Fatalf("recovered provider requests = %#v, want one request for %q", requests, latest.DispatchRequestID)
	}

	history, err := server.tasks.ConclusionDispatches(obligation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("dispatch history = %d rows, want 2 (initial + recovery)", len(history))
	}
	for i := range history {
		if history[i].DispatchRequestID == claimed.DispatchRequestID {
			if history[i].ContinuationID != original.ID || history[i].SourceSessionID != "session-original" {
				t.Fatalf("historical dispatch binding was rewritten: %#v", history[i])
			}
			if history[i].DeliveryState != task.ConclusionDispatchSuperseded {
				t.Fatalf("historical dispatch delivery_state = %s, want superseded", history[i].DeliveryState)
			}
		}
	}
}

func TestRecoveryConclusionDispatchLeavesTerminalObligationAlone(t *testing.T) {
	server, created, original := newAssistedConclusionRecoveryServer(t)

	// A clean obligation is terminal; it must not receive a recovery dispatch.
	if _, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-clean", "session-original", "turn-clean",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1, SemanticPersistence: 1},
	); err != nil {
		t.Fatalf("record clean checkpoint: %v", err)
	}
	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	server.recoverConclusionObligationsForReplacedContinuation(created.ID, original.ID, replacement.ID, "session-replacement")

	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest obligation: %v %#v", err, latest)
	}
	if latest.ContinuationID != original.ID {
		t.Fatalf("terminal clean obligation was moved: continuation_id=%s, want %s", latest.ContinuationID, original.ID)
	}
}
