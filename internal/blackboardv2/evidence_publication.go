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

func (s *Service) verifyJournaledEvidenceTemp(tempPath, digest string, size int64) (bool, error) {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return false, fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	directory, file, _, _, err := openSecureRegularFile(root, tempPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, semanticError("evidence_integrity_failed", "journaled Evidence temp path is symlinked or unsafe", "source_path", nil)
	}
	defer directory.Close()
	defer file.Close()
	hash := sha256.New()
	actualSize, err := io.Copy(hash, file)
	if evidenceLockViolation(err) {
		// Another publisher is actively journaling this inode; the read is
		// temporarily blocked, not an integrity failure. Linux flock never
		// blocks other handles' I/O, so only Windows reports this.
		return false, &Error{Code: "evidence_publication_in_progress", Message: "journaled Evidence temp is blocked by an active publisher", Path: "source_path", Retryable: true}
	}
	if err != nil {
		return false, fmt.Errorf("hash journaled Evidence temp: %w", err)
	}
	return actualSize == size && hex.EncodeToString(hash.Sum(nil)) == digest, nil
}

func (s *Service) evidencePublicationRecoveryExists(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow) (bool, bool, error) {
	row.previousTemp = filepath.ToSlash(row.previousTemp)
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return false, false, fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	stagingDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.tempPath))
	if err != nil {
		return false, false, fmt.Errorf("open private Evidence staging directory: %w", err)
	}
	defer stagingDirectory.Close()
	deterministicExists := false
	if _, err := stagingDirectory.Lstat(filepath.Base(row.tempPath)); err == nil {
		deterministicExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, false, fmt.Errorf("inspect journaled Evidence temp: %w", err)
	}
	if row.previousTemp != "" && row.previousTemp != row.tempPath {
		claimed, err := s.evidenceManagedPathClaimed(ctx, projectID, row.previousTemp)
		if err != nil {
			return false, false, err
		}
		if !claimed {
			valid, err := s.verifyJournaledEvidenceTemp(row.previousTemp, row.sha256, row.size)
			if err != nil {
				return false, false, err
			}
			if valid {
				return true, true, nil
			}
		}
	}
	finalDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.internalPath))
	if err != nil {
		return false, false, fmt.Errorf("open managed Evidence recovery directory: %w", err)
	}
	defer finalDirectory.Close()
	opened, err := finalDirectory.Open(".")
	if err != nil {
		return false, false, fmt.Errorf("open managed Evidence recovery directory listing: %w", err)
	}
	entries, readErr := opened.ReadDir(-1)
	closeErr := opened.Close()
	if readErr != nil {
		return false, false, fmt.Errorf("read managed Evidence recovery directory: %w", readErr)
	}
	if closeErr != nil {
		return false, false, fmt.Errorf("close managed Evidence recovery directory: %w", closeErr)
	}
	for _, entry := range entries {
		if !isHistoricalEvidenceTempName(entry.Name()) {
			continue
		}
		candidatePath := internalEvidencePath(filepath.Dir(row.internalPath), entry.Name())
		claimed, err := s.evidenceManagedPathClaimed(ctx, projectID, candidatePath)
		if err != nil {
			return false, false, err
		}
		if claimed {
			continue
		}
		valid, err := legacyEvidenceCandidateValid(finalDirectory, entry.Name(), row.sha256, row.size)
		if err != nil {
			return false, false, err
		}
		if valid {
			return true, true, nil
		}
	}
	return deterministicExists, false, nil
}

func (s *Service) evidenceManagedPathClaimed(ctx context.Context, projectID, internalPath string) (bool, error) {
	return evidenceManagedPathClaimedBy(ctx, s.db, projectID, internalPath)
}

type evidenceClaimQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func evidenceManagedPathClaimedBy(ctx context.Context, querier evidenceClaimQuerier, projectID, internalPath string) (bool, error) {
	var references int
	if err := querier.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?) +
			(SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND managed_internal_path=?)`,
		projectID, internalPath, projectID, internalPath,
	).Scan(&references); err != nil {
		return false, fmt.Errorf("check managed Evidence path claim: %w", err)
	}
	return references != 0, nil
}

func isHistoricalEvidenceTempName(name string) bool {
	const prefix = ".retain-"
	if len(name) != len(prefix)+24 || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, character := range name[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func legacyEvidenceCandidateValid(root *os.Root, name, digest string, size int64) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, nil
	}
	file, err := root.Open(name)
	if err != nil {
		return false, nil
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, nil
	}
	valid, err := verifyOpenEvidenceFile(file, digest, size)
	if err != nil {
		return false, nil
	}
	return valid, nil
}

func (s *Service) ensureEvidencePublished(ctx context.Context, projectID, continuationID, key string, row *evidenceRequestRow, source *evidenceSource) error {
	if source != nil && !evidenceSourceStillSame(*source) {
		return semanticError("evidence_source_changed", "Evidence source was replaced during retention", "source_path", nil)
	}
	if err := s.failEvidence(EvidenceFailureBeforeFilePublish); err != nil {
		return err
	}
	managedRoot, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer managedRoot.Close()
	finalDirectoryPath := filepath.Dir(row.internalPath)
	destinationRoot, err := s.openSecureEvidenceDirectory(managedRoot, finalDirectoryPath)
	if err != nil {
		return fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer destinationRoot.Close()
	stagingDirectoryPath := filepath.Dir(row.tempPath)
	stagingRoot, err := s.openSecureEvidenceDirectory(managedRoot, stagingDirectoryPath)
	if err != nil {
		return fmt.Errorf("open private Evidence staging directory: %w", err)
	}
	defer stagingRoot.Close()
	name := filepath.Base(row.internalPath)
	tempName := filepath.Base(row.tempPath)
	if _, err := destinationRoot.Lstat(name); err == nil {
		existing, err := openLockedEvidenceFile(destinationRoot, name, "managed Evidence payload")
		if err != nil {
			return err
		}
		defer unlockAndCloseEvidencePublisher(existing)
		ready, verifyErr := verifyOpenEvidenceFile(existing, row.sha256, row.size)
		if verifyErr != nil {
			return verifyErr
		}
		if !ready {
			return semanticError("evidence_integrity_failed", "managed Evidence payload failed integrity verification", "key", nil)
		}
		if _, err := s.sweepLegacyEvidenceTemps(ctx, projectID, *row, destinationRoot, nil); err != nil {
			return err
		}
		if _, err := s.recoverPreviousEvidenceTemp(ctx, projectID, *row, destinationRoot, nil); err != nil {
			return err
		}
		if err := removeInactiveJournaledEvidenceTemp(stagingRoot, tempName); err != nil {
			return err
		}
		return s.syncAndCheckpointPublishedEvidence(ctx, projectID, continuationID, key, row.internalPath, row.tempPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect managed Evidence destination: %w", err)
	}
	publisher, err := s.acquireEvidencePublisher(ctx, stagingRoot, projectID, continuationID, key, row)
	if err != nil {
		return err
	}
	defer publisher.close()
	if err := syncEvidenceDirectory(stagingRoot); err != nil {
		return err
	}
	tempReady, err := verifyOpenEvidenceFile(publisher.file, row.sha256, row.size)
	if err != nil {
		return fmt.Errorf("verify locked Evidence temp: %w", err)
	}
	previousAdopted, err := s.recoverPreviousEvidenceTemp(ctx, projectID, *row, destinationRoot, func() (*os.File, error) {
		if tempReady {
			return nil, nil
		}
		return publisher.writer()
	})
	if err != nil {
		return err
	}
	tempReady = tempReady || previousAdopted
	var legacyDestination func() (*os.File, error)
	if !tempReady {
		legacyDestination = publisher.writer
	}
	legacyAdopted, err := s.sweepLegacyEvidenceTemps(ctx, projectID, *row, destinationRoot, legacyDestination)
	if err != nil {
		return err
	}
	tempReady = tempReady || legacyAdopted
	if !tempReady {
		if source == nil {
			return semanticError("evidence_source_changed", "Evidence source is required to rebuild an incomplete journaled temp", "source_path", nil)
		}
		temp, err := publisher.writer()
		if err != nil {
			return err
		}
		if err := temp.Truncate(0); err != nil {
			return fmt.Errorf("truncate journaled Evidence temp: %w", err)
		}
		if _, err := temp.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind journaled Evidence temp: %w", err)
		}
		if err := s.failEvidence(EvidenceFailureBeforeTempCopy); err != nil {
			return err
		}
		copiedHash := sha256.New()
		copiedSize, copyErr := copyEvidenceWithMidpointFailure(temp, copiedHash, source.file, func() error {
			return s.failEvidence(EvidenceFailureMidTempCopy)
		})
		if copyErr == nil && (copiedSize != source.size || hex.EncodeToString(copiedHash.Sum(nil)) != source.sha256) {
			return semanticError("evidence_source_changed", "Evidence source bytes changed during retention", "source_path", nil)
		}
		if copyErr != nil {
			return fmt.Errorf("write journaled Evidence temp: %w", copyErr)
		}
		if !evidenceSourceStillSame(*source) {
			return semanticError("evidence_source_changed", "Evidence source was replaced during retention", "source_path", nil)
		}
	}
	if err := publisher.file.Chmod(0o400); err != nil {
		return fmt.Errorf("make journaled Evidence temp immutable: %w", err)
	}
	if err := publisher.file.Sync(); err != nil {
		return fmt.Errorf("sync journaled Evidence temp: %w", err)
	}
	if err := syncEvidenceDirectory(stagingRoot); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureAfterTempDirectorySync); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureAfterTempSync); err != nil {
		return err
	}
	ready, err := verifyOpenEvidenceFile(publisher.file, row.sha256, row.size)
	if err != nil {
		return fmt.Errorf("rehash synced Evidence temp: %w", err)
	}
	if !ready {
		return semanticError("evidence_integrity_failed", "synced Evidence temp failed integrity verification", "source_path", nil)
	}
	if err := publisher.validate(ctx, s.db, stagingRoot, projectID, continuationID, key, *row); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET payload_owned=1,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND managed_internal_path=? AND publisher_token=? AND publisher_temp_identity=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, row.internalPath, publisher.token, publisher.identity)
	if err != nil {
		return fmt.Errorf("claim managed Evidence payload: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &Error{Code: "evidence_publication_in_progress", Message: "Evidence publisher claim changed before publication", Path: "idempotency_key", Retryable: true}
	}
	row.payloadOwned = true
	if err := s.failEvidence(EvidenceFailureBeforeFileRename); err != nil {
		return err
	}
	if err := managedRoot.Rename(row.tempPath, row.internalPath); err != nil {
		return fmt.Errorf("publish managed Evidence: %w", err)
	}
	lockedInfo, err := publisher.file.Stat()
	if err != nil {
		return fmt.Errorf("stat published Evidence inode: %w", err)
	}
	finalInfo, err := destinationRoot.Lstat(name)
	if err != nil || !os.SameFile(lockedInfo, finalInfo) {
		return semanticError("evidence_integrity_failed", "published Evidence path is not the synced publisher inode", "source_path", nil)
	}
	if err := s.failEvidence(EvidenceFailureAfterFileRename); err != nil {
		return err
	}
	if err := syncEvidenceDirectory(destinationRoot); err != nil {
		return err
	}
	if err := syncEvidenceDirectory(stagingRoot); err != nil {
		return err
	}
	// Release the exclusive publisher lock now that the temp has been renamed
	// to its managed path. The byte-range lock follows the file across the
	// rename on Windows, so the fresh verification handle below would collide
	// with the still-locked range; the DB publisher claim guards the rest of
	// the publication checkpoint.
	unlockAndCloseEvidencePublisher(publisher.file)
	publisher.file = nil
	if err := s.failEvidence(EvidenceFailureBeforePublishStore); err != nil {
		return err
	}
	ready, err = s.verifyManagedEvidencePayload(row.internalPath, row.sha256, row.size)
	if err != nil {
		return err
	}
	if !ready {
		return semanticError("evidence_integrity_failed", "published Evidence payload failed integrity verification", "key", nil)
	}
	result, err = s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='published',publisher_token='',publisher_temp_identity='',updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND publisher_token=? AND publisher_temp_identity=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, publisher.token, publisher.identity)
	if err != nil {
		return fmt.Errorf("checkpoint Evidence publication: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &Error{Code: "evidence_publication_in_progress", Message: "Evidence publisher claim changed before checkpoint", Path: "idempotency_key", Retryable: true}
	}
	row.status = "published"
	return s.failEvidence(EvidenceFailureAfterFilePublish)
}
