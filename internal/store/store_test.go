package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"pentest/internal/store"
)

// TestOpenRunsMigrationsIdempotently guards against re-running migrations on an
// existing database failing.
func TestOpenRunsMigrationsIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("second open (migration rerun): %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Fatalf("second close: %v", err)
		}
	})

	if err := second.Ping(); err != nil {
		t.Fatalf("ping after reopen: %v", err)
	}
}

// TestEnsureColumnUpgradesLegacyDatabase simulates a database created before the
// defaults_json column existed and checks that Open adds it without data loss.
func TestEnsureColumnUpgradesLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	// Build a legacy schema that predates the defaults_json column and insert a
	// row the way the very first migration would have.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	_, err = legacy.Exec(`CREATE TABLE projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		scope_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO projects (id, name, description, scope_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"legacy-1", "Legacy", "", "{}", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Reopen through store.Open; migration should add defaults_json and keep
	// the existing row readable.
	upgraded, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade open: %v", err)
	}
	t.Cleanup(func() {
		if err := upgraded.Close(); err != nil {
			t.Fatalf("close upgraded db: %v", err)
		}
	})

	var id, defaultsJSON string
	err = upgraded.QueryRow("SELECT id, defaults_json FROM projects WHERE id = ?", "legacy-1").Scan(&id, &defaultsJSON)
	if err != nil {
		t.Fatalf("read legacy row after upgrade: %v", err)
	}
	if id != "legacy-1" {
		t.Fatalf("expected legacy-1, got %q", id)
	}
	if defaultsJSON != "{}" {
		t.Fatalf("expected default defaults_json {}, got %q", defaultsJSON)
	}
}
