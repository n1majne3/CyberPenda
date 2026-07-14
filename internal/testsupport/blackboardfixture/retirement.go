package blackboardfixture

import (
	"testing"

	"pentest/internal/store"
)

// InstallLegacyWriteGuards prepares the frozen-table state produced by M05.
func InstallLegacyWriteGuards(t testing.TB, db *store.DB) {
	t.Helper()
	for _, table := range []string{
		"project_facts", "project_fact_versions", "project_fact_relations", "fact_key_aliases",
		"findings", "finding_versions", "finding_key_aliases", "evidence_artifacts",
	} {
		for _, operation := range []string{"insert", "update", "delete"} {
			trigger := "blackboard_legacy_" + table + "_" + operation + "_guard"
			statement := `CREATE TRIGGER "` + trigger + `" BEFORE ` + operation + ` ON "` + table + `" BEGIN SELECT RAISE(ABORT,'` + table + ` is frozen after graph_v1 cutover'); END`
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("install %s: %v", trigger, err)
			}
		}
	}
}
