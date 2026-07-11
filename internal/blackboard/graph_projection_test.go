package blackboard_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboard"
)

// TestCanonicalMainGraphGoldenBytesSurviveRandomSQLOrderAndReopen is the C09
// first-red test. It fixes the complete CanonicalMainGraphV1 wire image rather
// than recomputing expectations through the renderer under test.
func TestCanonicalMainGraphGoldenBytesSurviveRandomSQLOrderAndReopen(t *testing.T) {
	graph, projects, path := newGraphServices(t)
	generatedProjectID, execCtx := mustGraphProject(t, projects)
	const projectID = "project_golden"
	if _, err := graph.DBForTesting().Exec(`UPDATE projects SET id=? WHERE id=?`, projectID, generatedProjectID); err != nil {
		t.Fatalf("stabilize fixture project id: %v", err)
	}
	execCtx.ProjectID = projectID

	result, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "c09:golden",
		Context:        execCtx,
		Operations: []blackboard.Operation{
			{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:z-service"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"summary": "TLS is enabled", "scope_status": "in_scope", "category": "service"}}},
			{OpID: "finding", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:a-header"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Header missing"}}},
			{OpID: "supports", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupports, From: blackboard.NodeRef{OpID: "fact"}, To: blackboard.NodeRef{OpID: "finding"}, Summary: "supports finding"}},
			{OpID: "contradicts", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeContradicts, From: blackboard.NodeRef{OpID: "fact"}, To: blackboard.NodeRef{OpID: "finding"}, Summary: "counterpoint"}},
		},
	})
	if err != nil {
		t.Fatalf("seed golden graph: %v", err)
	}

	// Deliberately make SQLite reverse any unordered scan. Canonical ordering
	// must be imposed by the renderer, not inherited from row insertion order.
	if _, err := graph.DBForTesting().Exec(`PRAGMA reverse_unordered_selects=ON`); err != nil {
		t.Fatalf("enable reverse unordered selects: %v", err)
	}
	projection, err := graph.CanonicalMainGraph(context.Background(), projectID, result.GraphRevision)
	if err != nil {
		t.Fatalf("render canonical main graph: %v", err)
	}

	const golden = `{"schema_version":1,"project_id":"project_golden","project_kind":"pentest","graph_revision":1,"nodes":[{"id":"node_2","node_type":"project_fact","stable_key":"fact:z-service","version":1,"disposition":"main","properties":{"category":"service","confidence":"tentative","scope_status":"in_scope","summary":"TLS is enabled"},"created_at":"2024-01-02T03:04:05.000000000Z","updated_at":"2024-01-02T03:04:05.000000000Z","created_provenance":{"actor_type":"system","actor_id":"test-system","task_id":null,"continuation_id":null,"runtime_profile_id":null,"runner":null,"source_event_ids":[],"migration_source":null,"recorded_at":"2024-01-02T03:04:05.000000000Z"},"updated_provenance":{"actor_type":"system","actor_id":"test-system","task_id":null,"continuation_id":null,"runtime_profile_id":null,"runner":null,"source_event_ids":[],"migration_source":null,"recorded_at":"2024-01-02T03:04:05.000000000Z"}},{"id":"mut_1","node_type":"finding","stable_key":"finding:a-header","version":1,"disposition":"main","properties":{"status":"unconfirmed","title":"Header missing"},"created_at":"2024-01-02T03:04:05.000000000Z","updated_at":"2024-01-02T03:04:05.000000000Z","created_provenance":{"actor_type":"system","actor_id":"test-system","task_id":null,"continuation_id":null,"runtime_profile_id":null,"runner":null,"source_event_ids":[],"migration_source":null,"recorded_at":"2024-01-02T03:04:05.000000000Z"},"updated_provenance":{"actor_type":"system","actor_id":"test-system","task_id":null,"continuation_id":null,"runtime_profile_id":null,"runner":null,"source_event_ids":[],"migration_source":null,"recorded_at":"2024-01-02T03:04:05.000000000Z"}}],"edges":[{"id":"prov_1","edge_type":"supports","from_node_id":"node_2","to_node_id":"mut_1","version":1,"state":"active","summary":"supports finding","created_at":"2024-01-02T03:04:05.000000000Z","updated_at":"2024-01-02T03:04:05.000000000Z","created_provenance":{"actor_type":"system","actor_id":"test-system","task_id":null,"continuation_id":null,"runtime_profile_id":null,"runner":null,"source_event_ids":[],"migration_source":null,"recorded_at":"2024-01-02T03:04:05.000000000Z"},"updated_provenance":{"actor_type":"system","actor_id":"test-system","task_id":null,"continuation_id":null,"runtime_profile_id":null,"runner":null,"source_event_ids":[],"migration_source":null,"recorded_at":"2024-01-02T03:04:05.000000000Z"}},{"id":"node_4","edge_type":"contradicts","from_node_id":"node_2","to_node_id":"mut_1","version":1,"state":"active","summary":"counterpoint","created_at":"2024-01-02T03:04:05.000000000Z","updated_at":"2024-01-02T03:04:05.000000000Z","created_provenance":{"actor_type":"system","actor_id":"test-system","task_id":null,"continuation_id":null,"runtime_profile_id":null,"runner":null,"source_event_ids":[],"migration_source":null,"recorded_at":"2024-01-02T03:04:05.000000000Z"},"updated_provenance":{"actor_type":"system","actor_id":"test-system","task_id":null,"continuation_id":null,"runtime_profile_id":null,"runner":null,"source_event_ids":[],"migration_source":null,"recorded_at":"2024-01-02T03:04:05.000000000Z"}}],"frontier_node_ids":[],"current_truth_node_ids":["node_2"]}`
	if !bytes.Equal(projection.Bytes, []byte(golden)) {
		t.Fatalf("canonical bytes mismatch\n got: %s\nwant: %s", projection.Bytes, golden)
	}
	if projection.Hash != "52b869307db404a5415d5343462e665e19ae802beae7d9a71373328af9e8aea4" {
		t.Fatalf("projection hash: got %q", projection.Hash)
	}
	if projection.ByteCount != len(golden) {
		t.Fatalf("byte count: got %d want %d", projection.ByteCount, len(golden))
	}
	if projection.EstimatedTokens != (len(golden)+3)/4 {
		t.Fatalf("token estimate: got %d want %d", projection.EstimatedTokens, (len(golden)+3)/4)
	}
	if projection.RendererVersion != blackboard.CanonicalMainGraphRendererV1 || projection.EstimatorVersion != blackboard.UTF8BytesDiv4EstimatorV1 {
		t.Fatalf("projection versions: got %q/%q", projection.RendererVersion, projection.EstimatorVersion)
	}

	reopened := reopenGraphServices(t, path)
	afterReopen, err := reopened.CanonicalMainGraph(context.Background(), projectID, result.GraphRevision)
	if err != nil {
		t.Fatalf("render after reopen: %v", err)
	}
	if !bytes.Equal(afterReopen.Bytes, projection.Bytes) || afterReopen.Hash != projection.Hash {
		t.Fatalf("projection drift after reopen: before=%q/%s after=%q/%s", projection.Bytes, projection.Hash, afterReopen.Bytes, afterReopen.Hash)
	}
}

func TestPinnedCanonicalMainGraphSurvivesLaterMutationsAndRegeneratesMissingSnapshot(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c09:pin:create", Context: execCtx,
		Operations: []blackboard.Operation{
			{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:pinned"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "pinned", "scope_status": "in_scope"}}},
			{OpID: "later-merge-source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:merge-source"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "source", "scope_status": "in_scope"}}},
		},
	})
	if err != nil {
		t.Fatalf("create pinned revision: %v", err)
	}
	projection, err := graph.CanonicalMainGraph(context.Background(), projectID, created.GraphRevision)
	if err != nil {
		t.Fatalf("render pinned revision: %v", err)
	}
	pin := projection.ImmutablePin()

	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c09:pin:mutate", Context: execCtx,
		Operations: []blackboard.Operation{
			{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:merge-source"}, Canonical: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:pinned"}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}},
		},
	}); err != nil {
		t.Fatalf("apply later mutations: %v", err)
	}

	historical, err := graph.CanonicalMainGraph(context.Background(), projectID, pin.GraphRevision)
	if err != nil {
		t.Fatalf("re-render pinned revision: %v", err)
	}
	if !bytes.Equal(historical.Bytes, projection.Bytes) || historical.Hash != pin.ProjectionHash {
		t.Fatalf("historical pin changed after later mutations")
	}

	snapshotPath := filepath.Join(t.TempDir(), ".pentest", "blackboard.json")
	if err := graph.MaterializeCanonicalMainGraphSnapshot(context.Background(), pin, snapshotPath); err != nil {
		t.Fatalf("materialize missing snapshot: %v", err)
	}
	materialized, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read materialized snapshot: %v", err)
	}
	if !bytes.Equal(materialized, projection.Bytes) {
		t.Fatalf("regenerated bytes differ from pin")
	}
	info, err := os.Stat(snapshotPath)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot permissions: got %o want 600", info.Mode().Perm())
	}
	if err := blackboard.VerifyCanonicalMainGraphSnapshot(pin, snapshotPath); err != nil {
		t.Fatalf("verify snapshot: %v", err)
	}
	unsupported := pin
	unsupported.RendererVersion = "canonical_main_graph_v2"
	if err := blackboard.VerifyCanonicalMainGraphSnapshot(unsupported, snapshotPath); err == nil {
		t.Fatal("expected unsupported renderer pin to fail snapshot verification")
	}
	tampered := append([]byte(nil), materialized...)
	tampered[len(tampered)/2] ^= 1
	if err := os.WriteFile(snapshotPath, tampered, 0o600); err != nil {
		t.Fatalf("tamper snapshot: %v", err)
	}
	if err := blackboard.VerifyCanonicalMainGraphSnapshot(pin, snapshotPath); err == nil {
		t.Fatal("expected hash mismatch to fail snapshot verification")
	} else if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("verify tampered snapshot: got %v, want hash mismatch", err)
	}
}

func TestCanonicalMainGraphPinSurvivesArchiveAndRestore(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c09:lifecycle:create", Context: execCtx,
		Operations: []blackboard.Operation{{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:lifecycle"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Historical finding"}}}},
	})
	if err != nil {
		t.Fatalf("create pinned node: %v", err)
	}
	pinned, err := graph.CanonicalMainGraph(context.Background(), projectID, created.GraphRevision)
	if err != nil {
		t.Fatalf("render pinned revision: %v", err)
	}
	nodeID := created.Operations[0].NodeID
	deprecated, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c09:lifecycle:deprecate", Context: execCtx,
		Operations: []blackboard.Operation{{OpID: "false-positive", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: nodeID}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "false_positive"}}},
	})
	if err != nil {
		t.Fatalf("transition after pin: %v", err)
	}
	archived, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c09:lifecycle:archive", Context: execCtx,
		Operations: []blackboard.Operation{{OpID: "archive", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: nodeID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: deprecated.Operations[0].NodeVersion, Disposition: blackboard.DispositionArchived}}},
	})
	if err != nil {
		t.Fatalf("archive after pin: %v", err)
	}
	execCtx = blackboard.SystemRestoreExecutionContext(projectID, execCtx.ProjectKind, execCtx.ActorID, blackboard.RestoreManifest{ID: "restore:c09", Nodes: []string{nodeID}})
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c09:lifecycle:restore", Context: execCtx,
		Operations: []blackboard.Operation{{OpID: "restore", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: nodeID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: archived.Operations[0].NodeVersion, Disposition: blackboard.DispositionMain, RestoreManifestID: "restore:c09"}}},
	}); err != nil {
		t.Fatalf("restore after pin: %v", err)
	}
	after, err := graph.CanonicalMainGraph(context.Background(), projectID, pinned.GraphRevision)
	if err != nil {
		t.Fatalf("render historical revision after lifecycle changes: %v", err)
	}
	if !bytes.Equal(after.Bytes, pinned.Bytes) || after.Hash != pinned.Hash {
		t.Fatal("archive and restore altered an older canonical graph pin")
	}
}

func TestProjectionSizingFailurePreservesCommittedGraphForRemeasurement(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	first, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c09:dirty:first", Context: execCtx,
		Operations: []blackboard.Operation{{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:dirty"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "measured baseline", "scope_status": "in_scope"}}}},
	})
	if err != nil {
		t.Fatalf("apply first semantic write: %v", err)
	}
	assertGraphProjectionState(t, graph, projectID, first.GraphRevision, "unknown", "", 0, 0)
	if _, err := graph.RemeasureCanonicalMainGraph(context.Background(), projectID); err != nil {
		t.Fatalf("measure baseline graph: %v", err)
	}

	written, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c09:dirty:second", Context: execCtx,
		Operations: []blackboard.Operation{{OpID: "finding", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:dirty"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Committed before sizing"}}}},
	})
	if err != nil {
		t.Fatalf("apply semantic write before failed sizing: %v", err)
	}
	assertGraphProjectionState(t, graph, projectID, written.GraphRevision, "unknown", "", 0, 0)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := graph.RemeasureCanonicalMainGraph(cancelled, projectID); err == nil {
		t.Fatal("expected cancelled projection sizing to fail")
	}
	assertGraphProjectionState(t, graph, projectID, written.GraphRevision, "unknown", "", 0, 0)

	measured, err := graph.RemeasureCanonicalMainGraph(context.Background(), projectID)
	if err != nil {
		t.Fatalf("remeasure committed graph: %v", err)
	}
	assertGraphProjectionState(t, graph, projectID, 0, "within_target", measured.Hash, measured.ByteCount, measured.EstimatedTokens)
}

func assertGraphProjectionState(t *testing.T, graph *blackboard.GraphService, projectID string, wantDirtyRevision int, wantBudget, wantHash string, wantBytes, wantTokens int) {
	t.Helper()
	var dirtyRevision int
	var budget, hash string
	var projectionBytes, tokens sql.NullInt64
	if err := graph.DBForTesting().QueryRow(`SELECT projection_dirty_revision,budget_state,COALESCE(current_main_projection_hash,''),projection_bytes,projection_estimated_tokens FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&dirtyRevision, &budget, &hash, &projectionBytes, &tokens); err != nil {
		t.Fatalf("read graph projection state: %v", err)
	}
	if dirtyRevision != wantDirtyRevision || budget != wantBudget || hash != wantHash || int(projectionBytes.Int64) != wantBytes || int(tokens.Int64) != wantTokens {
		t.Fatalf("projection state: dirty=%d budget=%q hash=%q bytes=%v tokens=%v", dirtyRevision, budget, hash, projectionBytes, tokens)
	}
}
