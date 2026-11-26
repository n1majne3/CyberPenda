package daemon

import (
	"fmt"
	"time"

	"pentest/internal/childhistory"
	"pentest/internal/store"
)

func migrateLegacyEventReferencesOnce(db *store.DB) error {
	var version int
	if err := db.QueryRow(`SELECT version FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read legacy reference migration marker: %w", err)
	}
	if version != store.LegacyReferenceMigrationSourceVersion() {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin legacy reference migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := childhistory.MigrateLegacyEventReferences(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE schema_migrations SET version=?, applied_at=? WHERE version=?`, store.CurrentSchemaVersion(), time.Now().UTC().Format(time.RFC3339Nano), store.LegacyReferenceMigrationSourceVersion()); err != nil {
		return fmt.Errorf("mark legacy reference migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy reference migration: %w", err)
	}
	return nil
}
