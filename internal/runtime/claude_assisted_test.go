package runtime

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"pentest/internal/runtimeplugin"
)

const validClaudeAttemptResult = `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:claude","create":true,"summary":"No reusable result","outcome":"failed"},"tested_targets":[{"key":"objective:auth","create_objective":{"objective":"Inspect authentication"}}],"produced_targets":[]}`

func newAssistedClaudeSession(t *testing.T, kind RuntimeTurnKind) (*ClaudeCodeProviderSession, string) {
	t.Helper()
	requestID := "claude-control-request"
	if kind == RuntimeTurnKindWork {
		requestID = "claude-work-request"
	}
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"claude/input": {Result: json.RawMessage(`{"session_id":"claude-session","turn_id":"claude-turn"}`)},
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
		Transport: transport, SessionID: "claude-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: requestID, Message: "inspect", TurnKind: kind,
	}, nil); err != nil {
		t.Fatal(err)
	}
	return session, requestID
}

func TestClaudeWorkTurnAndSpoofedLineageNeverProduceAssistedResult(t *testing.T) {
	session, requestID := newAssistedClaudeSession(t, RuntimeTurnKindWork)
	callbacks := 0
	session.SetAttemptResultSink(func(ProviderSessionAttemptResult) { callbacks++ })
	session.SetAttemptResultValidationFailureSink(func(ProviderSessionAttemptResultValidationFailure) { callbacks++ })
	var observations []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) { observations = append(observations, observation) })

	for _, params := range []string{
		`{"request_id":"` + requestID + `","session_id":"claude-session","turn_id":"claude-turn","result":` + strconv.Quote(validClaudeAttemptResult) + `}`,
		`{"request_id":"other-request","session_id":"claude-session","turn_id":"claude-turn","tool_call_id":"tool-1","tool_name":"Read"}`,
		`{"request_id":"` + requestID + `","session_id":"other-session","turn_id":"claude-turn","tool_call_id":"tool-1","tool_name":"Read"}`,
		`{"request_id":"` + requestID + `","session_id":"claude-session","turn_id":"other-turn","tool_call_id":"tool-1","tool_name":"Read"}`,
	} {
		method := "claude/tool/used"
		if json.Valid([]byte(params)) {
			var decoded map[string]any
			_ = json.Unmarshal([]byte(params), &decoded)
			if _, ok := decoded["result"]; ok {
				method = "claude/attempt_result"
			}
		}
		session.HandleEvent(SandboxBridgeEvent{Method: method, Params: json.RawMessage(params)}, nil)
	}
	if callbacks != 0 || len(observations) != 0 {
		t.Fatalf("uncorrelated/work callbacks = %d, observations = %#v", callbacks, observations)
	}
}

func TestClaudeInvalidControlResultReportsOneBoundedFailure(t *testing.T) {
	session, requestID := newAssistedClaudeSession(t, RuntimeTurnKindControl)
	var failures []ProviderSessionAttemptResultValidationFailure
	session.SetAttemptResultValidationFailureSink(func(failure ProviderSessionAttemptResultValidationFailure) {
		failures = append(failures, failure)
	})
	event := SandboxBridgeEvent{Method: "claude/attempt_result_invalid", Params: json.RawMessage(
		`{"request_id":"` + requestID + `","session_id":"claude-session","turn_id":"claude-turn","reason":"secret provider output"}`,
	)}
	session.HandleEvent(event, nil)
	session.HandleEvent(event, nil)
	want := ProviderSessionAttemptResultValidationFailure{
		RequestID: requestID, SessionID: "claude-session", ProviderTurnID: "claude-turn",
		ValidationErrorCode: ProviderSessionAttemptResultInvalid,
	}
	if len(failures) != 1 || failures[0] != want {
		t.Fatalf("validation failures = %#v, want %#v", failures, want)
	}
}

func TestClaudeDuplicateToolEventsProduceOneObservation(t *testing.T) {
	session, requestID := newAssistedClaudeSession(t, RuntimeTurnKindWork)
	var observations []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) { observations = append(observations, observation) })
	event := SandboxBridgeEvent{Method: "claude/tool/used", Params: json.RawMessage(
		`{"request_id":"` + requestID + `","session_id":"claude-session","turn_id":"claude-turn","tool_call_id":"tool-1","tool_name":"Read","input":{"secret":"discard"}}`,
	)}
	session.HandleEvent(event, nil)
	session.HandleEvent(event, nil)
	if len(observations) != 1 || observations[0].ToolCallID != "tool-1" || observations[0].ToolName != "Read" {
		t.Fatalf("tool observations = %#v", observations)
	}
}

func TestClaudeTerminalStatusesAndDuplicateDeliveryMatchObservationContract(t *testing.T) {
	for _, test := range []struct {
		name, status string
	}{
		{name: "completed", status: "completed"},
		{name: "failed", status: "failed"},
		{name: "interrupted", status: "interrupted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
				"claude/input": {Result: json.RawMessage(`{"session_id":"claude-session","turn_id":"claude-turn"}`)},
			}}
			session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
				Transport: transport, SessionID: "claude-session",
				Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
			})
			if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
				RequestID: "claude-request", Message: "inspect", TurnKind: RuntimeTurnKindWork,
			}, nil); err != nil {
				t.Fatal(err)
			}
			var got []ProviderSessionObservation
			session.SetObservationSink(func(observation ProviderSessionObservation) { got = append(got, observation) })
			event := SandboxBridgeEvent{Method: "claude/turn/completed", Params: json.RawMessage(
				`{"request_id":"claude-request","session_id":"claude-session","turn_id":"claude-turn","status":"` + test.status + `"}`,
			)}
			session.HandleEvent(event, nil)
			session.HandleEvent(event, nil)
			want := ProviderSessionObservation{
				Kind: ProviderSessionObservationTurnCompleted, RequestID: "claude-request",
				SessionID: "claude-session", ProviderTurnID: "claude-turn", Status: test.status,
			}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("terminal observations = %#v", got)
			}
		})
	}
}

func TestClaudeAndFakeProviderSessionsExposeTheSameAssistedSeams(t *testing.T) {
	claudeSession := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "claude-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	fakeSession := NewFakeProviderSession(FakeProviderSessionConfig{
		SessionID:    "fake-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	for name, session := range map[string]ProviderSession{"claude": claudeSession, "fake": fakeSession} {
		capabilities := session.Capabilities()
		if !capabilities.PersistentSession || !capabilities.SendTurn || !capabilities.AssistedConclusion {
			t.Fatalf("%s assisted capabilities = %#v", name, capabilities)
		}
		if _, ok := session.(ProviderSessionObservationSink); !ok {
			t.Fatalf("%s lacks normalized observation seam", name)
		}
		if _, ok := session.(ProviderSessionCompleteTurnLineageResolver); !ok {
			t.Fatalf("%s lacks complete Turn lineage seam", name)
		}
		if _, ok := session.(ProviderSessionAttemptResultSource); !ok {
			t.Fatalf("%s lacks structured Attempt result seam", name)
		}
	}
}
