package blackboard_test

import (
	"context"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/task"
)

func TestCurrentTruthIncludesTentativeAndOutOfScopeFactsWithExplicitActionability(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "u02:truth", Context: execCtx,
		Operations: []blackboard.Operation{
			{OpID: "tentative", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:tentative"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Tentative", "body": "detail", "confidence": "tentative", "scope_status": "in_scope"}}},
			{OpID: "deprecated", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:deprecated"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Deprecated", "confidence": "deprecated", "scope_status": "unknown"}}},
		},
	})
	if err != nil {
		t.Fatalf("seed Current Truth: %v", err)
	}
	createObjectiveForAttempts(t, graph, projectID, execCtx)
	tasks := task.NewService(graph.DBForTesting(), projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "confirm out-of-scope fact", RuntimeProfileID: "profile-u02", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-u02", "test", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeCtx := blackboard.ExecutionContext{ProjectID: projectID, ProjectKind: execCtx.ProjectKind, ActorType: blackboard.ActorTypeRuntime, ActorID: "runtime-u02", TaskID: createdTask.ID, ContinuationID: continuation.ID, RuntimeProfileID: "profile-u02", Runner: string(task.RunnerSandbox)}
	open := createOpenAttempt(t, graph, runtimeCtx, "attempt:u02-confirm")
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "u02:confirmed", Context: runtimeCtx, Operations: []blackboard.Operation{
		{OpID: "terminal", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:u02-confirm"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "succeeded", Summary: "confirmed"}},
		{OpID: "confirmed", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:confirmed"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Confirmed", "confidence": "confirmed", "scope_status": "out_of_scope"}}},
		{OpID: "produced", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:u02-confirm"}, To: blackboard.NodeRef{OpID: "confirmed"}}},
	}})
	if err != nil {
		t.Fatalf("create confirmed fact: %v", err)
	}

	envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion, ProjectID: projectID,
		Kind: blackboard.ReadKindCurrentTruthV1, CurrentTruth: &blackboard.CurrentTruthRequest{},
	})
	if err != nil {
		t.Fatalf("read Current Truth: %v", err)
	}
	truth := envelope.Result.(blackboard.CurrentTruthV1)
	if len(truth.Items) != 2 {
		t.Fatalf("Current Truth items = %d want 2", len(truth.Items))
	}
	if truth.Items[0].Fact.StableKey != "fact:confirmed" || !truth.Items[0].NonActionable {
		t.Fatalf("confirmed out-of-scope item = %#v", truth.Items[0])
	}
	if truth.Items[1].Fact.StableKey != "fact:tentative" || truth.Items[1].NonActionable {
		t.Fatalf("tentative item = %#v", truth.Items[1])
	}
}

func TestExplorationFrontierOrdersByParentTaskStatusAndExcludesStrandedObjectives(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)
	pending, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "Pending parent", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create pending Task: %v", err)
	}
	running, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "Running parent", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create running Task: %v", err)
	}
	if _, err := tasks.UpdateStatus(running.ID, task.StatusRunning); err != nil {
		t.Fatalf("run Task: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "u02:frontier", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "pending", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:a-pending"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Pending objective", "status": "open"}}},
		{OpID: "running", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:z-running"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Running objective", "status": "open"}}},
		{OpID: "blocker", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:blocker"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Unresolved blocker", "status": "open"}}},
		{OpID: "stranded", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:stranded"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Stranded objective", "status": "open"}}},
		{OpID: "pending-parent", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "pending"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeGoal, StableKey: "task:" + pending.ID + ":goal"}}},
		{OpID: "running-parent", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "running"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeGoal, StableKey: "task:" + running.ID + ":goal"}}},
		{OpID: "blocked", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeBlocks, From: blackboard.NodeRef{OpID: "blocker"}, To: blackboard.NodeRef{OpID: "stranded"}}},
	}})
	if err != nil {
		t.Fatalf("seed Frontier: %v", err)
	}
	envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindExplorationFrontierV1, ExplorationFrontier: &blackboard.ExplorationFrontierRequest{}})
	if err != nil {
		t.Fatalf("read Frontier: %v", err)
	}
	frontier := envelope.Result.(blackboard.ExplorationFrontierV1)
	got := make([]string, len(frontier.Items))
	for i := range frontier.Items {
		got[i] = frontier.Items[i].Objective.StableKey
	}
	want := []string{"objective:z-running", "objective:a-pending", "objective:blocker"}
	if !equalStrings(got, want) {
		t.Fatalf("Frontier order = %v want %v", got, want)
	}
	for i, item := range frontier.Items {
		if item.Rank != i+1 {
			t.Fatalf("rank %d = %d", i, item.Rank)
		}
	}
}

func TestEntityDAGTraversalIsDeterministicAndCredentialDetailIsSecretSafe(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u02:entities", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "root-b", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:root-b"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "Root B", "scope_status": "in_scope"}}},
		{OpID: "root-a", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:root-a"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "Root A", "scope_status": "in_scope"}}},
		{OpID: "service", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:service"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "service", "name": "Admin HTTPS", "locator": "example.com:443", "scope_status": "in_scope"}}},
		{OpID: "credential", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:credential"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "credential", "name": "Admin token", "scope_status": "in_scope", "credential_ref": "credential://admin-token"}}},
		{OpID: "service-a", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "service"}, To: blackboard.NodeRef{OpID: "root-a"}}},
		{OpID: "service-b", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "service"}, To: blackboard.NodeRef{OpID: "root-b"}}},
		{OpID: "credential-service", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "credential"}, To: blackboard.NodeRef{OpID: "service"}}},
	}})
	if err != nil {
		t.Fatalf("seed Entity DAG: %v", err)
	}
	credentialID := created.Operations[3].NodeID
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	rootsEnvelope, err := reads.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindEntityCollectionV1, EntityCollection: &blackboard.EntityCollectionRequest{ParentID: "root"}})
	if err != nil {
		t.Fatalf("read Entity roots: %v", err)
	}
	roots := rootsEnvelope.Result.(blackboard.EntityCollectionV1)
	if got, want := entityKeys(roots), []string{"entity:root-a", "entity:root-b"}; !equalStrings(got, want) {
		t.Fatalf("roots = %v want %v", got, want)
	}
	detailEnvelope, err := reads.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindEntityDetailV1, EntityDetail: &blackboard.EntityDetailRequest{NodeID: credentialID}})
	if err != nil {
		t.Fatalf("read Credential detail: %v", err)
	}
	detail := detailEnvelope.Result.(blackboard.EntityDetailV1)
	if len(detail.Breadcrumbs) != 2 {
		t.Fatalf("breadcrumb paths = %d want 2", len(detail.Breadcrumbs))
	}
	if got := detail.Entity.Properties["credential_ref"]; got != "credential://admin-token" {
		t.Fatalf("credential_ref = %v", got)
	}
	for key := range detail.Entity.Properties {
		if key == "value" || key == "secret" || key == "token" {
			t.Fatalf("secret-like property %q leaked", key)
		}
	}
}

func entityKeys(result blackboard.EntityCollectionV1) []string {
	out := make([]string, len(result.Items))
	for i := range result.Items {
		out[i] = result.Items[i].Entity.StableKey
	}
	return out
}

func TestRecordMutationCapabilityHintsAreAdvisoryAcrossConcurrentWrite(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u02:capability", Context: execCtx, Operations: []blackboard.Operation{{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:capability"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "original", "confidence": "tentative", "scope_status": "in_scope"}}}}})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	nodeID := created.Operations[0].NodeID
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	envelope, err := reads.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindRecordDetailV1, RecordDetail: &blackboard.RecordDetailRequest{NodeID: nodeID}})
	if err != nil {
		t.Fatalf("read detail: %v", err)
	}
	detail := envelope.Result.(blackboard.RecordDetailV1)
	if !detail.Capabilities.Transition.Allowed || detail.Capabilities.ExpectedVersion != 1 {
		t.Fatalf("capabilities = %#v", detail.Capabilities)
	}
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u02:concurrent", Context: execCtx, Operations: []blackboard.Operation{{OpID: "transition", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: nodeID}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "deprecated"}}}}); err != nil {
		t.Fatalf("concurrent transition: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u02:stale", Context: execCtx, Operations: []blackboard.Operation{{OpID: "transition", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: nodeID}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: detail.Capabilities.ExpectedVersion, Status: "deprecated"}}}})
	assertGraphErrorCode(t, err, blackboard.ErrCodeVersionConflict)
}

func TestProjectBlackboardSummaryCountsGoldenGraphRecords(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u02:summary", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:summary"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Fact", "confidence": "tentative", "scope_status": "in_scope"}}},
		{OpID: "finding", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:summary"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Finding", "status": "unconfirmed", "target": "example.com"}}},
		{OpID: "evidence", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:summary"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"artifact_type": "other", "managed_path": "missing://summary", "summary": "Evidence", "status": "missing"}}},
	}})
	if err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindProjectBlackboardSummaryV1, ProjectSummary: &blackboard.ProjectBlackboardSummaryRequest{}})
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	summary := envelope.Result.(blackboard.ProjectBlackboardSummaryV1)
	if summary.Blackboard.CurrentTruth != 1 || summary.Blackboard.UnconfirmedFindings != 1 || summary.Blackboard.MissingEvidence != 1 {
		t.Fatalf("summary counts = %#v", summary.Blackboard)
	}
	if summary.Blackboard.NodesByType[string(blackboard.NodeTypeProjectFact)] != 1 {
		t.Fatalf("node counts = %#v", summary.Blackboard.NodesByType)
	}
}

func TestBlackboardWorkRecentChangesIncludesSemanticEdgesAndExcludesReplay(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	batch := blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u02:recent", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "entity", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:recent"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "Recent", "scope_status": "in_scope"}}},
		{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:recent"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "host", "summary": "Recent fact", "confidence": "tentative", "scope_status": "in_scope"}}},
		{OpID: "about", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{OpID: "fact"}, To: blackboard.NodeRef{OpID: "entity"}}},
	}}
	if _, err := graph.Apply(context.Background(), batch); err != nil {
		t.Fatalf("seed changes: %v", err)
	}
	if _, err := graph.Apply(context.Background(), batch); err != nil {
		t.Fatalf("replay changes: %v", err)
	}
	envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindBlackboardWorkV1, BlackboardWork: &blackboard.BlackboardWorkRequest{}})
	if err != nil {
		t.Fatalf("read Work: %v", err)
	}
	changes := envelope.Result.(blackboard.BlackboardWorkViewV1).RecentChanges
	if changes.Page.TotalItems != 3 {
		t.Fatalf("recent total=%d want 3", changes.Page.TotalItems)
	}
	edges := 0
	for _, change := range changes.Items {
		if change.Kind == "edge" {
			edges++
		}
	}
	if edges != 1 {
		t.Fatalf("edge changes=%d want 1: %#v", edges, changes.Items)
	}
}

func TestAttentionCursorPinsHealthRunAcrossConcurrentScan(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u02:health-pin", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "a", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:a"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "A", "status": "unconfirmed", "target": "a"}}},
		{OpID: "b", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:b"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "B", "status": "unconfirmed", "target": "b"}}},
	}})
	if err != nil {
		t.Fatalf("seed records: %v", err)
	}
	insertHealth := func(run, started string, critical, warning string) {
		t.Helper()
		if _, err := graph.DBForTesting().Exec(`INSERT INTO blackboard_health_runs(project_id,run_id,checked_graph_revision,checked_state_hash,checked_projection_hash,checker_version,status,artifact_scan_status,started_at,completed_at,metrics_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, projectID, run, created.GraphRevision, created.ResultingStateHash, "projection", "blackboard_health_v1", "critical", "complete", started, started, `{}`); err != nil {
			t.Fatalf("insert run: %v", err)
		}
		for _, result := range []struct{ id, severity string }{{critical, "critical"}, {warning, "warning"}} {
			if _, err := graph.DBForTesting().Exec(`INSERT INTO blackboard_health_results(project_id,run_id,fingerprint,code,severity,subject_kind,subject_id,details_json) VALUES(?,?,?,?,?,?,?,?)`, projectID, run, run+result.severity, "fixture", result.severity, "node", result.id, `{}`); err != nil {
				t.Fatalf("insert result: %v", err)
			}
		}
	}
	insertHealth("health:first", "9998-01-01T00:00:00Z", created.Operations[0].NodeID, created.Operations[1].NodeID)
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	request := func(cursor string) blackboard.ReadRequest {
		return blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindRecordCollectionV1, RecordCollection: &blackboard.RecordCollectionRequest{Sort: blackboard.RecordSortAttention, Limit: 1, Cursor: cursor}}
	}
	first, err := reads.Read(context.Background(), request(""))
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	firstResult := first.Result.(blackboard.RecordCollectionV1)
	if got := recordKeys(firstResult); !equalStrings(got, []string{"finding:a"}) {
		t.Fatalf("first=%v", got)
	}
	insertHealth("health:second", "9999-01-01T00:00:00Z", created.Operations[1].NodeID, created.Operations[0].NodeID)
	second, err := reads.Read(context.Background(), request(firstResult.Page.NextCursor))
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := recordKeys(second.Result.(blackboard.RecordCollectionV1)); !equalStrings(got, []string{"finding:b"}) {
		t.Fatalf("pinned second=%v", got)
	}
	if first.SourcePins["health_run_id"] != "health:first" || second.SourcePins["health_run_id"] != "health:first" {
		t.Fatalf("source pins first=%v second=%v", first.SourcePins, second.SourcePins)
	}
}
