package blackboardv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (s *Service) sweepLegacyEvidenceTemps(ctx context.Context, projectID string, row evidenceRequestRow, root *os.Root, destination func() (*os.File, error)) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin legacy Evidence temp sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND source_sha256=? AND source_size_bytes=? AND status<>'completed'`, projectID, row.sha256, row.size).Scan(&unresolved); err != nil {
		return false, fmt.Errorf("count unresolved shared Evidence reservations: %w", err)
	}
	preserveValid := unresolved - 1
	if preserveValid < 0 {
		preserveValid = 0
	}
	adopted, err := sweepEvidenceRecoveryTemps(root, destination, row.sha256, row.size, preserveValid, func(name string) (bool, error) {
		if !isHistoricalEvidenceTempName(name) {
			return false, nil
		}
		candidatePath := internalEvidencePath(filepath.Dir(row.internalPath), name)
		claimed, err := evidenceManagedPathClaimedBy(ctx, tx, projectID, candidatePath)
		return !claimed, err
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit legacy Evidence temp sweep: %w", err)
	}
	return adopted, nil
}

func (s *Service) recoverPreviousEvidenceTemp(ctx context.Context, projectID string, row evidenceRequestRow, root *os.Root, destination func() (*os.File, error)) (bool, error) {
	row.previousTemp = filepath.ToSlash(row.previousTemp)
	if row.previousTemp == "" || row.previousTemp == row.tempPath || filepath.Dir(row.previousTemp) != filepath.Dir(row.internalPath) {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin previous Evidence temp recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	claimed, err := evidenceManagedPathClaimedBy(ctx, tx, projectID, row.previousTemp)
	if err != nil || claimed {
		if err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit claimed previous Evidence temp check: %w", err)
		}
		return false, nil
	}
	previousName := filepath.Base(row.previousTemp)
	adopted, err := sweepEvidenceRecoveryTemps(root, destination, row.sha256, row.size, 0, func(name string) (bool, error) {
		return name == previousName, nil
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit previous Evidence temp recovery: %w", err)
	}
	return adopted, nil
}

func sweepEvidenceRecoveryTemps(root *os.Root, destination func() (*os.File, error), digest string, size int64, preserveValid int, eligible func(string) (bool, error)) (bool, error) {
	directory, err := root.Open(".")
	if err != nil {
		return false, fmt.Errorf("open managed Evidence directory for legacy sweep: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return false, fmt.Errorf("read managed Evidence directory for legacy sweep: %w", readErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close managed Evidence legacy sweep directory: %w", closeErr)
	}
	adopted := false
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		allowed, err := eligible(name)
		if err != nil {
			return false, err
		}
		if !allowed {
			continue
		}
		valid := false
		info, statErr := root.Lstat(name)
		if statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			candidate, openErr := root.Open(name)
			if openErr != nil {
				return false, fmt.Errorf("open legacy Evidence temp: %w", openErr)
			}
			valid, openErr = verifyOpenEvidenceFile(candidate, digest, size)
			if openErr == nil && valid && !adopted && destination != nil {
				target, targetErr := destination()
				if targetErr != nil {
					_ = candidate.Close()
					return false, targetErr
				}
				if target != nil {
					if err := target.Truncate(0); err == nil {
						_, err = target.Seek(0, io.SeekStart)
					}
					if err == nil {
						_, err = candidate.Seek(0, io.SeekStart)
					}
					if err == nil {
						_, err = io.Copy(target, candidate)
					}
					if err == nil {
						err = target.Sync()
					}
					if err == nil {
						var ready bool
						ready, err = verifyOpenEvidenceFile(target, digest, size)
						if err == nil && !ready {
							err = errors.New("adopted Evidence temp failed integrity verification")
						}
					}
					if err != nil {
						_ = candidate.Close()
						return false, fmt.Errorf("adopt legacy Evidence temp: %w", err)
					}
					adopted = true
				}
			}
			if openErr != nil {
				_ = candidate.Close()
				return false, fmt.Errorf("verify legacy Evidence temp: %w", openErr)
			}
			if err := candidate.Close(); err != nil {
				return false, fmt.Errorf("close legacy Evidence temp: %w", err)
			}
		}
		if valid && preserveValid > 0 {
			preserveValid--
			continue
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("remove legacy Evidence temp: %w", err)
		} else if err == nil {
			removed = true
		}
	}
	if removed {
		if err := syncEvidenceDirectory(root); err != nil {
			return false, err
		}
	}
	return adopted, nil
}

func copyEvidenceWithMidpointFailure(destination io.Writer, hash io.Writer, source io.Reader, fail func() error) (int64, error) {
	buffer := make([]byte, 32*1024)
	var copied int64
	failureChecked := false
	for {
		count, readErr := source.Read(buffer)
		if count != 0 {
			written, writeErr := io.MultiWriter(destination, hash).Write(buffer[:count])
			copied += int64(written)
			if writeErr != nil {
				return copied, writeErr
			}
			if !failureChecked {
				failureChecked = true
				if err := fail(); err != nil {
					return copied, err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return copied, nil
		}
		if readErr != nil {
			return copied, readErr
		}
	}
}
