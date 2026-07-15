package blackboardfixture

import (
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/store"
	"pentest/internal/task"
)

const SentinelSummary = "v1 sentinel must stay invisible to blackboard_v2"

// SeedLegacyState places recognizable v1 rows under a v2 epoch. Production
// v2 boundaries must neither expose nor mutate them; they exist only to make
// accidental fallback observable in tests.
func SeedLegacyState(t testing.TB, db *store.DB, projectID, taskID string) {
	t.Helper()
	facts := blackboard.NewService(db)
	if _, err := facts.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "fact:v1-sentinel", Category: "test",
		Summary: SentinelSummary, Confidence: blackboard.ConfidenceConfirmed,
	}); err != nil {
		t.Fatalf("seed v1 Fact: %v", err)
	}
	if _, err := facts.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID: projectID, FindingKey: "finding:v1-sentinel", Title: SentinelSummary,
		Status: blackboard.FindingStatusUnconfirmed,
	}); err != nil {
		t.Fatalf("seed v1 Finding: %v", err)
	}
	if _, err := facts.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID: projectID, EvidenceKey: "evidence:v1-sentinel",
		AttachToType: blackboard.EvidenceAttachFact, AttachToKey: "fact:v1-sentinel",
		ArtifactType: "text", SourcePath: "sentinel.txt", Summary: SentinelSummary,
	}); err != nil {
		t.Fatalf("seed v1 Evidence: %v", err)
	}
	if _, err := task.NewService(db).PutSummary(taskID, SentinelSummary, "v1-sentinel"); err != nil {
		t.Fatalf("seed v1 Task Summary: %v", err)
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
		  AND (name LIKE 'blackboard_%'
		       OR name IN ('task_summary_versions','project_facts','project_fact_versions',
		                   'project_fact_relations','fact_key_aliases','finding_key_aliases',
		                   'findings','finding_versions','evidence_artifacts'))
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
