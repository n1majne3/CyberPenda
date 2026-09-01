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

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/session"
)

func TestAssistedSessionRestartReplaysAPreSendReceiptWithProvenRuntimeOwnership(t *testing.T) {
	provider := newConclusionRecoverySession()
	factory := &conclusionRecoveryFactory{session: provider, liveness: ProviderSessionRecoveryLive}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	found, err := server.sessions.Create(session.CreateRequest{
		Input: "recover Session conclusion", BlackboardMode: session.BlackboardModeWorkingGraph,
	})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	continuation, err := server.sessions.CreateContinuation(found.ID, "profile-1", "codex", session.RunnerHost)
	if err != nil {
		t.Fatalf("create Session Continuation: %v", err)
	}
	continuation, err = server.sessions.UpdateContinuationRuntimeMetadata(continuation.ID, "", provider.SessionID(), "/sessions/source.jsonl")
	if err != nil {
		t.Fatalf("store native Session identity: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatalf("mark Session Continuation running: %v", err)
	}
	receipt, inserted, err := server.sessions.RecordBlackboardConclusionCheckpoint(
		found.ID, continuation.ID, "work-request", provider.SessionID(), "work-turn",
		session.RuntimeTurnSelection{ModelProviderID: "model-provider", Model: "model"}, session.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil || !inserted {
		t.Fatalf("record Session conclusion checkpoint: %#v inserted=%v err=%v", receipt, inserted, err)
	}

	report := server.recoverSessionBlackboardConclusionReceipts(context.Background())
	server.providerControlWG.Wait()
	if len(report.LiveOwnerIDs) != 1 || report.LiveOwnerIDs[0] != found.ID {
		t.Fatalf("live recovered Session IDs = %#v", report.LiveOwnerIDs)
	}
	recovered, err := server.sessions.LatestBlackboardConclusion(found.ID)
	if err != nil || recovered == nil || recovered.InternalState != session.BlackboardConclusionReceiptAwaitingResult {
		t.Fatalf("recovered Session receipt = %#v, err=%v", recovered, err)
	}
	requests := provider.LastRequests()
	if len(requests) != 1 || requests[0].RequestID != recovered.DispatchRequestID {
		t.Fatalf("recovered Session provider requests = %#v", requests)
	}
}

func TestRetrySessionConclusionBindsProvenLiveReplacementRuntime(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	found, err := server.sessions.Create(session.CreateRequest{
		Input: "retry Session conclusion", BlackboardMode: session.BlackboardModeWorkingGraph,
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := server.sessions.CreateContinuation(found.ID, "profile-1", "codex", session.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	original, err = server.sessions.UpdateContinuationRuntimeMetadata(original.ID, "", "session-original", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(original.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.blackboardV2.BindSessionContinuation(context.Background(), found.ID, original.ID); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := server.sessions.RecordBlackboardConclusionCheckpoint(
		found.ID, original.ID, "work-request-rebind", original.NativeSessionID, "work-turn-rebind",
		session.RuntimeTurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		session.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, won, err := server.sessions.ClaimBlackboardConclusionDispatch(receipt.ID, 0)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch: %#v won=%v err=%v", initial, won, err)
	}
	if _, changed, err := server.sessions.MarkBlackboardConclusionRecoveryActionRequired(
		initial.DispatchRequestID, session.ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0,
	); err != nil || !changed {
		t.Fatalf("mark dispatch_failed: changed=%v err=%v", changed, err)
	}
	replacement, err := server.sessions.CreateReplacementContinuation(original, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.blackboardV2.RebindSessionContinuation(context.Background(), found.ID, original.ID, replacement.ID); err != nil {
		t.Fatal(err)
	}
	const replacementSessionID = "session-replacement"
	replacement, err = server.sessions.UpdateContinuationRuntimeMetadata(replacement.ID, "", replacementSessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(replacement.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatal(err)
	}
	unauthorizedRequest := httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+found.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
	unauthorizedRequest.Header.Set("Idempotency-Key", "host-runtime-session-retry")
	unauthorizedResponse := httptest.NewRecorder()
	server.ServeHTTP(unauthorizedResponse, unauthorizedRequest)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless Session retry status=%d body=%s, want unauthorized", unauthorizedResponse.Code, unauthorizedResponse.Body.String())
	}
	missingRuntimeRequest := httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+found.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, missingRuntimeRequest)
	missingRuntimeRequest.Header.Set("Idempotency-Key", "session-runtime-rebind")
	missingRuntimeResponse := httptest.NewRecorder()
	server.ServeHTTP(missingRuntimeResponse, missingRuntimeRequest)
	if missingRuntimeResponse.Code != http.StatusConflict {
		t.Fatalf("missing Runtime retry status=%d body=%s, want conflict", missingRuntimeResponse.Code, missingRuntimeResponse.Body.String())
	}
	mismatchedProvider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-other",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	if err := server.BindSessionProviderSession(found.ID, mismatchedProvider); err != nil {
		t.Fatal(err)
	}
	unprovenRequest := httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+found.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, unprovenRequest)
	unprovenRequest.Header.Set("Idempotency-Key", "session-runtime-rebind")
	unprovenResponse := httptest.NewRecorder()
	server.ServeHTTP(unprovenResponse, unprovenRequest)
	if unprovenResponse.Code != http.StatusConflict {
		t.Fatalf("mismatched Runtime retry status=%d body=%s, want conflict", unprovenResponse.Code, unprovenResponse.Body.String())
	}
	_ = server.sessionProviderSessions.remove(found.ID)
	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: replacementSessionID,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	if err := server.BindSessionProviderSession(found.ID, provider); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+found.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, request)
	request.Header.Set("Idempotency-Key", "session-runtime-rebind")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s, want accepted", response.Code, response.Body.String())
	}
	server.providerControlWG.Wait()
	latest, err := server.sessions.LatestBlackboardConclusion(found.ID)
	if err != nil || latest == nil || latest.InternalState != session.BlackboardConclusionReceiptAwaitingResult ||
		latest.ContinuationID != replacement.ID || latest.SourceSessionID != replacementSessionID {
		t.Fatalf("replacement Session retry=%#v err=%v", latest, err)
	}
	if requests := provider.LastRequests(); len(requests) != 1 || requests[0].RequestID != latest.DispatchRequestID {
		t.Fatalf("replacement Session Runtime requests=%#v", requests)
	}
	history, err := server.sessions.ConclusionDispatches(receipt.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("immutable Session dispatch history=%#v err=%v", history, err)
	}
	foundInitial := false
	for _, dispatch := range history {
		if dispatch.DispatchRequestID == initial.DispatchRequestID {
			foundInitial = true
			if dispatch.ContinuationID != original.ID || dispatch.SourceSessionID != "session-original" ||
				dispatch.DeliveryState != session.ConclusionDispatchSuperseded {
				t.Fatalf("historical Session dispatch changed=%#v", dispatch)
			}
		}
	}
	if !foundInitial {
		t.Fatalf("initial Session dispatch missing from history=%#v", history)
	}
}

func TestSessionInitialRuntimeRetryRemainsInitialAfterRestart(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	found, err := server.sessions.Create(session.CreateRequest{
		Input: "restart initial Session conclusion", BlackboardMode: session.BlackboardModeWorkingGraph,
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.sessions.CreateContinuation(found.ID, "profile-1", "codex", session.RunnerHost)
	if err != nil {
		t.Fatal(err)
	}
	continuation, err = server.sessions.UpdateContinuationRuntimeMetadata(
		continuation.ID, "", "assisted-session", "/sessions/assisted-session.jsonl",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.blackboardV2.BindSessionContinuation(context.Background(), found.ID, continuation.ID); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := server.sessions.RecordBlackboardConclusionCheckpoint(
		found.ID, continuation.ID, "work-request-initial-restart", continuation.NativeSessionID, "work-turn-initial-restart",
		session.RuntimeTurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		session.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, changed, err := server.sessions.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
		receipt.ID, session.ConclusionRecoveryRuntimeOwnershipNotProven, now, 0,
	); err != nil || !changed {
		t.Fatalf("mark recovery: changed=%v err=%v", changed, err)
	}
	retried, won, initial, err := server.sessions.RetryLatestBlackboardConclusionForRuntime(
		found.ID, "session-restart-initial", continuation.ID, continuation.NativeSessionID, 0, now,
	)
	if err != nil || !won || !initial || retried.DispatchKind != session.ConclusionDispatchKindInitial {
		t.Fatalf("persist initial retry=%#v won=%v initial=%v err=%v", retried, won, initial, err)
	}
	replacement, err := server.sessions.CreateReplacementContinuation(continuation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.blackboardV2.RebindSessionContinuation(
		context.Background(), found.ID, continuation.ID, replacement.ID,
	); err != nil {
		t.Fatal(err)
	}
	replacement, err = server.sessions.UpdateContinuationRuntimeMetadata(
		replacement.ID, "", "assisted-session", "/sessions/assisted-session.jsonl",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(replacement.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatal(err)
	}
	rebound, created, err := server.sessions.CreateRecoveryConclusionDispatch(
		retried.ID, replacement.ID, replacement.NativeSessionID, now.Add(time.Second),
	)
	if err != nil || !created || rebound.DispatchKind != session.ConclusionDispatchKindRecovery ||
		rebound.DirectiveKind != session.ConclusionDirectiveKindInitial {
		t.Fatalf("rebind initial retry=%#v created=%v err=%v", rebound, created, err)
	}
	if _, err := server.db.Exec(`UPDATE session_conclusion_dispatches SET directive_kind='' WHERE id=?`, rebound.ActiveDispatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.db.Exec(`DELETE FROM schema_migrations WHERE version=60`); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	provider := newConclusionRecoverySession()
	restarted, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
		ProviderSessionFactory: &conclusionRecoveryFactory{
			session: provider, liveness: ProviderSessionRecoveryLive,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restarted.providerControlWG.Wait()
	requests := provider.LastRequests()
	if len(requests) != 1 || !strings.Contains(requests[0].Message, "perform only the Harness conclusion below") ||
		strings.Contains(requests[0].Message, "previous Blackboard conclusion result was invalid") {
		t.Fatalf("restarted initial Session directive=%#v", requests)
	}
}
