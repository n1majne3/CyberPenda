package task

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

	"pentest/internal/project"
	"pentest/internal/store"
)

// The racing driver inserts one status write between a caller's pre-read and
// its guarded UPDATE, which is the exact interleaving of a lost
// compare-and-swap race between two continuation observers. Task and Session
// must keep the same contract here (ADR 0020): a lost race surfaces
// ErrContinuationStatusConflict unless the winner wrote the requested status.
var (
	taskRaceDriverOnce sync.Once
	taskRaceMu         sync.Mutex
	taskRaceSabotage   *taskRaceUpdate
)

type taskRaceUpdate struct {
	query string
	args  []driver.NamedValue
}

type taskRacingDriver struct{ inner driver.Driver }

func (d *taskRacingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &taskRacingConn{Conn: conn}, nil
}

type taskRacingConn struct{ driver.Conn }

func (c *taskRacingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	taskRaceMu.Lock()
	sabotage := taskRaceSabotage
	taskRaceSabotage = nil
	taskRaceMu.Unlock()
	if sabotage != nil && strings.HasPrefix(query, "UPDATE task_continuations SET status = ") {
		if _, err := c.exec(ctx, sabotage.query, sabotage.args); err != nil {
			return nil, err
		}
	}
	return c.exec(ctx, query, args)
}

func (c *taskRacingConn) exec(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	type execer interface {
		ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error)
	}
	inner, ok := c.Conn.(execer)
	if !ok {
		return nil, driver.ErrSkip
	}
	return inner.ExecContext(ctx, query, args)
}

func armTaskContinuationStatusRace(t *testing.T, status, continuationID string) {
	t.Helper()
	taskRaceMu.Lock()
	defer taskRaceMu.Unlock()
	taskRaceSabotage = &taskRaceUpdate{
		query: "UPDATE task_continuations SET status = '" + status + "' WHERE id = ?",
		args:  []driver.NamedValue{{Value: continuationID, Ordinal: 1}},
	}
}

// TestLostContinuationStatusRaceSurfacesConflict locks the Task twin of the
// lost-race contract asserted for Sessions.
func TestLostContinuationStatusRaceSurfacesConflict(t *testing.T) {
	dataRoot := t.TempDir()
	path := filepath.Join(dataRoot, "pentest.db")
	seed, err := store.Open(path)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	projects := project.NewService(seed)
	svc := NewService(seed, projects)
	createdProject, err := projects.Create("Race parity", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	created, err := svc.Create(CreateRequest{ProjectID: createdProject.ID, Type: TypePentest, Goal: "race", Runner: RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile", "codex", RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	taskRaceDriverOnce.Do(func() {
		sql.Register("sqlite-task-race", &taskRacingDriver{inner: &sqlite.Driver{}})
	})
	racing, err := sql.Open("sqlite-task-race", path)
	if err != nil {
		t.Fatalf("open racing store: %v", err)
	}
	defer func() { _ = racing.Close() }()
	racingService := NewService(&store.DB{DB: racing}, project.NewService(&store.DB{DB: racing}))

	armTaskContinuationStatusRace(t, string(StatusCompleted), continuation.ID)
	current, err := racingService.UpdateContinuationStatus(continuation.ID, StatusStopped)
	if !errors.Is(err, ErrContinuationStatusConflict) {
		t.Fatalf("lost race error = %v, want ErrContinuationStatusConflict", err)
	}
	if current.Status != StatusCompleted {
		t.Fatalf("lost race returned status %q, want the winning status %q", current.Status, StatusCompleted)
	}
}
