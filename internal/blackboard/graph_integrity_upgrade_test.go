package blackboard

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

// TestIntegrityCutoverPreservesLegacyHistoryAndChainsNewMutations proves that
// upgrading to C07 never rewrites an existing append-only mutation. The
// migration's cutover marker selects the legacy verifier through the recorded
// sequence, then the next mutation chains from that legacy hash using C07's
// canonical changed-record algorithm.
func TestIntegrityCutoverPreservesLegacyHistoryAndChainsNewMutations(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Upgrade", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	ctx := SystemExecutionContext(createdProject.ID, createdProject.Kind, "integrity-upgrade")
	legacyTask, err := task.NewService(db, projects).Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Legacy integrity fixture", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create legacy Task: %v", err)
	}
	ctx.TaskID = legacyTask.ID
	if _, err := db.Exec(`INSERT INTO task_events(id,task_id,seq,kind,payload_json,created_at) VALUES('legacy-event',?,1,'status','{}','2024-01-01T00:00:00Z')`, ctx.TaskID); err != nil {
		t.Fatalf("insert legacy Task Event: %v", err)
	}
	graph := NewGraphService(db,
		NewSequenceClock("2024-01-01T00:00:00Z", "2024-01-01T00:00:01Z"),
		NewSequenceIDSource("legacy-prov", "legacy-node", "legacy-mutation", "current-prov", "current-node", "current-mutation"),
	)
	batch := func(key, stableKey string) MutationBatch {
		return MutationBatch{SchemaVersion: GraphMutationSchemaVersion, IdempotencyKey: key, Context: ctx, SourceEventIDsByOp: map[string][]string{"fact": {"legacy-event"}}, Operations: []Operation{{
			OpID: "fact", Kind: OpCreateNode, Node: NodeRef{NodeType: NodeTypeProjectFact, StableKey: stableKey},
			Create: CreateNodeInput{Properties: ProjectFactProperties{Category: "upgrade", Summary: stableKey, ScopeStatus: ScopeStatusInScope}},
		}}}
	}
	legacyResult, err := graph.Apply(context.Background(), batch("legacy", "upgrade:legacy"))
	if err != nil {
		t.Fatalf("seed mutation: %v", err)
	}
	requestHash, _ := hex.DecodeString(legacyResult.RequestHash)
	resultHash, _ := hex.DecodeString(legacyResult.ResultHash)
	stateHash, _ := hex.DecodeString(legacyResult.ResultingStateHash)
	legacyHash := computeLegacyMutationHash(mutationHashInput{
		ProjectID: createdProject.ID, MutationID: legacyResult.MutationID, PreviousHash: genesisHash(createdProject.ID),
		MutationSeq: 1, BaseRevision: 0, ResultRevision: 1, SchemaVersion: GraphMutationSchemaVersion,
		MutationKind: string(MutationKindNormal), MaintenanceMetadataJSON: "{}",
		IdempotencyScope: ctx.idempotencyScope(), IdempotencyKey: "legacy", RequestHash: requestHash,
		ResultHash: resultHash, RecordedAt: legacyResult.RecordedAt, ResultingStateHash: stateHash, ProjectionStatus: "dirty",
	})
	if _, err := db.Exec(`DROP TRIGGER blackboard_graph_mutations_no_update`); err != nil {
		t.Fatalf("drop mutation guard for upgrade fixture: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_graph_mutations SET mutation_hash=? WHERE project_id=? AND mutation_seq=1`, hex.EncodeToString(legacyHash), createdProject.ID); err != nil {
		t.Fatalf("install legacy mutation hash: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_graph_state SET history_head_hash=? WHERE project_id=?`, hex.EncodeToString(legacyHash), createdProject.ID); err != nil {
		t.Fatalf("install legacy history head: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO blackboard_graph_integrity_cutovers(project_id,legacy_through_mutation_seq) VALUES(?,1)`, createdProject.ID); err != nil {
		t.Fatalf("record integrity cutover: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO blackboard_graph_legacy_record_anchors(project_id,mutation_seq,record_kind,record_identity,record_json) SELECT project_id,mutation_seq,record_kind,record_identity,record_json FROM blackboard_graph_legacy_current_records WHERE project_id=? AND mutation_seq=1`, createdProject.ID); err != nil {
		t.Fatalf("record legacy changed-record anchors: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_graph_legacy_record_anchors SET record_json='tampered' WHERE project_id=?`, createdProject.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("legacy changed-record anchor update was not rejected: %v", err)
	}
	if err := graph.VerifyIntegrity(context.Background(), createdProject.ID); err != nil {
		t.Fatalf("verify legacy history: %v", err)
	}
	if _, err := graph.Apply(context.Background(), batch("current", "upgrade:current")); err != nil {
		t.Fatalf("append current mutation after legacy cutover: %v", err)
	}
	if err := graph.VerifyIntegrity(context.Background(), createdProject.ID); err != nil {
		t.Fatalf("verify mixed legacy/current chain: %v", err)
	}
	var originalProperties string
	if err := db.QueryRow(`SELECT properties_json FROM blackboard_node_versions WHERE project_id=? AND node_id='legacy-node' AND version=1`, createdProject.ID).Scan(&originalProperties); err != nil {
		t.Fatalf("read legacy node version: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER blackboard_node_versions_no_update`); err != nil {
		t.Fatalf("drop node-version corruption guard: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_node_versions SET properties_json='{}' WHERE project_id=? AND node_id='legacy-node' AND version=1`, createdProject.ID); err != nil {
		t.Fatalf("corrupt legacy node version: %v", err)
	}
	if err := graph.VerifyIntegrity(context.Background(), createdProject.ID); err == nil {
		t.Fatal("legacy node-version alteration did not break the integrity overlay")
	}
	if _, err := db.Exec(`UPDATE blackboard_node_versions SET properties_json=? WHERE project_id=? AND node_id='legacy-node' AND version=1`, originalProperties, createdProject.ID); err != nil {
		t.Fatalf("restore legacy node version fixture: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER blackboard_graph_provenance_events_no_delete`); err != nil {
		t.Fatalf("drop Task Event corruption guard: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM blackboard_graph_provenance_events WHERE project_id=? AND provenance_id='legacy-prov' AND ordinal=0`, createdProject.ID); err != nil {
		t.Fatalf("delete legacy Task Event: %v", err)
	}
	if err := graph.VerifyIntegrity(context.Background(), createdProject.ID); err == nil {
		t.Fatal("legacy Task Event deletion did not break the integrity overlay")
	}
}
