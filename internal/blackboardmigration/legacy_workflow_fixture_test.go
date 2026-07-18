package blackboardmigration

import (
	"testing"

	"pentest/internal/store"
)

// installLegacyWorkflowStateFixture models source-only v1 workflow columns.
// It is intentionally test-local to the offline migration package.
func installLegacyWorkflowStateFixture(t *testing.T, db *store.DB) {
	t.Helper()
	for _, statement := range []string{
		`ALTER TABLE task_continuations ADD COLUMN blackboard_finish_summary_version_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_continuations ADD COLUMN blackboard_finish_graph_revision INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE task_continuations ADD COLUMN blackboard_finish_mutation_sequence INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE task_continuations ADD COLUMN blackboard_finished_at TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE task_summary_versions (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			continuation_id TEXT,
			version INTEGER NOT NULL,
			summary TEXT NOT NULL,
			submitted_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("install legacy workflow fixture schema: %v", err)
		}
	}
}
