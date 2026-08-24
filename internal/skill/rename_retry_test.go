package skill

import (
	"errors"
	"io/fs"
	"testing"
	"time"
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
