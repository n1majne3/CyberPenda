package skill

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/store"
)

func TestRenameRetryingRetriesTransientPermissionDenied(t *testing.T) {
	calls := 0
	rename := func(oldpath, newpath string) error {
		calls++
		if calls < 3 {
			return &fs.PathError{Op: "rename", Path: oldpath, Err: fs.ErrPermission}
		}
		return nil
	}
	slept := make(chan time.Duration, 4)
	sleep := func(d time.Duration) { slept <- d }

	if err := renameRetrying(rename, "a", "b", true, sleep); err != nil {
		t.Fatalf("renameRetrying = %v, want nil after transient denials", err)
	}
	if calls != 3 {
		t.Fatalf("rename calls = %d, want 3", calls)
	}
	if len(slept) != 2 {
		t.Fatalf("sleeps = %d, want 2", len(slept))
	}
}

func TestRenameRetryingGivesUpAfterBoundedBackoff(t *testing.T) {
	denied := &fs.PathError{Op: "rename", Path: "a", Err: fs.ErrPermission}
	rename := func(oldpath, newpath string) error { return denied }
	var sleeps []time.Duration
	sleep := func(d time.Duration) { sleeps = append(sleeps, d) }

	err := renameRetrying(rename, "a", "b", true, sleep)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("renameRetrying = %v, want permission error", err)
	}
	want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleep[%d] = %v, want %v", i, sleeps[i], want[i])
		}
	}
}

func TestRenameRetryingReturnsNonPermissionErrorsImmediately(t *testing.T) {
	calls := 0
	boom := &fs.PathError{Op: "rename", Path: "a", Err: fs.ErrNotExist}
	rename := func(oldpath, newpath string) error {
		calls++
		return boom
	}
	sleep := func(time.Duration) {}

	if err := renameRetrying(rename, "a", "b", true, sleep); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("renameRetrying = %v, want original error", err)
	}
	if calls != 1 {
		t.Fatalf("rename calls = %d, want 1 (no retry)", calls)
	}
}

func TestRenameRetryingDoesNotRetryWhenDisabled(t *testing.T) {
	calls := 0
	rename := func(oldpath, newpath string) error {
		calls++
		return &fs.PathError{Op: "rename", Path: oldpath, Err: fs.ErrPermission}
	}
	sleep := func(time.Duration) {}

	if err := renameRetrying(rename, "a", "b", false, sleep); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("renameRetrying = %v, want permission error without retry", err)
	}
	if calls != 1 {
		t.Fatalf("rename calls = %d, want 1 (retry disabled)", calls)
	}
}

func TestPublishRestoresPreviousLiveSkillAfterTransientRollbackDenial(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	svc := NewService(db, filepath.Join(t.TempDir(), "skills"))
	ctx := context.Background()
	if _, err := svc.Publish(ctx, PublishRequest{
		Metadata: Metadata{ID: "recon-helper", Name: "Recon Helper"},
		Files:    map[string]string{"SKILL.md": "version one"},
	}); err != nil {
		t.Fatalf("publish initial Skill: %v", err)
	}
	livePath := svc.bundlePath("recon-helper")

	originalRename := svc.bundleFiles.rename
	restoreAttempts := 0
	svc.bundleFiles.retryPermission = true
	svc.bundleFiles.sleep = func(time.Duration) {}
	svc.bundleFiles.rename = func(oldpath, newpath string) error {
		if strings.Contains(filepath.Base(oldpath), ".backup-") && newpath == livePath {
			restoreAttempts++
			if restoreAttempts == 1 {
				return &fs.PathError{Op: "rename", Path: oldpath, Err: fs.ErrPermission}
			}
		}
		return originalRename(oldpath, newpath)
	}

	// Force metadata persistence to fail after the replacement bundle reaches
	// the live path. Publication must restore the previous live Skill.
	if err := db.Close(); err != nil {
		t.Fatalf("close Store before failed update: %v", err)
	}
	if _, err := svc.Publish(ctx, PublishRequest{
		Metadata: Metadata{ID: "recon-helper", Name: "Recon Helper Updated"},
		Files:    map[string]string{"SKILL.md": "version two"},
	}); err == nil {
		t.Fatal("expected metadata persistence failure")
	}

	content, err := os.ReadFile(filepath.Join(livePath, "SKILL.md"))
	if err != nil {
		t.Fatalf("read restored live Skill: %v", err)
	}
	if string(content) != "version one" {
		t.Fatalf("restored live Skill = %q, want version one", content)
	}
	if restoreAttempts != 2 {
		t.Fatalf("restore attempts = %d, want 2 after one transient denial", restoreAttempts)
	}
}

func TestPublishReturnsRollbackFailure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	svc := NewService(db, filepath.Join(t.TempDir(), "skills"))
	ctx := context.Background()
	if _, err := svc.Publish(ctx, PublishRequest{
		Metadata: Metadata{ID: "recon-helper", Name: "Recon Helper"},
		Files:    map[string]string{"SKILL.md": "version one"},
	}); err != nil {
		t.Fatalf("publish initial Skill: %v", err)
	}
	livePath := svc.bundlePath("recon-helper")

	originalRename := svc.bundleFiles.rename
	svc.bundleFiles.retryPermission = false
	svc.bundleFiles.rename = func(oldpath, newpath string) error {
		if strings.Contains(filepath.Base(oldpath), ".backup-") && newpath == livePath {
			return &fs.PathError{Op: "rename", Path: oldpath, Err: fs.ErrPermission}
		}
		return originalRename(oldpath, newpath)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close Store before failed update: %v", err)
	}
	_, err = svc.Publish(ctx, PublishRequest{
		Metadata: Metadata{ID: "recon-helper", Name: "Recon Helper Updated"},
		Files:    map[string]string{"SKILL.md": "version two"},
	})
	if err == nil || !strings.Contains(err.Error(), "restore previous live Skill") {
		t.Fatalf("publish error = %v, want reported rollback failure", err)
	}
}
