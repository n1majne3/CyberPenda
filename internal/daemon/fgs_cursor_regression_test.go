package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/fgs"
	"pentest/internal/project"
	"pentest/internal/task"
)

func TestFGSReceiverAdvancesPastDeletedOwnerPage(t *testing.T) {
	root := t.TempDir()
	s, err := NewServer(Config{DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.fgsCancel()
	s.fgsWG.Wait()
	p, err := s.projects.Create("FGS cursor", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := s.tasks.Create(task.CreateRequest{ProjectID: p.ID, Type: task.TypePentest, Goal: "Old work", Runner: task.RunnerHost, RunControls: task.RunControls{BlackboardMode: task.BlackboardModeWorkingGraph}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.tasks.UpdateStatus(deleted.ID, task.StatusStopped); err != nil {
		t.Fatal(err)
	}
	if err := s.tasks.Delete(deleted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.tasks.Get(deleted.ID); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("deleted Task lookup=%v", err)
	}
	for i := 0; i < 64; i++ {
		_, err := s.db.Exec(`INSERT INTO task_continuations (id,task_id,number,runtime_profile_id,runtime_provider,runner,status,started_at,updated_at,ended_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("deleted-%03d", i), deleted.ID, i+1, "profile", "codex", "host", "stopped", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
	}
	found, err := s.tasks.Create(task.CreateRequest{ProjectID: p.ID, Type: task.TypePentest, Goal: "New work", Runner: task.RunnerHost, RunControls: task.RunControls{BlackboardMode: task.BlackboardModeWorkingGraph}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.tasks.CreateContinuation(found.ID, "profile", "codex", task.RunnerHost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE task_continuations SET id=? WHERE id=?`, "zz-valid", c.ID); err != nil {
		t.Fatal(err)
	}
	c.ID = "zz-valid"
	var firstValid string
	if err := s.db.QueryRow(`SELECT id FROM task_continuations ORDER BY id LIMIT 1 OFFSET 64`).Scan(&firstValid); err != nil {
		t.Fatal(err)
	}
	if firstValid != c.ID {
		t.Fatalf("65th binding=%q, want %q", firstValid, c.ID)
	}
	workdir := filepath.Join(s.runtimeRoot, found.ID, "workdir")
	if err := os.MkdirAll(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	contract := found.OwnerContract(workdir)
	update, err := fgs.Emit(t.Context(), contract, c.ID, []fgs.Operation{{Op: "goal.create", Key: "goal:after-deleted", Title: "New work", SuccessCriteria: "Received after the deleted owner page"}})
	if err != nil {
		t.Fatal(err)
	}
	s.startFGSReceiver()
	deadline := time.Now().Add(4 * time.Second)
	for {
		receipt, err := s.fgs.Receipt(t.Context(), contract, c.ID, update.ID)
		if err == nil {
			if receipt.State != "applied" {
				t.Fatalf("receipt=%+v", receipt)
			}
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("valid owner has no durable receipt after 64 deleted-owner bindings: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
