package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"pentest/internal/task"
)

func TestPiLoopStepDoesNotCompleteTheProviderTurn(t *testing.T) {
	r := newPiLoopRecorder(t)
	r.push(t, "pi/turn_end", map[string]any{"message": piLoopAssistant("stop")})
	if got := r.session.currentTurn(); got != "turn-1" || r.session.assistedState().delivered["request-1"] {
		t.Fatalf("a model step settled its Provider Turn: turn=%q, delivered=%v", got, r.session.assistedState().delivered)
	}
	r.push(t, "pi/agent_end", map[string]any{"willRetry": false, "messages": []any{piLoopAssistant("stop")}})
	if got := r.session.currentTurn(); got != "turn-1" {
		t.Fatalf("settling Pi loop lost its Provider Turn: %q", got)
	}
	r.push(t, "pi/agent_settled", map[string]any{"aborted": false})
	r.push(t, "pi/agent_settled", map[string]any{"aborted": false})
	r.push(t, "pi/turn_start", nil)
	r.assertTerminal(t, "completed")
}

func TestPiLoopRetainsFailedOutcomeAfterAllRetries(t *testing.T) {
	r := newPiLoopRecorder(t)
	for attempt := range 4 {
		r.push(t, "pi/turn_end", map[string]any{"message": piLoopAssistant("error")})
		r.push(t, "pi/agent_end", map[string]any{"willRetry": attempt < 3, "messages": []any{piLoopAssistant("error")}})
		if got := r.session.currentTurn(); got != "turn-1" || r.session.assistedState().delivered["request-1"] {
			t.Fatalf("attempt %d completed the retrying Provider Turn: %q", attempt, got)
		}
		if attempt < 3 {
			r.push(t, "pi/auto_retry_start", map[string]any{"attempt": attempt + 1, "errorMessage": "Connection error."})
			r.push(t, "pi/agent_start", nil)
		}
	}
	r.push(t, "pi/auto_retry_end", map[string]any{"success": false, "finalError": "Connection error."})
	r.push(t, "pi/agent_settled", map[string]any{"aborted": false})
	r.assertTerminal(t, "failed")
	if got := r.events[len(r.events)-1]["error"]; got != "Connection error." {
		t.Fatalf("final failure lost its cause: %#v", r.events)
	}
}

func TestPiLoopSuccessfulRetryDoesNotRetainAnOldError(t *testing.T) {
	r := newPiLoopRecorder(t)
	r.push(t, "pi/agent_end", map[string]any{"willRetry": true, "messages": []any{piLoopAssistant("error")}})
	r.push(t, "pi/auto_retry_start", map[string]any{"errorMessage": "Connection error."})
	r.push(t, "pi/agent_start", nil)
	r.push(t, "pi/agent_end", map[string]any{"willRetry": false, "messages": []any{piLoopAssistant("stop")}})
	r.push(t, "pi/auto_retry_end", map[string]any{"success": true})
	r.push(t, "pi/agent_settled", map[string]any{"aborted": false})
	r.assertTerminal(t, "completed")
	if r.events[len(r.events)-1]["error"] != nil {
		t.Fatalf("successful retry retained an error: %#v", r.events)
	}
}

func TestPiLoopAbortAndMissingResultNeverClaimSuccess(t *testing.T) {
	for _, aborted := range []bool{true, false} {
		r := newPiLoopRecorder(t)
		r.push(t, "pi/agent_settled", map[string]any{"aborted": aborted})
		want := "failed"
		if aborted {
			want = "interrupted"
		}
		r.assertTerminal(t, want)
	}
}

func TestPiLoopRejectsAnotherNativeSession(t *testing.T) {
	r := newPiLoopRecorder(t)
	before := len(r.events)
	r.push(t, "pi/agent_end", map[string]any{"session_id": "other-session", "messages": []any{piLoopAssistant("error")}})
	if len(r.events) != before || r.session.currentTurn() != "turn-1" {
		t.Fatalf("foreign Session changed the current Provider Turn: %#v", r.events)
	}
}

func TestPiLoopTerminalBeforePromptResponseDoesNotResurrectTheTurn(t *testing.T) {
	r := &piLoopRecorder{}
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		params, err := json.Marshal(map[string]any{"session_id": "pi-session", "turn_id": request.ID,
			"willRetry": false, "messages": []any{piLoopAssistant("stop")}})
		if err != nil {
			t.Fatal(err)
		}
		r.session.HandleEvent(SandboxBridgeEvent{Method: "pi/agent_end", Params: params}, r.emit)
		params, err = json.Marshal(map[string]any{"session_id": "pi-session", "turn_id": request.ID, "aborted": false})
		if err != nil {
			t.Fatal(err)
		}
		r.session.HandleEvent(SandboxBridgeEvent{Method: "pi/agent_settled", Params: params}, r.emit)
		result, err := json.Marshal(map[string]any{"session_id": "pi-session", "turn_id": request.ID})
		return SandboxBridgeResponse{Result: result}, err
	}}
	r.session = NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-session"})
	t.Cleanup(func() { _ = r.session.Close(context.Background()) })
	if _, err := r.session.SendTurn(t.Context(), ProviderSessionRequest{RequestID: "early-1", Message: "Say hello", TurnKind: RuntimeTurnKindWork}, r.emit); err != nil {
		t.Fatal(err)
	}
	r.assertTerminal(t, "completed")
}

type piCloseFailureTransport struct{ fakeProviderTransport }

func (*piCloseFailureTransport) Close(context.Context) error { return errors.New("cleanup failed") }

func TestPiLoopCleanupFailureDoesNotRetainClosedState(t *testing.T) {
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: &piCloseFailureTransport{}, SessionID: "pi-session"})
	session.SetAttemptResultSink(func(ProviderSessionAttemptResult) {})
	if err := session.Close(t.Context()); err == nil || !session.SessionClosed() {
		t.Fatalf("expected a closed session with a cleanup error, got %v", err)
	}
	if _, retained := piAssistedStates.Load(session); retained {
		t.Fatal("cleanup failure retained a closed Pi session")
	}
}

func TestPiLoopLateMessageCannotRecreateClosedState(t *testing.T) {
	r := newPiLoopRecorderWithKind(t, RuntimeTurnKindControl)
	if err := r.session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.push(t, "pi/message_end", map[string]any{"message": piLoopAssistant("stop")})
	if _, retained := piAssistedStates.Load(r.session); retained {
		t.Fatal("late Provider output retained a closed Pi session")
	}
}

func TestPiLoopPublishesControlCandidateOnlyAfterSuccessfulSettlement(t *testing.T) {
	for _, reason := range []string{"error", "stop"} {
		t.Run(reason, func(t *testing.T) {
			r := newPiLoopRecorderWithKind(t, RuntimeTurnKindControl)
			published := false
			r.session.SetAttemptResultSink(func(ProviderSessionAttemptResult) { published = true })
			raw := `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:pi","create":true,"summary":"No reusable result","outcome":"failed"},"tested_targets":[{"key":"objective:auth","create_objective":{"objective":"Inspect authentication"}}],"produced_targets":[]}`
			r.push(t, "pi/message_end", map[string]any{"message": map[string]any{
				"role": "assistant", "content": []any{map[string]any{"type": "text", "text": raw}},
			}})
			r.push(t, "pi/agent_end", map[string]any{"willRetry": false, "messages": []any{piLoopAssistant(reason)}})
			if published {
				t.Fatal("Control Turn published a result before settlement")
			}
			r.push(t, "pi/agent_settled", map[string]any{"aborted": false})
			if published != (reason == "stop") {
				t.Fatalf("Control Turn result publication disagrees with %s outcome", reason)
			}
		})
	}
}

func TestPiLoopNativeErrorCorpusReplay(t *testing.T) {
	path := os.Getenv("CYBERPENDA_PI_RPC_ERROR_CORPUS")
	if path == "" {
		t.Skip("set CYBERPENDA_PI_RPC_ERROR_CORPUS to replay native Pi RPC output")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	r := newPiLoopRecorder(t)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	started, settled := false, false
	for scanner.Scan() {
		var params map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &params); err != nil {
			t.Fatal(err)
		}
		kind, _ := params["type"].(string)
		if kind == "response" {
			if params["command"] == "prompt" && params["success"] == true {
				started = true
			}
			continue
		}
		if !started {
			continue
		}
		r.push(t, "pi/"+kind, params)
		if kind == "agent_settled" {
			settled = true
		}
		if !settled && r.session.currentTurn() != "turn-1" {
			t.Fatalf("native %s completed a Provider Turn before settlement", kind)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !started || !settled {
		t.Fatal("corpus has no complete prompt and settlement interval")
	}
	r.assertTerminal(t, "failed")
}

type piLoopRecorder struct {
	session *PiProviderSession
	events  []task.EventPayload
}

func newPiLoopRecorder(t *testing.T) *piLoopRecorder {
	return newPiLoopRecorderWithKind(t, RuntimeTurnKindWork)
}

func newPiLoopRecorderWithKind(t *testing.T, kind RuntimeTurnKind) *piLoopRecorder {
	t.Helper()
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/prompt": {Result: json.RawMessage(`{"session_id":"pi-session","turn_id":"turn-1","status":"started"}`)},
	}}
	r := &piLoopRecorder{session: NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-session"})}
	t.Cleanup(func() { _ = r.session.Close(context.Background()) })
	if _, err := r.session.SendTurn(t.Context(), ProviderSessionRequest{RequestID: "request-1", Message: "Say hello", TurnKind: kind}, r.emit); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *piLoopRecorder) emit(kind task.EventKind, payload task.EventPayload) {
	if kind == task.EventKindLifecycle {
		r.events = append(r.events, payload)
	}
}

func (r *piLoopRecorder) push(t *testing.T, method string, params map[string]any) {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["session_id"]; !ok {
		params["session_id"] = "pi-session"
	}
	params["turn_id"] = "turn-1"
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	r.session.HandleEvent(SandboxBridgeEvent{Method: method, Params: raw}, r.emit)
}

func (r *piLoopRecorder) assertTerminal(t *testing.T, want string) {
	t.Helper()
	var terminals []task.EventPayload
	for _, event := range r.events {
		if outcome := event["outcome"]; outcome == "completed" || outcome == "failed" || outcome == "interrupted" {
			terminals = append(terminals, event)
		}
	}
	if r.session.currentTurn() != "" || len(terminals) != 1 || terminals[0]["outcome"] != want {
		t.Fatalf("want one %s terminal and no active Provider Turn; active=%q, events=%#v", want, r.session.currentTurn(), r.events)
	}
}

func piLoopAssistant(reason string) map[string]any {
	message := map[string]any{"role": "assistant", "stopReason": reason}
	if reason == "error" {
		message["errorMessage"] = "Connection error."
	}
	return message
}

func FuzzPiTurnLifecycleSettlementIsAbsorbing(f *testing.F) {
	f.Add([]byte{0, 1, 3, 4, 6, 0, 1, 2, 5, 7, 9, 0, 9})
	f.Add([]byte{8, 9, 0, 2, 10})
	f.Fuzz(func(t *testing.T, events []byte) {
		methods := []string{"pi/agent_start", "pi/turn_start", "pi/turn_end", "pi/turn_end", "pi/agent_end", "pi/agent_end", "pi/auto_retry_start", "pi/auto_retry_end", "pi/agent_settled", "pi/agent_settled", "pi/agent_end"}
		var current piTurnLifecycle
		for index, encoded := range events {
			if index >= 1024 {
				break
			}
			code := int(encoded) % len(methods)
			params := map[string]any{}
			switch code {
			case 2, 3:
				reason := "stop"
				if code == 3 {
					reason = "error"
				}
				params["message"] = piLoopAssistant(reason)
			case 4, 5:
				params["willRetry"] = code == 4
				params["messages"] = []any{piLoopAssistant("error")}
			case 7:
				params["success"] = encoded%2 == 0
			case 8, 9:
				params["aborted"] = code == 8
			}
			next, projection := transitionPiTurn(current, methods[code], params)
			if current.phase == piTurnSettled && (next != current || projection != "") {
				t.Fatal("duplicate or delayed events reopened a settled Provider Turn")
			}
			if current.phase != piTurnSettled && (code == 2 || code == 3) && next.phase == piTurnSettled {
				t.Fatal("a model step closed its Provider Turn")
			}
			if next.phase == piTurnSettled && next.outcome != "completed" && next.outcome != "failed" && next.outcome != "interrupted" {
				t.Fatalf("terminal Provider Turn has an unknown outcome: %#v", next)
			}
			current = next
		}
	})
}
