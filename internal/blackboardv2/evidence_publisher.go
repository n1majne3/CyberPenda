package blackboardv2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"pentest/internal/store"
)

type evidencePublisher struct {
	file     *os.File
	token    string
	identity string
}

func (s *Service) acquireEvidencePublisher(ctx context.Context, root *os.Root, projectID, continuationID, key string, row *evidenceRequestRow) (*evidencePublisher, error) {
	tempName := filepath.Base(row.tempPath)
	if info, err := root.Lstat(tempName); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, semanticError("evidence_integrity_failed", "journaled Evidence temp path is symlinked", "source_path", nil)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect journaled Evidence temp: %w", err)
	}
	// The publisher handle carries the payload write path on every platform
	// (a second handle would collide with the exclusive byte-range lock on
	// Windows, and the write path now goes through the locked handle
	// everywhere). A stale temp from an interrupted publisher may still be
	// immutable (0400); clear that before opening, matching the write path.
	if info, err := root.Lstat(tempName); err == nil && info.Mode().Perm()&0o200 == 0 {
		if err := root.Chmod(tempName, 0o600); err != nil {
			return nil, fmt.Errorf("unlock immutable journaled Evidence temp: %w", err)
		}
	}
	file, err := root.OpenFile(tempName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journaled Evidence temp publisher: %w", err)
	}
	if err := lockEvidencePublisherFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		unlockAndCloseEvidencePublisher(file)
		return nil, semanticError("evidence_integrity_failed", "journaled Evidence temp is not a regular file", "source_path", nil)
	}
	pathInfo, err := root.Lstat(tempName)
	if err != nil || !os.SameFile(info, pathInfo) {
		unlockAndCloseEvidencePublisher(file)
		return nil, semanticError("evidence_integrity_failed", "journaled Evidence temp changed while claiming publisher", "source_path", nil)
	}
	token, err := newEvidencePublisherToken()
	if err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, err
	}
	identity := fileIdentity(row.tempPath, info)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, fmt.Errorf("begin Evidence publisher claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var payloadState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, row.internalPath).Scan(&payloadState); err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, fmt.Errorf("validate Evidence publisher payload claim: %w", err)
	}
	if payloadState != "active" {
		unlockAndCloseEvidencePublisher(file)
		return nil, &Error{Code: "evidence_payload_gc_in_progress", Message: "Evidence payload cleanup is in progress", Path: "source_path", Retryable: true}
	}
	result, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET publisher_token=?,publisher_temp_identity=?,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND temp_internal_path=? AND status='reserved'`, token, identity, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, row.requestHash, row.tempPath)
	if err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, fmt.Errorf("claim Evidence publisher: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		unlockAndCloseEvidencePublisher(file)
		return nil, &Error{Code: "evidence_publication_in_progress", Message: "Evidence publisher reservation changed", Path: "idempotency_key", Retryable: true}
	}
	if err := tx.Commit(); err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, fmt.Errorf("commit Evidence publisher claim: %w", err)
	}
	row.publisherToken = token
	row.publisherID = identity
	return &evidencePublisher{file: file, token: token, identity: identity}, nil
}

func (publisher *evidencePublisher) writer() (*os.File, error) {
	if err := publisher.file.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("make journaled Evidence temp writable: %w", err)
	}
	// Write through the publisher handle itself: it carries the exclusive
	// publication lock (LockFileEx byte-range lock on Windows), and a second
	// handle to the same file would collide with the locked range.
	return publisher.file, nil
}

func (publisher *evidencePublisher) validate(ctx context.Context, db *store.DB, root *os.Root, projectID, continuationID, key string, row evidenceRequestRow) error {
	lockedInfo, err := publisher.file.Stat()
	if err != nil {
		return fmt.Errorf("stat locked Evidence publisher: %w", err)
	}
	pathInfo, err := root.Lstat(filepath.Base(row.tempPath))
	if err != nil || !os.SameFile(lockedInfo, pathInfo) {
		return semanticError("evidence_integrity_failed", "journaled Evidence temp inode changed before publication", "source_path", nil)
	}
	var token, identity string
	if err := db.QueryRowContext(ctx, `SELECT publisher_token,publisher_temp_identity FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=?`, projectID, continuationID, key, row.requestHash).Scan(&token, &identity); err != nil {
		return fmt.Errorf("validate Evidence publisher claim: %w", err)
	}
	if token != publisher.token || identity != publisher.identity || fileIdentity(row.tempPath, lockedInfo) != publisher.identity {
		return &Error{Code: "evidence_publication_in_progress", Message: "Evidence publisher claim changed", Path: "idempotency_key", Retryable: true}
	}
	return nil
}

func (publisher *evidencePublisher) close() {
	if publisher.file != nil {
		unlockAndCloseEvidencePublisher(publisher.file)
		publisher.file = nil
	}
}

func openLockedEvidenceFile(root *os.Root, name, description string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, semanticError("evidence_integrity_failed", description+" is symlinked or not a regular file", "source_path", nil)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", description, err)
	}
	if err := lockEvidencePublisherFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		unlockAndCloseEvidencePublisher(file)
		return nil, semanticError("evidence_integrity_failed", description+" changed while acquiring its inode lock", "source_path", nil)
	}
	return file, nil
}

func removeInactiveJournaledEvidenceTemp(root *os.Root, name string) error {
	if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect stale journaled Evidence temp: %w", err)
	}
	file, err := openLockedEvidenceFile(root, name, "journaled Evidence temp")
	if err != nil {
		return err
	}
	defer unlockAndCloseEvidencePublisher(file)
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale journaled Evidence temp: %w", err)
	}
	return syncEvidenceDirectory(root)
}

func verifyOpenEvidenceFile(file *os.File, digest string, size int64) (bool, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	hash := sha256.New()
	actualSize, err := io.Copy(hash, file)
	if err != nil {
		return false, err
	}
	_, seekErr := file.Seek(0, io.SeekStart)
	return actualSize == size && hex.EncodeToString(hash.Sum(nil)) == digest, seekErr
}

func newEvidencePublisherToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create Evidence publisher token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
