package blackboard_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/task"
)

// TestRecordProvenanceJoinsCapturedRuntimeStateWithoutRawOutputOrSecrets is the
// U03 first-red test at BlackboardReadService.Read. It proves provenance uses
// durable Task launch snapshots after live Runtime Profile deletion while only
// exposing compact Event metadata and an explicit safe Runtime configuration.
func TestRecordProvenanceJoinsCapturedRuntimeStateWithoutRawOutputOrSecrets(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	proj, err := projects.Create("Provenance", "", project.Scope{
		Domains:       []string{"example.com"},
		URLs:          []string{"https://example.com/admin"},
		Ports:         []string{"443"},
		TestingLimits: []string{"no denial of service"},
	}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	db := graph.DBForTesting()
	if _, err := db.Exec(`INSERT INTO runtime_profiles(id,name,provider,fields_json,created_at,updated_at) VALUES('profile-u03','Codex','codex','{}','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed live Runtime Profile: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "Validate admin authentication", RuntimeProfileID: "profile-u03", Runner: task.RunnerHost})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	config, err := tasks.RecordRuntimeConfig(createdTask.ID, "profile-u03", map[string]any{
		"runtime_plugin_id": "codex",
		"model_provider_id": "provider-u03",
		"model":             "gpt-u03",
		"api_token":         "SECRET-TOKEN-MUST-NOT-LEAK",
		"command_line":      "codex --dangerously-bypass-everything",
	})
	if err != nil {
		t.Fatalf("record Runtime configuration: %v", err)
	}
	projection, err := graph.CanonicalMainGraph(context.Background(), proj.ID, 0)
	if err != nil {
		t.Fatalf("render initial Blackboard: %v", err)
	}
	continuation, err := tasks.CreateContinuationWithSnapshotPin(createdTask.ID, "profile-u03", "codex", task.RunnerHost, task.ContinuationSnapshotPin{
		RuntimeConfigVersionID:              config.ID,
		BlackboardGraphRevision:             projection.GraphRevision,
		BlackboardRendererVersion:           projection.RendererVersion,
		BlackboardEstimatorVersion:          projection.EstimatorVersion,
		BlackboardProjectionHash:            projection.Hash,
		BlackboardProjectionBytes:           projection.ByteCount,
		BlackboardProjectionEstimatedTokens: projection.EstimatedTokens,
	})
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	event, err := tasks.AppendEvent(createdTask.ID, task.EventKindRuntimeOutput, task.EventPayload{
		"phase": "checkpoint",
		"text":  "RAW OUTPUT MUST NOT LEAK",
		"token": "BEARER-TOKEN-MUST-NOT-LEAK",
	})
	if err != nil {
		t.Fatalf("append source Event: %v", err)
	}
	if _, err := db.Exec(`UPDATE task_events SET continuation_id=? WHERE id=?`, continuation.ID, event.ID); err != nil {
		t.Fatalf("bind source Event to Continuation: %v", err)
	}

	createObjectiveForAttempts(t, graph, proj.ID, blackboard.SystemExecutionContext(proj.ID, proj.Kind, "test-system"))

	runtimeContext := blackboard.ExecutionContext{
		ProjectID: proj.ID, ProjectKind: proj.Kind, ActorType: blackboard.ActorTypeRuntime,
		ActorID: "runtime:codex:" + continuation.ID, TaskID: createdTask.ID,
		ContinuationID: continuation.ID, RuntimeProfileID: "profile-u03", Runner: string(task.RunnerHost),
	}
	result, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:      blackboard.GraphMutationSchemaVersion,
		IdempotencyKey:     "u03:provenance",
		Context:            runtimeContext,
		SourceEventIDsByOp: map[string][]string{"fact": {event.ID}},
		Operations: []blackboard.Operation{
			{OpID: "attempt", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:u03"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{}}},
			{OpID: "tests", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeTests, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:c05"}}},
			{
				OpID: "fact", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:admin-auth"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category": "service", "summary": "Admin authentication checked", "confidence": "tentative", "scope_status": "in_scope",
				}},
			},
			{OpID: "produced", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{OpID: "fact"}}},
		},
	})
	if err != nil {
		t.Fatalf("record runtime-authored fact: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM runtime_profiles WHERE id='profile-u03'`); err != nil {
		t.Fatalf("delete live Runtime Profile: %v", err)
	}

	envelope, err := blackboard.NewBlackboardReadService(db).Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion:  blackboard.BlackboardReadProtocolVersion,
		ProjectID:        proj.ID,
		Kind:             blackboard.ReadKindRecordProvenanceV1,
		RecordProvenance: &blackboard.RecordProvenanceRequest{NodeID: result.Operations[2].NodeID, Version: "current", Provenance: "all"},
	})
	if err != nil {
		t.Fatalf("read Record provenance: %v", err)
	}
	got := envelope.Result.(blackboard.RecordProvenanceV1)
	if got.Version != 1 || len(got.Entries) != 1 {
		t.Fatalf("Record provenance = %#v", got)
	}
	entry := got.Entries[0]
	if entry.Task == nil || entry.Task.ID != createdTask.ID || entry.Task.Goal != createdTask.Goal {
		t.Fatalf("Task join = %#v", entry.Task)
	}
	if entry.Continuation == nil || entry.Continuation.ID != continuation.ID || entry.Continuation.Number != 1 {
		t.Fatalf("Continuation join = %#v", entry.Continuation)
	}
	if entry.RuntimeConfiguration == nil || entry.RuntimeConfiguration.VersionID != config.ID || entry.RuntimeConfiguration.RuntimePluginID != "codex" || entry.RuntimeConfiguration.RuntimeProfileID != "profile-u03" || entry.RuntimeConfiguration.Runner != "host" || entry.RuntimeConfiguration.ModelProviderID != "provider-u03" || entry.RuntimeConfiguration.Model != "gpt-u03" {
		t.Fatalf("captured Runtime configuration = %#v", entry.RuntimeConfiguration)
	}
	if entry.ScopeSnapshot == nil || entry.ScopeSnapshot.Summary.Domains != 1 || entry.ScopeSnapshot.Summary.URLs != 1 || entry.ScopeSnapshot.Summary.Ports != 1 || !entry.ScopeSnapshot.Summary.HasTestingLimits {
		t.Fatalf("Scope Snapshot join = %#v", entry.ScopeSnapshot)
	}
	if len(entry.SourceEvents) != 1 || entry.SourceEvents[0].ID != event.ID || entry.SourceEvents[0].Sequence != event.Seq || entry.SourceEvents[0].Kind != string(task.EventKindRuntimeOutput) || entry.SourceEvents[0].Phase != "checkpoint" {
		t.Fatalf("compact source Events = %#v", entry.SourceEvents)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encode provenance: %v", err)
	}
	for _, forbidden := range []string{"SECRET-TOKEN-MUST-NOT-LEAK", "BEARER-TOKEN-MUST-NOT-LEAK", "RAW OUTPUT MUST NOT LEAK", "dangerously-bypass"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("provenance leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestRecordProvenanceDoesNotJoinDurableStateFromAnotherProject(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	first, err := projects.Create("First", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create first Project: %v", err)
	}
	second, err := projects.Create("Second", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create second Project: %v", err)
	}
	db := graph.DBForTesting()
	if _, err := db.Exec(`INSERT INTO runtime_profiles(id,name,provider,fields_json,created_at,updated_at) VALUES('profile-moved','Moved','codex','{}','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed moved Runtime Profile: %v", err)
	}
	tasks := task.NewService(db, projects)
	movedTask, err := tasks.Create(task.CreateRequest{ProjectID: first.ID, Goal: "SECRET MOVED GOAL", RuntimeProfileID: "profile-moved", Runner: task.RunnerHost})
	if err != nil {
		t.Fatalf("create Task before Project move: %v", err)
	}
	config, err := tasks.RecordRuntimeConfig(movedTask.ID, "profile-moved", map[string]any{
		"runtime_plugin_id": "moved-plugin",
		"model":             "SECRET-MOVED-MODEL",
	})
	if err != nil {
		t.Fatalf("record moved Runtime configuration: %v", err)
	}
	projection, err := graph.CanonicalMainGraph(context.Background(), first.ID, 0)
	if err != nil {
		t.Fatalf("render local Blackboard: %v", err)
	}
	movedContinuation, err := tasks.CreateContinuationWithSnapshotPin(movedTask.ID, "profile-moved", "moved-plugin", task.RunnerHost, task.ContinuationSnapshotPin{
		RuntimeConfigVersionID:              config.ID,
		BlackboardGraphRevision:             projection.GraphRevision,
		BlackboardRendererVersion:           projection.RendererVersion,
		BlackboardEstimatorVersion:          projection.EstimatorVersion,
		BlackboardProjectionHash:            projection.Hash,
		BlackboardProjectionBytes:           projection.ByteCount,
		BlackboardProjectionEstimatedTokens: projection.EstimatedTokens,
	})
	if err != nil {
		t.Fatalf("create Continuation before Project move: %v", err)
	}
	movedEvent, err := tasks.AppendEvent(movedTask.ID, task.EventKindRuntimeOutput, task.EventPayload{"phase": "SECRET-MOVED-PHASE"})
	if err != nil {
		t.Fatalf("append Event before Project move: %v", err)
	}
	if _, err := db.Exec(`UPDATE task_events SET continuation_id=? WHERE id=?`, movedContinuation.ID, movedEvent.ID); err != nil {
		t.Fatalf("bind moved source Event: %v", err)
	}
	execCtx := blackboard.SystemExecutionContext(first.ID, first.Kind, "test-system")
	execCtx.TaskID = movedTask.ID
	execCtx.ContinuationID = movedContinuation.ID
	execCtx.RuntimeProfileID = "profile-moved"
	execCtx.Runner = "host"

	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u03:cross-project-provenance",
		Context:        execCtx,
		SourceEventIDsByOp: map[string][]string{
			"fact": {movedEvent.ID},
		},
		Operations: []blackboard.Operation{{
			OpID: "fact",
			Kind: blackboard.OpCreateNode,
			Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:cross-project-provenance"},
			Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{
				Category: "test", Summary: "Local fact", ScopeStatus: blackboard.ScopeStatusUnknown,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create local Fact: %v", err)
	}
	if _, err := db.Exec(`UPDATE tasks SET project_id=? WHERE id=?`, second.ID, movedTask.ID); err != nil {
		t.Fatalf("move durable Task to another Project: %v", err)
	}

	envelope, err := blackboard.NewBlackboardReadService(db).Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion:  blackboard.BlackboardReadProtocolVersion,
		ProjectID:        first.ID,
		Kind:             blackboard.ReadKindRecordProvenanceV1,
		RecordProvenance: &blackboard.RecordProvenanceRequest{NodeID: created.Operations[0].NodeID, Version: "current", Provenance: "all"},
	})
	if err != nil {
		t.Fatalf("read corrupt provenance: %v", err)
	}
	entry := envelope.Result.(blackboard.RecordProvenanceV1).Entries[0]
	if entry.JoinStatus != "missing" || entry.Task != nil || entry.ScopeSnapshot != nil || entry.Continuation != nil || entry.RuntimeConfiguration != nil || len(entry.SourceEvents) != 0 {
		t.Fatalf("cross-Project provenance join = %#v", entry)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("encode cross-Project provenance: %v", err)
	}
	if strings.Contains(string(encoded), "SECRET MOVED GOAL") || strings.Contains(string(encoded), "SECRET-MOVED-MODEL") || strings.Contains(string(encoded), "SECRET-MOVED-PHASE") {
		t.Fatalf("cross-Project provenance leaked durable state: %s", encoded)
	}
}
