package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"pentest/internal/task"
)

// ProviderSessionRunAdapter keeps the Harness continuation alive while the
// provider session remains owned by the Task bridge. A launch sends exactly
// one native turn; subsequent native steer operations reuse this adapter and
// bridge without creating another container.
type ProviderSessionRunAdapter struct {
	session ProviderSession
	closed  <-chan struct{}

	mu           sync.Mutex
	continuation string
	emit         func(task.EventKind, task.EventPayload)
	record       func(NativeSessionMetadata) error
	metadata     func() NativeSessionMetadata
	// initialTurn carries resolved Runtime Turn Selection fields for the
	// launch turn. Message is still supplied by the Harness goal.
	initialTurn ProviderSessionRequest
}

// SetMetadataRecorder lets the Harness persist the bridge/container and
// provider session identity on the active Continuation without exposing the
// bridge handle to the daemon API.
func (a *ProviderSessionRunAdapter) SetMetadataRecorder(record func(NativeSessionMetadata) error) {
	a.mu.Lock()
	a.record = record
	a.mu.Unlock()
}

func (a *ProviderSessionRunAdapter) SetSessionMetadata(metadata func() NativeSessionMetadata) {
	a.mu.Lock()
	a.metadata = metadata
	a.mu.Unlock()
}

func NewProviderSessionRunAdapter(session ProviderSession, closed <-chan struct{}) *ProviderSessionRunAdapter {
	return &ProviderSessionRunAdapter{session: session, closed: closed}
}

func (a *ProviderSessionRunAdapter) Name() string {
	if a.session == nil {
		return "provider-session"
	}
	return "provider-session:" + a.session.SessionID()
}

func (a *ProviderSessionRunAdapter) BindContinuation(id string) {
	a.mu.Lock()
	a.continuation = strings.TrimSpace(id)
	a.mu.Unlock()
}

// SetInitialTurnSelection records the resolved Model Provider, model, and
// Requested Reasoning Effort for the launch Runtime Turn.
func (a *ProviderSessionRunAdapter) SetInitialTurnSelection(selection ProviderSessionRequest) {
	a.mu.Lock()
	a.initialTurn = selection
	a.mu.Unlock()
}

func (a *ProviderSessionRunAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	if a.session == nil {
		return fmt.Errorf("provider session is required")
	}
	a.mu.Lock()
	continuation := a.continuation
	selection := a.initialTurn
	a.emit = emit
	a.mu.Unlock()
	if continuation == "" {
		return fmt.Errorf("provider session continuation is required")
	}
	requestID := "launch:" + continuation
	request := ProviderSessionRequest{
		RequestID:                requestID,
		Message:                  goal,
		ModelProviderID:          selection.ModelProviderID,
		Model:                    selection.Model,
		RequestedReasoningEffort: selection.RequestedReasoningEffort,
	}
	_, err := a.session.SendTurn(ctx, request, emit)
	if err != nil {
		return err
	}
	a.mu.Lock()
	record, metadata := a.record, a.metadata
	a.mu.Unlock()
	if record != nil && metadata != nil {
		if err := record(metadata()); err != nil {
			return err
		}
	}
	if a.closed == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.closed:
		return ErrProviderSessionClosed
	}
}

// HandleEvent forwards provider notifications to the active Harness event
// sink. The provider adapter performs protocol-specific normalization.
func (a *ProviderSessionRunAdapter) HandleBridgeEvent(event SandboxBridgeEvent) {
	a.mu.Lock()
	emit := a.emit
	a.mu.Unlock()
	if handler, ok := a.session.(ProviderSessionEventHandler); ok {
		handler.HandleEvent(event, emit)
		return
	}
	if emit != nil && event.Method != "" {
		emit(task.EventKindRuntimeOutput, task.EventPayload{"provider_event": event.Method, "session_id": a.session.SessionID()})
	}
}
