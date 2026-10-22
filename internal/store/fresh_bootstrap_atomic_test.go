package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestFreshBootstrapFailureRollsBackAndPublicOpenRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh-bootstrap.db")
	dsn, err := buildDSN(path)
	if err != nil {
		t.Fatalf("build fresh bootstrap DSN: %v", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open fresh bootstrap fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("ping fresh bootstrap fixture: %v", err)
	}

	if err := createBaselineSchema(db, []string{
		`CREATE TABLE probe_ok (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE broken (`,
	}); err == nil {
		_ = db.Close()
		t.Fatal("fresh bootstrap accepted an invalid statement sequence")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close failed fresh bootstrap: %v", err)
	}

	inspection, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("inspect failed fresh bootstrap: %v", err)
	}
	var schemaObjects int
	if err := inspection.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE lower(name) NOT GLOB 'sqlite_*'`).Scan(&schemaObjects); err != nil {
		_ = inspection.Close()
		t.Fatalf("count schema objects after failed fresh bootstrap: %v", err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatalf("close failed-bootstrap inspection: %v", err)
	}
	if schemaObjects != 0 {
		t.Fatalf("failed fresh bootstrap left %d user schema objects, want none", schemaObjects)
	}

	opened, err := Open(path)
	if err != nil {
		t.Fatalf("retry public Open after failed fresh bootstrap: %v", err)
	}
	epoch, err := opened.CanonicalStore()
	if err != nil {
		_ = opened.Close()
		t.Fatalf("read retried bootstrap epoch: %v", err)
	}
	if epoch != CanonicalStoreBlackboardV2 {
		_ = opened.Close()
		t.Fatalf("retried bootstrap epoch = %q, want blackboard_v2", epoch)
	}
	var version int
	var name string
	if err := opened.QueryRow(`SELECT version, name FROM schema_migrations`).Scan(&version, &name); err != nil {
		_ = opened.Close()
		t.Fatalf("read retried baseline marker: %v", err)
	}
	if version != baselineSchemaVersion || name != baselineSchemaName {
		_ = opened.Close()
		t.Fatalf("retried baseline marker = %d %q, want %d %q", version, name, baselineSchemaVersion, baselineSchemaName)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("close retried Store: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("deterministically reopen retried Store: %v", err)
	}
	defer reopened.Close()
	reopenedEpoch, err := reopened.CanonicalStore()
	if err != nil {
		t.Fatalf("read deterministic reopen epoch: %v", err)
	}
	if reopenedEpoch != CanonicalStoreBlackboardV2 {
		t.Fatalf("deterministic reopen epoch = %q, want blackboard_v2", reopenedEpoch)
	}
}
