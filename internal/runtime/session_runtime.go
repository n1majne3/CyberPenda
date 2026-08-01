package runtime

import (
	"context"
	"fmt"
	"time"

	"pentest/internal/session"
	"pentest/internal/task"
)

// SessionLaunchRequest is the Session-facing view of the shared Runtime
// Harness input. It intentionally carries no Project or Scope fields.
type SessionLaunchRequest struct {
	SessionID        string
	Goal             string
	Adapter          Adapter
	ContinuationID   string
	Metadata         func() (NativeSessionMetadata, error)
	StopConfirmation StopConfirmation
}

// SessionHarness is an owner adapter over the same OwnerHarness used by Task.
// Session-specific behavior is limited to mapping events and persisting the
// Session continuation status through callbacks.
type SessionHarness struct {
	core *OwnerHarness
}

func NewSessionHarness(sessions *session.Service) *SessionHarness {
	h := &SessionHarness{}
	h.core = NewOwnerHarness(OwnerHarnessConfig{
		VerifyOwner: func(ownerID string) error {
			_, err := sessions.Get(ownerID)
			return err
		},
		AppendEvent: func(ownerID, continuationID string, kind task.EventKind, payload task.EventPayload) error {
			mappedKind, mappedPayload := mapSessionRuntimeEvent(kind, payload)
			if continuationID != "" {
				if _, exists := mappedPayload["continuation_id"]; !exists {
					mappedPayload["continuation_id"] = continuationID
				}
			}
			_, err := sessions.AppendEvent(ownerID, mappedKind, mappedPayload)
			return err
		},
		MarkRunning: func(_, continuationID string) error {
			if continuationID == "" {
				return nil
			}
			_, err := sessions.UpdateContinuationStatus(continuationID, session.RuntimeStatusRunning)
			return err
		},
		UpdateContinuationRuntimeMetadata: func(continuationID, containerID, nativeSessionID, nativeSessionPath string) error {
			_, err := sessions.UpdateContinuationRuntimeMetadata(continuationID, containerID, nativeSessionID, nativeSessionPath)
			return err
		},
		Finalize: func(_, continuationID, status string) error {
			if continuationID == "" {
				return nil
			}
			_, err := sessions.UpdateContinuationStatus(continuationID, session.RuntimeStatus(status))
			return err
		},
	})
	return h
}

func (h *SessionHarness) Launch(ctx context.Context, req SessionLaunchRequest) error {
	if h == nil || h.core == nil {
		return fmt.Errorf("Session Runtime Harness is unavailable")
	}
	return h.core.Launch(ctx, OwnerLaunchRequest{
		OwnerID: req.SessionID, Goal: req.Goal, Adapter: req.Adapter,
		ContinuationID: req.ContinuationID, Metadata: req.Metadata, StopConfirmation: req.StopConfirmation,
	})
}

func (h *SessionHarness) Stop(sessionID string)          { h.core.Stop(sessionID) }
func (h *SessionHarness) IsActive(sessionID string) bool { return h.core.IsActive(sessionID) }
func (h *SessionHarness) StopAndWait(sessionID string, timeout time.Duration) bool {
	return h.core.StopAndWait(sessionID, timeout)
}
func (h *SessionHarness) RebindContinuation(sessionID, continuationID string) error {
	return h.core.RebindContinuation(sessionID, continuationID)
}

func mapSessionRuntimeEvent(kind task.EventKind, payload task.EventPayload) (session.EventKind, session.EventPayload) {
	copyPayload := session.EventPayload{}
	for key, value := range payload {
		copyPayload[key] = value
	}
	switch kind {
	case task.EventKindConversation:
		return session.EventKindConversation, copyPayload
	case task.EventKindRuntimeOutput:
		return session.EventKindRuntimeOutput, copyPayload
	case task.EventKindSteering:
		return session.EventKindSteering, copyPayload
	case task.EventKindLifecycle:
		if phase, _ := copyPayload["phase"].(string); phase == "provider_permission_requested" {
			return session.EventKindPermission, copyPayload
		}
		return session.EventKindLifecycle, copyPayload
	default:
		return session.EventKindTurn, copyPayload
	}
}
