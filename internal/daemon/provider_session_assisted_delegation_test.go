package daemon

import (
	"errors"
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
)

type incompleteAssistedDelegationSession struct {
	runtime.ProviderSession
}

type assistedDelegationSession struct {
	runtime.ProviderSession
	observationSink runtime.ProviderSessionObserve
	resultSink      runtime.ProviderSessionAttemptResultSink
	failureSink     runtime.ProviderSessionAttemptResultValidationFailureSink
	lineage         runtime.ProviderSessionTurnLineage
}

func newAssistedDelegationSession() *assistedDelegationSession {
	return &assistedDelegationSession{
		ProviderSession: runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
			SessionID: "assisted-session",
			Capabilities: runtimeplugin.Capabilities{
				PersistentSession:  true,
				SendTurn:           true,
				AssistedConclusion: true,
			},
		}),
		lineage: runtime.ProviderSessionTurnLineage{RequestID: "request-1", ProviderTurnID: "turn-1"},
	}
}

func (s *assistedDelegationSession) SetObservationSink(sink runtime.ProviderSessionObserve) {
	s.observationSink = sink
}

func (s *assistedDelegationSession) ResolveProviderSessionTurnLineage(_, _ string) (runtime.ProviderSessionTurnLineage, bool) {
	return s.lineage, true
}

func (s *assistedDelegationSession) SetAttemptResultSink(sink runtime.ProviderSessionAttemptResultSink) {
	s.resultSink = sink
}

func (s *assistedDelegationSession) SetAttemptResultValidationFailureSink(sink runtime.ProviderSessionAttemptResultValidationFailureSink) {
	s.failureSink = sink
}

func TestAssistedProductionBoundSessionDelegatesCompleteContract(t *testing.T) {
	inner := newAssistedDelegationSession()
	base := &productionBoundSession{ProviderSession: inner}

	session, ok := newAssistedProductionBoundSession(base)
	if !ok {
		t.Fatal("complete assisted session was rejected")
	}
	observationSink := func(runtime.ProviderSessionObservation) {}
	resultSink := func(runtime.ProviderSessionAttemptResult) {}
	failureSink := func(runtime.ProviderSessionAttemptResultValidationFailure) {}
	session.SetObservationSink(observationSink)
	session.SetAttemptResultSink(resultSink)
	session.SetAttemptResultValidationFailureSink(failureSink)
	lineage, found := session.ResolveProviderSessionTurnLineage("request-1", "turn-1")

	if inner.observationSink == nil || inner.resultSink == nil || inner.failureSink == nil {
		t.Fatal("assisted sinks were not delegated to the native session")
	}
	if !found || lineage != inner.lineage {
		t.Fatalf("lineage = %#v, %v; want %#v, true", lineage, found, inner.lineage)
	}
}

func TestProductionBoundSessionFailsClosedWhenAdvertisedContractIsIncomplete(t *testing.T) {
	inner := &incompleteAssistedDelegationSession{ProviderSession: runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "incomplete-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession:  true,
			SendTurn:           true,
			AssistedConclusion: true,
		},
	})}

	session, err := newProductionBoundProviderSession(inner, nil)

	if !errors.Is(err, errAssistedConclusionUnsupported) {
		t.Fatalf("error = %v, want assisted conclusion unsupported", err)
	}
	if session != nil {
		t.Fatalf("session = %#v, want nil", session)
	}
}

func TestProductionBoundSessionDoesNotExposeAssistedContractsWhenDisabled(t *testing.T) {
	inner := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "ordinary-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})

	session, err := newProductionBoundProviderSession(inner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := session.(runtime.ProviderSessionObservationSink); ok {
		t.Fatal("ordinary production binding exposes observation sink")
	}
	if _, ok := session.(runtime.ProviderSessionCompleteTurnLineageResolver); ok {
		t.Fatal("ordinary production binding exposes lineage resolver")
	}
	if _, ok := session.(runtime.ProviderSessionAttemptResultSource); ok {
		t.Fatal("ordinary production binding exposes attempt result source")
	}
}
