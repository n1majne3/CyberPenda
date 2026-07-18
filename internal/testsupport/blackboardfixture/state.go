package blackboardfixture

import (
	"testing"

	"pentest/internal/store"
)

const SentinelSummary = "v1 sentinel must stay invisible to blackboard_v2"

// SeedLegacyState places recognizable v1 rows under a v2 epoch. Production
// v2 boundaries must neither expose nor mutate them; they exist only to make
// accidental fallback observable in tests.
func SeedLegacyState(t testing.TB, db *store.DB, projectID, taskID string) {
	t.Helper()
	for _, table := range []string{"project_facts", "findings", "evidence_artifacts", "task_summary_versions"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("inspect retired table %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("active v2 fixture unexpectedly contains retired table %s", table)
		}
	}
}

// LegacyState captures every retained v1 Blackboard table count plus the
// global epoch row. Boundary tests compare it before and after public calls to
// prove that a refusal did not partially mutate legacy state.
type LegacyState struct {
	Epoch  store.BlackboardStoreState
	Tables map[string]int
}

func CaptureLegacyState(t testing.TB, db *store.DB) LegacyState {
	t.Helper()
	epoch, err := db.BlackboardStoreState()
	if err != nil {
		t.Fatalf("read Blackboard store state: %v", err)
	}
	rows, err := db.Query(`
		SELECT name
		FROM sqlite_master
		WHERE type='table'
		  AND (name LIKE 'blackboard_graph_%'
		       OR name IN ('task_summary_versions','project_facts','project_fact_versions',
		                   'project_fact_relations','fact_key_aliases','finding_key_aliases',
		                   'findings','finding_versions','evidence_artifacts',
		                   'blackboard_nodes','blackboard_node_versions','blackboard_node_heads',
		                   'blackboard_edges','blackboard_edge_versions','blackboard_edge_heads',
		                   'blackboard_key_registry','blackboard_key_events',
		                   'blackboard_legacy_mappings','blackboard_migration_runs'))
		ORDER BY name`)
	if err != nil {
		t.Fatalf("list legacy Blackboard tables: %v", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			t.Fatalf("scan legacy Blackboard table: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate legacy Blackboard tables: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close legacy Blackboard table query: %v", err)
	}

	tables := make(map[string]int, len(names))
	for _, name := range names {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + name + `"`).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		tables[name] = count
	}
	return LegacyState{Epoch: epoch, Tables: tables}
}
