package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigration70RenamesAndMigratesBlackboardModes(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration70.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	for _, statement := range []string{
		`CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			blackboard_conclusion_mode TEXT NOT NULL DEFAULT 'interactive'
				CHECK (blackboard_conclusion_mode IN ('interactive', 'assisted', 'disabled'))
		)`,
		`CREATE TABLE tasks (id TEXT PRIMARY KEY, run_controls_json TEXT NOT NULL DEFAULT '{}')`,
		`INSERT INTO sessions (id,blackboard_conclusion_mode) VALUES
			('session-assisted','assisted'),
			('session-interactive','interactive')`,
		`INSERT INTO tasks (id,run_controls_json) VALUES
			('task-assisted','{"blackboard_conclusion_mode":"assisted","notes":"keep"}'),
			('task-missing','{"notes":"keep"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture statement failed: %v", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migration70Up(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("migration70Up() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if has, err := storeDBHasColumn(db, "sessions", "blackboard_mode"); err != nil || !has {
		t.Fatalf("sessions.blackboard_mode exists = %v, err=%v", has, err)
	}
	if has, err := storeDBHasColumn(db, "sessions", "blackboard_conclusion_mode"); err != nil || has {
		t.Fatalf("legacy Session column exists = %v, err=%v", has, err)
	}

	var assistedMode, interactiveMode string
	if err := db.QueryRow(`SELECT blackboard_mode FROM sessions WHERE id='session-assisted'`).Scan(&assistedMode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT blackboard_mode FROM sessions WHERE id='session-interactive'`).Scan(&interactiveMode); err != nil {
		t.Fatal(err)
	}
	if assistedMode != "working_graph" || interactiveMode != "interactive" {
		t.Fatalf("migrated Session modes = assisted:%q interactive:%q", assistedMode, interactiveMode)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id) VALUES ('session-default')`); err != nil {
		t.Fatal(err)
	}
	var defaultMode string
	if err := db.QueryRow(`SELECT blackboard_mode FROM sessions WHERE id='session-default'`).Scan(&defaultMode); err != nil {
		t.Fatal(err)
	}
	if defaultMode != "disabled" {
		t.Fatalf("new Session default = %q, want disabled", defaultMode)
	}

	var taskMode, notes string
	var legacyKeyType sql.NullString
	if err := db.QueryRow(`SELECT json_extract(run_controls_json,'$.blackboard_mode'), json_type(run_controls_json,'$.blackboard_conclusion_mode'), json_extract(run_controls_json,'$.notes') FROM tasks WHERE id='task-assisted'`).Scan(&taskMode, &legacyKeyType, &notes); err != nil {
		t.Fatal(err)
	}
	if taskMode != "working_graph" || legacyKeyType.Valid || notes != "keep" {
		t.Fatalf("migrated assisted Task = mode:%q legacy:%#v notes:%q", taskMode, legacyKeyType, notes)
	}
	if err := db.QueryRow(`SELECT json_extract(run_controls_json,'$.blackboard_mode') FROM tasks WHERE id='task-missing'`).Scan(&taskMode); err != nil {
		t.Fatal(err)
	}
	if taskMode != "working_graph" {
		t.Fatalf("missing Task mode migrated to %q, want working_graph", taskMode)
	}
}

func TestMigration70IsIdempotentForRepairedCurrentSchema(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration70-current.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			blackboard_mode TEXT NOT NULL DEFAULT 'disabled'
				CHECK (blackboard_mode IN ('interactive', 'working_graph', 'disabled'))
		)`,
		`CREATE TABLE tasks (id TEXT PRIMARY KEY, run_controls_json TEXT NOT NULL DEFAULT '{}')`,
		`INSERT INTO sessions (id,blackboard_mode) VALUES ('session-current','working_graph')`,
		`INSERT INTO tasks (id,run_controls_json) VALUES ('task-current','{"blackboard_mode":"interactive"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migration70Up(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`SELECT json_extract(run_controls_json,'$.blackboard_mode') FROM tasks WHERE id='task-current'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "interactive" {
		t.Fatalf("current Task mode changed to %q", mode)
	}
}
