package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

// conflictingWorkTurnSession models a provider whose in-flight work turn never
// settles, so every assisted-conclusion control SendTurn is rejected with the
// single-active-call conflict (issue #177).
type conflictingWorkTurnSession struct {
	*runtime.FakeProviderSession
}

func (session *conflictingWorkTurnSession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	if request.TurnKind == runtime.RuntimeTurnKindControl {
		return runtime.ProviderSessionResult{}, runtime.ErrProviderSessionControlConflict
	}
	return session.FakeProviderSession.SendTurn(ctx, request, emit)
}

// TestAssistedConclusionNonSettlingWorkTurnBecomesNonRetryableTerminal proves
// that a permanently active work turn does not strand the operator on an
// endlessly-retryable semantic_conclusion_runtime_recovery_required. After a
// bounded number of conflict-driven recoveries the receipt reaches the distinct
// non-retryable semantic_conclusion_work_turn_never_settled terminal, and
// further retries are refused instead of looping.
func TestAssistedConclusionNonSettlingWorkTurnBecomesNonRetryableTerminal(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			return &conflictingWorkTurnSession{FakeProviderSession: fake}
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID,
		ProviderTurnID: "work-turn-never-settles", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID,
		ProviderTurnID: "work-turn-never-settles", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}

	// Automatic dispatch conflicts. The first conflicts stay recoverable so a
	// transient work turn could still settle on retry.
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("automatic dispatch conflict = %#v, want runtime recovery required", found.BlackboardConclusion)
	}

	// Two explicit retries also conflict. The bounded budget is exhausted on the
	// second retry, flipping the receipt into the distinct non-retryable terminal.
	unauthorized := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+projectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
	unauthorized.Header.Set("Idempotency-Key", "host-runtime-must-not-retry")
	unauthorizedResponse := httptest.NewRecorder()
	server.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless retry status = %d body %s, want unauthorized", unauthorizedResponse.Code, unauthorizedResponse.Body.String())
	}

	retry := func(key string) int {
		request := httptest.NewRequest(http.MethodPost,
			"/api/projects/"+projectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
		request.Header.Set("Authorization", "Bearer "+server.operatorToken)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response.Code
	}

	if code := retry("work-turn-retry-1"); code != http.StatusAccepted {
		t.Fatalf("retry #1 status = %d", code)
	}
	found = waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("retry #1 conflict = %#v, want runtime recovery required", found.BlackboardConclusion)
	}

	if code := retry("work-turn-retry-2"); code != http.StatusAccepted {
		t.Fatalf("retry #2 status = %d", code)
	}
	found = waitForBlackboardConclusionErrorCode(t, server, projectID, created.ID, task.BlackboardConclusionErrorWorkTurnNeverSettled)
	if found.BlackboardConclusion.State != task.BlackboardConclusionStateActionRequired {
		t.Fatalf("terminal state = %q, want action_required", found.BlackboardConclusion.State)
	}
	if found.BlackboardConclusion.RetryAvailable {
		t.Fatalf("work-turn-never-settled terminal must not be retryable: %#v", found.BlackboardConclusion)
	}

	// A further retry is refused rather than looping on the recoverable error.
	if code := retry("work-turn-retry-3"); code != http.StatusConflict {
		t.Fatalf("retry #3 status = %d, want 409 for non-retryable terminal", code)
	}
	found = waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorWorkTurnNeverSettled {
		t.Fatalf("terminal error regressed to %q after refused retry", found.BlackboardConclusion.ErrorCode)
	}
}

func waitForBlackboardConclusionErrorCode(t *testing.T, server *Server, projectID, taskID string, code task.BlackboardConclusionErrorCode) task.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			var found task.Task
			if err := json.NewDecoder(response.Body).Decode(&found); err == nil && found.BlackboardConclusion.ErrorCode == code {
				return found
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Task %s did not reach Blackboard conclusion error code %q", taskID, code)
	return task.Task{}
}
