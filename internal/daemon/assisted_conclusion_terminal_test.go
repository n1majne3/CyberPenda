package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

type interruptingConclusionSession struct {
	*runtime.FakeProviderSession
	started     chan struct{}
	interrupted chan struct{}
}

func (session *interruptingConclusionSession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	if request.TurnKind != runtime.RuntimeTurnKindControl {
		return session.FakeProviderSession.SendTurn(ctx, request, emit)
	}
	close(session.started)
	<-ctx.Done()
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, Status: "interrupted",
	}); err != nil {
		return runtime.ProviderSessionResult{}, err
	}
	close(session.interrupted)
	return runtime.ProviderSessionResult{}, ctx.Err()
}

func TestAssistedConclusionFailedControlTerminalRequiresRuntimeRecovery(t *testing.T) {
	for _, status := range []string{"failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
			created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
			waitForAssistedProviderRequests(t, session, 1)
			work := session.LastRequests()[0]
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID,
				ProviderTurnID: "work-turn-terminal-" + status, ToolCallID: "tool-terminal-" + status,
				ToolName: "shell", Status: "succeeded",
			}); err != nil {
				t.Fatal(err)
			}
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID,
				ProviderTurnID: "work-turn-terminal-" + status, Status: "completed",
			}); err != nil {
				t.Fatal(err)
			}
			waitForAssistedProviderRequests(t, session, 2)

			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationTurnCompleted, Status: status,
			}); err != nil {
				t.Fatal(err)
			}

			found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
			if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
				t.Fatalf("error code = %q, want runtime recovery required", found.BlackboardConclusion.ErrorCode)
			}
			if len(session.LastRequests()) != 2 {
				t.Fatalf("provider requests = %d, failed terminal must not dispatch repair", len(session.LastRequests()))
			}
		})
	}
}

func TestStopInterruptedControlTerminalRecordsRuntimeRecoveryRequired(t *testing.T) {
	var interrupting *interruptingConclusionSession
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			interrupting = &interruptingConclusionSession{
				FakeProviderSession: fake, started: make(chan struct{}), interrupted: make(chan struct{}),
			}
			return interrupting
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID,
		ProviderTurnID: "work-turn-stop-terminal", ToolCallID: "tool-stop-terminal",
		ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID,
		ProviderTurnID: "work-turn-stop-terminal", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-interrupting.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Conclude SendTurn did not start")
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Stop status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-interrupting.interrupted:
	case <-time.After(time.Second):
		t.Fatal("provider interruption was not observed")
	}
	receipt, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || receipt == nil {
		t.Fatalf("latest receipt = %#v, %v", receipt, err)
	}
	if receipt.InternalState != task.BlackboardConclusionReceiptActionRequired || receipt.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("Stop interruption did not preserve recoverable semantic debt: %#v", receipt)
	}
}

func TestFailedWorkTurnWithNonBlackboardResultsSurfacesDurableAttention(t *testing.T) {
	for _, status := range []string{"failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
			created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
			waitForAssistedProviderRequests(t, session, 1)
			work := session.LastRequests()[0]
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID,
				ProviderTurnID: "work-terminal-" + status, ToolCallID: "tool-" + status,
				ToolName: "shell", Status: "succeeded",
			}); err != nil {
				t.Fatal(err)
			}
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID,
				ProviderTurnID: "work-terminal-" + status, Status: status,
			}); err != nil {
				t.Fatal(err)
			}
			found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
			if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired ||
				found.BlackboardConclusion.SourceWorkWatermark != 1 {
				t.Fatalf("failed Work attention = %#v", found.BlackboardConclusion)
			}
			if len(session.LastRequests()) != 1 {
				t.Fatalf("failed Work dispatched automatic Conclude: %d requests", len(session.LastRequests()))
			}
		})
	}
}
