package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pentest/internal/store"
)

// TestOpenBootstrapsSchemaIdempotently guards against re-running the baseline
// bootstrap on an existing database failing.
func TestOpenBootstrapsSchemaIdempotently(t *testing.T) {
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

// TestOpenRestrictsDatabaseFilePermissions pins issue #160: the database stores
// plaintext "literal" credential secrets in credential_bindings.source_json, so
// the main file and its WAL/SHM sidecars must be owner-only (0600) rather than
// the umask default (typically 0644) that other local OS users could read.
func TestOpenRestrictsDatabaseFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX owner-only permission bits (0600) do not exist on Windows")
	}
	path := filepath.Join(t.TempDir(), "pentest.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, suffix := range []string{"", "-wal", "-shm"} {
		candidate := path + suffix
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) && suffix != "" {
				continue // sidecar not present; nothing to assert
			}
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("expected %s mode 0600, got %04o", filepath.Base(candidate), mode)
		}
	}
}

func TestOpenActiveV2StoreDoesNotRetainRemovedWorkflowState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "contracted.db"))
	if err != nil {
		t.Fatalf("open contracted store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if tableExists(t, db.DB, "task_summary_versions") {
		t.Fatal("active v2 store retains removed task_summary_versions table")
	}
	for _, column := range []string{
		"blackboard_finish_summary_version_id",
		"blackboard_finish_graph_revision",
		"blackboard_finish_mutation_sequence",
		"blackboard_finished_at",
	} {
		if columnExists(t, db.DB, "task_continuations", column) {
			t.Fatalf("active v2 store retains removed task_continuations.%s column", column)
		}
	}
}

// TestOpenDefaultsCanonicalStoreToBlackboardV2 records the accepted epoch on
// every fresh database while proving no v1 table is dropped during bootstrap.
func TestOpenDefaultsCanonicalStoreToBlackboardV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatalf("CanonicalStore: %v", err)
	}
	if epoch != store.CanonicalStoreBlackboardV2 {
		t.Fatalf("canonical store: got %q want %q", epoch, store.CanonicalStoreBlackboardV2)
	}

	// A fresh active store contains only v2 semantic state and trusted control
	// records; the historical v1 graph ledger is not installed.
	for _, table := range []string{
		"schema_migrations",
		"blackboard_store_state",
		"blackboard_read_state",
		"blackboard_v2_project_state",
		"blackboard_v2_records",
		"blackboard_v2_record_history",
		"blackboard_v2_relationships",
		"blackboard_v2_relationship_history",
		"blackboard_v2_idempotency_receipts",
		"blackboard_v2_attempt_origins",
		"blackboard_v2_operator_attempt_origins",
		"blackboard_v2_operator_evidence_origins",
		"blackboard_v2_operator_evidence_requests",
		"blackboard_v2_evidence_requests",
		"blackboard_v2_evidence_payloads",
		"blackboard_v2_key_redirects",
	} {
		if !tableExists(t, db.DB, table) {
			t.Fatalf("expected table %s", table)
		}
	}
	for _, table := range []string{"blackboard_graph_mutations", "blackboard_nodes", "blackboard_migration_runs"} {
		if tableExists(t, db.DB, table) {
			t.Fatalf("retired table %s remains", table)
		}
	}

	// Trusted runtime and v2 evidence columns are present.
	for _, col := range []struct {
		table, column string
	}{
		{"projects", "kind"},
		{"task_events", "continuation_id"},
		{"task_events", "attempt_node_id"},
		{"task_continuations", "runtime_config_version_id"},
		{"task_continuations", "blackboard_reconciliation_status"},
		{"task_continuations", "blackboard_reconciliation_mutation_id"},
		{"task_continuations", "blackboard_reconciled_at"},
		{"blackboard_v2_idempotency_receipts", "continuation_id"},
		{"blackboard_v2_evidence_requests", "temp_internal_path"},
		{"blackboard_v2_evidence_requests", "publisher_token"},
		{"blackboard_v2_evidence_requests", "publisher_temp_identity"},
		{"blackboard_v2_evidence_requests", "previous_temp_internal_path"},
	} {
		if !columnExists(t, db.DB, col.table, col.column) {
			t.Fatalf("expected column %s.%s", col.table, col.column)
		}
	}
}

// TestTransactionConnectionReportsRequiredPragmas proves every transaction
// connection carries the storage-contract PRAGMAs and immediate lock mode.
func TestTransactionConnectionReportsRequiredPragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var foreignKeys int
	if err := tx.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys: got %d want 1", foreignKeys)
	}

	var busyTimeout int
	if err := tx.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busyTimeout < 5000 {
		t.Fatalf("busy_timeout: got %d want >= 5000", busyTimeout)
	}

	var synchronous int
	if err := tx.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	// FULL == 2
	if synchronous != 2 {
		t.Fatalf("synchronous: got %d want 2 (FULL)", synchronous)
	}

	var journalMode string
	if err := tx.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode: got %q want wal", journalMode)
	}

	// Immediate lock: a second connection must not acquire an exclusive write
	// lock while this transaction holds the reserved write lock without having
	// written yet (DEFERRED would still allow it until first write).
	second, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(100)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	second.SetMaxOpenConns(1)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		tx2, err := second.BeginTx(ctx, nil)
		if err != nil {
			done <- err
			return
		}
		_ = tx2.Rollback()
		done <- nil
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected second IMMEDIATE begin to block/fail while first transaction holds write lock")
		}
	case <-time.After(2 * time.Second):
		// Blocked past timeout is also proof of immediate locking.
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&n)
	if err != nil {
		t.Fatalf("tableExists %s: %v", name, err)
	}
	return n == 1
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return false
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if n != want {
		t.Fatalf("%s count: got %d want %d", table, n, want)
	}
}

func TestBlackboardReadCursorSecretIsDatabaseSpecificAndPersistsAcrossReopen(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first.db")
	first, err := store.Open(firstPath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	var before []byte
	if err := first.QueryRow(`SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&before); err != nil {
		t.Fatalf("read first cursor secret: %v", err)
	}
	if len(before) != 32 {
		t.Fatalf("cursor secret length = %d want 32", len(before))
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	first, err = store.Open(firstPath)
	if err != nil {
		t.Fatalf("reopen first store: %v", err)
	}
	defer first.Close()
	var after []byte
	if err := first.QueryRow(`SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&after); err != nil {
		t.Fatalf("read persisted cursor secret: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cursor secret changed across reopen")
	}

	second, err := store.Open(filepath.Join(t.TempDir(), "second.db"))
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer second.Close()
	var other []byte
	if err := second.QueryRow(`SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&other); err != nil {
		t.Fatalf("read second cursor secret: %v", err)
	}
	if bytes.Equal(before, other) {
		t.Fatal("independent databases share the same cursor secret")
	}
}
