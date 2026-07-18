package blackboardmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
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

func TestRebuildOmitsGoalsAndGoalOnlyEdges(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedGoalsAndGoalOnlyEdges(t, db, artifactRoot)

	rebuildResult, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if err != nil {
		t.Fatalf("Execute(rebuild): %v", err)
	}
	for _, mapping := range rebuildResult.Rebuild.Mappings {
		if mapping.SourceType == "goal" {
			t.Fatalf("Goal was copied into v2 mappings: %#v", mapping)
		}
	}

	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", "task:task-1:goal"); err == nil {
		t.Fatal("Task Goal must not become a current v2 record")
	}
	projection, err := v2.ProjectRuntimeSnapshot(context.Background(), "project-rebuild")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range projection.Snapshot.Relations {
		if len(rel) < 3 {
			continue
		}
		from, _ := rel[0].(string)
		to, _ := rel[2].(string)
		if strings.Contains(from, "goal") || strings.Contains(to, "goal") {
			t.Fatalf("Goal-only edge leaked into v2 relations: %#v", rel)
		}
	}
	// Unrelated reusable state still rebuilds.
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", "objective:open"); err != nil {
		t.Fatalf("open objective should rebuild: %v", err)
	}
}

func TestRebuildMapsObservationsByConfidenceAndSupport(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedObservationMappingGraph(t, db, artifactRoot)

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if err != nil {
		t.Fatalf("Execute(rebuild): %v", err)
	}
	if result.Rebuild.Status != "rebuilt" {
		t.Fatalf("rebuild = %#v", result.Rebuild)
	}

	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	tentative, err := v2.ReadCurrent(context.Background(), "project-rebuild", "observation:plain")
	if err != nil {
		t.Fatalf("plain observation as fact: %v", err)
	}
	if tentative.Type != "fact" || tentative.Record.Confidence != "tentative" || tentative.Record.Summary != "Plain observed result" {
		t.Fatalf("tentative observation mapping = %#v", tentative)
	}
	confirmed, err := v2.ReadCurrent(context.Background(), "project-rebuild", "observation:supported")
	if err != nil {
		t.Fatalf("supported observation as fact: %v", err)
	}
	if confirmed.Type != "fact" || confirmed.Record.Confidence != "confirmed" {
		t.Fatalf("supported observation should confirm: %#v", confirmed)
	}
	// Superseded observation is discarded from current knowledge.
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", "observation:old"); err == nil {
		t.Fatal("superseded observation must not be current")
	}
}

func TestRebuildRequiresSourceDigestBoundDecisionsForAmbiguousAndActiveWorkflow(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedDecisionBoundWorkflowGraph(t, db, artifactRoot)

	// Without decisions, rebuild blocks and leaves disposable v2 empty.
	blocked, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if !errors.Is(err, ErrRebuildBlocked) {
		t.Fatalf("error = %v, want ErrRebuildBlocked", err)
	}
	assertRebuildBlockerCodes(t, blocked.Rebuild.Blockers,
		"missing_decision",
	)
	assertNoDisposableV2Commit(t, db, "project-rebuild")

	inspect, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatal(err)
	}
	digest := inspect.Plan.SourceDigest
	if digest == "" {
		t.Fatal("inspect source digest required")
	}

	// Stale digest blocks.
	stale, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindRebuildUnambiguous,
		SourceDigest: "sha256:" + strings.Repeat("0", 64),
		Decisions: []MigrationDecision{
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "observation", Key: "observation:ambiguous"}, Decision: "tentative_fact"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:active"}, Decision: "objective"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "project_directive", Key: "directive:active"}, Decision: "objective"},
		},
	})
	if !errors.Is(err, ErrRebuildBlocked) {
		t.Fatalf("stale digest error = %v", err)
	}
	assertRebuildBlockerCodes(t, stale.Rebuild.Blockers, "stale_source_digest")
	assertNoDisposableV2Commit(t, db, "project-rebuild")

	// Disallowed / unknown / duplicate / wrong-source decisions block.
	invalid, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindRebuildUnambiguous,
		SourceDigest: digest,
		Decisions: []MigrationDecision{
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "observation", Key: "observation:ambiguous"}, Decision: "discard"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:active"}, Decision: "objective"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:active"}, Decision: "tentative_fact"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "project_directive", Key: "directive:active"}, Decision: "objective"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:missing"}, Decision: "discard"},
			{Source: MigrationSourceRef{Project: "project-other", Type: "hypothesis", Key: "hypothesis:active"}, Decision: "discard"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "legacy_unknown", Key: "x"}, Decision: "discard"},
		},
	})
	if !errors.Is(err, ErrRebuildBlocked) {
		t.Fatalf("invalid decisions error = %v", err)
	}
	assertRebuildBlockerCodes(t, invalid.Rebuild.Blockers,
		"disallowed_decision", "duplicate_decision", "unknown_decision", "wrong_source",
	)
	assertNoDisposableV2Commit(t, db, "project-rebuild")

	// The digest covers graph-v1 semantic rows, not only legacy source tables.
	var hypothesisID string
	if err := db.QueryRow(`SELECT id FROM blackboard_nodes WHERE project_id='project-rebuild' AND original_stable_key='hypothesis:active'`).Scan(&hypothesisID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO blackboard_node_versions(
			project_id,node_id,version,result_graph_revision,mutation_seq,operation_index,
			schema_version,disposition,merge_target_id,properties_json,semantic_hash,updated_at
		)
		SELECT project_id,node_id,2,result_graph_revision,mutation_seq,operation_index,
			schema_version,disposition,merge_target_id,?,semantic_hash,'2026-01-03T00:00:00Z'
		FROM blackboard_node_versions
		WHERE project_id='project-rebuild' AND node_id=? AND version=1`,
		`{"statement":"Login may be injectable after source mutation","status":"open"}`,
		hypothesisID,
	); err != nil {
		t.Fatalf("append graph-v1 decision source version: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_node_heads SET version=2 WHERE project_id='project-rebuild' AND node_id=?`, hypothesisID); err != nil {
		t.Fatalf("advance graph-v1 decision source head: %v", err)
	}
	mutated, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindRebuildUnambiguous,
		SourceDigest: digest,
		Decisions: []MigrationDecision{
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "observation", Key: "observation:ambiguous"}, Decision: "confirmed_fact", TargetKey: "fact:from-obs"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:active"}, Decision: "objective"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "project_directive", Key: "directive:active"}, Decision: "scope_limit"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:noise"}, Decision: "discard"},
		},
	})
	if !errors.Is(err, ErrRebuildBlocked) {
		t.Fatalf("mutated source digest error = %v", err)
	}
	assertRebuildBlockerCodes(t, mutated.Rebuild.Blockers, "stale_source_digest")
	inspect, err = service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatal(err)
	}
	digest = inspect.Plan.SourceDigest

	// Accepted decisions rebuild successfully.
	ok, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindRebuildUnambiguous,
		SourceDigest: digest,
		Decisions: []MigrationDecision{
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "observation", Key: "observation:ambiguous"}, Decision: "confirmed_fact", TargetKey: "fact:from-obs"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:active"}, Decision: "objective"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "project_directive", Key: "directive:active"}, Decision: "scope_limit"},
			{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:noise"}, Decision: "discard"},
		},
	})
	if err != nil {
		t.Fatalf("accepted decisions rebuild: %v blockers=%#v", err, ok.Rebuild)
	}
	if ok.Rebuild.Status != "rebuilt" {
		t.Fatalf("rebuild status = %#v", ok.Rebuild)
	}

	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	fact, err := v2.ReadCurrent(context.Background(), "project-rebuild", "fact:from-obs")
	if err != nil {
		t.Fatalf("ambiguous observation decision target: %v", err)
	}
	if fact.Type != "fact" || fact.Record.Confidence != "confirmed" {
		t.Fatalf("confirmed_fact decision = %#v", fact)
	}
	objective, err := v2.ReadCurrent(context.Background(), "project-rebuild", "hypothesis:active")
	if err != nil {
		t.Fatalf("hypothesis->objective: %v", err)
	}
	if objective.Type != "objective" || objective.Record.Status != "open" || objective.Record.Objective != "Login may be injectable after source mutation" {
		t.Fatalf("hypothesis objective mapping = %#v", objective)
	}
	// scope_limit never creates a v2 Directive/Objective record from that key.
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", "directive:active"); err == nil {
		t.Fatal("scope_limit must not create a v2 semantic Directive record")
	}
	var scopeJSON string
	if err := db.QueryRow(`SELECT scope_json FROM projects WHERE id='project-rebuild'`).Scan(&scopeJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(scopeJSON, "Keep admin testing bounded") {
		t.Fatalf("disposable rebuild mutated authoritative Project Scope: %s", scopeJSON)
	}
	var stagedLimit string
	if err := db.QueryRow(`SELECT limit_text FROM blackboard_v2_rebuild_scope_limits WHERE project_id='project-rebuild'`).Scan(&stagedLimit); err != nil {
		t.Fatal(err)
	}
	if stagedLimit != "Keep admin testing bounded" {
		t.Fatalf("staged scope limit = %q", stagedLimit)
	}
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", "hypothesis:noise"); err == nil {
		t.Fatal("discarded hypothesis must not be current")
	}
	// proposed / retired directives discarded without decision.
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", "directive:proposed"); err == nil {
		t.Fatal("proposed directive must be discarded")
	}
	if _, err := v2.ReadCurrent(context.Background(), "project-rebuild", "directive:retired"); err == nil {
		t.Fatal("retired directive must be discarded")
	}
}

func TestRebuildReversesBlocksDropsLeadsToAndRejectsCycles(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedBlocksAndLeadsToGraph(t, db, artifactRoot, false)

	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous}); err != nil {
		t.Fatalf("Execute(rebuild): %v", err)
	}
	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	projection, err := v2.ProjectRuntimeSnapshot(context.Background(), "project-rebuild")
	if err != nil {
		t.Fatal(err)
	}
	// A blocks B => B depends_on A
	if !snapshotHasRelation(projection.Snapshot, "objective:blocked", "depends_on", "objective:blocker") {
		t.Fatalf("blocks was not reversed into depends_on: %s", projection.Bytes)
	}
	if snapshotHasRelation(projection.Snapshot, "objective:blocker", "blocks", "objective:blocked") {
		t.Fatal("blocks must not remain as a v2 relation type")
	}
	for _, rel := range projection.Snapshot.Relations {
		if len(rel) >= 2 {
			if got, _ := rel[1].(string); got == "leads_to" {
				t.Fatalf("leads_to must be removed: %#v", rel)
			}
		}
	}

	// Cycle through blocks + depends_on is a blocker with no commit.
	db2, service2, artifactRoot2 := newGraphV1RebuildService(t)
	seedBlocksAndLeadsToGraph(t, db2, artifactRoot2, true)
	cycled, err := service2.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if !errors.Is(err, ErrRebuildBlocked) {
		t.Fatalf("cycle error = %v, want ErrRebuildBlocked", err)
	}
	assertRebuildBlockerCodes(t, cycled.Rebuild.Blockers, "relationship_cycle")
	assertNoDisposableV2Commit(t, db2, "project-rebuild")
}

func TestRebuildMapsReusableTerminalSummariesWithoutOutcomesToTentativeFacts(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot := newGraphV1RebuildService(t)
	seedTerminalSummariesWithoutOutcomes(t, db, artifactRoot)

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindRebuildUnambiguous})
	if err != nil {
		t.Fatalf("Execute(rebuild): %v", err)
	}
	if result.Rebuild.Status != "rebuilt" {
		t.Fatalf("rebuild = %#v", result.Rebuild)
	}

	v2 := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	// Terminal Attempt summary without produced outcomes becomes a conservative Fact.
	found := false
	for _, mapping := range result.Rebuild.Mappings {
		if mapping.Action == "terminal_summary_fact" && strings.Contains(mapping.SourceKey, "attempt:dead-end") {
			found = true
			detail, err := v2.ReadCurrent(context.Background(), "project-rebuild", mapping.TargetKey)
			if err != nil {
				t.Fatalf("terminal summary fact: %v", err)
			}
			if detail.Type != "fact" || detail.Record.Confidence != "tentative" || detail.Record.Summary != "Attempt concluded without durable outcome records" {
				t.Fatalf("terminal summary fact detail = %#v", detail)
			}
		}
	}
	if !found {
		t.Fatalf("expected terminal_summary_fact mapping, got %#v", result.Rebuild.Mappings)
	}
	// Terminal workflow Attempt itself is history-only, not current work.
	projection, err := v2.ProjectRuntimeSnapshot(context.Background(), "project-rebuild")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Snapshot.Work.Attempts != nil {
		if _, ok := projection.Snapshot.Work.Attempts["attempt:dead-end"]; ok {
			t.Fatal("terminal attempt must not remain current work")
		}
	}
	history, err := v2.ReadHistory(context.Background(), "project-rebuild", "attempt:dead-end", blackboardv2.HistoryOptions{Limit: 10})
	if err != nil || len(history.Items) == 0 {
		t.Fatalf("terminal attempt should be in Semantic History: err=%v history=%#v", err, history)
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

func assertRebuildBlockerCodes(t *testing.T, blockers []MigrationBlocker, want ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, blocker := range blockers {
		got[blocker.Code] = true
	}
	for _, code := range want {
		if !got[code] {
			t.Fatalf("missing blocker %q in %#v", code, blockers)
		}
	}
}

func assertNoDisposableV2Commit(t *testing.T, db *store.DB, projectID string) {
	t.Helper()
	var records, relations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_records WHERE project_id=?`, projectID).Scan(&records); err != nil {
		// Table may not exist if ensure never committed; treat as empty.
		if !strings.Contains(err.Error(), "no such table") {
			t.Fatal(err)
		}
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_relationships WHERE project_id=?`, projectID).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if records != 0 || relations != 0 {
		t.Fatalf("blocked rebuild committed disposable v2 state: records=%d relations=%d", records, relations)
	}
	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epoch != store.CanonicalStoreGraphV1 {
		t.Fatalf("blocked rebuild mutated epoch to %q", epoch)
	}
}

func seedUnambiguousGraphHeads(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	payload := []byte("hello world\n")
	if err := os.MkdirAll(filepath.Join(artifactRoot, "evidence"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "evidence", "login.html"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	payloadHash := sha256.Sum256(payload)
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
					"sha256":        hex.EncodeToString(payloadHash[:]),
					"size_bytes":    float64(len(payload)),
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

func seedGoalsAndGoalOnlyEdges(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES('project-rebuild','Rebuild','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO tasks(id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at)
		VALUES('task-1','project-rebuild','Do work','running','local','profile','{}','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	ctx := blackboard.SystemExecutionContext("project-rebuild", "pentest", "rebuild-test")
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "goal-seed-objective",
		Context:        ctx,
		Operations: []blackboard.Operation{{
			OpID: "objective", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:open"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Open work", "status": "open"}},
		}},
	}); err != nil {
		t.Fatalf("seed objective: %v", err)
	}
	// Goals are system-owned Task projections; insert the v1 Goal head directly.
	goalID := "node-goal-1"
	objID := ""
	if err := db.QueryRow(`SELECT id FROM blackboard_nodes WHERE project_id='project-rebuild' AND original_stable_key='objective:open'`).Scan(&objID); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO blackboard_nodes(project_id,id,node_type,original_stable_key,created_mutation_seq,created_operation_index,created_at)
		 VALUES('project-rebuild','` + goalID + `','goal','task:task-1:goal',50,0,'2026-01-01T00:00:00Z')`,
		`INSERT INTO blackboard_node_versions(project_id,node_id,version,result_graph_revision,mutation_seq,operation_index,schema_version,disposition,merge_target_id,properties_json,semantic_hash,updated_at)
		 VALUES('project-rebuild','` + goalID + `',1,50,50,0,1,'main',NULL,'{"task_id":"task-1","text":"Do work","task_status":"running"}','00','2026-01-01T00:00:00Z')`,
		`INSERT INTO blackboard_node_heads(project_id,node_id,node_type,version,graph_revision,disposition,semantic_hash)
		 VALUES('project-rebuild','` + goalID + `','goal',1,50,'main','00')`,
		`INSERT INTO blackboard_edges(project_id,id,edge_type,created_mutation_seq,created_operation_index,created_at)
		 VALUES('project-rebuild','edge-part-goal','part_of',51,0,'2026-01-01T00:00:00Z')`,
		`INSERT INTO blackboard_edge_versions(project_id,edge_id,version,result_graph_revision,mutation_seq,operation_index,from_node_id,to_node_id,state,summary,semantic_hash,updated_at)
		 VALUES('project-rebuild','edge-part-goal',1,51,51,0,'` + objID + `','` + goalID + `','active','','00','2026-01-01T00:00:00Z')`,
		`INSERT INTO blackboard_edge_heads(project_id,edge_id,edge_type,from_node_id,to_node_id,version,graph_revision,state,semantic_hash)
		 VALUES('project-rebuild','edge-part-goal','part_of','` + objID + `','` + goalID + `',1,51,'active','00')`,
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed goal sql: %v\n%s", err, statement)
		}
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
}

func seedObservationMappingGraph(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES('project-rebuild','Rebuild','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	ctx := blackboard.SystemExecutionContext("project-rebuild", "pentest", "rebuild-test")
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "obs-seed",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID: "attempt", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:obs"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"status": "open", "summary": "Collect observations"}},
			},
			{
				OpID: "objective", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:obs"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Observe auth", "status": "open"}},
			},
			{
				OpID: "tests", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeTests, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{OpID: "objective"}},
			},
			{
				OpID: "plain", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeObservation, StableKey: "observation:plain"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"summary": "Plain observed result", "scope_status": "in_scope"}},
			},
			{
				OpID: "produced-plain", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{OpID: "plain"}},
			},
			{
				OpID: "supported", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeObservation, StableKey: "observation:supported"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"summary": "Supported observed result", "scope_status": "in_scope"}},
			},
			{
				OpID: "produced-supported", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{OpID: "supported"}},
			},
			{
				OpID: "evidence", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:support"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"artifact_type": "http_exchange", "summary": "Proof", "managed_path": "evidence/support.html",
					"sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "size_bytes": float64(4), "status": "available",
				}},
			},
			{
				OpID: "evidences", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence"}, To: blackboard.NodeRef{OpID: "supported"}},
			},
			{
				OpID: "old", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeObservation, StableKey: "observation:old"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"summary": "Old observation", "scope_status": "in_scope", "status": "recorded"}},
			},
			{
				OpID: "produced-old", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{OpID: "old"}},
			},
		},
	}); err != nil {
		t.Fatalf("seed observations: %v", err)
	}
	// Observations do not support transition_node; mark retired/superseded state
	// by archiving the head (discarded by migration disposition rules).
	if _, err := db.Exec(`
		UPDATE blackboard_node_heads SET disposition='archived'
		WHERE project_id='project-rebuild'
		  AND node_id=(SELECT id FROM blackboard_nodes WHERE project_id='project-rebuild' AND original_stable_key='observation:old')`); err != nil {
		t.Fatal(err)
	}
}

func seedDecisionBoundWorkflowGraph(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES('project-rebuild','Rebuild','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	ctx := blackboard.SystemExecutionContext("project-rebuild", "pentest", "rebuild-test")
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "decision-seed",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID: "obs", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeObservation, StableKey: "observation:ambiguous"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"summary": "Ambiguous confidence observation", "scope_status": "in_scope",
				}},
			},
			{
				OpID: "hyp", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeHypothesis, StableKey: "hypothesis:active"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"statement": "Login may be injectable", "status": "open",
				}},
			},
			{
				OpID: "hyp-noise", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeHypothesis, StableKey: "hypothesis:noise"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"statement": "No reusable meaning", "status": "inconclusive",
				}},
			},
			{
				OpID: "dir", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectDirective, StableKey: "directive:active"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"directive": "Keep admin testing bounded", "status": "active",
				}},
			},
			{
				OpID: "dir-proposed", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectDirective, StableKey: "directive:proposed"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"directive": "Proposed only", "status": "proposed",
				}},
			},
			{
				OpID: "dir-retired", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectDirective, StableKey: "directive:retired"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"directive": "Retired steer", "status": "retired",
				}},
			},
		},
	}); err != nil {
		t.Fatalf("seed decision graph: %v", err)
	}
	// Graph schema rejects unknown Observation confidence; inject ambiguous
	// confidence through a new append-only version for the migration decision path.
	var nodeID string
	if err := db.QueryRow(`SELECT id FROM blackboard_nodes WHERE project_id='project-rebuild' AND original_stable_key='observation:ambiguous'`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO blackboard_node_versions(
			project_id,node_id,version,result_graph_revision,mutation_seq,operation_index,
			schema_version,disposition,merge_target_id,properties_json,semantic_hash,updated_at
		)
		SELECT project_id,node_id,2,result_graph_revision,mutation_seq,operation_index,
			schema_version,disposition,merge_target_id,?,semantic_hash,'2026-01-02T00:00:00Z'
		FROM blackboard_node_versions
		WHERE project_id='project-rebuild' AND node_id=? AND version=1`,
		`{"summary":"Ambiguous confidence observation","scope_status":"in_scope","status":"recorded","confidence":"ambiguous"}`,
		nodeID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE blackboard_node_heads SET version=2
		WHERE project_id='project-rebuild' AND node_id=?`, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
}

func seedBlocksAndLeadsToGraph(t *testing.T, db *store.DB, artifactRoot string, withCycle bool) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES('project-rebuild','Rebuild','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	ctx := blackboard.SystemExecutionContext("project-rebuild", "pentest", "rebuild-test")
	ops := []blackboard.Operation{
		{
			OpID: "blocker", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:blocker"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Blocker objective", "status": "open"}},
		},
		{
			OpID: "blocked", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:blocked"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Blocked objective", "status": "open"}},
		},
		{
			OpID: "blocks", Kind: blackboard.OpPutEdge,
			PutEdge: blackboard.PutEdgeInput{
				EdgeType: blackboard.EdgeTypeBlocks,
				From:     blackboard.NodeRef{OpID: "blocker"},
				To:       blackboard.NodeRef{OpID: "blocked"},
			},
		},
		{
			OpID: "leads", Kind: blackboard.OpPutEdge,
			PutEdge: blackboard.PutEdgeInput{
				EdgeType: blackboard.EdgeTypeLeadsTo,
				From:     blackboard.NodeRef{OpID: "blocker"},
				To:       blackboard.NodeRef{OpID: "blocked"},
				Summary:  "vague chain",
			},
		},
	}
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "blocks-seed",
		Context:        ctx,
		Operations:     ops,
	}); err != nil {
		t.Fatalf("seed blocks graph: %v", err)
	}
	if withCycle {
		// Graph service rejects cycles; inject a depends_on that closes the
		// cycle with reversed blocks for rebuild validation.
		var blockerID, blockedID string
		if err := db.QueryRow(`SELECT id FROM blackboard_nodes WHERE project_id='project-rebuild' AND original_stable_key='objective:blocker'`).Scan(&blockerID); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT id FROM blackboard_nodes WHERE project_id='project-rebuild' AND original_stable_key='objective:blocked'`).Scan(&blockedID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO blackboard_edges(project_id,id,edge_type,created_mutation_seq,created_operation_index,created_at)
			VALUES('project-rebuild','edge-cycle-depends','depends_on',80,0,'2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO blackboard_edge_versions(project_id,edge_id,version,result_graph_revision,mutation_seq,operation_index,from_node_id,to_node_id,state,summary,semantic_hash,updated_at)
			VALUES('project-rebuild','edge-cycle-depends',1,80,80,0,?,?, 'active','','00','2026-01-01T00:00:00Z')`, blockerID, blockedID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO blackboard_edge_heads(project_id,edge_id,edge_type,from_node_id,to_node_id,version,graph_revision,state,semantic_hash)
			VALUES('project-rebuild','edge-cycle-depends','depends_on',?,?,1,80,'active','00')`, blockerID, blockedID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			t.Fatal(err)
		}
	}
}

func seedTerminalSummariesWithoutOutcomes(t *testing.T, db *store.DB, artifactRoot string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES('project-rebuild','Rebuild','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
	ctx := blackboard.SystemExecutionContext("project-rebuild", "pentest", "rebuild-test")
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "terminal-summary-seed",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID: "objective", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:probe"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Probe without outcome", "status": "open"}},
			},
			{
				OpID: "attempt", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:dead-end"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"status": "open", "summary": "Open probe"}},
			},
			{
				OpID: "tests", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{
					EdgeType: blackboard.EdgeTypeTests,
					From:     blackboard.NodeRef{OpID: "attempt"},
					To:       blackboard.NodeRef{OpID: "objective"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("seed terminal summary open: %v", err)
	}
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "terminal-summary-fail",
		Context:        ctx,
		Operations: []blackboard.Operation{{
			OpID: "fail", Kind: blackboard.OpTransitionNode,
			Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:dead-end"},
			Transition: blackboard.TransitionNodeInput{
				ExpectedVersion: 1,
				Status:          "failed",
				Summary:         "Attempt concluded without durable outcome records",
			},
		}},
	}); err != nil {
		t.Fatalf("terminal attempt fail: %v", err)
	}
}
