package blackboardv2

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"pentest/internal/owner"
)

// SessionContinuationAuthority is the server-derived capability used by
// trusted Session Runtime control paths. The owner contract and continuation
// identity are kept out of semantic request payloads and cannot be selected by
// a Runtime caller.
type SessionContinuationAuthority struct {
	Contract       owner.Contract `json:"-"`
	ContinuationID string         `json:"-"`
}

// AuthorizeSessionContinuation resolves one live Session Continuation into a
// narrow Session Blackboard capability. The durable continuation row is the
// grant boundary; callers receive no authority for another Session.
func (s *Service) AuthorizeSessionContinuation(ctx context.Context, sessionID, continuationID string) (SessionContinuationAuthority, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(continuationID) == "" {
		return SessionContinuationAuthority{}, semanticError("authority_denied", "trusted Session Continuation identity is required", "", nil)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionContinuationAuthority{}, fmt.Errorf("begin Session Continuation authorization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionExists(ctx, tx, sessionID); err != nil {
		return SessionContinuationAuthority{}, err
	}
	if err := validateSessionContinuation(ctx, tx, sessionID, continuationID); err != nil {
		return SessionContinuationAuthority{}, err
	}
	return SessionContinuationAuthority{
		Contract:       owner.NewSessionContract(sessionID, ""),
		ContinuationID: continuationID,
	}, nil
}

func (authority SessionContinuationAuthority) validate() error {
	if err := authority.Contract.Validate(); err != nil || !authority.Contract.IsSession() || strings.TrimSpace(authority.ContinuationID) == "" {
		return semanticError("authority_denied", "Session Continuation authority is invalid", "authority", nil)
	}
	return nil
}

// ApplyForSessionAuthority applies a trusted Session Blackboard batch under a
// previously resolved owner capability.
func (s *Service) ApplyForSessionAuthority(ctx context.Context, authority SessionContinuationAuthority, batch ChangeBatch) (ChangeResult, error) {
	if err := authority.validate(); err != nil {
		return ChangeResult{}, err
	}
	return s.ApplyForSessionContinuation(ctx, authority.Contract.SessionID, authority.ContinuationID, batch)
}

// FinishSessionAuthority closes only the authorized Session Continuation.
func (s *Service) FinishSessionAuthority(ctx context.Context, authority SessionContinuationAuthority, idempotencyKey string) (FinishContinuationResult, error) {
	if err := authority.validate(); err != nil {
		return FinishContinuationResult{}, err
	}
	return s.FinishSessionContinuation(ctx, authority.Contract.SessionID, authority.ContinuationID, idempotencyKey)
}

// CheckpointSessionAttemptAuthority records an Attempt checkpoint under the
// same server-derived Session capability.
func (s *Service) CheckpointSessionAttemptAuthority(ctx context.Context, authority SessionContinuationAuthority, request CheckpointAttemptRequest) (ChangeResult, error) {
	if err := authority.validate(); err != nil {
		return ChangeResult{}, err
	}
	return s.CheckpointSessionAttemptForContinuation(ctx, authority.Contract.SessionID, authority.ContinuationID, request)
}
