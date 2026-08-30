package daemon

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
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

func newAuthorizedAssistedConclusionRecoveryServer(t *testing.T) (*Server, task.Task, task.TaskContinuation) {
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
	projectRecord, err := server.projects.Create("AuthorizedRecoveryDispatch", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Recovery Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "recover assisted conclusion", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: projectRecord.ID, TaskID: created.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.UpdateContinuationRuntimeMetadata(launch.Continuation.ID, "", "session-original", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	return server, created, continuation
}

func prepareRuntimeRebindRecovery(t *testing.T, reason task.ConclusionRecoveryReason, providerSessionID string) (*Server, task.Task, task.BlackboardConclusionReceipt) {
	t.Helper()
	server, created, original := newAuthorizedAssistedConclusionRecoveryServer(t)
	receipt, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-runtime-proof", "session-original", "turn-runtime-proof",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
		receipt.ID, reason, time.Now().UTC(), 0,
	); err != nil || !changed {
		t.Fatalf("mark action_required: changed=%v err=%v", changed, err)
	}
	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.blackboardV2Continuity.RebindContinuationForNativeSteer(
		context.Background(), original.ID, replacement.ID,
	); err != nil {
		t.Fatal(err)
	}
	const replacementSessionID = "session-replacement-proof"
	if _, err := server.tasks.UpdateContinuationRuntimeMetadata(replacement.ID, "", replacementSessionID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(original.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(replacement.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if providerSessionID != "" {
		provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
			SessionID: providerSessionID,
			Capabilities: runtimeplugin.Capabilities{
				PersistentSession: true, SendTurn: true, AssistedConclusion: true,
			},
		})
		if err := server.providerSessions.bind(created.ID, provider); err != nil {
			t.Fatal(err)
		}
	}
	return server, created, receipt
}

func TestRetryDispatchFailedConclusionRequiresMatchingLiveRuntime(t *testing.T) {
	reasons := []task.ConclusionRecoveryReason{
		task.ConclusionRecoveryRuntimeOwnershipNotProven,
		task.ConclusionRecoveryWritableReplacementUnavailable,
		task.ConclusionRecoveryDispatchFailed,
		task.ConclusionRecoveryLegacyCorrelationUnproven,
	}
	for _, reason := range reasons {
		for _, test := range []struct {
			name              string
			providerSessionID string
		}{
			{name: "provider missing"},
			{name: "native session mismatch", providerSessionID: "session-other"},
		} {
			t.Run(string(reason)+"/"+test.name, func(t *testing.T) {
				server, created, receipt := prepareRuntimeRebindRecovery(t, reason, test.providerSessionID)
				request := httptest.NewRequest(http.MethodPost,
					"/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry",
					bytes.NewBufferString(`{}`))
				authorizeOperatorTestRequest(server, request)
				request.Header.Set("Idempotency-Key", "retry-without-runtime-proof")
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)

				if response.Code != http.StatusConflict {
					t.Fatalf("retry status=%d body=%s, want conflict", response.Code, response.Body.String())
				}
				latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
				if err != nil || latest == nil || latest.ExplicitRetryCount != 0 ||
					latest.InternalState != task.BlackboardConclusionReceiptActionRequired {
					t.Fatalf("failed-closed conclusion=%#v err=%v", latest, err)
				}
				if history, err := server.tasks.ConclusionDispatches(receipt.ID); err != nil || len(history) != 0 {
					t.Fatalf("failed-closed dispatch history=%#v err=%v", history, err)
				}
			})
		}
	}
}

func TestRetryDispatchFailedConclusionWithoutBlackboardAuthorityFailsClosed(t *testing.T) {
	server, created, original := newAssistedConclusionRecoveryServer(t)

	obligation, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-no-authority", "session-original", "turn-no-authority",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
		obligation.ID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0,
	); err != nil || !changed {
		t.Fatalf("mark action_required: changed=%v err=%v", changed, err)
	}

	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	const replacementSessionID = "session-without-blackboard-authority"
	if _, err := server.tasks.UpdateContinuationRuntimeMetadata(replacement.ID, "", replacementSessionID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(original.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(replacement.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: replacementSessionID,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	if err := server.providerSessions.bind(created.ID, provider); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry",
		bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, request)
	request.Header.Set("Idempotency-Key", "retry-without-blackboard-authority")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("retry status=%d body=%s, want conflict", response.Code, response.Body.String())
	}
	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil || latest.ExplicitRetryCount != 0 || latest.InternalState != task.BlackboardConclusionReceiptActionRequired {
		t.Fatalf("failed-closed conclusion=%#v err=%v", latest, err)
	}
	if history, err := server.tasks.ConclusionDispatches(obligation.ID); err != nil || len(history) != 0 {
		t.Fatalf("failed-closed dispatch history=%#v err=%v", history, err)
	}
}

func TestRetryDispatchFailedConclusionBindsProvenLiveReplacementRuntime(t *testing.T) {
	server, created, original := newAuthorizedAssistedConclusionRecoveryServer(t)

	obligation, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-rebind", "session-original", "turn-rebind",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, won, err := server.tasks.ClaimBlackboardConclusionDispatch(obligation.ID, 0)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch: %#v won=%v err=%v", initial, won, err)
	}
	if _, changed, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequired(
		initial.DispatchRequestID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0,
	); err != nil || !changed {
		t.Fatalf("mark action_required: changed=%v err=%v", changed, err)
	}

	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.blackboardV2Continuity.RebindContinuationForNativeSteer(
		context.Background(), original.ID, replacement.ID,
	); err != nil {
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
	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: replacementSessionID,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	if err := server.providerSessions.bind(created.ID, provider); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry",
		bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, request)
	request.Header.Set("Idempotency-Key", "retry-on-live-replacement")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s, want accepted", response.Code, response.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry",
		bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, replay)
	replay.Header.Set("Idempotency-Key", "retry-on-live-replacement")
	replayResponse := httptest.NewRecorder()
	server.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusOK {
		t.Fatalf("retry replay status=%d body=%s, want OK", replayResponse.Code, replayResponse.Body.String())
	}
	server.providerControlWG.Wait()

	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest conclusion=%#v err=%v", latest, err)
	}
	if latest.InternalState != task.BlackboardConclusionReceiptAwaitingResult ||
		latest.ContinuationID != replacement.ID || latest.SourceSessionID != replacementSessionID {
		t.Fatalf("replacement retry=%#v", latest)
	}
	if requests := provider.LastRequests(); len(requests) != 1 || requests[0].RequestID != latest.DispatchRequestID {
		t.Fatalf("replacement Runtime requests=%#v, want one idempotent retry", requests)
	}
	history, err := server.tasks.ConclusionDispatches(obligation.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("dispatch history=%#v err=%v", history, err)
	}
	foundInitial := false
	for _, dispatch := range history {
		if dispatch.DispatchRequestID == initial.DispatchRequestID {
			foundInitial = true
			if dispatch.ContinuationID != original.ID || dispatch.SourceSessionID != "session-original" {
				t.Fatalf("historical dispatch binding changed: %#v", dispatch)
			}
			if dispatch.DeliveryState != task.ConclusionDispatchSuperseded {
				t.Fatalf("historical dispatch state=%s, want superseded", dispatch.DeliveryState)
			}
		}
	}
	if !foundInitial {
		t.Fatalf("initial dispatch missing from history: %#v", history)
	}
	if _, changed, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequired(
		latest.DispatchRequestID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0,
	); err != nil || !changed {
		t.Fatalf("mark replacement dispatch_failed: changed=%v err=%v", changed, err)
	}
	_ = server.providerSessions.remove(created.ID)
	lostResponseReplay := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry",
		bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, lostResponseReplay)
	lostResponseReplay.Header.Set("Idempotency-Key", "retry-on-live-replacement")
	lostResponseReplayResponse := httptest.NewRecorder()
	server.ServeHTTP(lostResponseReplayResponse, lostResponseReplay)
	if lostResponseReplayResponse.Code != http.StatusOK {
		t.Fatalf("retry replay after Runtime loss status=%d body=%s, want OK",
			lostResponseReplayResponse.Code, lostResponseReplayResponse.Body.String())
	}
	afterReplay, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || afterReplay == nil || afterReplay.ExplicitRetryCount != 1 ||
		afterReplay.InternalState != task.BlackboardConclusionReceiptActionRequired {
		t.Fatalf("retry replay after Runtime loss=%#v err=%v", afterReplay, err)
	}
}

func TestRetryUndispatchedConclusionUsesInitialDirectiveOnProvenLiveReplacement(t *testing.T) {
	server, created, original := newAuthorizedAssistedConclusionRecoveryServer(t)

	obligation, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-undispatched", "session-original", "turn-undispatched",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
		obligation.ID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0,
	); err != nil || !changed {
		t.Fatalf("mark action_required: changed=%v err=%v", changed, err)
	}

	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.blackboardV2Continuity.RebindContinuationForNativeSteer(
		context.Background(), original.ID, replacement.ID,
	); err != nil {
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
	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: replacementSessionID,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	if err := server.providerSessions.bind(created.ID, provider); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry",
		bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, request)
	request.Header.Set("Idempotency-Key", "retry-undispatched-on-live-replacement")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s, want accepted", response.Code, response.Body.String())
	}
	server.providerControlWG.Wait()

	requests := provider.LastRequests()
	if len(requests) != 1 {
		t.Fatalf("replacement Runtime requests=%#v, want one initial Conclude Turn", requests)
	}
	if !strings.Contains(requests[0].Message, "perform only the Harness conclusion below") {
		t.Fatalf("initial Conclude directive missing: %q", requests[0].Message)
	}
	if strings.Contains(requests[0].Message, "previous Blackboard conclusion result was invalid") {
		t.Fatalf("undispatched retry used repair directive: %q", requests[0].Message)
	}
	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil || latest.InternalState != task.BlackboardConclusionReceiptAwaitingResult ||
		latest.ContinuationID != replacement.ID || latest.SourceSessionID != replacementSessionID {
		t.Fatalf("undispatched replacement retry=%#v err=%v", latest, err)
	}
	if latest.DispatchKind != task.ConclusionDispatchKindInitial {
		t.Fatalf("undispatched replacement dispatch kind=%s, want initial for restart-safe Conclude recovery", latest.DispatchKind)
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
