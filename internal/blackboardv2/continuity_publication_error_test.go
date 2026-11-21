package blackboardv2

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestCreateContinuationRestoresSnapshotAfterPublicationError(t *testing.T) {
	for _, priorExists := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "prior bytes"}[priorExists], func(t *testing.T) {
			db, continuity, request := newPublicationErrorFixture(t)
			prior := []byte(`{"revision":99}`)
			if priorExists {
				if err := materializeWorkingSnapshot(continuity.runtimeRoot, request.TaskID, prior); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(continuity.runtimeRoot, request.TaskID, "workdir", ".pentest", "blackboard.json")
			failure := errors.New("directory sync failed after rename")
			published := false
			continuity.publishWorkingSnapshot = func(root, taskID string, data []byte) error {
				if err := materializeWorkingSnapshot(root, taskID, data); err != nil {
					return err
				}
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, data) || bytes.Equal(got, prior) {
					t.Fatalf("publication did not replace prior bytes: %s, %v", got, err)
				}
				published = true
				return failure
			}
			if _, err := continuity.CreateContinuation(context.Background(), request); !errors.Is(err, failure) || !published {
				t.Fatalf("launch = %v, published=%t", err, published)
			}
			got, err := os.ReadFile(path)
			if priorExists {
				if err != nil || !bytes.Equal(got, prior) {
					t.Fatalf("prior Working Snapshot not restored: %s, %v", got, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("uncommitted Working Snapshot remains: %s, %v", got, err)
			}
			for _, table := range []string{"task_continuations", "blackboard_v2_continuation_pins", "blackboard_v2_continuation_state"} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("launch rows in %s = %d, %v", table, count, err)
				}
			}
		})
	}
}

func newPublicationErrorFixture(t *testing.T) (*store.DB, *ContinuityService, ContinuationLaunchRequest) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "launch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	owner, err := projects.Create("Publication rollback", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := runtimeprofile.NewService(db).Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	tasks := task.NewService(db, projects)
	created, err := tasks.Create(task.CreateRequest{ProjectID: owner.ID, Type: task.TypePentest, Goal: "Test publication", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	return db, NewContinuityService(db, NewService(db), tasks, t.TempDir()), ContinuationLaunchRequest{
		ProjectID: owner.ID, TaskID: created.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "test"},
	}
}
