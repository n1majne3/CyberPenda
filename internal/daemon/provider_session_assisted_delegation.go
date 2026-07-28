package daemon

import (
	"context"

	"pentest/internal/runtime"
)

// assistedProductionBoundSession preserves the production binding lifecycle
// while exposing assisted Blackboard hooks only when the native session owns
// the complete observation, lineage, and structured-result contract.
type assistedProductionBoundSession struct {
	*productionBoundSession
	observationSink runtime.ProviderSessionObservationSink
	lineageResolver runtime.ProviderSessionCompleteTurnLineageResolver
	resultSource    runtime.ProviderSessionAttemptResultSource
}

func newAssistedProductionBoundSession(base *productionBoundSession) (*assistedProductionBoundSession, bool) {
	if base == nil || base.ProviderSession == nil {
		return nil, false
	}
	observationSink, observationOK := base.ProviderSession.(runtime.ProviderSessionObservationSink)
	lineageResolver, lineageOK := base.ProviderSession.(runtime.ProviderSessionCompleteTurnLineageResolver)
	resultSource, resultOK := base.ProviderSession.(runtime.ProviderSessionAttemptResultSource)
	if !observationOK || !lineageOK || !resultOK {
		return nil, false
	}
	return &assistedProductionBoundSession{
		productionBoundSession: base,
		observationSink:        observationSink,
		lineageResolver:        lineageResolver,
		resultSource:           resultSource,
	}, true
}

func newProductionBoundProviderSession(native runtime.ProviderSession, onClose func(context.Context)) (runtime.ProviderSession, error) {
	base := &productionBoundSession{ProviderSession: native, onClose: onClose}
	capabilities := native.Capabilities()
	if !capabilities.AssistedConclusion {
		return base, nil
	}
	if !capabilities.PersistentSession || !capabilities.SendTurn {
		return nil, errAssistedConclusionUnsupported
	}
	assisted, ok := newAssistedProductionBoundSession(base)
	if !ok {
		return nil, errAssistedConclusionUnsupported
	}
	return assisted, nil
}

func (s *assistedProductionBoundSession) SetObservationSink(sink runtime.ProviderSessionObserve) {
	s.observationSink.SetObservationSink(sink)
}

func (s *assistedProductionBoundSession) ResolveProviderSessionTurnLineage(requestID, providerTurnID string) (runtime.ProviderSessionTurnLineage, bool) {
	return s.lineageResolver.ResolveProviderSessionTurnLineage(requestID, providerTurnID)
}

func (s *assistedProductionBoundSession) SetAttemptResultSink(sink runtime.ProviderSessionAttemptResultSink) {
	s.resultSource.SetAttemptResultSink(sink)
}

func (s *assistedProductionBoundSession) SetAttemptResultValidationFailureSink(sink runtime.ProviderSessionAttemptResultValidationFailureSink) {
	s.resultSource.SetAttemptResultValidationFailureSink(sink)
}

var (
	_ runtime.ProviderSessionObservationSink             = (*assistedProductionBoundSession)(nil)
	_ runtime.ProviderSessionCompleteTurnLineageResolver = (*assistedProductionBoundSession)(nil)
	_ runtime.ProviderSessionAttemptResultSource         = (*assistedProductionBoundSession)(nil)
)
