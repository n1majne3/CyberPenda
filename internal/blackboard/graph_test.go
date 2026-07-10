package blackboard_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/store"
)

// newGraphServices opens a file-backed SQLite database and returns a graph
// service wired with deterministic clock and ID sources plus the project
// service used to seed the owning Project. The file path is returned so tests
// can reopen the database to prove reopen/historical stability.
func newGraphServices(t *testing.T) (*blackboard.GraphService, *project.Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	clock := blackboard.NewSequenceClock(
		"2024-01-02T03:04:05.000000000Z",
		"2024-01-02T03:04:06.000000000Z",
		"2024-01-02T03:04:07.000000000Z",
		"2024-01-02T03:04:08.000000000Z",
		"2024-01-02T03:04:09.000000000Z",
		"2024-01-02T03:04:10.000000000Z",
	)
	ids := blackboard.NewSequenceIDSource("node_1", "node_2", "node_3", "mut_1", "mut_2", "prov_1", "prov_2")
	return blackboard.NewGraphService(db, clock, ids), project.NewService(db), path
}

// reopenGraphServices reopens the database at path and returns a graph service
// wired with fresh deterministic sources so reopen behavior is observable.
func reopenGraphServices(t *testing.T, path string) *blackboard.GraphService {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	clock := blackboard.NewSequenceClock(
		"2024-02-02T03:04:05.000000000Z",
		"2024-02-02T03:04:06.000000000Z",
		"2024-02-02T03:04:07.000000000Z",
	)
	ids := blackboard.NewSequenceIDSource("node_late_1", "node_late_2", "mut_late_1", "prov_late_1")
	return blackboard.NewGraphService(db, clock, ids)
}

func mustGraphProject(t *testing.T, projects *project.Service) (string, blackboard.ExecutionContext) {
	t.Helper()
	proj, err := projects.Create("P", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	ctx := blackboard.SystemExecutionContext(proj.ID, proj.Kind, "test-system")
	return proj.ID, ctx
}

func validFactBatch(projectID string, ctx blackboard.ExecutionContext) blackboard.MutationBatch {
	return blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "task-1:create-fact",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID:   "fact",
				Kind:   blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "dns:example.com"},
				Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "dns", Summary: "example.com resolves to 1.2.3.4", Body: "full details", ScopeStatus: blackboard.ScopeStatusInScope}},
			},
		},
	}
}

// TestApplyCreatesTentativeProjectFactAndReadReturnsItAfterReopen is the C02
// first-red test: creating a tentative ProjectFact returns version 1 and graph
// revision 1, and reopening the database returns the same semantic record and
// the same hashes.
func TestApplyCreatesTentativeProjectFactAndReadReturnsItAfterReopen(t *testing.T) {
	graph, projects, path := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)

	result, err := graph.Apply(context.Background(), validFactBatch(projectID, ctx))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.GraphRevision != 1 {
		t.Fatalf("graph revision: got %d want 1", result.GraphRevision)
	}
	if len(result.Operations) != 1 || result.Operations[0].NodeVersion != 1 {
		t.Fatalf("expected one op result at version 1, got %+v", result.Operations)
	}
	if result.Operations[0].StableKey != "dns:example.com" {
		t.Fatalf("stable key: got %q want dns:example.com", result.Operations[0].StableKey)
	}
	firstResultHash := result.ResultHash
	if firstResultHash == "" {
		t.Fatal("expected non-empty result hash")
	}
	firstStateHash := result.ResultingStateHash
	if firstStateHash == "" {
		t.Fatal("expected non-empty state hash")
	}

	reopened := reopenGraphServices(t, path)
	read, err := reopened.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: projectID,
		NodeType:  blackboard.NodeTypeProjectFact,
		Key:       "dns:example.com",
	})
	if err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	if read.Node.StableKey != "dns:example.com" {
		t.Fatalf("stable key after reopen: got %q want dns:example.com", read.Node.StableKey)
	}
	props := read.Node.ProjectFact
	if props.Category != "dns" || props.Summary != "example.com resolves to 1.2.3.4" || props.Body != "full details" {
		t.Fatalf("properties after reopen: got %+v", props)
	}
	if props.Confidence != blackboard.ConfidenceTentative {
		t.Fatalf("confidence default: got %q want tentative", props.Confidence)
	}
	if props.ScopeStatus != blackboard.ScopeStatusInScope {
		t.Fatalf("scope status default: got %q want in_scope", props.ScopeStatus)
	}
	if read.Node.Version != 1 {
		t.Fatalf("version after reopen: got %d want 1", read.Node.Version)
	}
	if read.Node.Disposition != blackboard.DispositionMain {
		t.Fatalf("disposition after reopen: got %q want main", read.Node.Disposition)
	}
	if read.ObservedGraphRevision != 1 {
		t.Fatalf("observed graph revision: got %d want 1", read.ObservedGraphRevision)
	}
	if read.Node.SemanticHash != result.Operations[0].SemanticHash {
		t.Fatalf("semantic hash drift after reopen: head %q result %q", read.Node.SemanticHash, result.Operations[0].SemanticHash)
	}
	// The semantic content is what matters on reopen; the state hash is stable
	// across reopen because it excludes server-generated IDs/timestamps.
	if read.Node.StateHash != firstStateHash {
		t.Fatalf("state hash drift after reopen: got %q want %q", read.Node.StateHash, firstStateHash)
	}
	_ = firstResultHash
}

// TestApplyProjectFactCreateReturnsVersion1GraphRevision1 proves a valid
// ProjectFact create returns version 1 and graph revision 1.
func TestApplyProjectFactCreateReturnsVersion1GraphRevision1(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)

	result, err := graph.Apply(context.Background(), validFactBatch(projectID, ctx))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.GraphRevision != 1 {
		t.Fatalf("graph revision: got %d want 1", result.GraphRevision)
	}
	if len(result.Operations) != 1 {
		t.Fatalf("op results: got %d want 1", len(result.Operations))
	}
	if result.Operations[0].NodeVersion != 1 {
		t.Fatalf("node version: got %d want 1", result.Operations[0].NodeVersion)
	}
	if result.Operations[0].NodeID == "" {
		t.Fatal("expected node id")
	}
	if result.RequestHash == "" || result.ResultHash == "" {
		t.Fatal("expected non-empty request/result hashes")
	}
}

// TestApplyFailedBatchLeavesNoMutationVersionKeyOrHead proves a validation
// failure rolls back every effect: no mutation, version, key event, head, or
// registry row is written for the rejected batch.
func TestApplyFailedBatchLeavesNoMutationVersionKeyOrHead(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	db := graph.DBForTesting()
	projectID, ctx := mustGraphProject(t, projects)

	// A ProjectFact create with a missing required summary fails validation.
	batch := blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "task-1:bad-fact",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID:   "fact",
				Kind:   blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "dns:missing.example.com"},
				Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "dns"}},
			},
		},
	}
	_, err := graph.Apply(context.Background(), batch)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	var verr *blackboard.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if verr.Code != blackboard.ErrCodeMissingProperty {
		t.Fatalf("error code: got %q want %q", verr.Code, blackboard.ErrCodeMissingProperty)
	}

	for _, check := range []struct {
		table string
		where string
	}{
		{"blackboard_graph_mutations", "project_id = ?"},
		{"blackboard_node_versions", "project_id = ?"},
		{"blackboard_key_events", "project_id = ?"},
		{"blackboard_node_heads", "project_id = ?"},
		{"blackboard_key_registry", "project_id = ?"},
		{"blackboard_nodes", "project_id = ?"},
		{"blackboard_graph_operations", "project_id = ?"},
		{"blackboard_graph_provenance", "project_id = ?"},
	} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+check.table+" WHERE "+check.where, projectID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", check.table, err)
		}
		if n != 0 {
			t.Fatalf("%s has %d rows after failed batch; want 0", check.table, n)
		}
	}
}

// TestApplyCrossProjectReferenceFailsBeforeAnyStateChange proves a mutation
// bound to one Project cannot create state scoped to another Project; the
// rejection happens before any ledger row is written.
func TestApplyCrossProjectReferenceFailsBeforeAnyStateChange(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	db := graph.DBForTesting()
	projectID, ctx := mustGraphProject(t, projects)

	// Create a second project. The caller declares intent to mutate it
	// (batch.ProjectID = other) while the trusted context is bound to the
	// first project. The authoritative context wins: project_mismatch before
	// any state change.
	other, err := projects.Create("Other", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	batch := validFactBatch(projectID, ctx)
	batch.ProjectID = other.ID

	_, err = graph.Apply(context.Background(), batch)
	if err == nil {
		t.Fatal("expected project mismatch error, got nil")
	}
	var verr *blackboard.ValidationError
	if !errors.As(err, &verr) || verr.Code != blackboard.ErrCodeProjectMismatch {
		t.Fatalf("expected project_mismatch, got %v", err)
	}

	for _, table := range []string{
		"blackboard_graph_mutations",
		"blackboard_node_versions",
		"blackboard_key_events",
		"blackboard_node_heads",
		"blackboard_key_registry",
	} {
		for _, pid := range []string{projectID, other.ID} {
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project_id = ?", pid).Scan(&n); err != nil {
				t.Fatalf("count %s for %s: %v", table, pid, err)
			}
			if n != 0 {
				t.Fatalf("%s has %d rows for %s after rejected cross-project batch; want 0", table, n, pid)
			}
		}
	}
}

// TestApplyRejectsUnknownProperty proves the closed node envelope rejects
// unknown properties for schema version 1.
func TestApplyRejectsUnknownProperty(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	_, ctx := mustGraphProject(t, projects)

	// Construct a property set carrying an unknown field via the raw properties
	// path. The service treats extra properties as unknown_property.
	batch := blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "task-1:unknown-prop",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{
				OpID:   "fact",
				Kind:   blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "dns:unknown.example.com"},
				Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "dns", Summary: "x"}, ExtraProperties: map[string]any{"bogus": "no"}},
			},
		},
	}
	_, err := graph.Apply(context.Background(), batch)
	if err == nil {
		t.Fatal("expected unknown_property error, got nil")
	}
	var verr *blackboard.ValidationError
	if !errors.As(err, &verr) || verr.Code != blackboard.ErrCodeUnknownProperty {
		t.Fatalf("expected unknown_property, got %v", err)
	}
}

// TestApplyStableKeyConflict proves a duplicate create against a live stable
// key returns node_key_conflict.
func TestApplyStableKeyConflict(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)

	first := validFactBatch(projectID, ctx)
	if _, err := graph.Apply(context.Background(), first); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Second create under a fresh idempotency key but the same stable key must
	// conflict rather than silently update.
	dup := validFactBatch(projectID, ctx)
	dup.IdempotencyKey = "task-1:create-fact-again"
	_, err := graph.Apply(context.Background(), dup)
	if err == nil {
		t.Fatal("expected node_key_conflict, got nil")
	}
	var verr *blackboard.ValidationError
	if !errors.As(err, &verr) || verr.Code != blackboard.ErrCodeNodeKeyConflict {
		t.Fatalf("expected node_key_conflict, got %v", err)
	}
}

// TestApplyResultDeterminism proves the request hash is a deterministic pure
// function of the batch's semantic content (excluding server-generated IDs and
// timestamps): the same batch produces the same request hash.
func TestApplyResultDeterminism(t *testing.T) {
	_, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)

	batch := validFactBatch(projectID, ctx)
	h1, err := blackboard.ComputeRequestHashForTesting(batch)
	if err != nil {
		t.Fatalf("hash 1: %v", err)
	}
	h2, err := blackboard.ComputeRequestHashForTesting(batch)
	if err != nil {
		t.Fatalf("hash 2: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("request hash not deterministic for identical batch: %q vs %q", h1, h2)
	}
	// A different stable key must produce a different hash.
	other := validFactBatch(projectID, ctx)
	other.Operations[0].Node.StableKey = "dns:other.example.com"
	h3, err := blackboard.ComputeRequestHashForTesting(other)
	if err != nil {
		t.Fatalf("hash 3: %v", err)
	}
	if h3 == h1 {
		t.Fatal("expected different request hash for different stable key")
	}
}

// TestApplyRejectsBadStableKey proves stable-key grammar is enforced.
func TestApplyRejectsBadStableKey(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)

	for _, bad := range []string{
		"",                       // empty
		"DNS:UPPER",              // uppercase not allowed by grammar for new keys
		strings.Repeat("a", 161), // over the 160-char cap after the first char
	} {
		batch := validFactBatch(projectID, ctx)
		batch.IdempotencyKey = "task-1:bad-key"
		batch.Operations[0].Node.StableKey = bad
		_, err := graph.Apply(context.Background(), batch)
		if err == nil {
			t.Fatalf("expected error for bad stable key %q", bad)
		}
	}
}
