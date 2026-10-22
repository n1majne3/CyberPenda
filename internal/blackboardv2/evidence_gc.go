package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) claimEvidencePayloadGC(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow) (evidenceCleanupDecision, error) {
	semanticPath, err := semanticEvidencePath(projectID, row.internalPath, row.sha256)
	if err != nil {
		return evidenceCleanupPreserve, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("begin Evidence payload cleanup claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var requestCount, receiptCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath).Scan(&requestCount); err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("validate Evidence cleanup request: %w", err)
	}
	receiptKey := "retain-evidence-v2:" + continuationID + ":" + key
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_idempotency_receipts WHERE project_id=? AND idempotency_key=?`, projectID, receiptKey).Scan(&receiptCount); err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("validate Evidence cleanup receipt: %w", err)
	}
	if requestCount != 1 || receiptCount != 0 {
		return evidenceCleanupPreserve, nil
	}
	var state, gcContinuationID, gcKey string
	err = tx.QueryRowContext(ctx, `SELECT state,gc_continuation_id,gc_idempotency_key FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, row.internalPath).Scan(&state, &gcContinuationID, &gcKey)
	if errors.Is(err, sql.ErrNoRows) {
		if row.payloadOwned {
			return evidenceCleanupPreserve, nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("delete unclaimed Evidence request: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("commit Evidence request cleanup decision: %w", err)
		}
		return evidenceCleanupRequestRetired, nil
	}
	if err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("read Evidence payload cleanup claim: %w", err)
	}
	if state == "gc" {
		if gcContinuationID != continuationID || gcKey != key {
			return evidenceCleanupPreserve, nil
		}
		if err := tx.Commit(); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("commit resumed Evidence payload cleanup claim: %w", err)
		}
		return evidenceCleanupOwnPayload, nil
	}
	var semanticReferences int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_records WHERE project_id=? AND type='evidence' AND json_extract(record_json,'$.managed_path')=?`, projectID, semanticPath).Scan(&semanticReferences); err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("count semantic Evidence payload references: %w", err)
	}
	if semanticReferences != 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("delete semantically referenced Evidence request: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("commit semantically referenced Evidence cleanup: %w", err)
		}
		return evidenceCleanupRequestRetired, nil
	}
	var nextContinuationID, nextKey string
	err = tx.QueryRowContext(ctx, `
		SELECT continuation_id,idempotency_key
		FROM blackboard_v2_evidence_requests
		WHERE project_id=? AND managed_internal_path=? AND NOT (continuation_id=? AND idempotency_key=?)
		ORDER BY created_at,continuation_id,idempotency_key
		LIMIT 1`, projectID, row.internalPath, continuationID, key).Scan(&nextContinuationID, &nextKey)
	if err == nil {
		transfer, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET payload_owned=1,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND managed_internal_path=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, nextContinuationID, nextKey, row.internalPath)
		if err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("transfer shared Evidence payload ownership: %w", err)
		}
		if changed, _ := transfer.RowsAffected(); changed != 1 {
			return evidenceCleanupPreserve, fmt.Errorf("shared Evidence payload ownership target changed")
		}
		deleted, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath)
		if err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("delete departing Evidence payload owner: %w", err)
		}
		if changed, _ := deleted.RowsAffected(); changed != 1 {
			return evidenceCleanupPreserve, fmt.Errorf("departing Evidence payload owner changed during transfer")
		}
		if err := tx.Commit(); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("commit shared Evidence payload ownership transfer: %w", err)
		}
		return evidenceCleanupRequestRetired, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return evidenceCleanupPreserve, fmt.Errorf("select next Evidence payload owner: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_evidence_payloads SET state='gc',gc_continuation_id=?,gc_idempotency_key=?,updated_at=? WHERE project_id=? AND managed_internal_path=? AND state='active'`, continuationID, key, now, projectID, row.internalPath)
	if err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("claim Evidence payload cleanup: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return evidenceCleanupPreserve, nil
	}
	if err := tx.Commit(); err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("commit Evidence payload cleanup claim: %w", err)
	}
	return evidenceCleanupOwnPayload, nil
}

func (s *Service) recoverOwnedEvidenceGC(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow) (bool, error) {
	var state, gcContinuationID, gcKey string
	err := s.db.QueryRowContext(ctx, `SELECT state,gc_continuation_id,gc_idempotency_key FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, row.internalPath).Scan(&state, &gcContinuationID, &gcKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read recoverable Evidence payload cleanup: %w", err)
	}
	if state != "gc" || gcContinuationID != continuationID || gcKey != key {
		return false, nil
	}
	if err := s.removeClaimedEvidencePayload(row); err != nil {
		return false, err
	}
	if err := s.failEvidence(EvidenceFailureAfterPayloadUnlink); err != nil {
		return false, err
	}
	if err := s.finalizeEvidencePayloadGC(ctx, projectID, continuationID, key, row); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) removeClaimedEvidencePayload(row evidenceRequestRow) error {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	stagingDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.tempPath))
	if err != nil {
		return fmt.Errorf("open private Evidence staging directory: %w", err)
	}
	defer stagingDirectory.Close()
	if err := stagingDirectory.Remove(filepath.Base(row.tempPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove claimed Evidence staging temp: %w", err)
	}
	if err := syncEvidenceDirectory(stagingDirectory); err != nil {
		return err
	}
	finalDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.internalPath))
	if err != nil {
		return fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer finalDirectory.Close()
	if err := finalDirectory.Remove(filepath.Base(row.internalPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove claimed Evidence payload: %w", err)
	}
	return syncEvidenceDirectory(finalDirectory)
}

func (s *Service) finalizeEvidencePayloadGC(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Evidence payload cleanup completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath); err != nil {
		return fmt.Errorf("delete cleaned Evidence request: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=? AND state='gc' AND gc_continuation_id=? AND gc_idempotency_key=?`, projectID, row.internalPath, continuationID, key)
	if err != nil {
		return fmt.Errorf("delete Evidence payload cleanup claim: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fmt.Errorf("Evidence payload cleanup claim changed during recovery")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Evidence payload cleanup completion: %w", err)
	}
	return nil
}

func evidenceSourceStillSame(source evidenceSource) bool {
	current, err := source.root.Lstat(source.relativePath)
	return err == nil && current.Mode()&os.ModeSymlink == 0 && os.SameFile(source.info, current)
}

func (s *Service) openSecureEvidenceDirectory(root *os.Root, relative string) (*os.Root, error) {
	return openSecureDirectoryDurable(root, relative, s.failEvidence)
}

func openSecureDirectoryDurable(root *os.Root, relative string, checkpoint func(EvidenceFailurePoint) error) (*os.Root, error) {
	clean := filepath.Clean(relative)
	if clean == "." {
		return root.OpenRoot(".")
	}
	if clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("managed directory escapes its root")
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			if err := current.Mkdir(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				_ = current.Close()
				return nil, err
			}
			if checkpoint != nil {
				if err := checkpoint(EvidenceFailureAfterDirectoryCreate); err != nil {
					_ = current.Close()
					return nil, err
				}
			}
		}
		if err := syncEvidenceDirectory(current); err != nil {
			_ = current.Close()
			return nil, err
		}
		if checkpoint != nil {
			if err := checkpoint(EvidenceFailureAfterDirectorySync); err != nil {
				_ = current.Close()
				return nil, err
			}
		}
		info, err = current.Lstat(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = current.Close()
			return nil, errors.New("managed directory contains a non-directory or symbolic link")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			_ = current.Close()
			return nil, errors.New("managed directory changed while opening")
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}
