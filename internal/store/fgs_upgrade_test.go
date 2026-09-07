package store_test

import (
	"path/filepath"
	"testing"

	"pentest/internal/store"
)

func TestUpgradeExistingOwnersToFGSPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects (id,name,scope_json,created_at,updated_at,blackboard_protocol) VALUES ('old','Existing','{}','now','now','legacy')`,
		`INSERT INTO sessions (id,title,lifecycle,workdir,blackboard_mode,created_at,updated_at,last_activity_at,blackboard_protocol) VALUES ('old','Existing','open','/tmp/old','disabled','now','now','now','legacy')`,
		`INSERT INTO session_events (id,session_id,seq,kind,payload_json,created_at) VALUES ('kept','old',1,'conversation','{"text":"keep history"}','now')`,
		`DELETE FROM schema_migrations WHERE version=77`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		db, err = store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"projects", "sessions"} {
			var protocol string
			if err := db.QueryRow("SELECT blackboard_protocol FROM " + table + " WHERE id='old'").Scan(&protocol); err != nil {
				t.Fatal(err)
			}
			if protocol != "fgs" {
				t.Errorf("%s protocol = %q, want fgs", table, protocol)
			}
		}
		var mode, history string
		if err := db.QueryRow(`SELECT blackboard_mode FROM sessions WHERE id='old'`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT payload_json FROM session_events WHERE id='kept'`).Scan(&history); err != nil {
			t.Fatal(err)
		}
		if mode != "disabled" || history != `{"text":"keep history"}` {
			t.Fatalf("changed mode or history: %q %q", mode, history)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
