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
	"pentest/internal/task"
)

// providerSessionRegistry is daemon-owned because a provider session belongs
// to a Task, while the concrete session and protocol remain provider-owned.
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
		return fmt.Errorf("provider session binding requires Task and session identity")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.sessions[taskID]; ok && existing.SessionID() != session.SessionID() {
		return fmt.Errorf("provider session already bound to Task")
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
	return server.providerSessions.bind(taskID, session)
}

// UnbindProviderSession removes daemon ownership of a Task session. It does
// not close the provider process; the adapter/bridge owns that lifecycle.
func (server *Server) UnbindProviderSession(taskID string) { server.providerSessions.remove(taskID) }

func (server *Server) closeProviderSession(taskID string) error {
	return server.providerSessions.closeTask(context.Background(), taskID)
}

type nativeSteerRequest struct {
	RequestID string `json:"request_id"`
	Message   string `json:"message"`
	Directive string `json:"directive"` // backwards-compatible alias
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
