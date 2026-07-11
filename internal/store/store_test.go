package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestOpenRejectsMigrationChecksumDriftWithoutChangingLegacyBlackboard is the
// C01 first red test: an applied migration whose checksum no longer matches the
// embedded definition fails closed, and legacy Blackboard rows stay untouched.
func TestOpenRejectsMigrationChecksumDriftWithoutChangingLegacyBlackboard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const (
		projectID = "proj-1"
		factKey   = "legacy-fact"
		summary   = "must-survive-checksum-drift"
	)
	_, err = db.Exec(`INSERT INTO projects (id, name, description, scope_json, defaults_json, created_at, updated_at)
		VALUES (?, ?, '', '{}', '{}', ?, ?)`,
		projectID, "P", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	_, err = db.Exec(`INSERT INTO project_facts (
		id, project_id, fact_key, category, summary, body, confidence, scope_status, created_at, updated_at
	) VALUES (?, ?, ?, '', ?, '', 'tentative', '', ?, ?)`,
		"fact-1", projectID, factKey, summary, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	res, err := raw.Exec(`UPDATE schema_migrations SET checksum = ? WHERE version = 1`, "deadbeef-checksum-drift")
	if err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("rows affected: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected to tamper exactly one migration row, got %d", n)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	_, err = store.Open(path)
	if err == nil {
		t.Fatal("expected Open to reject migration checksum drift")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Fatalf("expected checksum error, got: %v", err)
	}

	// Fail-closed must not rewrite or drop legacy Blackboard rows.
	verify, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open verify: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	var gotSummary string
	err = verify.QueryRow(
		`SELECT summary FROM project_facts WHERE project_id = ? AND fact_key = ?`,
		projectID, factKey,
	).Scan(&gotSummary)
	if err != nil {
		t.Fatalf("legacy fact after failed open: %v", err)
	}
	if gotSummary != summary {
		t.Fatalf("legacy fact changed: got %q want %q", gotSummary, summary)
	}
}

// TestOpenRejectsUnknownNewerSchemaVersion fails closed when the database has
// applied a migration version the running binary does not know.
func TestOpenRejectsUnknownNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	_, err = raw.Exec(
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		9999, "future", "abc", time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	_, err = store.Open(path)
	if err == nil {
		t.Fatal("expected Open to reject unknown newer schema version")
	}
	errText := strings.ToLower(err.Error())
	if !strings.Contains(errText, "newer") && !strings.Contains(errText, "unknown") {
		t.Fatalf("expected unknown/newer schema error, got: %v", err)
	}
}

// TestOpenDefaultsCanonicalStoreToLegacyV1 records the store epoch on every
// database without changing public Fact/Finding/Evidence ownership.
func TestOpenDefaultsCanonicalStoreToLegacyV1(t *testing.T) {
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
	if epoch != store.CanonicalStoreLegacyV1 {
		t.Fatalf("canonical store: got %q want %q", epoch, store.CanonicalStoreLegacyV1)
	}

	// Control tables and the C02 graph ledger core now exist (the full graph
	// schema lands across C03+ slices). Production graph writes stay dark
	// while the store epoch is legacy_v1.
	for _, table := range []string{
		"schema_migrations",
		"blackboard_store_state",
		"blackboard_migration_runs",
		"blackboard_legacy_mappings",
		"blackboard_graph_mutations",
		"blackboard_nodes",
		"blackboard_node_versions",
		"blackboard_key_events",
		"blackboard_key_registry",
		"blackboard_node_heads",
		"blackboard_graph_state",
		"blackboard_edges",
		"blackboard_edge_versions",
		"blackboard_edge_heads",
	} {
		if !tableExists(t, db.DB, table) {
			t.Fatalf("expected table %s", table)
		}
	}
	// C10 owns the rebuildable projection cache, append-only maintenance manifests,
	// and derived Blackboard Health persistence.
	for _, table := range []string{
		"blackboard_compactions",
		"blackboard_restore_manifests",
		"blackboard_projection_metrics",
		"blackboard_health_runs",
		"blackboard_health_results",
	} {
		if !tableExists(t, db.DB, table) {
			t.Fatalf("expected C10 graph table %s", table)
		}
	}

	// Graph-support surrounding columns are present.
	for _, col := range []struct {
		table, column string
	}{
		{"projects", "kind"},
		{"task_events", "continuation_id"},
		{"task_events", "attempt_node_id"},
		{"task_summary_versions", "continuation_id"},
		{"task_continuations", "runtime_config_version_id"},
		{"task_continuations", "blackboard_graph_revision"},
		{"task_continuations", "blackboard_renderer_version"},
		{"task_continuations", "blackboard_estimator_version"},
		{"task_continuations", "blackboard_projection_hash"},
		{"task_continuations", "blackboard_projection_bytes"},
		{"task_continuations", "blackboard_projection_estimated_tokens"},
		{"task_continuations", "blackboard_reconciliation_status"},
		{"task_continuations", "blackboard_reconciliation_mutation_id"},
		{"task_continuations", "blackboard_reconciled_at"},
	} {
		if !columnExists(t, db.DB, col.table, col.column) {
			t.Fatalf("expected column %s.%s", col.table, col.column)
		}
	}
}

// TestOpenPreservesEveryRowFromOlderDatabase upgrades a pre-migration database
// without losing existing project or Blackboard rows.
func TestOpenPreservesEveryRowFromOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	stmts := []string{
		`CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			scope_json TEXT NOT NULL,
			defaults_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE project_facts (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			fact_key TEXT NOT NULL,
			category TEXT NOT NULL,
			summary TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			confidence TEXT NOT NULL,
			scope_status TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (project_id, fact_key)
		);`,
		`CREATE TABLE findings (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			finding_key TEXT NOT NULL,
			version INTEGER NOT NULL,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			target TEXT NOT NULL DEFAULT '',
			proof TEXT NOT NULL DEFAULT '',
			impact TEXT NOT NULL DEFAULT '',
			recommendation TEXT NOT NULL DEFAULT '',
			cvss_version TEXT NOT NULL DEFAULT '',
			cvss_vector TEXT NOT NULL DEFAULT '',
			cvss_pending INTEGER NOT NULL DEFAULT 1,
			severity TEXT NOT NULL DEFAULT 'pending',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (project_id, finding_key)
		);`,
		`CREATE TABLE evidence_artifacts (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			evidence_key TEXT NOT NULL,
			attach_to_type TEXT NOT NULL,
			attach_to_key TEXT NOT NULL,
			artifact_type TEXT NOT NULL,
			source_path TEXT NOT NULL DEFAULT '',
			managed_path TEXT NOT NULL,
			sha256 TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (project_id, evidence_key)
		);`,
	}
	for _, s := range stmts {
		if _, err := legacy.Exec(s); err != nil {
			t.Fatalf("legacy ddl: %v", err)
		}
	}
	_, err = legacy.Exec(`INSERT INTO projects (id, name, description, scope_json, defaults_json, created_at, updated_at)
		VALUES ('p1', 'Old', '', '{}', '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO project_facts (
		id, project_id, fact_key, category, summary, body, confidence, scope_status, created_at, updated_at
	) VALUES ('f1', 'p1', 'k1', 'cat', 'sum', 'body', 'confirmed', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO findings (
		id, project_id, finding_key, version, title, description, status, created_at, updated_at
	) VALUES ('n1', 'p1', 'fk1', 1, 'title', '', 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert finding: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO evidence_artifacts (
		id, project_id, evidence_key, attach_to_type, attach_to_key, artifact_type, managed_path, created_at, updated_at
	) VALUES ('e1', 'p1', 'ev1', 'fact', 'k1', 'note', '/a', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	assertCount(t, db.DB, "projects", 1)
	assertCount(t, db.DB, "project_facts", 1)
	assertCount(t, db.DB, "findings", 1)
	assertCount(t, db.DB, "evidence_artifacts", 1)

	var kind string
	if err := db.QueryRow(`SELECT kind FROM projects WHERE id = 'p1'`).Scan(&kind); err != nil {
		t.Fatalf("project kind: %v", err)
	}
	if kind != "pentest" {
		t.Fatalf("legacy project kind: got %q want pentest", kind)
	}
	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatalf("CanonicalStore: %v", err)
	}
	if epoch != store.CanonicalStoreLegacyV1 {
		t.Fatalf("canonical store after upgrade: got %q", epoch)
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
