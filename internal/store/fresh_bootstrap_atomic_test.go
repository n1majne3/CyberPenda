package store

import (
	"database/sql"
	"errors"
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

	definitions := migrations()
	injected := errors.New("injected failure after migrations 1 and 2")
	definitions[2].up = func(*sql.Tx) error { return injected }
	if err := bootstrapFresh(db, definitions); !errors.Is(err, injected) {
		_ = db.Close()
		t.Fatalf("fresh bootstrap error = %v, want injected migration failure", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close failed fresh bootstrap: %v", err)
	}

	inspection, err := openReadOnlyInspection(path)
	if err != nil {
		t.Fatalf("inspect failed fresh bootstrap: %v", err)
	}
	var schemaObjects int
	if err := inspection.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE lower(name) NOT GLOB 'sqlite_*'`).Scan(&schemaObjects); err != nil {
		_ = inspection.Close()
		t.Fatalf("count schema objects after failed fresh bootstrap: %v", err)
	}
	if schemaObjects != 0 {
		_ = inspection.Close()
		t.Fatalf("failed fresh bootstrap left %d user schema objects, want none", schemaObjects)
	}
	if err := inspection.Close(); err != nil {
		t.Fatalf("close failed-bootstrap inspection: %v", err)
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
	if err := ValidateMigrationHistory(t.Context(), opened.DB); err != nil {
		_ = opened.Close()
		t.Fatalf("validate retried migration history: %v", err)
	}
	var applied int
	if err := opened.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		_ = opened.Close()
		t.Fatalf("count retried migrations: %v", err)
	}
	if applied != len(migrations()) {
		_ = opened.Close()
		t.Fatalf("retried migration count = %d, want %d", applied, len(migrations()))
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
