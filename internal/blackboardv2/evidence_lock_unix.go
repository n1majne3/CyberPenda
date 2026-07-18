//go:build !windows

package blackboardv2

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockEvidencePublisherFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return &Error{Code: "evidence_publication_in_progress", Message: "Evidence publication is already in progress", Path: "idempotency_key", Retryable: true}
		}
		return fmt.Errorf("lock Evidence publisher inode: %w", err)
	}
	return nil
}

func unlockAndCloseEvidencePublisher(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}
