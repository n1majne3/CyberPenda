package blackboard_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func newSolutionGraphServices(t *testing.T) (*blackboard.GraphService, *project.Service) {
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

func createCTFTaskContext(t *testing.T, graph *blackboard.GraphService, projects *project.Service, goal string) (project.Project, task.Task, blackboard.ExecutionContext) {
	t.Helper()
	createdProject, err := projects.CreateWithKind("CTF", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Project: %v", err)
	}
	tasks := task.NewService(graph.DBForTesting(), projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: goal, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create CTF Task: %v", err)
	}
	ctx := blackboard.SystemExecutionContext(createdProject.ID, createdProject.Kind, "c06-solution-test")
	ctx.TaskID = createdTask.ID
	return createdProject, createdTask, ctx
}

// TestVerifiedFlagSolvesOnlyCTFProjectAndRejectionReversesSolvedState is C06's
// first red test at BlackboardGraphService.Apply plus the derived CTF state
// read. A verified flag is valid only for a CTF Project, must satisfy its
// producing Task Goal, and ceases to solve the Project when rejected while the
// Solution's versioned lifecycle remains visible.
func TestVerifiedFlagSolvesOnlyCTFProjectAndRejectionReversesSolvedState(t *testing.T) {
	graph, projects := newSolutionGraphServices(t)

	pentestProject, err := projects.Create("Pentest", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Pentest Project: %v", err)
	}
	pentestCtx := blackboard.SystemExecutionContext(pentestProject.ID, pentestProject.Kind, "c06-pentest-test")
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c06:pentest-flag", Context: pentestCtx,
		Operations: []blackboard.Operation{{
			OpID: "flag", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:pentest-flag"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "flag", "summary": "Must be rejected", "value": "FLAG{pentest}", "status": "verified", "verification_summary": "accepted"}},
		}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeProjectKindViolation)

	ctfProject, _, ctfCtx := createCTFTaskContext(t, graph, projects, "Recover the challenge flag")
	state, err := graph.ReadCTFSolvedState(context.Background(), ctfProject.ID)
	if err != nil {
		t.Fatalf("read initial CTF solved state: %v", err)
	}
	if state.Solved || state.PrimaryVerifiedFlag != nil || len(state.VerifiedFlags) != 0 {
		t.Fatalf("zero verified flags must be unsolved, got %+v", state)
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c06:candidate", Context: ctfCtx,
		Operations: []blackboard.Operation{{
			OpID: "flag", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:flag"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "flag", "summary": "Recovered from the challenge", "value": "FLAG{correct}"}},
		}},
	})
	if err != nil {
		t.Fatalf("create candidate flag: %v", err)
	}
	candidate, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: ctfProject.ID, NodeType: blackboard.NodeTypeSolution, Key: "solution:flag"})
	if err != nil {
		t.Fatalf("read candidate flag: %v", err)
	}
	if candidate.Node.PropertyMap["status"] != "candidate" {
		t.Fatalf("Solution status must default to candidate, got %#v", candidate.Node.PropertyMap["status"])
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c06:verify", Context: ctfCtx,
		Operations: []blackboard.Operation{
			{OpID: "verify", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:flag"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: candidate.Node.Version, Status: "verified", VerificationSummary: "The challenge accepted the flag"}},
		},
	})
	if err != nil {
		t.Fatalf("verify satisfying flag: %v", err)
	}

	state, err = graph.ReadCTFSolvedState(context.Background(), ctfProject.ID)
	if err != nil {
		t.Fatalf("read solved CTF state: %v", err)
	}
	if !state.Solved || state.PrimaryVerifiedFlag == nil || len(state.VerifiedFlags) != 1 {
		t.Fatalf("one verified flag must solve the CTF Project, got %+v", state)
	}
	if state.PrimaryVerifiedFlag.StableKey != "solution:flag" {
		t.Fatalf("primary verified flag = %q", state.PrimaryVerifiedFlag.StableKey)
	}
	if len(state.PrimaryVerifiedFlag.SatisfyingGoals) != 0 {
		t.Fatalf("verified flag must not report copied Task Goals, got %+v", state.PrimaryVerifiedFlag.SatisfyingGoals)
	}

	verified, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: ctfProject.ID, NodeType: blackboard.NodeTypeSolution, Key: "solution:flag"})
	if err != nil {
		t.Fatalf("read verified flag: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c06:reject", Context: ctfCtx,
		Operations: []blackboard.Operation{{OpID: "reject", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:flag"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: verified.Node.Version, Status: "rejected"}}},
	})
	if err != nil {
		t.Fatalf("reject verified flag: %v", err)
	}
	state, err = graph.ReadCTFSolvedState(context.Background(), ctfProject.ID)
	if err != nil {
		t.Fatalf("read reversed CTF state: %v", err)
	}
	if state.Solved || state.PrimaryVerifiedFlag != nil || len(state.VerifiedFlags) != 0 {
		t.Fatalf("rejecting all verified flags must reverse solved state, got %+v", state)
	}
	rejected, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: ctfProject.ID, NodeType: blackboard.NodeTypeSolution, Key: "solution:flag"})
	if err != nil {
		t.Fatalf("read rejected flag: %v", err)
	}
	if rejected.Node.PropertyMap["status"] != "rejected" || rejected.Node.Version != 3 {
		t.Fatalf("rejected Solution lifecycle not preserved: version=%d properties=%+v", rejected.Node.Version, rejected.Node.PropertyMap)
	}
	rows, err := graph.DBForTesting().Query(`SELECT json_extract(properties_json, '$.status') FROM blackboard_node_versions WHERE project_id=? AND node_id=? ORDER BY version`, ctfProject.ID, rejected.Node.ID)
	if err != nil {
		t.Fatalf("read Solution history: %v", err)
	}
	defer rows.Close()
	var statuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan Solution history: %v", err)
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Solution history: %v", err)
	}
	wantStatuses := []string{"candidate", "verified", "rejected"}
	if len(statuses) != len(wantStatuses) {
		t.Fatalf("Solution history = %v want %v", statuses, wantStatuses)
	}
	for i := range wantStatuses {
		if statuses[i] != wantStatuses[i] {
			t.Fatalf("Solution history = %v want %v", statuses, wantStatuses)
		}
	}
}

func TestVerifiedFlagRequiresVerificationSummaryButNotTaskGoal(t *testing.T) {
	graph, projects := newSolutionGraphServices(t)
	_, _, ctx := createCTFTaskContext(t, graph, projects, "Produce the flag")

	base := blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "flag", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:guarded"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "flag", "summary": "Guarded flag", "value": "FLAG{guarded}", "status": "verified", "verification_summary": "accepted"}}},
		},
	}
	base.IdempotencyKey = "c06:wrong-goal"
	if _, err := graph.Apply(context.Background(), base); err != nil {
		t.Fatalf("verified flag should not require a Task Goal: %v", err)
	}

	base.IdempotencyKey = "c06:missing-verification"
	base.Operations[0].Create.PropertyMap["verification_summary"] = ""
	_, err := graph.Apply(context.Background(), base)
	assertGraphErrorCode(t, err, blackboard.ErrCodeMissingProperty)
}

func TestCTFSolvedStateOrdersVerifiedFlagsAndReportsDistinctValueConflict(t *testing.T) {
	graph, projects := newSolutionGraphServices(t)
	ctfProject, _, ctx := createCTFTaskContext(t, graph, projects, "Find every accepted flag")

	createVerified := func(idempotencyKey, stableKey, value string) {
		t.Helper()
		_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: idempotencyKey, Context: ctx,
			Operations: []blackboard.Operation{
				{OpID: "flag", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: stableKey}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "flag", "summary": "Accepted flag", "value": value, "status": "verified", "verification_summary": "accepted"}}},
			},
		})
		if err != nil {
			t.Fatalf("create verified flag %s: %v", stableKey, err)
		}
	}

	createVerified("c06:flag-z", "solution:z", "FLAG{same}")
	createVerified("c06:flag-a", "solution:a", "FLAG{same}")
	state, err := graph.ReadCTFSolvedState(context.Background(), ctfProject.ID)
	if err != nil {
		t.Fatalf("read same-value state: %v", err)
	}
	if state.PrimaryVerifiedFlag == nil || state.PrimaryVerifiedFlag.StableKey != "solution:a" {
		t.Fatalf("primary flag must be deterministic by stable key, got %+v", state.PrimaryVerifiedFlag)
	}
	if state.ConflictingVerifiedFlags {
		t.Fatal("duplicate verified values must not be reported as conflicting")
	}

	createVerified("c06:flag-m", "solution:m", "FLAG{different}")
	state, err = graph.ReadCTFSolvedState(context.Background(), ctfProject.ID)
	if err != nil {
		t.Fatalf("read distinct-value state: %v", err)
	}
	if !state.ConflictingVerifiedFlags || len(state.VerifiedFlags) != 3 {
		t.Fatalf("distinct verified values must report a conflict, got %+v", state)
	}
	wantOrder := []string{"solution:a", "solution:m", "solution:z"}
	for i, want := range wantOrder {
		if state.VerifiedFlags[i].StableKey != want {
			t.Fatalf("verified flag order[%d] = %q want %q", i, state.VerifiedFlags[i].StableKey, want)
		}
	}
}

func TestSupersedingEveryVerifiedFlagReversesSolvedState(t *testing.T) {
	graph, projects := newSolutionGraphServices(t)
	ctfProject, _, ctx := createCTFTaskContext(t, graph, projects, "Find and replace the flag")
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c06:verified-old", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "old", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:old"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "flag", "summary": "Old flag", "value": "FLAG{old}", "status": "verified", "verification_summary": "accepted"}}},
		},
	})
	if err != nil {
		t.Fatalf("create old verified flag: %v", err)
	}
	old, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: ctfProject.ID, NodeType: blackboard.NodeTypeSolution, Key: "solution:old"})
	if err != nil {
		t.Fatalf("read old flag: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c06:supersede-old", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "replacement", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:replacement"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "flag", "summary": "Replacement candidate", "value": "FLAG{replacement}"}}},
			{OpID: "supersedes", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupersedes, From: blackboard.NodeRef{OpID: "replacement"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:old"}}},
			{OpID: "transition", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:old"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: old.Node.Version, Status: "superseded"}},
		},
	})
	if err != nil {
		t.Fatalf("supersede verified flag: %v", err)
	}
	state, err := graph.ReadCTFSolvedState(context.Background(), ctfProject.ID)
	if err != nil {
		t.Fatalf("read superseded state: %v", err)
	}
	if state.Solved {
		t.Fatalf("superseding every verified flag must reverse solved state, got %+v", state)
	}
}

func TestCTFSolvedStateRejectsPentestProjects(t *testing.T) {
	graph, projects := newSolutionGraphServices(t)
	pentestProject, err := projects.Create("Pentest", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Pentest Project: %v", err)
	}
	_, err = graph.ReadCTFSolvedState(context.Background(), pentestProject.ID)
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeProjectKindViolation {
		t.Fatalf("expected project_kind_violation, got %v", err)
	}
}
