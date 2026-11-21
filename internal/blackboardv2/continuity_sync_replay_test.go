package blackboardv2

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/store"
)

func TestFinalizedSyncReplayPublishesOnlyForCurrentLiveOwner(t *testing.T) {
	for _, state := range []string{"pending", "running", "paused", "completed", "failed", "stopped", "interrupted", "newer", "foreign-newer", "missing"} {
		t.Run(state, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)
			for _, query := range []string{
				`CREATE TABLE task_continuations (id TEXT, task_id TEXT, status TEXT, number INTEGER)`,
				`CREATE TABLE blackboard_v2_continuation_state (continuation_id TEXT, working_snapshot_bytes BLOB)`,
				`CREATE TABLE blackboard_v2_sync_delivery_receipts (continuation_id TEXT, request_fingerprint TEXT, status TEXT, from_revision INTEGER, attachment_json TEXT)`,
				`INSERT INTO task_continuations VALUES ('old','task','running',1)`,
				`INSERT INTO blackboard_v2_continuation_state VALUES ('old','{"revision":1}')`,
				`INSERT INTO blackboard_v2_sync_delivery_receipts VALUES ('old','fingerprint','finalized',0,'{"revision":1}')`,
			} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			live := state == "pending" || state == "running" || state == "paused" || state == "foreign-newer"
			switch state {
			case "newer", "foreign-newer":
				taskID := "task"
				if state == "foreign-newer" {
					taskID = "other-task"
				}
				if _, err := db.Exec(`INSERT INTO task_continuations VALUES ('next',?,'running',2)`, taskID); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if _, err := db.Exec(`DELETE FROM task_continuations`); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := db.Exec(`UPDATE task_continuations SET status=?`, state); err != nil {
					t.Fatal(err)
				}
			}
			service := NewService(&store.DB{DB: db})
			service.runtimeRoot = t.TempDir()
			prior := []byte(`{"revision":2}`)
			if err := materializeWorkingSnapshot(service.runtimeRoot, "task", prior); err != nil {
				t.Fatal(err)
			}
			attachment, err := service.finalizeClaimedSyncDelivery(context.Background(), "project", "task", "old", "fingerprint", 0, true)
			if err != nil || attachment == nil || attachment.Revision != 1 {
				t.Fatalf("finalized replay = %#v, %v", attachment, err)
			}
			got, err := os.ReadFile(filepath.Join(service.runtimeRoot, "task", "workdir", ".pentest", "blackboard.json"))
			want := prior
			if live {
				want = []byte(`{"revision":1}`)
			}
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Working Snapshot = %s, %v; want %s", got, err, want)
			}
		})
	}
}
