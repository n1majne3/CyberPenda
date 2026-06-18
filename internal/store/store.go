// Package store owns the SQLite connection and schema migrations shared by
// every domain package. Domain repositories receive the opened database and
// keep their business logic separate from transport concerns so HTTP, MCP, and
// CLI handlers can all call the same services.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection with the daemon's schema applied.
type DB struct {
	*sql.DB
}

// Open connects to the SQLite database at path and runs migrations.
// An empty path uses an in-memory database, which is handy for tests.
func Open(path string) (*DB, error) {
	if path == "" {
		path = ":memory:"
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite busy timeout: %w", err)
	}
	if path != ":memory:" {
		if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure sqlite journal mode: %w", err)
		}
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &DB{db}, nil
}

// migrate applies schema changes. Each statement is idempotent so running it
// against an existing database is safe.
func migrate(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			scope_json TEXT NOT NULL,
			defaults_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS runtime_profiles (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			provider TEXT NOT NULL,
			fields_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS credential_bindings (
			id TEXT PRIMARY KEY,
			credential_ref TEXT NOT NULL,
			scope TEXT NOT NULL,
			scope_id TEXT NOT NULL DEFAULT '',
			source_json TEXT NOT NULL,
			disabled INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (credential_ref, scope, scope_id)
		);`,
		`CREATE TABLE IF NOT EXISTS tasks (
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
		);`,
		`CREATE TABLE IF NOT EXISTS task_runtime_config_versions (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			runtime_profile_id TEXT NOT NULL,
			config_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE (task_id, version)
		);`,
		`CREATE TABLE IF NOT EXISTS task_events (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			kind TEXT NOT NULL,
			payload_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			UNIQUE (task_id, seq)
		);`,
		`CREATE TABLE IF NOT EXISTS task_summary_versions (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			summary TEXT NOT NULL,
			submitted_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			UNIQUE (task_id, version)
		);`,
		`CREATE TABLE IF NOT EXISTS project_facts (
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
		`CREATE TABLE IF NOT EXISTS project_fact_versions (
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
		);`,
		`CREATE TABLE IF NOT EXISTS project_fact_relations (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			source_fact_key TEXT NOT NULL,
			target_fact_key TEXT NOT NULL,
			relation TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (project_id, source_fact_key, target_fact_key, relation)
		);`,
		`CREATE TABLE IF NOT EXISTS fact_key_aliases (
			-- A fact_key_alias maps a historical Fact Key to the canonical Fact Key
			-- it was merged into. Reads/writes through an alias resolve to the
			-- canonical key, so an alias never produces separate Current Truth.
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			alias_fact_key TEXT NOT NULL,
			canon_fact_key TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE (project_id, alias_fact_key)
		);`,
		`CREATE TABLE IF NOT EXISTS finding_key_aliases (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			alias_finding_key TEXT NOT NULL,
			canon_finding_key TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE (project_id, alias_finding_key)
		);`,
		`CREATE TABLE IF NOT EXISTS findings (
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
		`CREATE TABLE IF NOT EXISTS finding_versions (
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
		);`,
		`CREATE TABLE IF NOT EXISTS approvals (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			task_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			requester TEXT NOT NULL DEFAULT '',
			requested_action TEXT NOT NULL,
			rationale TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL DEFAULT '{}',
			reviewer TEXT NOT NULL DEFAULT '',
			decision TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			task_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			summary TEXT NOT NULL,
			payload_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS evidence_artifacts (
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

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("apply migration: %w", err)
		}
	}

	// Additive column migrations for databases created before the column existed.
	if err := ensureColumn(db, "projects", "defaults_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return fmt.Errorf("ensure projects.defaults_json: %w", err)
	}

	return nil
}

// ensureColumn adds a column to a table if it is not already present. SQLite
// has no IF NOT EXISTS for ADD COLUMN, so the presence check is explicit.
func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
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

	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}
