package projectinterface_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

type launchFixture struct {
	db      *store.DB
	graph   *blackboard.GraphService
	tasks   *task.Service
	service *projectinterface.Service
	project project.Project
	task    task.Task
}

type failingLaunchTokenSource struct{}

func (failingLaunchTokenSource) NewToken() (string, error) {
	return "", errors.New("token source unavailable")
}

func newLaunchFixture(t *testing.T) launchFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "launch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	proj, err := projects.Create("I06 project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	tasks := task.NewService(db, projects)
	tasks.SetGoalProjector(graph)
	created, err := tasks.Create(task.CreateRequest{
		ProjectID: proj.ID, Goal: "Keep the full graph pinned", RuntimeProfileID: "profile-i06", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	grants := projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
	tasks.SetContinuationTerminalMarker(grants)
	service := projectinterface.NewService(projectinterface.Deps{DB: db, Graph: graph, Grants: grants, Tasks: tasks})
	return launchFixture{db: db, graph: graph, tasks: tasks, service: service, project: proj, task: created}
}

func (f launchFixture) launch(t *testing.T) projectinterface.ContinuationLaunch {
	t.Helper()
	launch, err := f.service.CreateContinuationLaunch(context.Background(), projectinterface.ContinuationLaunchRequest{
		ProjectID: f.project.ID, TaskID: f.task.ID, RuntimeProfileID: "profile-i06",
		RuntimePluginID: "codex", Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"model": "test-model"},
	})
	if err != nil {
		t.Fatalf("create Continuation launch: %v", err)
	}
	return launch
}

func TestContinuationLaunchRollsBackWhenSnapshotUnavailableWithoutRewritingHealth(t *testing.T) {
	fixture := newLaunchFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO blackboard_health_runs
		(project_id,run_id,checked_graph_revision,checked_state_hash,checked_projection_hash,checker_version,status,artifact_scan_status,started_at,completed_at,metrics_json)
		VALUES (?, 'health-before-launch', 1, 'state', 'projection', 'test', 'attention', 'complete', '2024-01-01T00:00:00Z', '2024-01-01T00:00:01Z', '{}')`, fixture.project.ID); err != nil {
		t.Fatalf("seed Health run: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO blackboard_health_results
		(project_id,run_id,fingerprint,code,severity,subject_kind,subject_id,details_json)
		VALUES (?, 'health-before-launch', 'warning-before-launch', 'test_warning', 'warning', 'project', ?, '{}')`, fixture.project.ID, fixture.project.ID); err != nil {
		t.Fatalf("seed Health result: %v", err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM blackboard_graph_state WHERE project_id=?`, fixture.project.ID); err != nil {
		t.Fatalf("remove projection readiness state: %v", err)
	}

	_, err := fixture.service.CreateContinuationLaunch(context.Background(), projectinterface.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: fixture.task.ID, RuntimeProfileID: "profile-i06",
		RuntimePluginID: "codex", Runner: task.RunnerSandbox, RuntimeConfig: map[string]any{"model": "test-model"},
	})
	var interfaceErr *projectinterface.Error
	if !errors.As(err, &interfaceErr) || interfaceErr.Code != projectinterface.ErrCodeSnapshotUnavailable {
		t.Fatalf("launch error = %#v want snapshot_unavailable", err)
	}
	for _, table := range []string{"task_runtime_config_versions", "task_continuations", "blackboard_continuation_grants"} {
		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d partial launch rows", table, count)
		}
	}
	var severity string
	if err := fixture.db.QueryRow(`SELECT severity FROM blackboard_health_results WHERE project_id=? AND fingerprint='warning-before-launch'`, fixture.project.ID).Scan(&severity); err != nil {
		t.Fatalf("read preserved Health result: %v", err)
	}
	if severity != "warning" {
		t.Fatalf("snapshot readiness rewrote Health severity to %q", severity)
	}
}

func TestContinuationLaunchRollsBackConfigPinAndGrantWhenGrantIssuanceFails(t *testing.T) {
	fixture := newLaunchFixture(t)
	grants := projectinterface.NewGrantStore(fixture.db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, failingLaunchTokenSource{})
	service := projectinterface.NewService(projectinterface.Deps{DB: fixture.db, Graph: fixture.graph, Grants: grants, Tasks: fixture.tasks})
	_, err := service.CreateContinuationLaunch(context.Background(), projectinterface.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: fixture.task.ID, RuntimeProfileID: "profile-i06",
		RuntimePluginID: "codex", Runner: task.RunnerSandbox, RuntimeConfig: map[string]any{"model": "test-model"},
	})
	if err == nil || !strings.Contains(err.Error(), "token source unavailable") {
		t.Fatalf("grant issuance error = %v", err)
	}
	for _, table := range []string{"task_runtime_config_versions", "task_continuations", "blackboard_continuation_grants"} {
		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows after atomic rollback", table, count)
		}
	}
}

func TestContinuationResumePinsNewFullGraphAndHistoricalSnapshotRegeneratesAt20K(t *testing.T) {
	fixture := newLaunchFixture(t)
	operations := make([]blackboard.Operation, 0, 64)
	for index := 0; index < 64; index++ {
		key := strconv.Itoa(index)
		operations = append(operations, blackboard.Operation{
			OpID: "fact-" + key, Kind: blackboard.OpCreateNode,
			Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:large:" + key},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
				"category": "service", "scope_status": "in_scope", "summary": strings.Repeat("full-context-", 32) + key,
			}},
		})
	}
	if _, err := fixture.graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "i06:large-graph",
		Context: blackboard.SystemExecutionContext(fixture.project.ID, fixture.project.Kind, "i06-test"), Operations: operations,
	}); err != nil {
		t.Fatalf("seed large graph: %v", err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM blackboard_health_runs WHERE project_id=?`, fixture.project.ID); err != nil {
		t.Fatalf("clear post-write Health so launch must rerun it: %v", err)
	}

	first := fixture.launch(t)
	if first.Projection.EstimatedTokens < 20_000 {
		t.Fatalf("fixture projection is only %d estimated tokens", first.Projection.EstimatedTokens)
	}
	var blocked int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM blackboard_health_results WHERE project_id=? AND code='compaction_blocked'`, fixture.project.ID).Scan(&blocked); err != nil {
		t.Fatalf("read pre-pin Health result: %v", err)
	}
	if blocked != 1 {
		t.Fatalf("pre-pin maintenance did not record compaction_blocked; count=%d", blocked)
	}
	firstPath := filepath.Join(t.TempDir(), "first", ".pentest", "blackboard.json")
	if err := fixture.graph.MaterializeCanonicalMainGraphSnapshot(context.Background(), first.Projection.ImmutablePin(), firstPath); err != nil {
		t.Fatalf("materialize first full snapshot: %v", err)
	}
	firstBytes, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read first full snapshot: %v", err)
	}
	if !bytes.Equal(firstBytes, first.Projection.Bytes) {
		t.Fatal("first Continuation did not receive exact full graph bytes")
	}
	if _, err := fixture.tasks.UpdateContinuationStatus(first.Continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("complete first Continuation: %v", err)
	}
	if _, err := fixture.graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "i06:after-first-pin",
		Context: blackboard.SystemExecutionContext(fixture.project.ID, fixture.project.Kind, "i06-test"),
		Operations: []blackboard.Operation{{
			OpID: "new-current-fact", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:resume:new"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "scope_status": "in_scope", "summary": "visible only to resumed context"}},
		}},
	}); err != nil {
		t.Fatalf("mutate graph before resume: %v", err)
	}

	resumed := fixture.launch(t)
	if resumed.Continuation.ID == first.Continuation.ID || resumed.Continuation.Number != first.Continuation.Number+1 {
		t.Fatalf("resume Continuation identity: first=%+v resumed=%+v", first.Continuation, resumed.Continuation)
	}
	if resumed.Projection.GraphRevision <= first.Projection.GraphRevision || resumed.Projection.Hash == first.Projection.Hash {
		t.Fatalf("resume did not supersede historical context: first=%d/%s resumed=%d/%s", first.Projection.GraphRevision, first.Projection.Hash, resumed.Projection.GraphRevision, resumed.Projection.Hash)
	}
	if err := os.Remove(firstPath); err != nil {
		t.Fatalf("remove first snapshot to simulate crash: %v", err)
	}
	if err := fixture.graph.MaterializeCanonicalMainGraphSnapshot(context.Background(), first.Projection.ImmutablePin(), firstPath); err != nil {
		t.Fatalf("regenerate historical snapshot: %v", err)
	}
	regenerated, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read regenerated historical snapshot: %v", err)
	}
	if !bytes.Equal(regenerated, firstBytes) {
		t.Fatal("regeneration selected a newer graph instead of the original pin")
	}
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM task_continuations WHERE task_id=?`, fixture.task.ID).Scan(&count); err != nil {
		t.Fatalf("count Continuations: %v", err)
	}
	if count != 2 {
		t.Fatalf("snapshot regeneration created another pin; Continuations=%d", count)
	}
}
