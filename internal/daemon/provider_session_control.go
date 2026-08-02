package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/session"
	"pentest/internal/task"
)

// providerSessionRegistry is daemon-owned because a provider session belongs
// to one aggregate owner, while the concrete session and protocol remain
// provider-owned.
// It is intentionally in-memory: restart recovery must fail closed rather
// than reattach an orphaned stdio process.
type providerSessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string]runtime.ProviderSession
}

func newProviderSessionRegistry() *providerSessionRegistry {
	return &providerSessionRegistry{sessions: make(map[string]runtime.ProviderSession)}
}

func (r *providerSessionRegistry) bind(taskID string, session runtime.ProviderSession) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || session == nil || strings.TrimSpace(session.SessionID()) == "" {
		return fmt.Errorf("provider session binding requires owner and session identity")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.sessions[taskID]; ok && existing.SessionID() != session.SessionID() {
		return fmt.Errorf("provider session already bound to owner")
	}
	r.sessions[taskID] = session
	return nil
}

func (r *providerSessionRegistry) get(taskID string) (runtime.ProviderSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[taskID]
	return session, ok
}

func (r *providerSessionRegistry) remove(taskID string) runtime.ProviderSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.sessions[taskID]
	delete(r.sessions, taskID)
	return session
}

func (r *providerSessionRegistry) closeTask(ctx context.Context, taskID string) error {
	r.mu.RLock()
	session := r.sessions[taskID]
	r.mu.RUnlock()
	if session == nil {
		return nil
	}
	if err := session.Close(ctx); err != nil && !errors.Is(err, runtime.ErrProviderSessionClosed) {
		return err
	}
	r.mu.Lock()
	if current, ok := r.sessions[taskID]; ok && current.SessionID() == session.SessionID() {
		delete(r.sessions, taskID)
	}
	r.mu.Unlock()
	return nil
}

func (r *providerSessionRegistry) closeAll(ctx context.Context) error {
	r.mu.Lock()
	sessions := make([]runtime.ProviderSession, 0, len(r.sessions))
	for taskID, session := range r.sessions {
		sessions = append(sessions, session)
		delete(r.sessions, taskID)
	}
	r.mu.Unlock()
	var errs []error
	for _, session := range sessions {
		if err := session.Close(ctx); err != nil && !errors.Is(err, runtime.ErrProviderSessionClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// BindProviderSession attaches a provider-native session to a Task. Provider
// adapters call this during launch assembly; the web API never receives a
// session or bridge handle.
func (server *Server) BindProviderSession(taskID string, session runtime.ProviderSession) error {
	if _, err := server.tasks.Get(taskID); err != nil {
		return err
	}
	if err := server.providerSessions.bind(taskID, session); err != nil {
		return err
	}
	server.clearRecoveredRuntimeActivity(taskID)
	if sink, ok := session.(runtime.ProviderSessionEventSink); ok {
		sink.SetEventSink(func(kind task.EventKind, payload task.EventPayload) {
			server.persistProviderSessionEvent(taskID, kind, payload)
		})
	}
	if source, ok := session.(runtime.ProviderSessionObservationSink); ok {
		lineage, lineageOK := session.(runtime.ProviderSessionCompleteTurnLineageResolver)
		continuationID := ""
		if active, activeErr := server.tasks.ActiveContinuation(taskID); activeErr == nil && active != nil {
			continuationID = active.ID
		}
		source.SetObservationSink(func(observation runtime.ProviderSessionObservation) {
			if !lineageOK {
				return
			}
			turnLineage, resolved := lineage.ResolveProviderSessionTurnLineage(observation.RequestID, observation.ProviderTurnID)
			if !resolved {
				server.logger.Printf("provider session observation: ignore unowned Turn %s for Task %s", observation.ProviderTurnID, taskID)
				return
			}
			server.observeProviderSession(taskID, continuationID, session.SessionID(), turnLineage, observation)
		})
	}
	if source, ok := session.(runtime.ProviderSessionAttemptResultSource); ok {
		source.SetAttemptResultSink(func(result runtime.ProviderSessionAttemptResult) {
			server.acceptBlackboardConclusionResult(taskID, result)
		})
		source.SetAttemptResultValidationFailureSink(func(failure runtime.ProviderSessionAttemptResultValidationFailure) {
			server.acceptBlackboardConclusionValidationFailure(taskID, failure)
		})
	}
	return nil
}

// BindSessionProviderSession attaches a provider-native Runtime to a
// Non-Project Session. The same provider event contract is used, but the
// durable owner and current continuation are Session identities.
func (server *Server) BindSessionProviderSession(sessionID string, providerSession runtime.ProviderSession) error {
	found, err := server.sessions.Get(sessionID)
	if err != nil {
		return err
	}
	if found.RunControls.BlackboardConclusionMode == session.BlackboardConclusionModeAssisted {
		if err := validateAssistedConclusionProviderSession(providerSession); err != nil {
			return err
		}
	}
	if err := server.sessionProviderSessions.bind(sessionID, providerSession); err != nil {
		return err
	}
	if sink, ok := providerSession.(runtime.ProviderSessionEventSink); ok {
		sink.SetEventSink(func(kind task.EventKind, payload task.EventPayload) {
			server.persistSessionProviderSessionEvent(sessionID, kind, payload)
		})
	}
	if source, ok := providerSession.(runtime.ProviderSessionObservationSink); ok {
		lineage, lineageOK := providerSession.(runtime.ProviderSessionCompleteTurnLineageResolver)
		source.SetObservationSink(func(observation runtime.ProviderSessionObservation) {
			if !lineageOK {
				return
			}
			turnLineage, resolved := lineage.ResolveProviderSessionTurnLineage(observation.RequestID, observation.ProviderTurnID)
			if !resolved {
				server.logger.Printf("provider session observation: ignore unowned Turn %s for Session %s", observation.ProviderTurnID, sessionID)
				return
			}
			continuationID := ""
			if active, activeErr := server.sessions.ActiveContinuation(sessionID); activeErr == nil && active != nil {
				continuationID = active.ID
			}
			server.observeSessionProviderSession(sessionID, continuationID, providerSession.SessionID(), turnLineage, observation)
		})
	}
	if source, ok := providerSession.(runtime.ProviderSessionAttemptResultSource); ok {
		source.SetAttemptResultSink(func(result runtime.ProviderSessionAttemptResult) {
			server.acceptSessionBlackboardConclusionResult(sessionID, result)
		})
		source.SetAttemptResultValidationFailureSink(func(failure runtime.ProviderSessionAttemptResultValidationFailure) {
			server.acceptSessionBlackboardConclusionValidationFailure(sessionID, failure)
		})
	}
	return nil
}

func validateAssistedConclusionProviderSession(providerSession runtime.ProviderSession) error {
	if providerSession == nil || !providerSession.Capabilities().AssistedConclusion {
		return errAssistedConclusionUnsupported
	}
	if _, ok := providerSession.(runtime.ProviderSessionObservationSink); !ok {
		return errAssistedConclusionUnsupported
	}
	if _, ok := providerSession.(runtime.ProviderSessionCompleteTurnLineageResolver); !ok {
		return errAssistedConclusionUnsupported
	}
	if _, ok := providerSession.(runtime.ProviderSessionAttemptResultSource); !ok {
		return errAssistedConclusionUnsupported
	}
	return nil
}

func (server *Server) persistSessionProviderSessionEvent(sessionID string, kind task.EventKind, payload task.EventPayload) {
	if continuation, err := server.sessions.ActiveContinuation(sessionID); err == nil && continuation != nil {
		server.persistSessionProviderEventForContinuation(sessionID, continuation.ID, kind, payload)
		return
	}
	server.persistSessionProviderEventForContinuation(sessionID, "", kind, payload)
}

func sessionProviderEventPayload(kind task.EventKind, payload task.EventPayload, continuationID string) (session.EventKind, session.EventPayload) {
	redacted := session.EventPayload{}
	for _, key := range []string{"provider", "provider_event", "request_id", "session_id", "provider_turn_id", "mode", "outcome", "permission_request_id", "permission_decision", "error_code", "phase"} {
		if value, ok := payload[key]; ok {
			redacted[key] = value
		}
	}
	if kind == task.EventKindRuntimeOutput {
		for _, key := range []string{"stream", "text"} {
			if value, ok := payload[key]; ok {
				redacted[key] = value
			}
		}
	}
	if redacted["mode"] == string(runtime.ProviderSessionModePermissionResponse) && redacted["outcome"] == "requested" {
		redacted["phase"] = "provider_permission_requested"
	}
	if strings.TrimSpace(continuationID) != "" {
		redacted["continuation_id"] = continuationID
	}
	var mappedKind session.EventKind
	switch kind {
	case task.EventKindRuntimeOutput:
		mappedKind = session.EventKindRuntimeOutput
	case task.EventKindSteering:
		mappedKind = session.EventKindSteering
	case task.EventKindLifecycle:
		if redacted["phase"] == "provider_permission_requested" {
			mappedKind = session.EventKindPermission
		} else {
			mappedKind = session.EventKindLifecycle
		}
	default:
		mappedKind = session.EventKindTurn
	}
	return mappedKind, redacted
}

func (server *Server) persistSessionProviderEventForContinuation(sessionID, continuationID string, kind task.EventKind, payload task.EventPayload) {
	if strings.TrimSpace(continuationID) == "" {
		if continuation, err := server.sessions.ActiveContinuation(sessionID); err == nil && continuation != nil {
			continuationID = continuation.ID
		}
	}
	mappedKind, redacted := sessionProviderEventPayload(kind, payload, continuationID)
	_, _ = server.sessions.AppendEvent(sessionID, mappedKind, redacted)
}

// persistProviderSessionEvent is the only daemon entry point for unsolicited
// provider notifications. It copies a fixed correlation allowlist and chooses
// the current Continuation at receipt time, so raw protocol payload cannot be
// persisted or leak into the Task Conversation.
func (server *Server) persistProviderSessionEvent(taskID string, kind task.EventKind, payload task.EventPayload) {
	redacted := task.EventPayload{}
	for _, key := range []string{"provider", "provider_event", "request_id", "session_id", "provider_turn_id", "mode", "outcome", "permission_request_id"} {
		if value, ok := payload[key]; ok {
			redacted[key] = value
		}
	}
	if kind == task.EventKindRuntimeOutput {
		for _, key := range []string{"stream", "text"} {
			if value, ok := payload[key]; ok {
				redacted[key] = value
			}
		}
	}
	if redacted["mode"] == string(runtime.ProviderSessionModePermissionResponse) && redacted["outcome"] == "requested" {
		redacted["phase"] = "provider_permission_requested"
	}
	continuation, err := server.tasks.ActiveContinuation(taskID)
	if err != nil {
		return
	}
	if continuation != nil {
		_, _ = server.tasks.AppendContinuationEvent(taskID, continuation.ID, kind, redacted)
		return
	}
	_, _ = server.tasks.AppendEvent(taskID, kind, redacted)
}

func (server *Server) closeProviderSession(taskID string) error {
	err := server.providerSessions.closeTask(context.Background(), taskID)
	if err == nil || errors.Is(err, runtime.ErrProviderSessionClosed) {
		server.blackboardConclusions.DeleteOwner(taskID)
	}
	return err
}

func (server *Server) closeSessionProviderSession(sessionID string) error {
	err := server.sessionProviderSessions.closeTask(context.Background(), sessionID)
	if err == nil || errors.Is(err, runtime.ErrProviderSessionClosed) {
		server.sessionBlackboardConclusions.DeleteOwner(sessionID)
	}
	return err
}

func (server *Server) closeSessionProviderSessionForStop(ctx context.Context, sessionID string) error {
	for {
		err := server.sessionProviderSessions.closeTask(ctx, sessionID)
		if err == nil || errors.Is(err, runtime.ErrProviderSessionClosed) {
			server.sessionBlackboardConclusions.DeleteOwner(sessionID)
			return nil
		}
		if !errors.Is(err, runtime.ErrProviderSessionControlConflict) {
			return err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (server *Server) closeProviderSessionForStop(ctx context.Context, taskID string) error {
	for {
		err := server.providerSessions.closeTask(ctx, taskID)
		if err == nil || errors.Is(err, runtime.ErrProviderSessionClosed) {
			server.blackboardConclusions.DeleteOwner(taskID)
			return nil
		}
		if !errors.Is(err, runtime.ErrProviderSessionControlConflict) {
			return err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type nativeSteerRequest struct {
	RequestID string `json:"request_id"`
	Message   string `json:"message"`
	Directive string `json:"directive"` // backwards-compatible alias
	taskContinuationSelectionInput
}

func newNativeSteerRequestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("steer-%d", time.Now().UnixNano())
	}
	return "steer-" + hex.EncodeToString(raw[:])
}

func nativeSteerMode(capabilities runtimeplugin.Capabilities) (runtime.ProviderSessionMode, error) {
	if !capabilities.PersistentSession || !capabilities.SendTurn {
		return "", &runtime.UnsupportedProviderSessionCapabilityError{Capability: runtime.ProviderSessionCapabilityPersistentSession}
	}
	if capabilities.InTurnSteer {
		return runtime.ProviderSessionModeInTurnSteer, nil
	}
	if capabilities.InterruptThenReplace {
		return runtime.ProviderSessionModeInterruptThenReplace, nil
	}
	return "", &runtime.UnsupportedProviderSessionCapabilityError{Capability: runtime.ProviderSessionCapabilityInterruptThenReplace}
}

func nativeSteerState(events []task.Event, requestID string) (mode runtime.ProviderSessionMode, outcome string, sessionID string) {
	for _, event := range events {
		if event.Payload["request_id"] != requestID {
			continue
		}
		if value, ok := event.Payload["mode"].(string); ok {
			mode = runtime.ProviderSessionMode(value)
		}
		if value, ok := event.Payload["outcome"].(string); ok {
			outcome = value
		}
		if value, ok := event.Payload["session_id"].(string); ok {
			sessionID = value
		}
	}
	return mode, outcome, sessionID
}

func nativeSteerOperation(session runtime.ProviderSession, mode runtime.ProviderSessionMode) func(context.Context, runtime.ProviderSessionRequest, runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	switch mode {
	case runtime.ProviderSessionModeInTurnSteer:
		return session.SteerInTurn
	case runtime.ProviderSessionModeInterruptThenReplace:
		return session.InterruptThenReplace
	default:
		return nil
	}
}
