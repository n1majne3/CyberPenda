package blackboardmigration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/store"
)

func TestRebuildUnambiguousHeadsMapsClosedSemanticFieldsAndExactSnapshot(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedUnambiguousGraphHeads(t, db, artifactRoot)

	epochBefore, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epochBefore != store.CanonicalStoreGraphV1 {
		t.Fatalf("precondition epoch = %q, want graph_v1", epochBefore)
	}

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if err != nil {
		t.Fatalf("Execute(rebuild_unambiguous): %v", err)
	}
	if result.Rebuild == nil || result.Rebuild.Status != "rebuilt" {
		t.Fatalf("rebuild result = %#v", result.Rebuild)
	}
	if result.Rebuild.StoreEpoch != store.CanonicalStoreGraphV1 {
		t.Fatalf("rebuild reported epoch %q", result.Rebuild.StoreEpoch)
	}
	if result.Rebuild.Validation.Status != "passed" || result.Rebuild.Validation.SnapshotsValidated != 1 {
		t.Fatalf("validation = %#v", result.Rebuild.Validation)
	}
	for _, mapping := range result.Rebuild.Mappings {
		if mapping.Project != "project-rebuild" {
			t.Fatalf("mapping lost Project identity: %#v", mapping)
		}
	}

	epochAfter, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epochAfter != store.CanonicalStoreGraphV1 {
		t.Fatalf("rebuild flipped epoch to %q", epochAfter)
	}

	// v1 graph heads remain authoritative source rows.
	var graphFacts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_node_heads WHERE project_id='project-rebuild' AND node_type='project_fact'`).Scan(&graphFacts); err != nil {
		t.Fatal(err)
	}
	if graphFacts == 0 {
		t.Fatal("rebuild deleted v1 graph heads")
	}

	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	projection, err := v2.ProjectRuntimeSnapshot(context.Background(), "project-rebuild")
	if err != nil {
		t.Fatalf("ProjectRuntimeSnapshot: %v", err)
	}
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.Validate("runtimeSnapshot", projection.Bytes); err != nil {
		t.Fatalf("snapshot contract: %v\n%s", err, projection.Bytes)
	}
	again, err := v2.ProjectRuntimeSnapshot(context.Background(), "project-rebuild")
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Bytes) != string(projection.Bytes) {
		t.Fatal("snapshot bytes are not deterministic")
	}

	// Retained conforming keys stay stable.
	detail, err := v2.ReadCurrent(context.Background(), "project-rebuild", "fact:login")
	if err != nil {
		t.Fatalf("ReadCurrent(fact:login): %v", err)
	}
	if detail.Type != "fact" || detail.Record.Confidence != "tentative" || detail.Record.Summary != "Login form exists" {
		t.Fatalf("fact detail = %#v", detail)
	}
	raw, _ := json.Marshal(detail)
	for _, banned := range []string{"node_id", "semantic_hash", "mutation_seq", "provenance", "migration_source", "graph_revision"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("detail leaked storage envelope %q: %s", banned, raw)
		}
	}

	entity, err := v2.ReadCurrent(context.Background(), "project-rebuild", "host:web")
	if err != nil {
		t.Fatalf("ReadCurrent(host:web): %v", err)
	}
	if entity.Type != "entity" || entity.Record.Kind != "host" || entity.Record.Name != "web" {
		t.Fatalf("entity detail = %#v", entity)
	}
	objective, err := v2.ReadCurrent(context.Background(), "project-rebuild", "objective:enumerate-auth")
	if err != nil {
		t.Fatalf("ReadCurrent(objective): %v", err)
	}
	if objective.Type != "objective" || objective.Record.Status != "open" {
		t.Fatalf("objective detail = %#v", objective)
	}
	attempt, err := v2.ReadCurrent(context.Background(), "project-rebuild", "attempt:probe-login")
	if err != nil {
		t.Fatalf("ReadCurrent(attempt): %v", err)
	}
	if attempt.Type != "attempt" || attempt.Record.Status != "open" {
		t.Fatalf("attempt detail = %#v", attempt)
	}
	finding, err := v2.ReadCurrent(context.Background(), "project-rebuild", "finding:missing-headers")
	if err != nil {
		t.Fatalf("ReadCurrent(finding): %v", err)
	}
	if finding.Type != "finding" || finding.Record.Status != "unconfirmed" {
		t.Fatalf("finding detail = %#v", finding)
	}
	evidence, err := v2.ReadCurrent(context.Background(), "project-rebuild", "evidence:login-html")
	if err != nil {
		t.Fatalf("ReadCurrent(evidence): %v", err)
	}
	if evidence.Type != "evidence" || evidence.Record.ArtifactType != "http_exchange" {
		t.Fatalf("evidence detail = %#v", evidence)
	}

	// Relationships rewritten to project-wide keys.
	if !snapshotHasRelation(projection.Snapshot, "attempt:probe-login", "tests", "objective:enumerate-auth") {
		t.Fatalf("missing tests relation in snapshot: %s", projection.Bytes)
	}
	if !snapshotHasRelation(projection.Snapshot, "fact:login", "about", "host:web") {
		t.Fatalf("missing about relation in snapshot: %s", projection.Bytes)
	}
	if !snapshotHasRelation(projection.Snapshot, "evidence:login-html", "evidences", "fact:login") {
		t.Fatalf("missing evidences relation in snapshot: %s", projection.Bytes)
	}

	// Prior semantic versions are addressable through Semantic History.
	history, err := v2.ReadHistory(context.Background(), "project-rebuild", "fact:login", blackboardv2.HistoryOptions{Limit: 20})
	if err != nil {
		t.Fatalf("ReadHistory(fact:login): %v", err)
	}
	if history.Schema != "semantic-history/v2" || len(history.Items) == 0 {
		t.Fatalf("expected prior history items, got %#v", history)
	}
	foundRelationHistory := false
	for _, item := range history.Items {
		if item.Kind == "relationship" && item.Version == 1 && item.From == "fact:login" && item.Relation == "about" && item.To == "host:web" {
			foundRelationHistory = true
		}
	}
	if !foundRelationHistory {
		t.Fatalf("expected prior relationship version in Semantic History, got %#v", history.Items)
	}
}

func TestRebuildRenamesOpaqueKeysAndProjectWideCollisionsDeterministically(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedCollisionAndOpaqueGraphHeads(t, db, artifactRoot)

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if err != nil {
		t.Fatalf("Execute(rebuild_unambiguous): %v", err)
	}
	if result.Rebuild == nil {
		t.Fatal("missing rebuild result")
	}

	bySource := map[string]MigrationMapping{}
	for _, mapping := range result.Rebuild.Mappings {
		bySource[mapping.SourceType+"\x00"+mapping.SourceKey] = mapping
	}

	factMapping := bySource["project_fact\x00shared-key"]
	findingMapping := bySource["finding\x00shared-key"]
	if factMapping.Action != "retain" || factMapping.TargetKey != "shared-key" {
		t.Fatalf("first conforming key should retain, got %#v", factMapping)
	}
	if findingMapping.Action != "rename" || findingMapping.TargetKey == "shared-key" || !strings.HasPrefix(findingMapping.TargetKey, "migrated:finding:") {
		t.Fatalf("colliding finding key should rename, got %#v", findingMapping)
	}

	opaque := bySource["project_fact\x00"+strings.Repeat("a", 100)]
	if opaque.Action != "rename" || !strings.HasPrefix(opaque.TargetKey, "migrated:fact:") || len(opaque.TargetKey) > 96 {
		t.Fatalf("opaque key mapping = %#v", opaque)
	}

	// Mapping rows let every imported endpoint resolve without source IDs in semantic payloads.
	var mappedTarget string
	if err := db.QueryRow(`SELECT target_key FROM blackboard_v2_rebuild_mappings WHERE project_id='project-rebuild' AND source_type='finding' AND source_key='shared-key'`).Scan(&mappedTarget); err != nil {
		t.Fatal(err)
	}
	if mappedTarget != findingMapping.TargetKey {
		t.Fatalf("persisted mapping %q != result %q", mappedTarget, findingMapping.TargetKey)
	}

	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", findingMapping.TargetKey); err != nil {
		t.Fatalf("renamed finding not readable: %v", err)
	}
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", factMapping.TargetKey); err != nil {
		t.Fatalf("retained fact not readable: %v", err)
	}
}

func TestRebuildKeepsProjectsIsolatedAndBlocksCrossProjectReferences(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedIsolatedProjects(t, db, artifactRoot)

	// Healthy rebuild first proves isolation of valid multi-project data.
	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if err != nil {
		t.Fatalf("Execute(rebuild_unambiguous): %v", err)
	}
	if result.Rebuild == nil || result.Rebuild.Validation.SnapshotsValidated != 2 {
		t.Fatalf("expected two project snapshots, got %#v", result.Rebuild)
	}
	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	a, err := v2.ReadCurrent(context.Background(), "project-a", "fact:a")
	if err != nil {
		t.Fatal(err)
	}
	if a.Record.Summary != "Project A fact" {
		t.Fatalf("project-a fact = %#v", a)
	}
	if _, err := v2.ReadCurrent(context.Background(), "project-a", "fact:b"); err == nil {
		t.Fatal("project-a should not see project-b keys")
	}
	b, err := v2.ReadCurrent(context.Background(), "project-b", "fact:b")
	if err != nil {
		t.Fatal(err)
	}
	if b.Record.Summary != "Project B fact" {
		t.Fatalf("project-b fact = %#v", b)
	}

	// Inject a cross-project edge endpoint by re-homing the target node while
	// leaving the edge head owned by project-a.
	var fromID, toID string
	if err := db.QueryRow(`SELECT id FROM blackboard_nodes WHERE project_id='project-a' AND original_stable_key='fact:a'`).Scan(&fromID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM blackboard_nodes WHERE project_id='project-b' AND original_stable_key='fact:b'`).Scan(&toID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO blackboard_edges(project_id,id,edge_type,created_mutation_seq,created_operation_index,created_at)
		VALUES('project-a','edge-cross','supports',99,0,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO blackboard_edge_versions(project_id,edge_id,version,result_graph_revision,mutation_seq,operation_index,from_node_id,to_node_id,state,summary,semantic_hash,updated_at)
		VALUES('project-a','edge-cross',1,1,99,0,?,?,'active','','00','2026-01-01T00:00:00Z')`, fromID, toID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO blackboard_edge_heads(project_id,edge_id,edge_type,from_node_id,to_node_id,version,graph_revision,state,semantic_hash)
		VALUES('project-a','edge-cross','supports',?,?,1,1,'active','00')`, fromID, toID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}

	blocked, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if !errors.Is(err, ErrRebuildBlocked) {
		t.Fatalf("error = %v, want ErrRebuildBlocked", err)
	}
	found := false
	for _, blocker := range blocked.Rebuild.Blockers {
		if blocker.Code == "cross_project_reference" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing cross_project_reference blocker: %#v", blocked.Rebuild.Blockers)
	}
	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epoch != store.CanonicalStoreGraphV1 {
		t.Fatalf("blocked rebuild changed epoch to %q", epoch)
	}
}

func TestRebuildLeavesTerminalWorkInHistoryAndSkipsRemovedWorkflowTypes(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedTerminalAndRemovedTypes(t, db, artifactRoot)

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if err != nil {
		t.Fatalf("Execute(rebuild_unambiguous): %v", err)
	}
	if result.Rebuild == nil {
		t.Fatal("missing rebuild result")
	}
	for _, mapping := range result.Rebuild.Mappings {
		if mapping.SourceType == "goal" || mapping.SourceType == "observation" || mapping.SourceType == "hypothesis" || mapping.SourceType == "project_directive" {
			t.Fatalf("removed workflow type was mapped in #121: %#v", mapping)
		}
	}

	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	projection, err := v2.ProjectRuntimeSnapshot(context.Background(), "project-rebuild")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Snapshot.Work.Objectives != nil {
		if _, ok := projection.Snapshot.Work.Objectives["objective:done"]; ok {
			t.Fatal("terminal objective should not be current work")
		}
	}
	history, err := v2.ReadHistory(context.Background(), "project-rebuild", "objective:done", blackboardv2.HistoryOptions{Limit: 20})
	if err != nil {
		t.Fatalf("terminal objective history: %v", err)
	}
	if len(history.Items) == 0 {
		t.Fatal("terminal objective missing from Semantic History")
	}
}

func newGraphV1RebuildService(t *testing.T) (*store.DB, *Service, string) {
	t.Helper()
	root := t.TempDir()
	databasePath := filepath.Join(root, "pentest.db")
	artifactRoot := filepath.Join(root, "artifacts")
	db, err := store.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Keep v1 authoritative while disposable v2 tables from numbered migrations remain available.
	if _, err := db.Exec(`
		UPDATE blackboard_store_state
		SET canonical_store='graph_v1',
		    cutover_state='graph',
		    migration_contract_version='legacy_blackboard_to_graph_v1',
		    graph_schema_version=1
		WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	return db, NewService(db, databasePath, artifactRoot), artifactRoot
}

func seedUnambiguousGraphHeads(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	projectID := "project-rebuild"
	if _, err := db.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		projectID, "Rebuild", "", "{}", "{}", "pentest", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	ctx := blackboard.SystemExecutionContext(projectID, "pentest", "rebuild-test")
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "rebuild-seed-1",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID: "entity", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:web"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"kind": "host", "name": "web", "locator": "web.example", "scope_status": "in_scope", "status": "active",
				}},
			},
			{
				OpID: "objective", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:enumerate-auth"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"objective": "Enumerate authentication surfaces", "status": "open",
				}},
			},
			{
				OpID: "attempt", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:probe-login"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"status": "open", "summary": "Probe login form",
				}},
			},
			{
				OpID: "tests", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{
					EdgeType: blackboard.EdgeTypeTests,
					From:     blackboard.NodeRef{OpID: "attempt"},
					To:       blackboard.NodeRef{OpID: "objective"},
				},
			},
			{
				OpID: "fact", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:login"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category": "asset", "summary": "Login form v1", "body": "first", "confidence": "tentative", "scope_status": "in_scope",
				}},
			},
			{
				OpID: "about", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{
					EdgeType: blackboard.EdgeTypeAbout,
					From:     blackboard.NodeRef{OpID: "fact"},
					To:       blackboard.NodeRef{OpID: "entity"},
				},
			},
			{
				OpID: "finding", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:missing-headers"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"title": "Missing security headers", "status": "unconfirmed", "description": "No CSP",
				}},
			},
			{
				OpID: "evidence", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:login-html"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"artifact_type": "http_exchange",
					"summary":       "Login response HTML",
					"managed_path":  "evidence/login.html",
					"sha256":        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					"size_bytes":    float64(12),
					"status":        "available",
				}},
			},
			{
				OpID: "evidences", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{
					EdgeType: blackboard.EdgeTypeEvidences,
					From:     blackboard.NodeRef{OpID: "evidence"},
					To:       blackboard.NodeRef{OpID: "fact"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("seed graph batch 1: %v", err)
	}
	// Create prior semantic version for the fact.
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "rebuild-seed-2",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID: "patch-fact", Kind: blackboard.OpPatchNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:login"},
				Patch: blackboard.PatchNodeInput{
					ExpectedVersion: 1,
					Properties:      map[string]any{"summary": "Login form exists", "body": "current"},
				},
			},
			{
				OpID: "update-about", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{
					EdgeType:        blackboard.EdgeTypeAbout,
					From:            blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:login"},
					To:              blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:web"},
					ExpectedVersion: 1,
					Summary:         "Legacy relationship annotation",
				},
			},
		},
	}); err != nil {
		t.Fatalf("seed fact history: %v", err)
	}
}

func seedCollisionAndOpaqueGraphHeads(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES('project-rebuild','Rebuild','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	ctx := blackboard.SystemExecutionContext("project-rebuild", "pentest", "rebuild-test")
	opaqueKey := strings.Repeat("a", 100)
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "collision-seed",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID: "fact", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "shared-key"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "asset", "summary": "Fact side", "confidence": "tentative", "scope_status": "in_scope"}},
			},
			{
				OpID: "finding", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "shared-key"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Finding side", "status": "unconfirmed"}},
			},
			{
				OpID: "opaque", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: opaqueKey},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "asset", "summary": "Opaque key fact", "confidence": "tentative", "scope_status": "in_scope"}},
			},
		},
	}); err != nil {
		t.Fatalf("seed collision graph: %v", err)
	}
}

func seedIsolatedProjects(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	for _, item := range []struct {
		id, key, summary string
	}{
		{"project-a", "fact:a", "Project A fact"},
		{"project-b", "fact:b", "Project B fact"},
	} {
		if _, err := db.Exec(`
			INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, item.id, item.id, "", "{}", "{}", "pentest", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		ctx := blackboard.SystemExecutionContext(item.id, "pentest", "rebuild-test")
		if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
			SchemaVersion:  blackboard.GraphMutationSchemaVersion,
			IdempotencyKey: "seed-" + item.id,
			Context:        ctx,
			Operations: []blackboard.Operation{{
				OpID: "fact", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: item.key},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category": "asset", "summary": item.summary, "confidence": "tentative", "scope_status": "in_scope",
				}},
			}},
		}); err != nil {
			t.Fatalf("seed %s: %v", item.id, err)
		}
	}
}

func seedTerminalAndRemovedTypes(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES('project-rebuild','Rebuild','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// Goal node is present in v1 but must not be mapped by #121.
	if _, err := db.Exec(`
		INSERT INTO tasks(id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at)
		VALUES('task-1','project-rebuild','Do work','completed','local','profile','{}','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	ctx := blackboard.SystemExecutionContext("project-rebuild", "pentest", "rebuild-test")
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "terminal-seed",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID: "objective", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:done"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Finished objective", "status": "open"}},
			},
			{
				OpID: "fact", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:support"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "asset", "summary": "Support", "confidence": "tentative", "scope_status": "in_scope"}},
			},
		},
	}); err != nil {
		t.Fatalf("seed terminal objective open: %v", err)
	}
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "terminal-abandon",
		Context:        ctx,
		Operations: []blackboard.Operation{{
			OpID: "abandon", Kind: blackboard.OpTransitionNode,
			Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:done"},
			Transition: blackboard.TransitionNodeInput{
				ExpectedVersion:   1,
				Status:            "abandoned",
				ResolutionSummary: "Abandoned during v1 work",
			},
		}},
	}); err != nil {
		t.Fatalf("abandon objective: %v", err)
	}
}

func snapshotHasRelation(snapshot blackboardv2.RuntimeSnapshot, from, relation, to string) bool {
	for _, item := range snapshot.Relations {
		if len(item) < 3 {
			continue
		}
		gotFrom, _ := item[0].(string)
		gotRel, _ := item[1].(string)
		gotTo, _ := item[2].(string)
		if gotFrom == from && gotRel == relation && gotTo == to {
			return true
		}
	}
	return false
}
