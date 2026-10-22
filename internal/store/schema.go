package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// baselineSchemaVersion identifies the schema created by this binary.
const baselineSchemaVersion = 1

const baselineSchemaName = "baseline_blackboard_v2"

// ErrUnsupportedStoreSchema reports a database created by an older CyberPenda
// version. This binary creates only the current baseline and never upgrades.
var ErrUnsupportedStoreSchema = errors.New("unsupported database schema: this database was created by an older CyberPenda version and cannot be opened; move it aside or choose a new database path")

// baselineStatements returns the complete baseline schema in creation order.
// Tables come first, then indexes, triggers, and views.
func baselineStatements() []string {
	groups := [][]string{
		baselineCoreStatements,
		baselineSessionStatements,
		baselineWorkflowStatements,
		baselineBlackboardStatements,
		baselineFGSStatements,
		baselineConstraintStatements,
	}
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	statements := make([]string, 0, total)
	for _, group := range groups {
		statements = append(statements, group...)
	}
	return statements
}

func baselineSchemaChecksum() string {
	sum := sha256.Sum256([]byte(strings.Join(baselineStatements(), ";\n")))
	return hex.EncodeToString(sum[:])
}

// initializeSchema creates the baseline schema on an empty database and
// refuses any database that does not carry this binary's baseline marker.
func initializeSchema(db *sql.DB) error {
	objects, err := countUserSchemaObjects(db)
	if err != nil {
		return fmt.Errorf("inspect Store schema: %w", err)
	}
	if objects == 0 {
		return createBaselineSchema(db, baselineStatements())
	}
	return validateBaselineSchema(db)
}

func createBaselineSchema(db *sql.DB, statements []string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin Store schema bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for index, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply Store baseline statement %d: %w", index+1, err)
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		baselineSchemaVersion, baselineSchemaName, baselineSchemaChecksum(), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record Store baseline schema: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO blackboard_store_state (id, canonical_store, cutover_state, migration_contract_version, graph_schema_version, updated_at) VALUES (1, ?, 'v2', 'blackboard_v2', 0, '1970-01-01T00:00:00Z')`,
		CanonicalStoreBlackboardV2,
	); err != nil {
		return fmt.Errorf("seed Store epoch: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO blackboard_read_state (id, cursor_secret) VALUES (1, randomblob(32))`); err != nil {
		return fmt.Errorf("seed Blackboard read cursor secret: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Store schema bootstrap: %w", err)
	}
	return nil
}

func validateBaselineSchema(db *sql.DB) error {
	rows, err := db.Query(`SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("%w: no readable baseline marker", ErrUnsupportedStoreSchema)
	}
	defer rows.Close()
	var count, version int
	var name, checksum string
	for rows.Next() {
		count++
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return fmt.Errorf("read Store baseline marker: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read Store baseline marker: %w", err)
	}
	if count != 1 || version != baselineSchemaVersion || name != baselineSchemaName || checksum != baselineSchemaChecksum() {
		return fmt.Errorf("%w: expected schema %q version %d", ErrUnsupportedStoreSchema, baselineSchemaName, baselineSchemaVersion)
	}
	var epoch string
	if err := db.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id = 1`).Scan(&epoch); err != nil {
		return fmt.Errorf("%w: no Store epoch", ErrUnsupportedStoreSchema)
	}
	if epoch != CanonicalStoreBlackboardV2 {
		return fmt.Errorf("%w: Store epoch %q", ErrUnsupportedStoreSchema, epoch)
	}
	return nil
}

func countUserSchemaObjects(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE lower(name) NOT GLOB 'sqlite_*'`).Scan(&count)
	return count, err
}
