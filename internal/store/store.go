// Package store owns the SQLite connection and schema migrations shared by
// every domain package. Domain repositories receive the opened database and
// keep their business logic separate from transport concerns so HTTP, MCP, and
// CLI handlers can all call the same services.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// memDBSeq makes each empty-path Open use an isolated in-memory database.
var memDBSeq atomic.Uint64

// Canonical store epochs for one SQLite database. Exactly one is active.
const (
	CanonicalStoreLegacyV1         = "legacy_v1"
	CanonicalStoreGraphV1          = "graph_v1"
	CanonicalStoreGraphV1Finalized = "graph_v1_finalized"
)

// DB wraps a SQLite connection with the daemon's schema applied.
type DB struct {
	*sql.DB
}

// Open connects to the SQLite database at path and runs numbered migrations.
// An empty path uses an in-memory database, which is handy for tests.
func Open(path string) (*DB, error) {
	dsn, err := buildDSN(path)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &DB{db}, nil
}

// CanonicalStore returns the global store epoch for this database.
func (db *DB) CanonicalStore() (string, error) {
	var epoch string
	err := db.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id = 1`).Scan(&epoch)
	if err != nil {
		return "", fmt.Errorf("read canonical store epoch: %w", err)
	}
	return epoch, nil
}

func buildDSN(path string) (string, error) {
	values := url.Values{}
	values.Add("_pragma", "busy_timeout(5000)")
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "synchronous(FULL)")
	values.Set("_txlock", "immediate")

	if path == "" || path == ":memory:" {
		// Unique shared-memory name per Open so concurrent tests stay isolated
		// while still allowing the pool to reopen the same image.
		name := "pentest_mem_" + strconv.FormatUint(memDBSeq.Add(1), 10)
		return "file:" + name + "?mode=memory&cache=shared&" + values.Encode(), nil
	}

	values.Add("_pragma", "journal_mode(WAL)")

	// Prefer a file URI so query parameters always attach cleanly. Absolute
	// paths become file:/abs/path; relative paths become file:rel/path.
	escaped := url.PathEscape(path)
	// PathEscape encodes slashes; restore them for a usable filesystem path.
	escaped = strings.ReplaceAll(escaped, "%2F", "/")
	return "file:" + escaped + "?" + values.Encode(), nil
}

type migration struct {
	version  int
	name     string
	checksum string
	up       func(tx *sql.Tx) error
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
	`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	applied, err := loadAppliedMigrations(db)
	if err != nil {
		return err
	}

	defs := migrations()
	byVersion := make(map[int]migration, len(defs))
	for _, m := range defs {
		byVersion[m.version] = m
	}

	for version, row := range applied {
		def, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("database schema is newer/unknown: applied migration version %d (%s) is not known to this binary", version, row.name)
		}
		if row.checksum != def.checksum {
			return fmt.Errorf("migration checksum mismatch for version %d (%s): database has %s, binary expects %s", version, def.name, row.checksum, def.checksum)
		}
	}

	for _, m := range defs {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}
	return nil
}

type appliedMigration struct {
	name     string
	checksum string
}

func loadAppliedMigrations(db *sql.DB) (map[int]appliedMigration, error) {
	rows, err := db.Query(`SELECT version, name, checksum FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return nil, err
		}
		out[version] = appliedMigration{name: name, checksum: checksum}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", m.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := m.up(tx); err != nil {
		return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
	}

	_, err = tx.Exec(
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.version, m.name, m.checksum, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.version, err)
	}
	return nil
}

func migrations() []migration {
	return []migration{
		newMigration(1, "baseline_legacy_schema", migration1BaselineSQL, migration1Up),
		newMigration(2, "store_epoch_and_graph_support", migration2SQL, migration2Up),
	}
}

func newMigration(version int, name, sqlBody string, up func(*sql.Tx) error) migration {
	sum := sha256.Sum256([]byte(sqlBody))
	return migration{
		version:  version,
		name:     name,
		checksum: hex.EncodeToString(sum[:]),
		up:       up,
	}
}

func execStatements(tx *sql.Tx, sqlBody string) error {
	// Split on semicolon-terminated statements. Migration SQL is static and
	// does not embed semicolons inside string literals.
	parts := strings.Split(sqlBody, ";")
	for _, part := range parts {
		stmt := strings.TrimSpace(part)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			// Skip empty fragments and pure-comment chunks.
			lines := strings.Split(stmt, "\n")
			onlyComments := true
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "--") {
					continue
				}
				onlyComments = false
				break
			}
			if onlyComments {
				continue
			}
		}
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", truncate(stmt, 80), err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ensureColumn adds a column when missing. Used for upgrading databases that
// already had CREATE TABLE IF NOT EXISTS without the newer columns.
func ensureColumn(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

const migration1BaselineSQL = `
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	scope_json TEXT NOT NULL,
	defaults_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_profiles (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	provider TEXT NOT NULL,
	fields_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS model_providers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	base_url TEXT NOT NULL,
	protocols_json TEXT NOT NULL DEFAULT '[]',
	catalog_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS skills (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	source_provenance_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS skill_profile_opt_outs (
	profile_id TEXT NOT NULL,
	skill_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (profile_id, skill_id)
);
CREATE TABLE IF NOT EXISTS credential_bindings (
	id TEXT PRIMARY KEY,
	credential_ref TEXT NOT NULL,
	scope TEXT NOT NULL,
	scope_id TEXT NOT NULL DEFAULT '',
	source_json TEXT NOT NULL,
	disabled INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (credential_ref, scope, scope_id)
);
CREATE TABLE IF NOT EXISTS tasks (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	goal TEXT NOT NULL,
	status TEXT NOT NULL,
	runner TEXT NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	run_controls_json TEXT NOT NULL,
	scope_snapshot_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_runtime_config_versions (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	config_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (task_id, version)
);
CREATE TABLE IF NOT EXISTS task_continuations (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	number INTEGER NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	runtime_provider TEXT NOT NULL,
	runner TEXT NOT NULL,
	status TEXT NOT NULL,
	container_id TEXT NOT NULL DEFAULT '',
	native_session_id TEXT NOT NULL DEFAULT '',
	native_session_path TEXT NOT NULL DEFAULT '',
	started_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	ended_at TEXT NOT NULL DEFAULT '',
	UNIQUE (task_id, number)
);
CREATE TABLE IF NOT EXISTS task_events (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	kind TEXT NOT NULL,
	payload_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	UNIQUE (task_id, seq)
);
CREATE TABLE IF NOT EXISTS task_summary_versions (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	summary TEXT NOT NULL,
	submitted_by TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	UNIQUE (task_id, version)
);
CREATE TABLE IF NOT EXISTS project_facts (
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
);
CREATE TABLE IF NOT EXISTS project_fact_versions (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	fact_key TEXT NOT NULL,
	version INTEGER NOT NULL,
	category TEXT NOT NULL,
	summary TEXT NOT NULL,
	body TEXT NOT NULL DEFAULT '',
	confidence TEXT NOT NULL,
	scope_status TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	UNIQUE (project_id, fact_key, version)
);
CREATE TABLE IF NOT EXISTS project_fact_relations (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	source_fact_key TEXT NOT NULL,
	target_fact_key TEXT NOT NULL,
	relation TEXT NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (project_id, source_fact_key, target_fact_key, relation)
);
CREATE TABLE IF NOT EXISTS fact_key_aliases (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	alias_fact_key TEXT NOT NULL,
	canon_fact_key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (project_id, alias_fact_key)
);
CREATE TABLE IF NOT EXISTS finding_key_aliases (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	alias_finding_key TEXT NOT NULL,
	canon_finding_key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (project_id, alias_finding_key)
);
CREATE TABLE IF NOT EXISTS findings (
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
);
CREATE TABLE IF NOT EXISTS finding_versions (
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
	UNIQUE (project_id, finding_key, version)
);
CREATE TABLE IF NOT EXISTS evidence_artifacts (
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
);
-- additive columns for pre-numbered legacy databases
-- projects.defaults_json TEXT NOT NULL DEFAULT '{}'
-- runtime_profiles.kind TEXT NOT NULL DEFAULT 'manual'
-- model_providers.endpoints_json TEXT NOT NULL DEFAULT '[]'
`

func migration1Up(tx *sql.Tx) error {
	if err := execStatements(tx, migration1BaselineSQL); err != nil {
		return err
	}
	if err := ensureColumn(tx, "projects", "defaults_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return fmt.Errorf("ensure projects.defaults_json: %w", err)
	}
	if err := ensureColumn(tx, "runtime_profiles", "kind", "TEXT NOT NULL DEFAULT 'manual'"); err != nil {
		return fmt.Errorf("ensure runtime_profiles.kind: %w", err)
	}
	if err := ensureColumn(tx, "model_providers", "endpoints_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return fmt.Errorf("ensure model_providers.endpoints_json: %w", err)
	}
	return nil
}

const migration2SQL = `
-- projects.kind TEXT NOT NULL DEFAULT 'pentest'
-- task_events.continuation_id TEXT
-- task_events.attempt_node_id TEXT
-- task_summary_versions.continuation_id TEXT
-- task_continuations.runtime_config_version_id TEXT
-- task_continuations.blackboard_graph_revision INTEGER
-- task_continuations.blackboard_renderer_version TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_estimator_version TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_projection_hash TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_projection_bytes INTEGER
-- task_continuations.blackboard_projection_estimated_tokens INTEGER
-- task_continuations.blackboard_reconciliation_status TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_reconciliation_mutation_id TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_reconciled_at TEXT NOT NULL DEFAULT ''
CREATE TABLE IF NOT EXISTS blackboard_store_state (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	canonical_store TEXT NOT NULL,
	cutover_state TEXT NOT NULL,
	migration_contract_version TEXT NOT NULL DEFAULT '',
	graph_schema_version INTEGER NOT NULL DEFAULT 0,
	cutover_id TEXT NOT NULL DEFAULT '',
	source_digest TEXT NOT NULL DEFAULT '',
	mapping_digest TEXT NOT NULL DEFAULT '',
	verified_backup_path TEXT NOT NULL DEFAULT '',
	verified_backup_sha256 TEXT NOT NULL DEFAULT '',
	cutover_application_version TEXT NOT NULL DEFAULT '',
	cutover_started_at TEXT NOT NULL DEFAULT '',
	cutover_committed_at TEXT NOT NULL DEFAULT '',
	post_cutover_write_committed INTEGER NOT NULL DEFAULT 0,
	latest_verification_at TEXT NOT NULL DEFAULT '',
	latest_verification_result_hash TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS blackboard_migration_runs (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	state TEXT NOT NULL,
	diagnostic_code TEXT NOT NULL DEFAULT '',
	message TEXT NOT NULL DEFAULT '',
	source_digest TEXT NOT NULL DEFAULT '',
	mapping_digest TEXT NOT NULL DEFAULT '',
	backup_path TEXT NOT NULL DEFAULT '',
	backup_sha256 TEXT NOT NULL DEFAULT '',
	counts_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	finished_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS blackboard_legacy_mappings (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	source_table TEXT NOT NULL,
	source_kind TEXT NOT NULL DEFAULT '',
	legacy_primary_id TEXT NOT NULL,
	original_stable_key TEXT NOT NULL DEFAULT '',
	original_version INTEGER,
	source_row_hash TEXT NOT NULL,
	target_kind TEXT NOT NULL DEFAULT '',
	target_id TEXT NOT NULL DEFAULT '',
	target_version INTEGER,
	mapping_status TEXT NOT NULL,
	compatibility_metadata_json TEXT NOT NULL DEFAULT '{}',
	migration_mutation_seq INTEGER,
	cutover_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_blackboard_legacy_mappings_project
	ON blackboard_legacy_mappings (project_id);
CREATE INDEX IF NOT EXISTS idx_blackboard_legacy_mappings_cutover
	ON blackboard_legacy_mappings (cutover_id);
INSERT INTO blackboard_store_state (
	id, canonical_store, cutover_state, migration_contract_version, graph_schema_version, updated_at
) VALUES (
	1, 'legacy_v1', 'legacy', 'legacy_blackboard_to_graph_v1', 0, '1970-01-01T00:00:00Z'
);
`

func migration2Up(tx *sql.Tx) error {
	// Surrounding schema required before graph cutover (storage contract §3).
	if err := ensureColumn(tx, "projects", "kind", "TEXT NOT NULL DEFAULT 'pentest'"); err != nil {
		return fmt.Errorf("ensure projects.kind: %w", err)
	}
	if err := ensureColumn(tx, "task_events", "continuation_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure task_events.continuation_id: %w", err)
	}
	if err := ensureColumn(tx, "task_events", "attempt_node_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure task_events.attempt_node_id: %w", err)
	}
	if err := ensureColumn(tx, "task_summary_versions", "continuation_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure task_summary_versions.continuation_id: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "runtime_config_version_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure task_continuations.runtime_config_version_id: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_graph_revision", "INTEGER"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_graph_revision: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_renderer_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_renderer_version: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_estimator_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_estimator_version: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_projection_hash", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_projection_hash: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_projection_bytes", "INTEGER"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_projection_bytes: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_projection_estimated_tokens", "INTEGER"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_projection_estimated_tokens: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_reconciliation_status", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_reconciliation_status: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_reconciliation_mutation_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_reconciliation_mutation_id: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_reconciled_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_reconciled_at: %w", err)
	}

	if err := execStatements(tx, migration2SQL); err != nil {
		return err
	}

	// Fresh databases insert the singleton via migration2SQL. Upgrades that
	// already have the table without a row still need the default epoch.
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM blackboard_store_state WHERE id = 1`).Scan(&n); err != nil {
		return fmt.Errorf("check store state: %w", err)
	}
	if n == 0 {
		_, err := tx.Exec(`
			INSERT INTO blackboard_store_state (
				id, canonical_store, cutover_state, migration_contract_version, graph_schema_version, updated_at
			) VALUES (1, ?, 'legacy', 'legacy_blackboard_to_graph_v1', 0, ?)
		`, CanonicalStoreLegacyV1, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert default store epoch: %w", err)
		}
	}
	return nil
}
