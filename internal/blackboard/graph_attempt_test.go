package blackboard_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func newLifecycleGraphServices(t *testing.T) (*blackboard.GraphService, *project.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return blackboard.NewGraphService(db, nil, nil), project.NewService(db)
}

func createObjectiveForAttempts(t *testing.T, graph *blackboard.GraphService, projectID string, ctx blackboard.ExecutionContext) {
	t.Helper()
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:create-objective", Context: ctx,
		Operations: []blackboard.Operation{{
			OpID: "objective", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:c05"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Determine whether the target behavior is present"}},
		}},
	})
	if err != nil {
		t.Fatalf("create objective: %v", err)
	}
}

func createOpenAttempt(t *testing.T, graph *blackboard.GraphService, ctx blackboard.ExecutionContext, key string) blackboard.ReadNodeResult {
	t.Helper()
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:create:" + key, Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "attempt", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: key}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{}}},
			{OpID: "tests", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeTests, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:c05"}}},
		},
	})
	if err != nil {
		t.Fatalf("create open attempt %s: %v", key, err)
	}
	got, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: ctx.ProjectID, NodeType: blackboard.NodeTypeAttempt, Key: key})
	if err != nil {
		t.Fatalf("read open attempt %s: %v", key, err)
	}
	return got
}

// TestAttemptTerminalOutcomesRequireTestsSummaryAndOutcomeSpecificGuards is the
// C05 first-red test at BlackboardGraphService.Apply. It proves each explicit
// Attempt outcome is retained, terminal summaries are mandatory, open Attempts
// remain attached to what they test, and success is distinguished by produced
// semantic output.
func TestAttemptTerminalOutcomesRequireTestsSummaryAndOutcomeSpecificGuards(t *testing.T) {
	graph, projects := newLifecycleGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	createObjectiveForAttempts(t, graph, projectID, ctx)

	for _, outcome := range []string{"succeeded", "failed", "blocked", "inconclusive", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			key := "attempt:" + outcome
			open := createOpenAttempt(t, graph, ctx, key)
			operations := []blackboard.Operation{}
			if outcome == "succeeded" {
				operations = append(operations,
					blackboard.Operation{OpID: "observation", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeObservation, StableKey: "observation:" + outcome}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"summary": "The attempt produced a useful result", "scope_status": "in_scope"}}},
					blackboard.Operation{OpID: "produced", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: key}, To: blackboard.NodeRef{OpID: "observation"}}},
				)
			}
			operations = append(operations, blackboard.Operation{
				OpID: "terminal", Kind: blackboard.OpTransitionNode,
				Node:       blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: key},
				Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: outcome, ResolutionSummary: fmt.Sprintf("Attempt ended as %s", outcome)},
			})

			transitionCtx := ctx
			if outcome == "interrupted" {
				transitionCtx.ActorID = "task-interruption-reconciler"
			}
			_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
				SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:terminal:" + outcome, Context: transitionCtx, Operations: operations,
			})
			if err != nil {
				t.Fatalf("transition %s attempt: %v", outcome, err)
			}
			got, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeAttempt, Key: key})
			if err != nil {
				t.Fatalf("read %s attempt: %v", outcome, err)
			}
			if got.Node.PropertyMap["status"] != outcome {
				t.Fatalf("status: got %v want %s", got.Node.PropertyMap["status"], outcome)
			}
			if got.Node.PropertyMap["summary"] == "" || got.Node.PropertyMap["ended_at"] == "" {
				t.Fatalf("terminal Attempt missing summary/ended_at: %+v", got.Node.PropertyMap)
			}
		})
	}

	missingSummary := createOpenAttempt(t, graph, ctx, "attempt:missing-summary")
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:missing-summary", Context: ctx,
		Operations: []blackboard.Operation{{OpID: "terminal", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:missing-summary"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: missingSummary.Node.Version, Status: "failed"}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeMissingProperty)

	noOutput := createOpenAttempt(t, graph, ctx, "attempt:no-output")
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:no-output", Context: ctx,
		Operations: []blackboard.Operation{{OpID: "terminal", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:no-output"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: noOutput.Node.Version, Status: "succeeded", ResolutionSummary: "No semantic output was produced"}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeTransitionGuardFailed)
}

func TestAttemptMustStayTestableAndTerminalOutcomesAreImmutable(t *testing.T) {
	graph, projects := newLifecycleGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	createObjectiveForAttempts(t, graph, projectID, ctx)

	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:orphan-attempt", Context: ctx,
		Operations: []blackboard.Operation{{OpID: "attempt", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:orphan"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{}}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeTransitionGuardFailed)

	open := createOpenAttempt(t, graph, ctx, "attempt:immutable")
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:immutable:failed", Context: ctx,
		Operations: []blackboard.Operation{{OpID: "failed", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:immutable"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "failed", Summary: "The attempt established a useful negative result"}}},
	})
	if err != nil {
		t.Fatalf("fail Attempt: %v", err)
	}
	failed, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeAttempt, Key: "attempt:immutable"})
	if err != nil {
		t.Fatalf("read failed Attempt: %v", err)
	}
	for _, next := range []string{"open", "blocked", "succeeded"} {
		_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:immutable:" + next, Context: ctx,
			Operations: []blackboard.Operation{{OpID: "change", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:immutable"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: failed.Node.Version, Status: next, Summary: "must remain failed"}}},
		})
		assertGraphErrorCode(t, err, blackboard.ErrCodeInvalidTransition)
	}

	open = createOpenAttempt(t, graph, ctx, "attempt:runtime-interrupt")
	runtimeCtx := ctx
	runtimeCtx.ActorType = blackboard.ActorTypeRuntime
	runtimeCtx.ActorID = "runtime"
	runtimeCtx.TaskID = "missing"
	runtimeCtx.ContinuationID = "missing"
	runtimeCtx.RuntimeProfileID = "profile"
	runtimeCtx.Runner = "sandbox"
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:runtime-interrupt", Context: runtimeCtx,
		Operations: []blackboard.Operation{{OpID: "interrupt", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:runtime-interrupt"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "interrupted", Summary: "runtime may not claim interruption"}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeProvenanceSpoofed)

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:ordinary-system-interrupt", Context: ctx,
		Operations: []blackboard.Operation{{OpID: "interrupt", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:runtime-interrupt"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "interrupted", Summary: "ordinary system actors may not claim interruption"}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeTransitionGuardFailed)

	operatorCtx := ctx
	operatorCtx.ActorType = blackboard.ActorTypeOperator
	operatorCtx.ActorID = "operator-c05"
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:operator-interrupt", Context: operatorCtx,
		Operations: []blackboard.Operation{{OpID: "interrupt", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:runtime-interrupt"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "interrupted", Summary: "operator may not claim interruption"}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeTransitionGuardFailed)
}

func TestHypothesisAndConfirmationGuardsAreAtomic(t *testing.T) {
	graph, projects := newLifecycleGraphServices(t)
	projectID, ctx := mustGraphProject(t, projects)
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:unsupported-hypothesis", Context: ctx,
		Operations: []blackboard.Operation{{OpID: "hypothesis", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeHypothesis, StableKey: "hypothesis:unsupported"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"statement": "Unsupported", "status": "supported"}}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeTransitionGuardFailed)

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:hypothesis", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "observation", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeObservation, StableKey: "observation:support"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"summary": "Observed the behavior", "scope_status": "in_scope"}}},
			{OpID: "hypothesis", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeHypothesis, StableKey: "hypothesis:supported"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"statement": "The behavior is reproducible"}}},
			{OpID: "supports", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupports, From: blackboard.NodeRef{OpID: "observation"}, To: blackboard.NodeRef{OpID: "hypothesis"}}},
		},
	})
	if err != nil {
		t.Fatalf("create supported hypothesis fixture: %v", err)
	}
	h, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeHypothesis, Key: "hypothesis:supported"})
	if err != nil {
		t.Fatalf("read hypothesis: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:hypothesis:supported", Context: ctx, Operations: []blackboard.Operation{{OpID: "supported", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeHypothesis, StableKey: "hypothesis:supported"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: h.Node.Version, Status: "supported"}}}})
	if err != nil {
		t.Fatalf("support hypothesis: %v", err)
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:unsupported-confirmation", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:unsupported"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Unsupported assertion", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "side-effect", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeObservation, StableKey: "observation:must-rollback"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"summary": "must roll back", "scope_status": "in_scope"}}},
		},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeTransitionGuardFailed)
	_, err = graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeObservation, Key: "observation:must-rollback"})
	assertGraphErrorCode(t, err, blackboard.ErrCodeNodeNotFound)

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:invalid-cvss", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "evidence", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:invalid-cvss"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"artifact_type": "other", "managed_path": "missing://invalid-cvss", "summary": "Proof fixture", "status": "missing"}}},
			{OpID: "finding", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:invalid-cvss"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Invalid scoring", "status": "confirmed", "target": "example.com", "proof": "Reproduced", "impact": "Impact", "recommendation": "Fix it", "cvss_version": "4.0", "cvss_vector": "CVSS:4.0/AV:Z/AC:L/AT:N/PR:N/UI:N/VC:L/VI:N/VA:N/SC:N/SI:N/SA:N"}}},
			{OpID: "evidences", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence"}, To: blackboard.NodeRef{OpID: "finding"}}},
		},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeInvalidProperty)

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:invalid-cvss-optional", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "evidence", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:invalid-cvss-optional"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"artifact_type": "other", "managed_path": "missing://invalid-cvss-optional", "summary": "Proof fixture", "status": "missing"}}},
			{OpID: "finding", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:invalid-cvss-optional"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Invalid optional scoring", "status": "confirmed", "target": "example.com", "proof": "Reproduced", "impact": "Impact", "recommendation": "Fix it", "cvss_version": "4.0", "cvss_vector": "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:L/VI:N/VA:N/SC:N/SI:N/SA:N/E:BOGUS"}}},
			{OpID: "evidences", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence"}, To: blackboard.NodeRef{OpID: "finding"}}},
		},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeInvalidProperty)

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:confirmed-finding", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "evidence", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:finding"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"artifact_type": "other", "managed_path": "missing://fixture", "summary": "Proof fixture", "status": "missing"}}},
			{OpID: "finding", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:confirmed"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Confirmed issue", "status": "confirmed", "target": "example.com", "proof": "Reproduced", "impact": "Impact", "recommendation": "Fix it", "cvss_version": "4.0", "cvss_vector": "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:L/VI:N/VA:N/SC:N/SI:N/SA:N"}}},
			{OpID: "evidences", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence"}, To: blackboard.NodeRef{OpID: "finding"}}},
		},
	})
	if err != nil {
		t.Fatalf("create confirmed Finding: %v", err)
	}
}

func TestSucceededAttemptSupportsConfirmedConclusionOnlyWithMatchingRuntimeProvenance(t *testing.T) {
	graph, projects := newLifecycleGraphServices(t)
	projectID, systemCtx := mustGraphProject(t, projects)
	createObjectiveForAttempts(t, graph, projectID, systemCtx)
	tasks := task.NewService(graph.DBForTesting(), projects)
	makeRuntime := func(goal string) blackboard.ExecutionContext {
		t.Helper()
		created, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: goal, RuntimeProfileID: "profile-c05", Runner: task.RunnerSandbox})
		if err != nil {
			t.Fatalf("create Task: %v", err)
		}
		continuation, err := tasks.CreateContinuation(created.ID, "profile-c05", "test", task.RunnerSandbox)
		if err != nil {
			t.Fatalf("create Continuation: %v", err)
		}
		return blackboard.ExecutionContext{ProjectID: projectID, ProjectKind: systemCtx.ProjectKind, ActorType: blackboard.ActorTypeRuntime, ActorID: "runtime-c05", TaskID: created.ID, ContinuationID: continuation.ID, RuntimeProfileID: "profile-c05", Runner: string(task.RunnerSandbox)}
	}
	runtimeCtx := makeRuntime("Test the objective")
	open := createOpenAttempt(t, graph, runtimeCtx, "attempt:runtime-success")
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:runtime-success", Context: runtimeCtx,
		Operations: []blackboard.Operation{
			{OpID: "terminal", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:runtime-success"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: open.Node.Version, Status: "succeeded", Summary: "The runtime reproduced the conclusion"}},
			{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:runtime-confirmed"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Runtime-confirmed conclusion", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "produced", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:runtime-success"}, To: blackboard.NodeRef{OpID: "fact"}}},
		},
	})
	if err != nil {
		t.Fatalf("confirm conclusion from matching succeeded Attempt: %v", err)
	}

	operatorCtx := systemCtx
	operatorCtx.ActorType = blackboard.ActorTypeOperator
	operatorCtx.ActorID = "operator-c05"
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:operator-produced-confirmation", Context: operatorCtx,
		Operations: []blackboard.Operation{
			{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:operator-produced"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Operator claim without an explicit confirmation basis", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "produced", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:runtime-success"}, To: blackboard.NodeRef{OpID: "fact"}}},
		},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeTransitionGuardFailed)

	otherRuntime := makeRuntime("Try to claim another continuation's result")
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c05:mismatched-provenance", Context: otherRuntime,
		Operations: []blackboard.Operation{
			{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:mismatched"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Cross-continuation claim", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "produced", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:runtime-success"}, To: blackboard.NodeRef{OpID: "fact"}}},
		},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeProvenanceSpoofed)
}
