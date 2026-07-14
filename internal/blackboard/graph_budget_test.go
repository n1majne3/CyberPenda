package blackboard_test

import (
	"context"
	"strings"
	"testing"

	"pentest/internal/blackboard"
)

func TestTwentyThousandTokenWriteCommitsFullGraphThenAttemptsSafeCompaction(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)

	written, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "c10:required:eligible",
		Context:        execCtx,
		Operations: []blackboard.Operation{{
			OpID: "deprecated-fact", Kind: blackboard.OpCreateNode,
			Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:large-deprecated"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
				"category": "service", "summary": strings.Repeat("x", 81_000),
				"confidence": "deprecated", "scope_status": "in_scope",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("apply oversized valid write: %v", err)
	}

	triggeringProjection, err := graph.CanonicalMainGraph(context.Background(), projectID, written.GraphRevision)
	if err != nil {
		t.Fatalf("render triggering revision: %v", err)
	}
	if triggeringProjection.EstimatedTokens < 20_000 {
		t.Fatalf("fixture did not cross required threshold: %d", triggeringProjection.EstimatedTokens)
	}

	manifest, err := graph.LatestCompaction(context.Background(), projectID)
	if err != nil {
		t.Fatalf("read compaction manifest: %v", err)
	}
	if manifest.BaseGraphRevision != written.GraphRevision || manifest.BeforeTokens != triggeringProjection.EstimatedTokens {
		t.Fatalf("manifest did not record committed triggering graph: %#v", manifest)
	}
	if len(manifest.ArchivedNodeIDs) != 1 || manifest.ArchivedNodeIDs[0] != written.Operations[0].NodeID {
		t.Fatalf("unexpected archived nodes: %#v", manifest.ArchivedNodeIDs)
	}
	if manifest.AfterTokens > 12_000 || manifest.AfterTokens >= manifest.BeforeTokens {
		t.Fatalf("compaction did not target 12K: before=%d after=%d", manifest.BeforeTokens, manifest.AfterTokens)
	}
	if _, err := graph.DBForTesting().Exec(`UPDATE blackboard_compactions SET after_tokens=after_tokens WHERE project_id=? AND manifest_id=?`, projectID, manifest.ID); err == nil {
		t.Fatal("compaction manifest was mutable")
	}
	var metricCount int
	if err := graph.DBForTesting().QueryRow(`SELECT COUNT(*) FROM blackboard_projection_metrics WHERE project_id=?`, projectID).Scan(&metricCount); err != nil {
		t.Fatal(err)
	}
	if metricCount < 2 {
		t.Fatalf("expected triggering and compacted projection metrics, got %d", metricCount)
	}

	health, err := graph.LatestHealth(context.Background(), projectID)
	if err != nil {
		t.Fatalf("read Blackboard Health: %v", err)
	}
	if health.Status != blackboard.HealthStatusHealthy || health.Metrics.BudgetState != blackboard.BudgetWithinTarget {
		t.Fatalf("unexpected post-compaction health: %#v", health)
	}
}

func TestBudgetBandsUseExactCanonicalThresholds(t *testing.T) {
	cases := []struct {
		tokens int
		want   blackboard.BudgetState
	}{
		{12_000, blackboard.BudgetWithinTarget},
		{12_001, blackboard.BudgetAboveTarget},
		{15_999, blackboard.BudgetAboveTarget},
		{16_000, blackboard.BudgetWarning},
		{19_999, blackboard.BudgetWarning},
		{20_000, blackboard.BudgetRequired},
	}
	for _, tc := range cases {
		if got := blackboard.BudgetStateForEstimatedTokens(tc.tokens); got != tc.want {
			t.Errorf("tokens=%d: got %q want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestSafeCandidateExhaustionKeepsFullGraphAndReportsCompactionBlocked(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	written, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:required:protected", Context: execCtx,
		Operations: []blackboard.Operation{{OpID: "truth", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:large-current-truth"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": strings.Repeat("p", 81_000), "confidence": "tentative", "scope_status": "in_scope"}}}},
	})
	if err != nil {
		t.Fatalf("apply oversized protected write: %v", err)
	}
	current, err := graph.CanonicalMainGraph(context.Background(), projectID, written.GraphRevision)
	if err != nil {
		t.Fatalf("render full oversized graph: %v", err)
	}
	if current.EstimatedTokens < 20_000 || !strings.Contains(string(current.Bytes), "fact:large-current-truth") {
		t.Fatalf("full graph was not preserved: tokens=%d", current.EstimatedTokens)
	}
	if _, err := graph.LatestCompaction(context.Background(), projectID); err == nil {
		t.Fatal("protected graph unexpectedly compacted")
	}
	health, err := graph.LatestHealth(context.Background(), projectID)
	if err != nil {
		t.Fatalf("read health: %v", err)
	}
	if health.Status != blackboard.HealthStatusCritical || !healthHasCode(health, "compaction_blocked") {
		t.Fatalf("missing compaction_blocked: %#v", health)
	}
}

func TestInterveningSemanticWriteInvalidatesCompleteCompactionPlan(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:stale:first", Context: execCtx, Operations: []blackboard.Operation{{OpID: "old", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:old"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "old", "confidence": "deprecated", "scope_status": "in_scope"}}}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := graph.PlanCompaction(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:stale:second", Context: execCtx, Operations: []blackboard.Operation{{OpID: "truth", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:new"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "new", "confidence": "tentative", "scope_status": "in_scope"}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.ApplyCompaction(context.Background(), plan); err == nil {
		t.Fatal("expected stale plan rejection")
	}
	if _, err := graph.LatestCompaction(context.Background(), projectID); err == nil {
		t.Fatal("stale plan persisted a manifest")
	}
}

func healthHasCode(run blackboard.HealthRun, code string) bool {
	for _, result := range run.Results {
		if result.Code == code {
			return true
		}
	}
	return false
}

func TestFailedAttemptRemainsAProtectedNegativeResultAnchor(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:failed-anchor:create", Context: execCtx,
		Operations: []blackboard.Operation{
			{OpID: "objective", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:anchor"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "test anchor"}}},
			{OpID: "attempt", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:anchor"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{}}},
			{OpID: "tests", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeTests, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{OpID: "objective"}}},
		},
	})
	if err != nil {
		t.Fatalf("create attempt branch: %v", err)
	}
	attemptID := created.Operations[1].NodeID
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:failed-anchor:finish", Context: execCtx, Operations: []blackboard.Operation{{OpID: "failed", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: attemptID}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "failed", Summary: "negative result retained"}}}}); err != nil {
		t.Fatalf("fail attempt: %v", err)
	}
	plan, err := graph.PlanCompaction(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range plan.ArchiveNodeIDs {
		if id == attemptID {
			t.Fatal("failed Attempt was eligible for compaction")
		}
	}
	found := false
	for _, id := range plan.PreservedAnchorIDs {
		if id == attemptID {
			found = true
		}
	}
	if !found {
		t.Fatal("failed Attempt missing from preserved anchors")
	}
}

func TestRestoreHoldPreventsImmediateRearchiveBeforeSnapshotPin(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	written, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:restore:create", Context: execCtx, Operations: []blackboard.Operation{{OpID: "old", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:restore-hold"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": strings.Repeat("r", 81_000), "confidence": "deprecated", "scope_status": "in_scope"}}}}})
	if err != nil {
		t.Fatalf("create and compact fixture: %v", err)
	}
	nodeID := written.Operations[0].NodeID
	manifest := blackboard.RestoreManifest{ID: "restore:c10:hold", Nodes: []string{nodeID}}
	restoreCtx := blackboard.SystemRestoreExecutionContext(projectID, execCtx.ProjectKind, execCtx.ActorID, manifest)
	restored, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:restore:apply", Context: restoreCtx, Operations: []blackboard.Operation{{OpID: "restore", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: nodeID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 2, Disposition: blackboard.DispositionMain, RestoreManifestID: manifest.ID}}}})
	if err != nil {
		t.Fatalf("restore archived component: %v", err)
	}
	projection, err := graph.CanonicalMainGraph(context.Background(), projectID, restored.GraphRevision)
	if err != nil {
		t.Fatal(err)
	}
	if projection.EstimatedTokens < 20_000 {
		t.Fatalf("restore fixture should remain oversized, got %d", projection.EstimatedTokens)
	}
	plan, err := graph.PlanCompaction(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ArchiveNodeIDs) != 0 {
		t.Fatalf("restore hold did not exclude restored component: %#v", plan.ArchiveNodeIDs)
	}
	overridePlan, err := graph.PlanCompactionWithOptions(context.Background(), projectID, blackboard.CompactionOptions{OverrideRestoreHold: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(overridePlan.ArchiveNodeIDs) != 1 || overridePlan.ArchiveNodeIDs[0] != nodeID {
		t.Fatalf("trusted override did not release restore hold: %#v", overridePlan.ArchiveNodeIDs)
	}
	if _, err := graph.DBForTesting().Exec(`DELETE FROM blackboard_restore_manifests WHERE project_id=? AND manifest_id=?`, projectID, manifest.ID); err == nil {
		t.Fatal("restore manifest was deletable")
	}
	health, err := graph.LatestHealth(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !healthHasCode(health, "compaction_required") || healthHasCode(health, "compaction_blocked") {
		t.Fatalf("restore hold health should remain oversized without declaring safe-candidate exhaustion: %#v", health.Results)
	}
}

func TestHealthPersistsStableDuplicateAndContradictionDetectors(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:health:detectors", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "a", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:a"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Same, FACT!", "confidence": "tentative", "scope_status": "in_scope"}}},
		{OpID: "b", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:b"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "same fact", "confidence": "tentative", "scope_status": "in_scope"}}},
		{OpID: "conflict", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeContradicts, From: blackboard.NodeRef{OpID: "a"}, To: blackboard.NodeRef{OpID: "b"}}},
	}})
	if err != nil {
		t.Fatalf("create detector fixture: %v", err)
	}
	health, err := graph.RunHealth(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !healthHasCode(health, "duplicate_candidate") || !healthHasCode(health, "unresolved_contradiction") {
		t.Fatalf("missing graph detectors: %#v", health.Results)
	}
	if health.Status != blackboard.HealthStatusDegraded {
		t.Fatalf("warning detector should degrade health, got %q", health.Status)
	}
	again, err := graph.RunHealth(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if detectorFingerprint(health, "duplicate_candidate") != detectorFingerprint(again, "duplicate_candidate") {
		t.Fatal("duplicate detector fingerprint was not stable")
	}
}

func detectorFingerprint(run blackboard.HealthRun, code string) string {
	for _, result := range run.Results {
		if result.Code == code {
			return result.Fingerprint
		}
	}
	return ""
}

func TestHealthPersistsProjectionStalenessAndUnknownSizingWithoutGraphMutation(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	written, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:health:stale", Context: execCtx, Operations: []blackboard.Operation{{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:health-stale"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "stale fixture", "confidence": "tentative", "scope_status": "in_scope"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.DBForTesting().Exec(`UPDATE blackboard_graph_state SET projection_dirty_revision=? WHERE project_id=?`, written.GraphRevision, projectID); err != nil {
		t.Fatal(err)
	}
	stale, err := graph.RunHealth(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !healthHasCode(stale, "projection_stale") || stale.Status != blackboard.HealthStatusDegraded {
		t.Fatalf("stale projection was not diagnosed: %#v", stale)
	}
	var beforeRevision int
	if err := graph.DBForTesting().QueryRow(`SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&beforeRevision); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := graph.RunHealth(cancelled, projectID); err == nil {
		t.Fatal("expected cancelled Health sizing to fail")
	}
	unknown, err := graph.LatestHealth(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Status != blackboard.HealthStatusUnknown || !healthHasCode(unknown, "budget_unknown") {
		t.Fatalf("unknown scan not persisted: %#v", unknown)
	}
	var afterRevision int
	if err := graph.DBForTesting().QueryRow(`SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&afterRevision); err != nil {
		t.Fatal(err)
	}
	if afterRevision != beforeRevision {
		t.Fatalf("Health mutated semantic graph revision: before=%d after=%d", beforeRevision, afterRevision)
	}
}

func TestCompactionRollsBackWhenMandatoryManifestPersistenceFails(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	written, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:rollback:compact:create", Context: execCtx, Operations: []blackboard.Operation{{OpID: "old", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:rollback-compaction"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": strings.Repeat("c", 61_000), "confidence": "deprecated", "scope_status": "in_scope"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := graph.PlanCompaction(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ArchiveNodeIDs) == 0 {
		t.Fatal("fixture did not produce a warning-band compaction plan")
	}
	if _, err := graph.DBForTesting().Exec(`CREATE TRIGGER c10_fail_compaction_manifest BEFORE INSERT ON blackboard_compactions BEGIN SELECT RAISE(ABORT,'injected compaction manifest failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.ApplyCompaction(context.Background(), plan); err == nil {
		t.Fatal("expected mandatory compaction manifest failure")
	}
	var revision int
	if err := graph.DBForTesting().QueryRow(`SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != written.GraphRevision {
		t.Fatalf("compaction semantic mutation committed without manifest: got revision %d want %d", revision, written.GraphRevision)
	}
	var disposition string
	if err := graph.DBForTesting().QueryRow(`SELECT disposition FROM blackboard_node_heads WHERE project_id=? AND node_id=?`, projectID, written.Operations[0].NodeID).Scan(&disposition); err != nil {
		t.Fatal(err)
	}
	if disposition != string(blackboard.DispositionMain) {
		t.Fatalf("node disposition changed despite rollback: %s", disposition)
	}
}

func TestRestoreRollsBackWhenMandatoryManifestPersistenceFails(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:rollback:restore:create", Context: execCtx, Operations: []blackboard.Operation{{OpID: "old", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:rollback-restore"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "restore rollback", "confidence": "deprecated", "scope_status": "in_scope"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	nodeID := created.Operations[0].NodeID
	archived, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:rollback:restore:archive", Context: execCtx, Operations: []blackboard.Operation{{OpID: "archive", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: nodeID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 1, Disposition: blackboard.DispositionArchived}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.DBForTesting().Exec(`CREATE TRIGGER c10_fail_restore_manifest BEFORE INSERT ON blackboard_restore_manifests BEGIN SELECT RAISE(ABORT,'injected restore manifest failure'); END`); err != nil {
		t.Fatal(err)
	}
	manifest := blackboard.RestoreManifest{ID: "restore:c10:rollback", Nodes: []string{nodeID}}
	restoreCtx := blackboard.SystemRestoreExecutionContext(projectID, execCtx.ProjectKind, execCtx.ActorID, manifest)
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:rollback:restore:apply", Context: restoreCtx, Operations: []blackboard.Operation{{OpID: "restore", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: nodeID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 2, Disposition: blackboard.DispositionMain, RestoreManifestID: manifest.ID}}}}); err == nil {
		t.Fatal("expected mandatory restore manifest failure")
	}
	var revision int
	if err := graph.DBForTesting().QueryRow(`SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != archived.GraphRevision {
		t.Fatalf("restore committed without manifest: got revision %d want %d", revision, archived.GraphRevision)
	}
	var disposition string
	if err := graph.DBForTesting().QueryRow(`SELECT disposition FROM blackboard_node_heads WHERE project_id=? AND node_id=?`, projectID, nodeID).Scan(&disposition); err != nil {
		t.Fatal(err)
	}
	if disposition != string(blackboard.DispositionArchived) {
		t.Fatalf("node restored despite rollback: %s", disposition)
	}
}

func TestHealthDetectsMaterializationAndArchiveManifestMismatches(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c10:health:integrity", Context: execCtx, Operations: []blackboard.Operation{{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:integrity"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "integrity", "confidence": "tentative", "scope_status": "in_scope"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.DBForTesting().Exec(`UPDATE blackboard_node_heads SET semantic_hash='tampered' WHERE project_id=? AND node_id=?`, projectID, created.Operations[0].NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.DBForTesting().Exec(`INSERT INTO blackboard_compactions(project_id,manifest_id,base_graph_revision,result_graph_revision,before_hash,after_hash,before_bytes,after_bytes,before_tokens,after_tokens,expected_versions_json,archived_node_ids_json,retired_edge_ids_json,preserved_anchor_ids_json,rationale_json,mutation_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, projectID, "compaction:fake", created.GraphRevision, created.GraphRevision, "a", "b", 1, 1, 1, 1, "{}", `["missing-node"]`, `[]`, `[]`, `[]`, "missing-mutation", "2024-01-02T03:04:06Z"); err != nil {
		t.Fatal(err)
	}
	health, err := graph.RunHealth(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !healthHasCode(health, "materialization_mismatch") || !healthHasCode(health, "archive_manifest_mismatch") {
		t.Fatalf("missing integrity detectors: %#v", health.Results)
	}
	if health.Status != blackboard.HealthStatusCritical {
		t.Fatalf("integrity mismatch should be critical, got %q", health.Status)
	}
}
