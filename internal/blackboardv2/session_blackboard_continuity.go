package blackboardv2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BindSessionContinuation captures the exact current Session snapshot as an
// immutable launch pin. Rebinding an existing continuation returns its
// original pin and never overwrites it.
func (s *Service) BindSessionContinuation(ctx context.Context, sessionID, continuationID string) (SessionContinuationPin, error) {
	if strings.TrimSpace(continuationID) == "" {
		return SessionContinuationPin{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionContinuationPin{}, fmt.Errorf("begin Session Continuation pin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	lifecycle, err := ensureSessionState(ctx, tx, sessionID)
	if err != nil {
		return SessionContinuationPin{}, err
	}
	if lifecycle != "open" {
		return SessionContinuationPin{}, semanticError("owner_archived", "archived Session cannot bind a new Continuation Blackboard pin", "", nil)
	}
	if err := validateSessionContinuation(ctx, tx, sessionID, continuationID); err != nil {
		return SessionContinuationPin{}, err
	}
	if pin, err := readSessionContinuationPinTx(ctx, tx, sessionID, continuationID); err != nil {
		return SessionContinuationPin{}, err
	} else if pin.ContinuationID != "" {
		if err := tx.Commit(); err != nil {
			return SessionContinuationPin{}, fmt.Errorf("commit Session Continuation pin replay: %w", err)
		}
		return pin, nil
	}
	snapshot, err := s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
	if err != nil {
		return SessionContinuationPin{}, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return SessionContinuationPin{}, fmt.Errorf("encode Session launch snapshot: %w", err)
	}
	digest := sessionSnapshotDigest(encoded)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_snapshots(session_id, revision, snapshot_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET revision=excluded.revision, snapshot_json=excluded.snapshot_json, updated_at=excluded.updated_at`,
		sessionID, snapshot.Revision, string(encoded), now); err != nil {
		return SessionContinuationPin{}, fmt.Errorf("store Session snapshot at Continuation bind: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_continuation_pins
		(continuation_id, session_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		 working_revision, working_snapshot_json, working_snapshot_sha256, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, continuationID, sessionID, snapshot.Revision, string(encoded), digest,
		snapshot.Revision, string(encoded), digest, now, now); err != nil {
		return SessionContinuationPin{}, fmt.Errorf("store Session Continuation launch pin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SessionContinuationPin{}, fmt.Errorf("commit Session Continuation launch pin: %w", err)
	}
	return SessionContinuationPin{
		ContinuationID: continuationID, SessionID: sessionID,
		Launch:  SessionContinuationSnapshot{Schema: snapshot.Schema, Revision: snapshot.Revision, Bytes: append([]byte(nil), encoded...), SHA256: digest},
		Working: SessionContinuationSnapshot{Schema: snapshot.Schema, Revision: snapshot.Revision, Bytes: append([]byte(nil), encoded...), SHA256: digest},
	}, nil
}

// RebindSessionContinuation carries the immutable launch snapshot and mutable
// Working Snapshot ownership across a persistent interrupt-and-replace turn.
// The old pin remains as history, while the replacement becomes the only
// trusted owner allowed to write after the old Continuation is settled.
func (s *Service) RebindSessionContinuation(ctx context.Context, sessionID, oldContinuationID, nextContinuationID string) error {
	if strings.TrimSpace(oldContinuationID) == "" || strings.TrimSpace(nextContinuationID) == "" || oldContinuationID == nextContinuationID {
		return semanticError("authority_denied", "distinct Session Continuation identities are required", "continuation_id", nil)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Session Continuation rebind: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	lifecycle, err := ensureSessionState(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if lifecycle != "open" {
		return semanticError("owner_archived", "archived Session cannot rebind a Continuation Blackboard pin", "", nil)
	}
	if _, err := sessionContinuationStatus(ctx, tx, sessionID, oldContinuationID); err != nil {
		return err
	}
	if err := validateSessionContinuation(ctx, tx, sessionID, nextContinuationID); err != nil {
		return err
	}
	if err := s.ensureSessionContinuationPinTx(ctx, tx, sessionID, oldContinuationID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO blackboard_v2_session_continuation_pins
		(continuation_id, session_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		 working_revision, working_snapshot_json, working_snapshot_sha256, created_at, updated_at)
		SELECT ?, session_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		       working_revision, working_snapshot_json, working_snapshot_sha256, ?, ?
		FROM blackboard_v2_session_continuation_pins
		WHERE session_id=? AND continuation_id=?`, nextContinuationID, now, now, sessionID, oldContinuationID); err != nil {
		return fmt.Errorf("carry Session Continuation Blackboard pin: %w", err)
	}
	var copied int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_session_continuation_pins WHERE session_id=? AND continuation_id=?`, sessionID, nextContinuationID).Scan(&copied); err != nil {
		return fmt.Errorf("verify Session Continuation Blackboard pin: %w", err)
	}
	if copied != 1 {
		return semanticError("authority_denied", "Session Continuation Blackboard pin is unavailable", "continuation_id", nil)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Session Continuation rebind: %w", err)
	}
	return nil
}

// ReadSessionContinuationPin returns the server-bound launch and Working
// snapshots without exposing Session identity inside either snapshot JSON.
func (s *Service) ReadSessionContinuationPin(ctx context.Context, sessionID, continuationID string) (SessionContinuationPin, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionContinuationPin{}, fmt.Errorf("begin Session Continuation pin read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionExists(ctx, tx, sessionID); err != nil {
		return SessionContinuationPin{}, err
	}
	pin, err := readSessionContinuationPinTx(ctx, tx, sessionID, continuationID)
	if err != nil {
		return SessionContinuationPin{}, err
	}
	if pin.ContinuationID == "" {
		return SessionContinuationPin{}, semanticError("not_found", "Session Continuation pin not found", "", nil)
	}
	return pin, nil
}

func validateSessionContinuation(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) error {
	status, err := sessionContinuationStatus(ctx, tx, sessionID, continuationID)
	if err != nil {
		return err
	}
	if !isOneOf(status, "pending", "running") {
		return semanticError("closed_continuation", "trusted Continuation is closed for new Session Blackboard writes", "", nil)
	}
	return nil
}

func sessionContinuationOwnsCurrentPath(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) (current, pinned bool, err error) {
	var status string
	var number int
	err = tx.QueryRowContext(ctx, `
		SELECT continuation.number, continuation.status,
		       EXISTS(SELECT 1 FROM blackboard_v2_session_continuation_pins AS pin WHERE pin.continuation_id=continuation.id)
		FROM session_continuations AS continuation
		WHERE continuation.id=? AND continuation.session_id=?`, continuationID, sessionID).Scan(&number, &status, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, semanticError("authority_denied", "trusted Continuation does not own this Session interface", "", nil)
	}
	if err != nil {
		return false, false, fmt.Errorf("read Session Continuation Working Snapshot owner: %w", err)
	}
	if !pinned || !isOneOf(status, "pending", "running") {
		return false, pinned, nil
	}
	var newer int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM session_continuations
		WHERE session_id=? AND number>?`, sessionID, number).Scan(&newer); err != nil {
		return false, pinned, fmt.Errorf("check newer Session Continuations: %w", err)
	}
	return newer == 0, pinned, nil
}

func sessionContinuationStatus(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `
		SELECT status FROM session_continuations WHERE id=? AND session_id=?`, continuationID, sessionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", semanticError("authority_denied", "trusted Continuation does not own this Session interface", "", nil)
	}
	if err != nil {
		return "", fmt.Errorf("validate Session Continuation: %w", err)
	}
	return status, nil
}

func (s *Service) ensureSessionContinuationPinTx(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) error {
	if pin, err := readSessionContinuationPinTx(ctx, tx, sessionID, continuationID); err != nil {
		return err
	} else if pin.ContinuationID != "" {
		return nil
	}
	snapshot, err := s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode Session launch snapshot: %w", err)
	}
	digest := sessionSnapshotDigest(encoded)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_continuation_pins
		(continuation_id, session_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		 working_revision, working_snapshot_json, working_snapshot_sha256, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, continuationID, sessionID, snapshot.Revision, string(encoded), digest,
		snapshot.Revision, string(encoded), digest, now, now); err != nil {
		return fmt.Errorf("store Session Continuation launch pin: %w", err)
	}
	return nil
}

func readSessionContinuationPinTx(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) (SessionContinuationPin, error) {
	var pin SessionContinuationPin
	var launchJSON, workingJSON, launchDigest, workingDigest string
	var launchRevision, workingRevision int
	err := tx.QueryRowContext(ctx, `
		SELECT continuation_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		       working_revision, working_snapshot_json, working_snapshot_sha256
		FROM blackboard_v2_session_continuation_pins
		WHERE session_id=? AND continuation_id=?`, sessionID, continuationID).Scan(
		&pin.ContinuationID, &launchRevision, &launchJSON, &launchDigest,
		&workingRevision, &workingJSON, &workingDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionContinuationPin{}, nil
	}
	if err != nil {
		return SessionContinuationPin{}, fmt.Errorf("read Session Continuation pin: %w", err)
	}
	launchBytes := []byte(launchJSON)
	if err := verifyPinnedSnapshot(snapshotSchema, launchRevision, launchBytes, launchDigest); err != nil {
		return SessionContinuationPin{}, err
	}
	workingBytes := []byte(workingJSON)
	if err := verifySnapshotEnvelope(workingBytes, workingRevision); err != nil {
		return SessionContinuationPin{}, fmt.Errorf("%w: Session Working Snapshot integrity failure", ErrLaunchPinIntegrity)
	}
	workingSum := sha256.Sum256(workingBytes)
	if !strings.EqualFold(hex.EncodeToString(workingSum[:]), workingDigest) {
		return SessionContinuationPin{}, fmt.Errorf("%w: Session Working Snapshot digest mismatch", ErrLaunchPinIntegrity)
	}
	pin.SessionID = sessionID
	pin.Launch = SessionContinuationSnapshot{Schema: snapshotSchema, Revision: launchRevision, Bytes: []byte(launchJSON), SHA256: launchDigest}
	pin.Working = SessionContinuationSnapshot{Schema: snapshotSchema, Revision: workingRevision, Bytes: []byte(workingJSON), SHA256: workingDigest}
	return pin, nil
}

func sessionSnapshotDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
