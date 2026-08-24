package blackboardv2

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const evidenceLockRange = ^uint32(0)

func lockEvidencePublisherFile(file *os.File) error {
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		evidenceLockRange,
		evidenceLockRange,
		&windows.Overlapped{},
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return &Error{Code: "evidence_publication_in_progress", Message: "Evidence publication is already in progress", Path: "idempotency_key", Retryable: true}
	}
	return fmt.Errorf("lock Evidence publisher inode: %w", err)
}

func unlockAndCloseEvidencePublisher(file *os.File) {
	_ = windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		evidenceLockRange,
		evidenceLockRange,
		&windows.Overlapped{},
	)
	_ = file.Close()
}

// evidenceLockViolation reports whether err is a byte-range lock conflict.
// LockFileEx blocks other handles' I/O over the locked range even inside the
// same process, so a fresh read of an actively-journaled evidence file fails
// this way and must be treated as publication-in-progress, not integrity data.
func evidenceLockViolation(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
