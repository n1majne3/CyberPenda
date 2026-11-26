package daemon

import (
	"fmt"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
)

func (server *Server) wrapOwnerProviderAdapter(adapter runtime.Adapter, contract owner.Contract, provider runtimeprofile.Provider, providerHome string) (runtime.Adapter, error) {
	if provider != runtimeprofile.ProviderPi || providerHome == "" {
		return adapter, nil
	}
	var retained []string
	collect := func(payload map[string]any) {
		if payload["stream"] == "pi_session" {
			if text, ok := payload["text"].(string); ok {
				retained = append(retained, text)
			}
		}
	}
	switch contract.Kind {
	case owner.KindTask:
		events, err := server.tasks.Events(contract.ID)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			if event.Kind == task.EventKindRuntimeOutput {
				collect(event.Payload)
			}
		}
	case owner.KindSession:
		events, err := server.sessions.EventsByKind(contract.ID, session.EventKindRuntimeOutput)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			collect(event.Payload)
		}
	default:
		return nil, fmt.Errorf("unknown Runtime Owner kind %q", contract.Kind)
	}
	return wrapPersistentProviderAdapter(adapter, provider, providerHome, retained...), nil
}
