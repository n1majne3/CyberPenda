package blackboardv2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func validateEvidenceReservationRow(row evidenceRequestRow, requestHash string, source evidenceSource) error {
	if row.requestHash != requestHash {
		return semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", nil)
	}
	if row.sourceIdentity != source.identity || row.sha256 != source.sha256 || row.size != source.size {
		return semanticError("evidence_source_changed", "Evidence source changed across idempotent retry", "source_path", nil)
	}
	if !isOneOf(row.status, "reserved", "published", "completed") {
		return fmt.Errorf("invalid retained Evidence request status %q", row.status)
	}
	return nil
}

func (s *Service) reserveEvidenceRequest(ctx context.Context, projectID, continuationID, key, requestHash, internalPath string, source evidenceSource) (evidenceRequestRow, bool, error) {
	tempPath, err := plannedEvidenceTempPath(internalPath, continuationID, key, requestHash)
	if err != nil {
		return evidenceRequestRow{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("begin Evidence reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	status, err := continuationProjectStatus(ctx, tx, projectID, continuationID)
	if err != nil {
		return evidenceRequestRow{}, false, err
	}
	if !continuationCanWrite(status) {
		return evidenceRequestRow{}, false, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	currentOwner, pinned, err := continuationOwnsCurrentWorkingPath(ctx, tx, continuationID)
	if err != nil {
		return evidenceRequestRow{}, false, err
	}
	if pinned && !currentOwner {
		return evidenceRequestRow{}, false, semanticError("closed_continuation", "trusted Continuation no longer owns the Task Working Snapshot", "", nil)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blackboard_v2_evidence_requests(project_id,continuation_id,idempotency_key,request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,temp_internal_path,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?, 'reserved',?,?)`, projectID, continuationID, key, requestHash, source.identity, source.sha256, source.size, internalPath, tempPath, now, now)
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("reserve Evidence request: %w", err)
	}
	rows, _ := result.RowsAffected()
	var row evidenceRequestRow
	var payloadOwned int
	if err := tx.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,temp_internal_path,previous_temp_internal_path,publisher_token,publisher_temp_identity,payload_owned,status,result_json FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, projectID, continuationID, key).Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.internalPath, &row.tempPath, &row.previousTemp, &row.publisherToken, &row.publisherID, &payloadOwned, &row.status, &row.resultJSON); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("read reserved Evidence request: %w", err)
	}
	row.payloadOwned = payloadOwned == 1
	if row.requestHash != requestHash || row.internalPath != internalPath || row.tempPath != tempPath {
		return evidenceRequestRow{}, false, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": key})
	}
	if err := validateEvidenceReservationRow(row, requestHash, source); err != nil {
		return evidenceRequestRow{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blackboard_v2_evidence_payloads(project_id,managed_internal_path,sha256,size_bytes,state,created_at,updated_at) VALUES(?,?,?,?,'active',?,?)`, projectID, internalPath, source.sha256, source.size, now, now); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("claim Evidence payload path: %w", err)
	}
	var payloadDigest, payloadState string
	var payloadSize int64
	if err := tx.QueryRowContext(ctx, `SELECT sha256,size_bytes,state FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, internalPath).Scan(&payloadDigest, &payloadSize, &payloadState); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("read Evidence payload claim: %w", err)
	}
	if payloadDigest != source.sha256 || payloadSize != source.size {
		return evidenceRequestRow{}, false, fmt.Errorf("Evidence payload claim metadata conflict")
	}
	if payloadState == "gc" {
		return evidenceRequestRow{}, false, &Error{Code: "evidence_payload_gc_in_progress", Message: "Evidence payload cleanup is in progress", Path: "source_path", Retryable: true}
	}
	if err := tx.Commit(); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("commit Evidence reservation: %w", err)
	}
	return row, rows == 1, nil
}

func (s *Service) readEvidenceRequest(ctx context.Context, projectID, continuationID, key string) (evidenceRequestRow, bool, error) {
	var row evidenceRequestRow
	var payloadOwned int
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,temp_internal_path,previous_temp_internal_path,publisher_token,publisher_temp_identity,payload_owned,status,result_json FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, projectID, continuationID, key).Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.internalPath, &row.tempPath, &row.previousTemp, &row.publisherToken, &row.publisherID, &payloadOwned, &row.status, &row.resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return evidenceRequestRow{}, false, nil
	}
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("read Evidence request: %w", err)
	}
	row.payloadOwned = payloadOwned == 1
	return row, true, nil
}

func (s *Service) verifyManagedEvidencePayload(internalPath, digest string, size int64) (bool, error) {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return false, fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	directory, file, info, _, err := openSecureRegularFile(root, internalPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, semanticError("evidence_integrity_failed", "managed Evidence path is missing, symlinked, or unsafe", "key", nil)
	}
	defer directory.Close()
	defer file.Close()
	if !info.Mode().IsRegular() {
		return false, semanticError("evidence_integrity_failed", "managed Evidence payload is not a regular file", "key", nil)
	}
	hash := sha256.New()
	actualSize, err := io.Copy(hash, file)
	if evidenceLockViolation(err) {
		// Another publisher is actively journaling this inode; the read is
		// temporarily blocked, not an integrity failure. Linux flock never
		// blocks other handles' I/O, so only Windows reports this.
		return false, &Error{Code: "evidence_publication_in_progress", Message: "Evidence payload read is blocked by an active publisher", Path: "key", Retryable: true}
	}
	if err != nil {
		return false, fmt.Errorf("hash managed Evidence payload: %w", err)
	}
	if actualSize != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return false, semanticError("evidence_integrity_failed", "managed Evidence payload failed integrity verification", "key", nil)
	}
	return true, nil
}

func (s *Service) evidenceIntegrityValid(projectID string, record EvidenceRecord) (bool, error) {
	if strings.TrimSpace(s.evidenceConfig.ArtifactRoot) == "" || record.Status != "available" {
		return false, nil
	}
	internalPath, err := plannedEvidenceInternalPath(projectID, record.SHA256, filepath.Base(record.ManagedPath))
	if err != nil {
		return false, nil
	}
	semanticPath, err := semanticEvidencePath(projectID, internalPath, record.SHA256)
	if err != nil || semanticPath != record.ManagedPath {
		return false, nil
	}
	valid, err := s.verifyManagedEvidencePayload(internalPath, record.SHA256, record.Size)
	if err != nil {
		var semanticErr *Error
		if errors.As(err, &semanticErr) {
			return false, nil
		}
		return false, err
	}
	return valid, nil
}
