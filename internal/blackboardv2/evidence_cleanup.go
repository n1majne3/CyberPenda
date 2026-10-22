package blackboardv2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func removeJournaledEvidenceTemp(root *os.Root, name string) error {
	err := root.Remove(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove journaled Evidence temp: %w", err)
	}
	if err == nil {
		return syncEvidenceDirectory(root)
	}
	return nil
}

func (s *Service) syncAndCheckpointPublishedEvidence(ctx context.Context, projectID, continuationID, key, internalPath, tempPath string) error {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	directory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(internalPath))
	if err != nil {
		return fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer directory.Close()
	stagingDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(tempPath))
	if err != nil {
		return fmt.Errorf("open private Evidence staging directory: %w", err)
	}
	defer stagingDirectory.Close()
	if err := syncEvidenceDirectory(directory); err != nil {
		return err
	}
	if err := syncEvidenceDirectory(stagingDirectory); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureBeforePublishStore); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='published',publisher_token='',publisher_temp_identity='',updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key); err != nil {
		return fmt.Errorf("checkpoint recovered Evidence publication: %w", err)
	}
	return s.failEvidence(EvidenceFailureAfterFilePublish)
}

func syncEvidenceDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open managed Evidence directory for sync: %w", err)
	}
	// Windows cannot fsync a directory handle (golang/go#47366); the rename
	// already made the published file visible, so skip the durability sync.
	if runtime.GOOS == "windows" {
		_ = directory.Close()
		return nil
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync managed Evidence directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close managed Evidence directory: %w", err)
	}
	return nil
}

func (s *Service) cleanupDefinitiveEvidenceFailure(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow, cause error) {
	var semanticErr *Error
	if !errors.As(cause, &semanticErr) || semanticErr.Retryable || semanticErr.Code == "idempotency_conflict" {
		return
	}
	guard, err := s.acquireEvidenceCleanupGuard(row)
	if err != nil {
		return
	}
	defer guard.close()
	if _, err := s.sweepLegacyEvidenceTemps(ctx, projectID, row, guard.finalDirectory, nil); err != nil {
		return
	}
	if _, err := s.recoverPreviousEvidenceTemp(ctx, projectID, row, guard.finalDirectory, nil); err != nil {
		return
	}
	if err := removeJournaledEvidenceTemp(guard.stagingDirectory, filepath.Base(row.tempPath)); err != nil {
		return
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET publisher_token='',publisher_temp_identity='',updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, row.requestHash); err != nil {
		return
	}
	decision, err := s.claimEvidencePayloadGC(ctx, projectID, continuationID, key, row)
	if err != nil || decision == evidenceCleanupPreserve {
		return
	}
	if decision == evidenceCleanupRequestRetired {
		return
	}
	if err := s.failEvidence(EvidenceFailureAfterPayloadGCClaim); err != nil {
		return
	}
	if err := s.removeClaimedEvidencePayload(row); err != nil {
		return
	}
	if err := s.failEvidence(EvidenceFailureAfterPayloadUnlink); err != nil {
		return
	}
	_ = s.finalizeEvidencePayloadGC(ctx, projectID, continuationID, key, row)
}

type evidenceCleanupGuard struct {
	root             *os.Root
	finalDirectory   *os.Root
	stagingDirectory *os.Root
	files            []*os.File
}

func (s *Service) acquireEvidenceCleanupGuard(row evidenceRequestRow) (*evidenceCleanupGuard, error) {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return nil, fmt.Errorf("open managed Artifact Root for cleanup: %w", err)
	}
	finalDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.internalPath))
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open managed Evidence directory for cleanup: %w", err)
	}
	stagingDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.tempPath))
	if err != nil {
		_ = finalDirectory.Close()
		_ = root.Close()
		return nil, fmt.Errorf("open private Evidence staging directory for cleanup: %w", err)
	}
	guard := &evidenceCleanupGuard{root: root, finalDirectory: finalDirectory, stagingDirectory: stagingDirectory}
	for _, candidate := range []struct {
		directory *os.Root
		name      string
	}{
		{directory: stagingDirectory, name: filepath.Base(row.tempPath)},
		{directory: finalDirectory, name: filepath.Base(row.internalPath)},
	} {
		if _, err := candidate.directory.Lstat(candidate.name); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			guard.close()
			return nil, fmt.Errorf("inspect Evidence cleanup inode: %w", err)
		}
		file, err := openLockedEvidenceFile(candidate.directory, candidate.name, "Evidence cleanup payload")
		if err != nil {
			guard.close()
			return nil, err
		}
		guard.files = append(guard.files, file)
	}
	return guard, nil
}

func (guard *evidenceCleanupGuard) close() {
	for index := len(guard.files) - 1; index >= 0; index-- {
		unlockAndCloseEvidencePublisher(guard.files[index])
	}
	if guard.stagingDirectory != nil {
		_ = guard.stagingDirectory.Close()
	}
	if guard.finalDirectory != nil {
		_ = guard.finalDirectory.Close()
	}
	if guard.root != nil {
		_ = guard.root.Close()
	}
}

type evidenceCleanupDecision uint8

const (
	evidenceCleanupPreserve evidenceCleanupDecision = iota
	evidenceCleanupRequestRetired
	evidenceCleanupOwnPayload
)
