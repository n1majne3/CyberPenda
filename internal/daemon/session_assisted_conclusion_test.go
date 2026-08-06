package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/runtime"
	"pentest/internal/session"
)

func TestAssistedSessionWorkTurnConcludesOnlySessionBlackboard(t *testing.T) {
	server, projectID, profileID, provider := newAssistedConclusionFixture(t, true)

	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"Inspect the standalone target",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create assisted Session status = %d body %s", createResponse.Code, createResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Session: %v", err)
	}
	if created.RunControls.BlackboardConclusionMode != session.BlackboardConclusionModeAssisted {
		t.Fatalf("created Session run controls = %#v", created.RunControls)
	}
	waitForAssistedProviderRequests(t, provider, 1)
	workRequest := provider.LastRequests()[0]
	projectBefore, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatalf("read Project Blackboard before Session conclusion: %v", err)
	}

	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID, ProviderTurnID: "session-work-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID, ProviderTurnID: "session-work-1", Status: "completed"},
	} {
		if err := provider.EmitObservation(observation); err != nil {
			t.Fatalf("emit Session observation %#v: %v", observation, err)
		}
	}
	waitForAssistedProviderRequests(t, provider, 2)
	controlRequest := provider.LastRequests()[1]
	if controlRequest.TurnKind != runtime.RuntimeTurnKindControl || controlRequest.ModelProviderID != workRequest.ModelProviderID || controlRequest.Model != workRequest.Model || controlRequest.RequestedReasoningEffort != workRequest.RequestedReasoningEffort {
		t.Fatalf("Session conclusion control selection = %#v, source = %#v", controlRequest, workRequest)
	}
	if !strings.Contains(controlRequest.Message, "Session Blackboard") || strings.Contains(controlRequest.Message, "Project Blackboard") {
		t.Fatalf("Session conclusion directive crossed owner boundary: %q", controlRequest.Message)
	}

	waitForSessionBlackboardConclusionState(t, server, created.ID, session.BlackboardConclusionStateConcluding)
	if err := emitSessionAttemptResultAndComplete(provider, `{
		"schema":"runtime-attempt-result/v1",
		"base_revision":0,
		"attempt":{"key":"attempt:session-search","create":true,"summary":"Inspected the standalone target.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:session-search","create_objective":{"objective":"Inspect the standalone target."}}],
		"produced_targets":[]
	}`); err != nil {
		t.Fatalf("emit Session Attempt result: %v", err)
	}
	waitForSessionBlackboardConclusionState(t, server, created.ID, session.BlackboardConclusionStateClean)

	snapshot, err := server.blackboardV2.SessionRuntimeSnapshot(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read Session Blackboard: %v", err)
	}
	history, err := server.blackboardV2.ReadSessionHistory(context.Background(), created.ID, "attempt:session-search", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read Session Attempt history: %v", err)
	}
	if snapshot.Revision == 0 || !sessionAttemptHistoryContainsStatus(history.Items, "inconclusive") {
		t.Fatalf("Session Blackboard conclusion snapshot = %#v, history = %#v", snapshot, history.Items)
	}
	projectAfter, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatalf("read Project Blackboard after Session conclusion: %v", err)
	}
	if projectAfter.Revision != projectBefore.Revision {
		t.Fatalf("Session conclusion mutated Project Blackboard revision from %d to %d", projectBefore.Revision, projectAfter.Revision)
	}
	if got := len(provider.LastRequests()); got != 2 {
		t.Fatalf("Session provider requests after duplicate-free conclusion = %d, want 2", got)
	}
}

func TestAssistedSessionStopSettlesPendingConclusionRecovery(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"Stop after exploratory work",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create assisted Session status = %d body %s", createResponse.Code, createResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Session: %v", err)
	}
	waitForAssistedProviderRequests(t, provider, 1)
	workRequest := provider.LastRequests()[0]
	if err := provider.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "session-stop-work", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "session-stop-work", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, provider, 2)

	stopRequest := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil)
	stopResponse := httptest.NewRecorder()
	server.ServeHTTP(stopResponse, stopRequest)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("stop assisted Session status = %d body %s", stopResponse.Code, stopResponse.Body.String())
	}
	found := waitForSessionBlackboardConclusionState(t, server, created.ID, session.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != session.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("stopped Session conclusion = %#v", found.BlackboardConclusion)
	}
}

func TestAssistedSessionMessageWaitsForPendingConclusionSettlement(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"Wait for the semantic Session boundary",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create assisted Session status = %d body %s", createResponse.Code, createResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Session: %v", err)
	}
	waitForAssistedProviderRequests(t, provider, 1)
	workRequest := provider.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID, ProviderTurnID: "session-queue-work", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID, ProviderTurnID: "session-queue-work", Status: "completed"},
	} {
		if err := provider.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, provider, 2)
	if got := len(provider.LastRequests()); got != 2 {
		t.Fatalf("provider requests before conclusion result = %d, want 2", got)
	}

	responseCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		messageRequest := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/messages", bytes.NewBufferString(`{"input":"Continue after settlement"}`))
		messageRequest.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, messageRequest)
		responseCh <- response
	}()
	select {
	case <-responseCh:
		t.Fatal("Session message returned before the pending conclusion settled")
	case <-time.After(50 * time.Millisecond):
	}
	if got := len(provider.LastRequests()); got != 2 {
		t.Fatalf("Session message overtook pending conclusion with %d provider requests", got)
	}

	if err := emitSessionAttemptResultAndComplete(provider, `{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:queued-message","create":true,"summary":"Settled the pending Session boundary.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:queued-message","create_objective":{"objective":"Settle the pending Session boundary."}}],
		"produced_targets":[]
	}`); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-responseCh:
		if response.Code != http.StatusAccepted {
			t.Fatalf("queued Session message status = %d body %s", response.Code, response.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued Session message")
	}
	waitForAssistedProviderRequests(t, provider, 3)
}

func TestAssistedSessionRegeneratesAfterConcurrentSessionBlackboardAdvance(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"Reconcile a changing Session Blackboard",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create assisted Session status = %d body %s", createResponse.Code, createResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Session: %v", err)
	}
	waitForAssistedProviderRequests(t, provider, 1)
	workRequest := provider.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID, ProviderTurnID: "session-version-work", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID, ProviderTurnID: "session-version-work", Status: "completed"},
	} {
		if err := provider.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, provider, 2)
	concludeRequest := provider.LastRequests()[1]
	peer, err := server.blackboardV2.ApplyForSession(context.Background(), created.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "session-peer-advance",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "objective:peer", Type: "objective",
			Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Peer Session progress"},
		}},
	})
	if err != nil || peer.Revision != 1 {
		t.Fatalf("advance Session Blackboard from peer = %#v, err=%v", peer, err)
	}
	if err := emitSessionAttemptResultAndComplete(provider, `{
		"schema":"runtime-attempt-result/v1",
		"base_revision":0,
		"attempt":{"key":"attempt:session-version","create":true,"summary":"Checked the changing Session.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:session-version","create_objective":{"objective":"Checked the changing Session."}}],
		"produced_targets":[]
	}`); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, provider, 3)
	regenerateRequest := provider.LastRequests()[2]
	directive := strings.ToLower(regenerateRequest.Message)
	if regenerateRequest.RequestID == concludeRequest.RequestID || !strings.Contains(regenerateRequest.RequestID, "version") ||
		!strings.Contains(directive, "session blackboard") || !strings.Contains(directive, "reread") || !strings.Contains(directive, "base_revision 1") || strings.Contains(directive, "project blackboard") {
		t.Fatalf("Session regeneration request = %#v", regenerateRequest)
	}
	if err := emitSessionAttemptResultAndComplete(provider, `{
		"schema":"runtime-attempt-result/v1",
		"base_revision":1,
		"attempt":{"key":"attempt:session-version","create":true,"summary":"Checked the changing Session.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:session-version","create_objective":{"objective":"Checked the changing Session."}}],
		"produced_targets":[]
	}`); err != nil {
		t.Fatal(err)
	}
	found := waitForSessionBlackboardConclusionState(t, server, created.ID, session.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil || *found.BlackboardConclusion.AppliedRevision <= peer.Revision || len(provider.LastRequests()) != 3 {
		t.Fatalf("Session regenerated conclusion = %#v requests=%d", found.BlackboardConclusion, len(provider.LastRequests()))
	}
}

func TestAssistedSessionInvalidConclusionUsesOneBoundedRepair(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"Bound invalid Session conclusions",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create assisted Session status = %d body %s", createResponse.Code, createResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Session: %v", err)
	}
	waitForAssistedProviderRequests(t, provider, 1)
	workRequest := provider.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID, ProviderTurnID: "session-invalid-work", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID, ProviderTurnID: "session-invalid-work", Status: "completed"},
	} {
		if err := provider.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, provider, 2)
	invalid := `{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`
	if err := emitSessionAttemptResultAndComplete(provider, invalid); err == nil {
		t.Fatal("invalid Session conclusion unexpectedly decoded")
	}
	waitForAssistedProviderRequests(t, provider, 3)
	if err := emitSessionAttemptResultAndComplete(provider, invalid); err == nil {
		t.Fatal("invalid Session repair unexpectedly decoded")
	}
	found := waitForSessionBlackboardConclusionState(t, server, created.ID, session.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != session.BlackboardConclusionErrorRepairExhausted || len(provider.LastRequests()) != 3 {
		t.Fatalf("Session invalid conclusion = %#v requests=%d", found.BlackboardConclusion, len(provider.LastRequests()))
	}
}

func TestAssistedSessionRepairDirectiveCarriesBoundedValidationReason(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	created := createAssistedConclusionSession(t, server, profileID, "Bound invalid Session conclusions")
	waitForAssistedProviderRequests(t, provider, 1)
	workRequest := provider.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID, ProviderTurnID: "session-reason-work", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID, ProviderTurnID: "session-reason-work", Status: "completed"},
	} {
		if err := provider.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, provider, 2)
	concludeRequest := provider.LastRequests()[1]
	invalid := `{
		"schema":"runtime-attempt-result/v1",
		"base_revision":0,
		"attempt":{"key":"attempt/arena-session","create":true,"summary":"Tested the Session surface.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test the Session surface."}}],
		"produced_targets":[]
	}`
	if err := emitSessionAttemptResultAndComplete(provider, invalid); err == nil {
		t.Fatal("invalid Session Conclude result unexpectedly decoded")
	}
	waitForAssistedProviderRequests(t, provider, 3)
	repairRequest := provider.LastRequests()[2]
	if repairRequest.RequestID == concludeRequest.RequestID {
		t.Fatalf("Session repair reused the Conclude request ID %q", repairRequest.RequestID)
	}
	for _, required := range []string{"invalid_key_format", "attempt.key", "attempt: prefix", "runtime-attempt-result/v1"} {
		if !strings.Contains(repairRequest.Message, required) {
			t.Fatalf("Session repair directive missing %q: %s", required, repairRequest.Message)
		}
	}
	for _, forbidden := range []string{"arena-session", invalid} {
		if strings.Contains(repairRequest.Message, forbidden) {
			t.Fatalf("Session repair directive leaked raw result content %q: %s", forbidden, repairRequest.Message)
		}
	}
	found := waitForSessionBlackboardConclusionState(t, server, created.ID, session.BlackboardConclusionStateConcluding)
	if found.BlackboardConclusion.ValidationReason != "" {
		t.Fatalf("Session concluding view exposed validation detail too early: %#v", found.BlackboardConclusion)
	}
}

func TestAssistedSessionRepeatedInvalidResultExposesBoundedReason(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	created := createAssistedConclusionSession(t, server, profileID, "Bound invalid Session conclusion budget")
	waitForAssistedProviderRequests(t, provider, 1)
	workRequest := provider.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID, ProviderTurnID: "session-budget-work", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID, ProviderTurnID: "session-budget-work", Status: "completed"},
	} {
		if err := provider.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, provider, 2)
	invalid := `{
		"schema":"runtime-attempt-result/v1",
		"base_revision":0,
		"attempt":{"key":"attempt/arena-session","create":true,"summary":"Tested the Session surface.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test the Session surface."}}],
		"produced_targets":[]
	}`
	if err := emitSessionAttemptResultAndComplete(provider, invalid); err == nil {
		t.Fatal("invalid Session Conclude result unexpectedly decoded")
	}
	waitForAssistedProviderRequests(t, provider, 3)
	if err := emitSessionAttemptResultAndComplete(provider, invalid); err == nil {
		t.Fatal("invalid Session repair result unexpectedly decoded")
	}
	found := waitForSessionBlackboardConclusionState(t, server, created.ID, session.BlackboardConclusionStateActionRequired)
	conclusion := found.BlackboardConclusion
	if conclusion.ErrorCode != session.BlackboardConclusionErrorRepairExhausted {
		t.Fatalf("Session action-required error code = %q", conclusion.ErrorCode)
	}
	if conclusion.ValidationReason != "invalid_key_format" || conclusion.ValidationFieldPath != "attempt.key" {
		t.Fatalf("Session action-required validation detail = %#v", conclusion)
	}
	if !strings.Contains(conclusion.ValidationExpected, "attempt: prefix") {
		t.Fatalf("Session action-required validation expected = %q, want attempt: prefix", conclusion.ValidationExpected)
	}
	if len(provider.LastRequests()) != 3 {
		t.Fatalf("Session automatic provider requests = %d, want work, Conclude, repair", len(provider.LastRequests()))
	}
}

func createAssistedConclusionSession(t *testing.T, server *Server, profileID, input string) session.Session {
	t.Helper()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"`+input+`",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create assisted Session status = %d body %s", createResponse.Code, createResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Session: %v", err)
	}
	return created
}

func sessionAttemptHistoryContainsStatus(items []blackboardv2.HistoryItem, status string) bool {
	for _, item := range items {
		if item.Record != nil && item.Record.Status == status {
			return true
		}
	}
	return false
}

func emitSessionAttemptResultAndComplete(provider *runtime.FakeProviderSession, raw string) error {
	resultErr := provider.EmitAttemptResult([]byte(raw))
	if err := provider.EmitObservation(runtime.ProviderSessionObservation{
		Kind:   runtime.ProviderSessionObservationTurnCompleted,
		Status: "completed",
	}); err != nil {
		return err
	}
	return resultErr
}

func waitForSessionBlackboardConclusionState(t *testing.T, server *Server, sessionID string, state session.BlackboardConclusionState) session.Session {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			var found session.Session
			if err := json.NewDecoder(response.Body).Decode(&found); err == nil && found.BlackboardConclusion.State == state {
				return found
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Session %s did not reach Blackboard conclusion state %q", sessionID, state)
	return session.Session{}
}
