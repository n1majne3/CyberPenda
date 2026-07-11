package blackboard_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/store"
)

// TestLostResponseReplayReturnsByteIdenticalMutationResult is the C07 first-red
// behavioral test at BlackboardGraphService.Apply. A retry after a committed
// response is lost must return the exact stored result rather than allocate any
// new identity, timestamp, sequence, version, or graph revision.
func TestLostResponseReplayReturnsByteIdenticalMutationResult(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	batch := validFactBatch(projectID, ctx)

	first, err := graph.Apply(context.Background(), batch)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if first.MutationSequence != 1 {
		t.Fatalf("mutation sequence: got %d want 1", first.MutationSequence)
	}
	if first.MutationID == "" || first.RecordedAt == "" || len(first.ResultBytes) == 0 {
		t.Fatalf("result is missing replay identity: %+v", first)
	}

	replayed, err := graph.Apply(context.Background(), batch)
	if err != nil {
		t.Fatalf("replay apply: %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("replay result differs:\nfirst:  %+v\nreplay: %+v", first, replayed)
	}
	if !bytes.Equal(replayed.ResultBytes, first.ResultBytes) {
		t.Fatalf("replay bytes differ:\nfirst:  %s\nreplay: %s", first.ResultBytes, replayed.ResultBytes)
	}
}

// TestFirstSeenIdenticalPutEdgeConsumesSequenceNotRevision proves the C07
// distinction between an exact replay and a newly accepted all-no-op batch.
// The latter is ledgered once, but it does not create a new edge version or
// advance the semantic graph revision.
func TestFirstSeenIdenticalPutEdgeConsumesSequenceNotRevision(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	seed := blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c07:seed-edge", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:source"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "test", Summary: "source", ScopeStatus: blackboard.ScopeStatusInScope}}},
			{OpID: "target", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:target"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "test", Summary: "target", ScopeStatus: blackboard.ScopeStatusInScope}}},
			{OpID: "link", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeDerivedFrom, From: blackboard.NodeRef{OpID: "source"}, To: blackboard.NodeRef{OpID: "target"}, Summary: "same evidence chain"}},
		},
	}
	created, err := graph.Apply(context.Background(), seed)
	if err != nil {
		t.Fatalf("seed edge: %v", err)
	}

	noop := blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c07:identical-edge", Context: ctx,
		Operations: []blackboard.Operation{{
			OpID: "link-again", Kind: blackboard.OpPutEdge,
			PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeDerivedFrom,
				From: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:source"},
				To:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:target"}, Summary: "same evidence chain"},
		}},
	}
	result, err := graph.Apply(context.Background(), noop)
	if err != nil {
		t.Fatalf("identical edge no-op: %v", err)
	}
	if result.MutationSequence != created.MutationSequence+1 {
		t.Fatalf("mutation sequence: got %d want %d", result.MutationSequence, created.MutationSequence+1)
	}
	if result.GraphRevision != created.GraphRevision {
		t.Fatalf("graph revision advanced for no-op: got %d want %d", result.GraphRevision, created.GraphRevision)
	}
	if len(result.Operations) != 1 || result.Operations[0].Changed || result.Operations[0].EdgeVersion != 1 || result.Operations[0].EdgeID != created.Operations[2].EdgeID {
		t.Fatalf("unexpected no-op result: %+v", result.Operations)
	}
	read, err := graph.ReadEdge(context.Background(), blackboard.ReadEdgeRequest{ProjectID: projectID, EdgeID: result.Operations[0].EdgeID})
	if err != nil || read.Version != 1 {
		t.Fatalf("edge version after no-op: read=%+v err=%v", read, err)
	}
}

func TestChangedReplayAndStaleNodeVersionPreserveCurrentState(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	batch := validFactBatch(projectID, ctx)
	created, err := graph.Apply(context.Background(), batch)
	if err != nil {
		t.Fatalf("create fact: %v", err)
	}

	changedReplay := batch
	changedReplay.Operations = append([]blackboard.Operation(nil), batch.Operations...)
	changedReplay.Operations[0].Create.Properties.Summary = "different payload"
	_, err = graph.Apply(context.Background(), changedReplay)
	assertValidationCode(t, err, blackboard.ErrCodeIdempotencyConflict)

	stale := blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c07:stale-node", Context: ctx,
		Operations: []blackboard.Operation{{OpID: "deprecate", Kind: blackboard.OpTransitionNode,
			Node:       blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "dns:example.com"},
			Transition: blackboard.TransitionNodeInput{ExpectedVersion: created.Operations[0].NodeVersion + 1, Status: "deprecated"}}},
	}
	_, err = graph.Apply(context.Background(), stale)
	assertValidationCode(t, err, blackboard.ErrCodeVersionConflict)
	read, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeProjectFact, Key: "dns:example.com"})
	if err != nil {
		t.Fatalf("read preserved fact: %v", err)
	}
	if read.Node.Version != 1 || read.Node.ProjectFact.Confidence != blackboard.ConfidenceTentative {
		t.Fatalf("stale write changed current state: %+v", read.Node)
	}
}

func TestDeletingHeadsAndKeysThenRebuildRestoresCurrentAndHistoricalOutput(t *testing.T) {
	graph, projects, path := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), validFactBatch(projectID, ctx))
	if err != nil {
		t.Fatalf("create fact: %v", err)
	}
	historicalBefore, err := graph.Reconstruct(context.Background(), projectID, created.GraphRevision)
	if err != nil {
		t.Fatalf("reconstruct historical before repair: %v", err)
	}
	transition := blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c07:deprecate", Context: ctx,
		Operations: []blackboard.Operation{{OpID: "deprecate", Kind: blackboard.OpTransitionNode,
			Node:       blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "dns:example.com"},
			Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "deprecated"}}},
	}
	updated, err := graph.Apply(context.Background(), transition)
	if err != nil {
		t.Fatalf("deprecate fact: %v", err)
	}
	currentBefore, err := graph.Reconstruct(context.Background(), projectID, updated.GraphRevision)
	if err != nil {
		t.Fatalf("reconstruct current before repair: %v", err)
	}

	db := graph.DBForTesting()
	for _, table := range []string{"blackboard_key_registry", "blackboard_edge_heads", "blackboard_node_heads", "blackboard_graph_state"} {
		if _, err := db.Exec("DELETE FROM "+table+" WHERE project_id = ?", projectID); err != nil {
			t.Fatalf("delete %s: %v", table, err)
		}
	}
	if err := graph.Rebuild(context.Background(), projectID); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	historicalAfter, err := graph.Reconstruct(context.Background(), projectID, created.GraphRevision)
	if err != nil {
		t.Fatalf("reconstruct historical after repair: %v", err)
	}
	currentAfter, err := graph.Reconstruct(context.Background(), projectID, updated.GraphRevision)
	if err != nil {
		t.Fatalf("reconstruct current after repair: %v", err)
	}
	if !reflect.DeepEqual(historicalAfter, historicalBefore) {
		t.Fatalf("historical reconstruction drifted:\nbefore: %+v\nafter:  %+v", historicalBefore, historicalAfter)
	}
	if !reflect.DeepEqual(currentAfter, currentBefore) {
		t.Fatalf("current reconstruction drifted:\nbefore: %+v\nafter:  %+v", currentBefore, currentAfter)
	}
	reopened := reopenGraphServices(t, path)
	historicalReopened, err := reopened.Reconstruct(context.Background(), projectID, created.GraphRevision)
	if err != nil {
		t.Fatalf("reconstruct historical after reopen: %v", err)
	}
	if !reflect.DeepEqual(historicalReopened, historicalBefore) {
		t.Fatalf("historical reconstruction drifted after reopen:\nbefore: %+v\nafter:  %+v", historicalBefore, historicalReopened)
	}
	read, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeProjectFact, Key: "dns:example.com"})
	if err != nil || read.Node.Version != 2 || read.Node.ProjectFact.Confidence != blackboard.ConfidenceDeprecated {
		t.Fatalf("rebuilt current read: node=%+v err=%v", read.Node, err)
	}
}

func TestDeletingProvenanceEventBreaksIntegrityChain(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	ctx.TaskID = "task-events"
	db := graph.DBForTesting()
	if _, err := db.Exec(`INSERT INTO task_events(id,task_id,seq,kind,payload_json,created_at,continuation_id) VALUES('event-1',?,1,'status','{}','2024-01-01T00:00:00Z',NULL)`, ctx.TaskID); err != nil {
		t.Fatalf("insert source event: %v", err)
	}
	batch := validFactBatch(projectID, ctx)
	batch.SourceEventIDsByOp = map[string][]string{"fact": {"event-1"}}
	if _, err := graph.Apply(context.Background(), batch); err != nil {
		t.Fatalf("apply with source event: %v", err)
	}
	if err := graph.VerifyIntegrity(context.Background(), projectID); err != nil {
		t.Fatalf("verify intact ledger: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER blackboard_graph_provenance_events_no_delete`); err != nil {
		t.Fatalf("drop corruption guard: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM blackboard_graph_provenance_events WHERE project_id=? AND event_id='event-1'`, projectID); err != nil {
		t.Fatalf("corrupt provenance event history: %v", err)
	}
	if err := graph.VerifyIntegrity(context.Background(), projectID); err == nil {
		t.Fatal("integrity verification accepted deleted provenance event")
	}
	if _, err := graph.Reconstruct(context.Background(), projectID, 1); err == nil {
		t.Fatal("historical reconstruction accepted a broken ledger chain")
	}
	if err := graph.Rebuild(context.Background(), projectID); err == nil {
		t.Fatal("rebuild accepted a broken ledger chain")
	}
}

func TestConcurrentSameKeyWritersConvergeOrConflict(t *testing.T) {
	graphA, projects, path := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	dbB, err := store.Open(path)
	if err != nil {
		t.Fatalf("open second writer: %v", err)
	}
	t.Cleanup(func() { _ = dbB.Close() })
	graphB := blackboard.NewGraphService(dbB, blackboard.SystemClock{}, blackboard.RandomIDSource{})

	run := func(a, b blackboard.MutationBatch) ([2]blackboard.MutationResult, [2]error) {
		var results [2]blackboard.MutationResult
		var errs [2]error
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; results[0], errs[0] = graphA.Apply(context.Background(), a) }()
		go func() { defer wg.Done(); <-start; results[1], errs[1] = graphB.Apply(context.Background(), b) }()
		close(start)
		wg.Wait()
		return results, errs
	}

	identical := validFactBatch(projectID, ctx)
	identical.IdempotencyKey = "c07:concurrent-identical"
	identical.Operations[0].Node.StableKey = "concurrent:identical"
	results, errs := run(identical, identical)
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("identical writers: %v / %v", errs[0], errs[1])
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		t.Fatalf("identical writers diverged:\nA: %+v\nB: %+v", results[0], results[1])
	}

	left := validFactBatch(projectID, ctx)
	left.IdempotencyKey = "c07:concurrent-conflict"
	left.Operations[0].Node.StableKey = "concurrent:conflict"
	right := left
	right.Operations = append([]blackboard.Operation(nil), left.Operations...)
	right.Operations[0].Create.Properties.Summary = "conflicting payload"
	_, errs = run(left, right)
	successes, conflicts := 0, 0
	for _, err := range errs {
		if err == nil {
			successes++
			continue
		}
		var validation *blackboard.ValidationError
		if errors.As(err, &validation) && validation.Code == blackboard.ErrCodeIdempotencyConflict {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent conflict error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent conflict outcomes: successes=%d conflicts=%d errors=%s", successes, conflicts, fmt.Sprint(errs))
	}
}

func TestPutEdgeSummaryRequiresCurrentExpectedVersion(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	seed := blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c07:edge-version-seed", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "edge-version:source"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "test", Summary: "source", ScopeStatus: blackboard.ScopeStatusInScope}}},
			{OpID: "target", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "edge-version:target"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "test", Summary: "target", ScopeStatus: blackboard.ScopeStatusInScope}}},
			{OpID: "link", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeDerivedFrom, From: blackboard.NodeRef{OpID: "source"}, To: blackboard.NodeRef{OpID: "target"}, Summary: "v1"}},
		},
	}
	created, err := graph.Apply(context.Background(), seed)
	if err != nil {
		t.Fatalf("seed edge: %v", err)
	}
	update := func(key string, expected int, summary string) (blackboard.MutationResult, error) {
		return graph.Apply(context.Background(), blackboard.MutationBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: key, Context: ctx,
			Operations: []blackboard.Operation{{OpID: "update-link", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{
				EdgeType: blackboard.EdgeTypeDerivedFrom, From: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "edge-version:source"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "edge-version:target"}, Summary: summary, ExpectedVersion: expected,
			}}},
		})
	}
	if _, err := update("c07:edge-version-stale", 2, "v2"); err == nil {
		t.Fatal("stale edge update succeeded")
	} else {
		assertValidationCode(t, err, blackboard.ErrCodeVersionConflict)
	}
	edge, err := graph.ReadEdge(context.Background(), blackboard.ReadEdgeRequest{ProjectID: projectID, EdgeID: created.Operations[2].EdgeID})
	if err != nil || edge.Version != 1 || edge.Summary != "v1" {
		t.Fatalf("stale update changed edge: edge=%+v err=%v", edge, err)
	}
	updated, err := update("c07:edge-version-current", 1, "v2")
	if err != nil {
		t.Fatalf("current edge update: %v", err)
	}
	if updated.GraphRevision != created.GraphRevision+1 || updated.Operations[0].EdgeVersion != 2 || !updated.Operations[0].Changed {
		t.Fatalf("unexpected edge update result: %+v", updated)
	}
	var fromNodeID, toNodeID string
	if err := graph.DBForTesting().QueryRow(`SELECT from_node_id,to_node_id FROM blackboard_edge_versions WHERE project_id=? AND edge_id=? AND version=2`, projectID, created.Operations[2].EdgeID).Scan(&fromNodeID, &toNodeID); err != nil {
		t.Fatalf("read full edge post-image: %v", err)
	}
	if fromNodeID == "" || toNodeID == "" {
		t.Fatalf("edge version omitted endpoint post-image: from=%q to=%q", fromNodeID, toNodeID)
	}
	if _, err := graph.DBForTesting().Exec(`INSERT INTO blackboard_edge_versions(project_id,edge_id,version,result_graph_revision,mutation_seq,operation_index,state,summary,semantic_hash,updated_at) VALUES(?,?,3,?,?,0,'active','invalid','00','2024-01-01T00:00:00Z')`, projectID, created.Operations[2].EdgeID, updated.GraphRevision, updated.MutationSequence); err == nil || !strings.Contains(err.Error(), "endpoints are required") {
		t.Fatalf("edge version without endpoint post-image was not rejected: %v", err)
	}
	if _, err := graph.DBForTesting().Exec(`UPDATE blackboard_edge_versions SET summary='tampered' WHERE project_id=? AND edge_id=? AND version=2`, projectID, created.Operations[2].EdgeID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("edge version update was not rejected by append-only guard: %v", err)
	}
	if _, err := graph.DBForTesting().Exec(`DELETE FROM blackboard_edges WHERE project_id=? AND id=?`, projectID, created.Operations[2].EdgeID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("edge identity delete was not rejected by append-only guard: %v", err)
	}
}

func TestSQLiteWriterLockExhaustionIsRetryableAndConsumesNoKey(t *testing.T) {
	graphA, projects, path := newGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	holder, err := graphA.DBForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin writer lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback() }()

	raw, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_pragma=busy_timeout(1)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open short-timeout writer: %v", err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	graphB := blackboard.NewGraphService(&store.DB{DB: raw}, blackboard.NewSequenceClock("2024-03-01T00:00:00Z"), blackboard.NewSequenceIDSource("prov-busy", "node-busy", "mutation-busy"))
	batch := validFactBatch(projectID, ctx)
	batch.IdempotencyKey = "c07:busy-retry"
	batch.Operations[0].Node.StableKey = "busy:retry"

	_, err = graphB.Apply(context.Background(), batch)
	var storageErr *blackboard.StorageError
	if !errors.As(err, &storageErr) || storageErr.Code != blackboard.ErrCodeStorageBusy || !storageErr.Retryable || errors.Unwrap(storageErr) == nil {
		t.Fatalf("busy error classification: %#v (%v)", storageErr, err)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}
	result, err := graphB.Apply(context.Background(), batch)
	if err != nil {
		t.Fatalf("retry after releasing writer lock: %v", err)
	}
	if result.MutationSequence != 1 {
		t.Fatalf("busy attempt consumed idempotency key or sequence: %+v", result)
	}
}

func assertValidationCode(t *testing.T, err error, want string) {
	t.Helper()
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != want {
		t.Fatalf("error: got %v want validation code %s", err, want)
	}
}
