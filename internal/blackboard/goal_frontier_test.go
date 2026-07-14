package blackboard_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/task"
)

// TestTaskCreationProjectsExactlyOneGoalAndFrontierWaitsForPrerequisites is
// the C04 first-red behavioral test. Durable Task state owns exactly one Goal,
// while Objective readiness is derived from current prerequisite state.
func TestTaskCreationProjectsExactlyOneGoalAndFrontierWaitsForPrerequisites(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, systemContext := mustGraphProject(t, projects)
	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)

	createdTask, err := tasks.Create(task.CreateRequest{
		ProjectID: projectID,
		Goal:      "Map the exposed authentication surface",
		Runner:    task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	goalKey := "task:" + createdTask.ID + ":goal"
	goal, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: projectID,
		NodeType:  blackboard.NodeTypeGoal,
		Key:       goalKey,
	})
	if err != nil {
		t.Fatalf("read projected goal: %v", err)
	}
	if got := goal.Node.PropertyMap["task_id"]; got != createdTask.ID {
		t.Fatalf("goal task_id: got %v want %q", got, createdTask.ID)
	}
	if got := goal.Node.PropertyMap["text"]; got != createdTask.Goal {
		t.Fatalf("goal text: got %v want %q", got, createdTask.Goal)
	}
	if got := goal.Node.PropertyMap["task_status"]; got != string(task.StatusPending) {
		t.Fatalf("goal task_status: got %v want pending", got)
	}

	// Reconciliation is idempotent and cannot create a second Goal.
	if err := graph.RepairTaskGoals(context.Background()); err != nil {
		t.Fatalf("repair task goals: %v", err)
	}
	repaired, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: projectID,
		NodeType:  blackboard.NodeTypeGoal,
		Key:       goalKey,
	})
	if err != nil {
		t.Fatalf("read repaired goal: %v", err)
	}
	if repaired.Node.ID != goal.Node.ID || repaired.Node.Version != goal.Node.Version {
		t.Fatalf("repair duplicated or rewrote exact goal: before=%+v after=%+v", goal.Node, repaired.Node)
	}

	objective := func(opID, key, text string) blackboard.Operation {
		return blackboard.Operation{
			OpID: opID,
			Kind: blackboard.OpCreateNode,
			Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: key},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
				"objective": text,
				"status":    "open",
			}},
		}
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "c04:create-objectives",
		Context:        systemContext,
		Operations: []blackboard.Operation{
			objective("prerequisite", "objective:enumerate-login", "Which login endpoints are exposed?"),
			objective("dependent", "objective:test-auth", "Can authentication be bypassed?"),
			{
				OpID: "depends",
				Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{
					EdgeType: blackboard.EdgeTypeDependsOn,
					From:     blackboard.NodeRef{OpID: "dependent"},
					To:       blackboard.NodeRef{OpID: "prerequisite"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create objective dependency: %v", err)
	}

	frontier, err := graph.ExplorationFrontier(context.Background(), projectID)
	if err != nil {
		t.Fatalf("read initial frontier: %v", err)
	}
	if len(frontier.Objectives) != 1 || frontier.Objectives[0].StableKey != "objective:enumerate-login" {
		t.Fatalf("initial frontier: got %+v want prerequisite only", frontier.Objectives)
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "c04:resolve-prerequisite",
		Context:        systemContext,
		Operations: []blackboard.Operation{
			{
				OpID: "fact",
				Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:login-endpoint"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category":     "service",
					"summary":      "Login endpoint identified",
					"confidence":   "tentative",
					"scope_status": "in_scope",
				}},
			},
			{
				OpID: "satisfies",
				Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{
					EdgeType: blackboard.EdgeTypeSatisfies,
					From:     blackboard.NodeRef{OpID: "fact"},
					To: blackboard.NodeRef{
						NodeType:  blackboard.NodeTypeExplorationObjective,
						StableKey: "objective:enumerate-login",
					},
				},
			},
			{
				OpID: "resolve",
				Kind: blackboard.OpTransitionNode,
				Node: blackboard.NodeRef{
					NodeType:  blackboard.NodeTypeExplorationObjective,
					StableKey: "objective:enumerate-login",
				},
				Transition: blackboard.TransitionNodeInput{
					ExpectedVersion:   1,
					Status:            "resolved",
					ResolutionSummary: "The login endpoint is now known.",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("resolve prerequisite: %v", err)
	}

	frontier, err = graph.ExplorationFrontier(context.Background(), projectID)
	if err != nil {
		t.Fatalf("read resolved frontier: %v", err)
	}
	if len(frontier.Objectives) != 1 || frontier.Objectives[0].StableKey != "objective:test-auth" {
		t.Fatalf("resolved frontier: got %+v want dependent only", frontier.Objectives)
	}
}

func TestGoalsAreSystemOwnedAndTaskStatusProjectionConverges(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	operator := blackboard.ExecutionContext{ProjectID: projectID, ProjectKind: "pentest", ActorType: blackboard.ActorTypeOperator, ActorID: "operator-1"}
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "operator:create-goal", Context: operator,
		Operations: []blackboard.Operation{{OpID: "goal", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeGoal, StableKey: "task:fake:goal"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"task_id": "fake", "text": "mutable", "task_status": "pending"}}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeInvalidRequest)

	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)
	created, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "Converge", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatalf("update task status: %v", err)
	}
	// Re-running the projector observes durable state and is an exact no-op.
	if err := graph.ProjectTaskGoal(created.ID); err != nil {
		t.Fatalf("repeat projection: %v", err)
	}
	goal, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeGoal, Key: "task:" + created.ID + ":goal"})
	if err != nil {
		t.Fatalf("read goal: %v", err)
	}
	if goal.Node.PropertyMap["task_status"] != "running" || goal.Node.Version != 2 {
		t.Fatalf("projected status/version: %+v", goal.Node)
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "operator:patch-goal", Context: operator,
		Operations: []blackboard.Operation{{OpID: "patch", Kind: blackboard.OpPatchNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeGoal, StableKey: "task:" + created.ID + ":goal"}, Patch: blackboard.PatchNodeInput{ExpectedVersion: 2, Properties: map[string]any{"text": "changed"}}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeInvalidRequest)
}

func TestTaskGoalRepairRejectsImmutableDrift(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)
	created, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "Immutable", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	key := "task:" + created.ID + ":goal"
	if _, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeGoal, Key: key}); err != nil {
		t.Fatalf("read goal: %v", err)
	}
	// Simulate legacy/corrupt durable Task text drift. Repair must surface the
	// immutable mismatch rather than silently rewriting the projected Goal.
	_, err = graph.DBForTesting().Exec(`UPDATE tasks SET goal=?, updated_at=? WHERE id=?`, "drifted", "2026-01-01T00:00:00Z", created.ID)
	if err != nil {
		t.Fatalf("inject immutable drift: %v", err)
	}
	assertGraphErrorCode(t, graph.RepairTaskGoals(context.Background()), blackboard.ErrCodeInvariantViolation)
}

func TestObjectiveLifecycleAndBlocksKeepFrontierDeterministic(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, systemContext := mustGraphProject(t, projects)
	objective := func(opID, key string) blackboard.Operation {
		return blackboard.Operation{OpID: opID, Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: key}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": key, "status": "open"}}}
	}
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:blocks", Context: systemContext, Operations: []blackboard.Operation{
		objective("blocker", "objective:blocker"), objective("blocked", "objective:blocked"),
		{OpID: "blocks", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeBlocks, From: blackboard.NodeRef{OpID: "blocker"}, To: blackboard.NodeRef{OpID: "blocked"}}},
	}})
	if err != nil {
		t.Fatalf("create blocking objectives: %v", err)
	}
	frontier, err := graph.ExplorationFrontier(context.Background(), projectID)
	if err != nil {
		t.Fatalf("frontier: %v", err)
	}
	if len(frontier.Objectives) != 1 || frontier.Objectives[0].StableKey != "objective:blocker" {
		t.Fatalf("blocked frontier: %+v", frontier)
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:resolve-without-satisfies", Context: systemContext, Operations: []blackboard.Operation{{OpID: "resolve", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:blocker"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "resolved", ResolutionSummary: "not enough"}}}})
	assertGraphErrorCode(t, err, blackboard.ErrCodeTransitionGuardFailed)

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:abandon-blocker", Context: systemContext, Operations: []blackboard.Operation{{OpID: "abandon", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:blocker"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "abandoned", ResolutionSummary: "cannot proceed"}}}})
	if err != nil {
		t.Fatalf("abandon blocker: %v", err)
	}
	frontier, err = graph.ExplorationFrontier(context.Background(), projectID)
	if err != nil {
		t.Fatalf("frontier after abandonment: %v", err)
	}
	if len(frontier.Objectives) != 0 {
		t.Fatalf("abandoned prerequisite must strand dependent: %+v", frontier)
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:reopen-terminal", Context: systemContext, Operations: []blackboard.Operation{{OpID: "reopen", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:blocker"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 2, Status: "resolved", ResolutionSummary: "reopen"}}}})
	assertGraphErrorCode(t, err, blackboard.ErrCodeInvalidTransition)
}

func assertGraphErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected graph error %q", code)
	}
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if validation.Code != code {
		t.Fatalf("error code: got %q want %q (%v)", validation.Code, code, err)
	}
}

func TestTaskGoalRepairCreatesMissingAndSynchronizesStaleStatus(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	// Simulate startup before production wiring by creating durable Task state
	// without a projector, then let reconciliation create the missing Goal.
	tasks := task.NewService(graph.DBForTesting(), projects)
	created, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "Repair me", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create unprojected task: %v", err)
	}
	if err := graph.RepairTaskGoals(context.Background()); err != nil {
		t.Fatalf("repair missing Goal: %v", err)
	}
	key := "task:" + created.ID + ":goal"
	first, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeGoal, Key: key})
	if err != nil {
		t.Fatalf("read repaired Goal: %v", err)
	}

	_, err = graph.DBForTesting().Exec(`UPDATE tasks SET status='paused',updated_at=? WHERE id=?`, "2026-01-02T00:00:00Z", created.ID)
	if err != nil {
		t.Fatalf("make Goal status stale: %v", err)
	}
	if err := graph.RepairTaskGoals(context.Background()); err != nil {
		t.Fatalf("repair stale Goal: %v", err)
	}
	second, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeGoal, Key: key})
	if err != nil {
		t.Fatalf("read status-repaired Goal: %v", err)
	}
	if second.Node.PropertyMap["task_status"] != "paused" || second.Node.Version != first.Node.Version+1 {
		t.Fatalf("stale status repair: before=%+v after=%+v", first.Node, second.Node)
	}
}

func TestExplorationObjectiveCreationDefaultsOpenAndRejectsTerminalState(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	_, systemContext := mustGraphProject(t, projects)

	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:objective-default-open", Context: systemContext,
		Operations: []blackboard.Operation{{
			OpID: "objective", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:default-open"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "What is exposed?"}},
		}},
	})
	if err != nil {
		t.Fatalf("create objective without status: %v", err)
	}
	got, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: systemContext.ProjectID, NodeType: blackboard.NodeTypeExplorationObjective, Key: "objective:default-open"})
	if err != nil {
		t.Fatalf("read default-open objective: %v", err)
	}
	if got.Node.PropertyMap["status"] != "open" || created.Operations[0].NodeVersion != 1 {
		t.Fatalf("default lifecycle: result=%+v node=%+v", created.Operations[0], got.Node)
	}

	for _, tc := range []struct {
		name  string
		props map[string]any
	}{
		{name: "terminal status", props: map[string]any{"objective": "Already done", "status": "resolved", "resolution_summary": "done"}},
		{name: "system timestamp", props: map[string]any{"objective": "Timestamp injection", "status": "open", "resolved_at": "2026-01-01T00:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
				SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:reject-create:" + strings.ReplaceAll(tc.name, " ", "-"), Context: systemContext,
				Operations: []blackboard.Operation{{OpID: "objective", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:reject-" + strings.ReplaceAll(tc.name, " ", "-")}, Create: blackboard.CreateNodeInput{PropertyMap: tc.props}}},
			})
			if tc.name == "terminal status" {
				assertGraphErrorCode(t, err, blackboard.ErrCodeInvalidTransition)
			} else {
				assertGraphErrorCode(t, err, blackboard.ErrCodeInvalidProperty)
			}
		})
	}
}

func TestExplorationObjectiveTransitionRequiresCurrentVersion(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	_, systemContext := mustGraphProject(t, projects)
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:create-versioned-objective", Context: systemContext,
		Operations: []blackboard.Operation{{OpID: "objective", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:versioned"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Version me"}}}},
	})
	if err != nil {
		t.Fatalf("create objective: %v", err)
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:stale-transition", Context: systemContext,
		Operations: []blackboard.Operation{{OpID: "abandon", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:versioned"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 2, Status: "abandoned", ResolutionSummary: "stale writer"}}},
	})
	assertGraphErrorCode(t, err, blackboard.ErrCodeVersionConflict)

	got, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: systemContext.ProjectID, NodeType: blackboard.NodeTypeExplorationObjective, Key: "objective:versioned"})
	if err != nil {
		t.Fatalf("read objective: %v", err)
	}
	if got.Node.Version != 1 || got.Node.PropertyMap["status"] != "open" {
		t.Fatalf("stale transition changed state: %+v", got.Node)
	}
}

func TestFrontierExcludesSupersededArchivedMergedMissingAndCorruptPrerequisites(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, systemContext := mustGraphProject(t, projects)
	objective := func(opID string) blackboard.Operation {
		return blackboard.Operation{OpID: opID, Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:" + opID}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": opID}}}
	}
	var operations []blackboard.Operation
	for _, name := range []string{"superseder", "superseded", "needs-superseded", "archived", "needs-archived", "merge-target", "merged", "needs-merged", "missing", "needs-missing", "corrupt", "needs-corrupt"} {
		operations = append(operations, objective(name))
	}
	for _, pair := range [][2]string{{"needs-superseded", "superseded"}, {"needs-archived", "archived"}, {"needs-merged", "merged"}, {"needs-missing", "missing"}, {"needs-corrupt", "corrupt"}} {
		operations = append(operations, blackboard.Operation{OpID: "depends-" + pair[0], Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeDependsOn, From: blackboard.NodeRef{OpID: pair[0]}, To: blackboard.NodeRef{OpID: pair[1]}}})
	}
	operations = append(operations,
		blackboard.Operation{OpID: "supersedes", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupersedes, From: blackboard.NodeRef{OpID: "superseder"}, To: blackboard.NodeRef{OpID: "superseded"}}},
	)
	result, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:all-prerequisite-states", Context: systemContext, Operations: operations})
	if err != nil {
		t.Fatalf("create prerequisite fixtures: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c04:mark-superseded", Context: systemContext, Operations: []blackboard.Operation{{OpID: "mark-superseded", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:superseded"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "superseded", ResolutionSummary: "replaced by a narrower objective"}}}})
	if err != nil {
		t.Fatalf("mark prerequisite superseded: %v", err)
	}
	ids := map[string]string{}
	for i, op := range operations {
		if op.Kind == blackboard.OpCreateNode {
			ids[op.OpID] = result.Operations[i].NodeID
		}
	}
	if _, err := graph.DBForTesting().Exec(`UPDATE blackboard_node_heads SET disposition='archived' WHERE project_id=? AND node_id=?`, projectID, ids["archived"]); err != nil {
		t.Fatalf("archive prerequisite fixture: %v", err)
	}
	if _, err := graph.DBForTesting().Exec(`UPDATE blackboard_node_heads SET disposition='merged',merge_target_id=? WHERE project_id=? AND node_id=?`, ids["merge-target"], projectID, ids["merged"]); err != nil {
		t.Fatalf("merge prerequisite fixture: %v", err)
	}
	if _, err := graph.DBForTesting().Exec(`DELETE FROM blackboard_node_heads WHERE project_id=? AND node_id=?`, projectID, ids["missing"]); err != nil {
		t.Fatalf("remove prerequisite head fixture: %v", err)
	}
	if _, err := graph.DBForTesting().Exec(`
		INSERT INTO blackboard_node_versions(project_id,node_id,version,result_graph_revision,mutation_seq,operation_index,schema_version,disposition,merge_target_id,properties_json,semantic_hash,updated_at)
		SELECT project_id,node_id,2,result_graph_revision,mutation_seq,operation_index,schema_version,disposition,merge_target_id,'[]',semantic_hash,updated_at
		FROM blackboard_node_versions WHERE project_id=? AND node_id=? AND version=1`, projectID, ids["corrupt"]); err != nil {
		t.Fatalf("insert corrupt prerequisite version: %v", err)
	}
	if _, err := graph.DBForTesting().Exec(`UPDATE blackboard_node_heads SET version=2 WHERE project_id=? AND node_id=?`, projectID, ids["corrupt"]); err != nil {
		t.Fatalf("point prerequisite at corrupt version: %v", err)
	}

	frontier, err := graph.ExplorationFrontier(context.Background(), projectID)
	if err != nil {
		t.Fatalf("frontier must handle unhealthy prerequisites deterministically: %v", err)
	}
	blocked := map[string]bool{"objective:needs-superseded": true, "objective:needs-archived": true, "objective:needs-merged": true, "objective:needs-missing": true, "objective:needs-corrupt": true}
	for _, item := range frontier.Objectives {
		if blocked[item.StableKey] {
			t.Fatalf("blocked objective leaked into frontier: %+v", item)
		}
	}
}

func TestConcurrentTaskStatusProjectionConvergesToDurableState(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)
	created, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "Converge concurrently", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	statuses := []task.Status{task.StatusRunning, task.StatusPaused, task.StatusRunning, task.StatusPaused}
	errs := make(chan error, len(statuses))
	var wg sync.WaitGroup
	for _, status := range statuses {
		wg.Add(1)
		go func(status task.Status) {
			defer wg.Done()
			_, err := tasks.UpdateStatus(created.ID, status)
			errs <- err
		}(status)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent status projection: %v", err)
		}
	}

	durable, err := tasks.Get(created.ID)
	if err != nil {
		t.Fatalf("read durable task: %v", err)
	}
	if err := graph.ProjectTaskGoal(created.ID); err != nil {
		t.Fatalf("final convergence projection: %v", err)
	}
	goal, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeGoal, Key: "task:" + created.ID + ":goal"})
	if err != nil {
		t.Fatalf("read projected goal: %v", err)
	}
	if goal.Node.PropertyMap["task_status"] != string(durable.Status) {
		t.Fatalf("Goal status %v did not converge to durable Task status %q", goal.Node.PropertyMap["task_status"], durable.Status)
	}
}
