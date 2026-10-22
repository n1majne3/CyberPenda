package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/store"
)

func TestOpenCreatesBaselineSchemaAndEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open empty store: %v", err)
	}
	epoch, err := db.CanonicalStore()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read epoch: %v", err)
	}
	if epoch != store.CanonicalStoreBlackboardV2 {
		_ = db.Close()
		t.Fatalf("epoch = %q, want %q", epoch, store.CanonicalStoreBlackboardV2)
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		_ = db.Close()
		t.Fatalf("count baseline marker: %v", err)
	}
	if applied != 1 {
		_ = db.Close()
		t.Fatalf("baseline marker count = %d, want 1", applied)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen baseline store: %v", err)
	}
	defer reopened.Close()
	if epoch, err := reopened.CanonicalStore(); err != nil || epoch != store.CanonicalStoreBlackboardV2 {
		t.Fatalf("reopened epoch = %q, err = %v", epoch, err)
	}
}

func TestOpenRefusesSchemaWithoutBaselineMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY)`); err != nil {
		_ = legacy.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy fixture: %v", err)
	}

	if _, err := store.Open(path); !errors.Is(err, store.ErrUnsupportedStoreSchema) {
		t.Fatalf("open unsupported schema error = %v, want ErrUnsupportedStoreSchema", err)
	}

	after, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen legacy fixture: %v", err)
	}
	defer after.Close()
	var tables int
	if err := after.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&tables); err != nil {
		t.Fatalf("inspect legacy schema after refusal: %v", err)
	}
	if tables != 1 {
		t.Fatalf("refusal changed the legacy schema, projects table count = %d", tables)
	}
}

func TestOpenRefusesUnknownStoreEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown-epoch.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open baseline store: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store='graph_v1' WHERE id=1`); err != nil {
		_ = db.Close()
		t.Fatalf("set unknown epoch: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close baseline store: %v", err)
	}

	if _, err := store.Open(path); !errors.Is(err, store.ErrUnsupportedStoreSchema) {
		t.Fatalf("open unknown epoch error = %v, want ErrUnsupportedStoreSchema", err)
	}
}
