package session

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"modernc.org/sqlite"

	"pentest/internal/store"
)

// The racing driver inserts one status write between a caller's pre-read and
// its guarded UPDATE, which is the exact interleaving of a lost
// compare-and-swap race between two continuation observers.
var (
	raceDriverOnce sync.Once
	raceMu         sync.Mutex
	raceSabotage   *raceUpdate
)

type raceUpdate struct {
	query string
	args  []driver.NamedValue
}

type racingDriver struct{ inner driver.Driver }

func (d *racingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &racingConn{Conn: conn}, nil
}

type racingConn struct{ driver.Conn }

func (c *racingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	raceMu.Lock()
	sabotage := raceSabotage
	raceSabotage = nil
	raceMu.Unlock()
	if sabotage != nil && strings.HasPrefix(query, "UPDATE session_continuations SET status=") {
		if _, err := c.exec(ctx, sabotage.query, sabotage.args); err != nil {
			return nil, err
		}
	}
	return c.exec(ctx, query, args)
}

func (c *racingConn) exec(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	type execer interface {
		ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error)
	}
	inner, ok := c.Conn.(execer)
	if !ok {
		return nil, driver.ErrSkip
	}
	return inner.ExecContext(ctx, query, args)
}

// armContinuationStatusRace changes one continuation's status through the
// wrapped connection immediately before the next guarded status UPDATE runs.
func armContinuationStatusRace(t *testing.T, status, continuationID string) {
	t.Helper()
	raceMu.Lock()
	defer raceMu.Unlock()
	raceSabotage = &raceUpdate{
		query: "UPDATE session_continuations SET status='" + status + "' WHERE id=?",
		args:  []driver.NamedValue{{Value: continuationID, Ordinal: 1}},
	}
}

func openRacingSessionService(t *testing.T, path, workdir string) *Service {
	t.Helper()
	raceDriverOnce.Do(func() {
		sql.Register("sqlite-session-race", &racingDriver{inner: &sqlite.Driver{}})
	})
	db, err := sql.Open("sqlite-session-race", path)
	if err != nil {
		t.Fatalf("open racing store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(&store.DB{DB: db}, workdir)
}

func newRunningRaceContinuation(t *testing.T) (*Service, Continuation) {
	t.Helper()
	dataRoot := t.TempDir()
	path := filepath.Join(dataRoot, "pentest.db")
	seed, err := store.Open(path)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	seedService := NewService(seed, filepath.Join(dataRoot, "workdirs"))
	created, err := seedService.Create(CreateRequest{Input: "probe"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	continuation, err := seedService.CreateContinuation(created.ID, "profile", "fake", RunnerSandbox, map[string]any{})
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := seedService.UpdateContinuationStatus(continuation.ID, RuntimeStatusRunning); err != nil {
		t.Fatalf("start continuation: %v", err)
	}
	workdir := seedService.workdirRoot
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	return openRacingSessionService(t, path, workdir), continuation
}

// A lost race must surface the conflict: the caller learns another observer
// changed the continuation, instead of silently receiving that newer state as
// its own successful transition (parity with the Task twin).
func TestUpdateContinuationStatusReportsLostRaceConflict(t *testing.T) {
	service, continuation := newRunningRaceContinuation(t)

	armContinuationStatusRace(t, string(RuntimeStatusCompleted), continuation.ID)
	current, err := service.UpdateContinuationStatus(continuation.ID, RuntimeStatusStopped)
	if !errors.Is(err, ErrContinuationStatusConflict) {
		t.Fatalf("lost race error = %v, want ErrContinuationStatusConflict", err)
	}
	if current.Status != RuntimeStatusCompleted {
		t.Fatalf("lost race returned status %q, want the winning status %q", current.Status, RuntimeStatusCompleted)
	}
}

// A lost race to the same requested status stays idempotent, matching the
// pre-check terminal path.
func TestUpdateContinuationStatusLostRaceToSameStatusIsIdempotent(t *testing.T) {
	service, continuation := newRunningRaceContinuation(t)

	armContinuationStatusRace(t, string(RuntimeStatusStopped), continuation.ID)
	current, err := service.UpdateContinuationStatus(continuation.ID, RuntimeStatusStopped)
	if err != nil {
		t.Fatalf("same-status lost race error = %v, want nil", err)
	}
	if current.Status != RuntimeStatusStopped {
		t.Fatalf("same-status lost race status = %q, want %q", current.Status, RuntimeStatusStopped)
	}
}
