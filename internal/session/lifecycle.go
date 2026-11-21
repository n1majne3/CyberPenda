package session

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) CleanupDeletedWorkdirs() error {
	entries, err := os.ReadDir(s.workdirRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list Session cleanup markers: %w", err)
	}
	var cleanupErr error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".deleting-") {
			continue
		}
		path := filepath.Join(s.workdirRoot, entry.Name())
		sessionID := strings.TrimPrefix(entry.Name(), ".deleting-")
		var storedWorkdir string
		err := s.db.QueryRow(`SELECT workdir FROM sessions WHERE id=?`, sessionID).Scan(&storedWorkdir)
		if err == nil {
			restorePath, pathErr := s.managedWorkdir(sessionID)
			if pathErr != nil || filepath.Clean(storedWorkdir) != filepath.Clean(restorePath) {
				cleanupErr = errors.Join(cleanupErr, ErrInvalidWorkdir)
				continue
			}
			if _, statErr := os.Stat(restorePath); statErr == nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore %s: target already exists", entry.Name()))
				continue
			} else if !errors.Is(statErr, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("inspect restore target %s: %w", entry.Name(), statErr))
				continue
			}
			if renameErr := os.Rename(path, restorePath); renameErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore %s: %w", entry.Name(), renameErr))
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("resolve %s: %w", entry.Name(), err))
			continue
		}
		if err := s.removeAll(path); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove %s: %w", entry.Name(), err))
		}
	}
	return cleanupErr
}

// DeriveTitle returns the bounded title from the first non-empty input line.

func (s *Service) Rename(id, title string) (Session, error) {
	if err := s.FlushRuntimeOutputs(id); err != nil {
		return Session{}, err
	}
	title = truncateTitle(strings.TrimSpace(title))
	if title == "" {
		return Session{}, ErrMissingTitle
	}
	found, err := s.Get(id)
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, fmt.Errorf("begin Session rename: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE sessions SET title=?,updated_at=?,last_activity_at=? WHERE id=?`, title, formatTime(now), formatTime(now), id); err != nil {
		return Session{}, fmt.Errorf("rename Session: %w", err)
	}
	if _, err := appendEventTx(tx, id, EventKindLifecycle, EventPayload{"phase": "renamed", "title": title}, now); err != nil {
		return Session{}, fmt.Errorf("store Session rename Event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit Session rename: %w", err)
	}
	found.Title, found.UpdatedAt, found.LastActivityAt = title, now, now
	return found, nil
}

// Archive closes an open Session's durable lifecycle while retaining all
// Session-owned Events and its Workdir.
func (s *Service) Archive(id string) (Session, error) {
	return s.transition(id, LifecycleOpen, LifecycleArchived, ErrAlreadyArchived, "archived")
}

func (s *Service) Restore(id string) (Session, error) {
	return s.transition(id, LifecycleArchived, LifecycleOpen, ErrNotArchived, "restored")
}

func (s *Service) transition(id string, from, to Lifecycle, wrongState error, phase string) (Session, error) {
	if err := s.FlushRuntimeOutputs(id); err != nil {
		return Session{}, err
	}
	found, err := s.Get(id)
	if err != nil {
		return Session{}, err
	}
	if found.Lifecycle != from {
		return Session{}, wrongState
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, fmt.Errorf("begin Session transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE sessions SET lifecycle=?,updated_at=?,last_activity_at=? WHERE id=? AND lifecycle=?`, string(to), formatTime(now), formatTime(now), id, string(from))
	if err != nil {
		return Session{}, fmt.Errorf("update Session lifecycle: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return Session{}, fmt.Errorf("read Session lifecycle update: %w", err)
		}
		return Session{}, wrongState
	}
	if _, err := appendEventTx(tx, id, EventKindLifecycle, EventPayload{"phase": phase}, now); err != nil {
		return Session{}, fmt.Errorf("store Session lifecycle Event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit Session transition: %w", err)
	}
	found.Lifecycle, found.UpdatedAt, found.LastActivityAt = to, now, now
	return found, nil
}

// Delete permanently removes an archived Session, its Events, and Workdir.
// The Workdir is first quarantined under the managed root so a failed database
// transaction can restore it without ever touching an arbitrary path.
func (s *Service) Delete(id string) error {
	if err := s.FlushRuntimeOutputs(id); err != nil {
		return err
	}
	found, err := s.Get(id)
	if err != nil {
		return err
	}
	if found.Lifecycle == LifecycleOpen {
		return ErrOpenSession
	}
	if found.Lifecycle != LifecycleArchived {
		return ErrInvalidLifecycle
	}
	if active, err := s.ActiveContinuation(id); err != nil {
		return fmt.Errorf("check Session Runtime before delete: %w", err)
	} else if active != nil {
		return ErrActiveContinuation
	}
	workdir, err := s.managedWorkdir(id)
	if err != nil {
		return err
	}
	if filepath.Clean(found.Workdir) != filepath.Clean(workdir) {
		return ErrInvalidWorkdir
	}
	quarantine := filepath.Join(s.workdirRoot, ".deleting-"+id)
	if _, err := os.Stat(quarantine); err == nil {
		return fmt.Errorf("delete Session: cleanup already in progress")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Session cleanup path: %w", err)
	}
	quarantined := false
	if _, err := os.Stat(workdir); err == nil {
		if err := os.Rename(workdir, quarantine); err != nil {
			return fmt.Errorf("quarantine Session Workdir: %w", err)
		}
		quarantined = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Session Workdir: %w", err)
	}
	restore := func() {
		if quarantined {
			_ = os.Rename(quarantine, workdir)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		restore()
		return fmt.Errorf("begin Session delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM session_events WHERE session_id=?`, id); err != nil {
		restore()
		return fmt.Errorf("delete Session Events: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM session_continuations WHERE session_id=?`, id); err != nil {
		restore()
		return fmt.Errorf("delete Session continuations: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM session_runtime_config_versions WHERE session_id=?`, id); err != nil {
		restore()
		return fmt.Errorf("delete Session Runtime configs: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM sessions WHERE id=? AND lifecycle=?`, id, string(LifecycleArchived))
	if err != nil {
		restore()
		return fmt.Errorf("delete Session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		restore()
		return fmt.Errorf("read Session deletion: %w", err)
	}
	if affected != 1 {
		restore()
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		restore()
		return fmt.Errorf("commit Session delete: %w", err)
	}
	if quarantined {
		// The database commit is the logical deletion boundary. A filesystem
		// failure leaves the quarantine path as a durable retry marker and must
		// not turn the already-committed operation into a false API failure.
		_ = s.removeAll(quarantine)
	}
	return nil
}
