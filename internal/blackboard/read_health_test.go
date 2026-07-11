package blackboard_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/task"
)

func TestLatestHealthReportsStalenessIndependentlyFromOverallSeverity(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "Health anchor", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task Goal: %v", err)
	}
	run, err := graph.RunHealth(context.Background(), projectID)
	if err != nil {
		t.Fatalf("run Health: %v", err)
	}
	if run.Status != blackboard.HealthStatusHealthy {
		t.Fatalf("initial Health overall = %q", run.Status)
	}
	if _, err := tasks.UpdateStatus(createdTask.ID, task.StatusRunning); err != nil {
		t.Fatalf("advance graph after Health: %v", err)
	}
	if _, err := graph.DBForTesting().Exec(`DELETE FROM blackboard_health_results WHERE project_id=? AND run_id<>?`, projectID, run.RunID); err != nil {
		t.Fatalf("remove automatic newer Health results: %v", err)
	}
	if _, err := graph.DBForTesting().Exec(`DELETE FROM blackboard_health_runs WHERE project_id=? AND run_id<>?`, projectID, run.RunID); err != nil {
		t.Fatalf("remove automatic newer Health runs: %v", err)
	}

	envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindBlackboardHealthV1, BlackboardHealth: &blackboard.BlackboardHealthRequest{}})
	if err != nil {
		t.Fatalf("read latest Health: %v", err)
	}
	got := envelope.Result.(blackboard.BlackboardHealthV1)
	if got.LatestRun == nil || got.LatestRun.RunID != run.RunID || got.LatestRun.Overall != blackboard.HealthStatusHealthy || !got.LatestRun.Stale {
		t.Fatalf("latest Health = %#v", got.LatestRun)
	}
	if got.CurrentGraph.Revision != run.CheckedGraphRevision+1 || got.LatestRun.CheckedGraphRevision != run.CheckedGraphRevision {
		t.Fatalf("Health revisions current=%d checked=%d", got.CurrentGraph.Revision, got.LatestRun.CheckedGraphRevision)
	}
}

func TestExplicitHealthRunIsIdempotentAndNeverChangesGraphRevision(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)
	if _, err := tasks.Create(task.CreateRequest{ProjectID: projectID, Goal: "Explicit Health anchor", Runner: task.RunnerSandbox}); err != nil {
		t.Fatalf("create Task Goal: %v", err)
	}
	before, err := graph.Reconstruct(context.Background(), projectID, 1)
	if err != nil {
		t.Fatalf("read graph before Health: %v", err)
	}
	first, err := graph.RunHealthExplicit(context.Background(), projectID, "health-request-1", "quick")
	if err != nil {
		t.Fatalf("run explicit Health: %v", err)
	}
	replayed, err := graph.RunHealthExplicit(context.Background(), projectID, "health-request-1", "quick")
	if err != nil {
		t.Fatalf("replay explicit Health: %v", err)
	}
	if replayed.RunID != first.RunID {
		t.Fatalf("Health replay run_id = %q want %q", replayed.RunID, first.RunID)
	}
	_, err = graph.RunHealthExplicit(context.Background(), projectID, "health-request-1", "full")
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeIdempotencyConflict {
		t.Fatalf("changed explicit Health request error = %#v", err)
	}
	after, err := graph.Reconstruct(context.Background(), projectID, 1)
	if err != nil {
		t.Fatalf("read graph after Health: %v", err)
	}
	if after.GraphRevision != before.GraphRevision || after.StateHash != before.StateHash {
		t.Fatalf("Health changed graph: before=%#v after=%#v", before, after)
	}
}

func TestHealthSupportsAProjectBeforeItsFirstGraphMutation(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	read := blackboard.NewBlackboardReadService(graph.DBForTesting())
	envelope, err := read.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindBlackboardHealthV1, BlackboardHealth: &blackboard.BlackboardHealthRequest{}})
	if err != nil {
		t.Fatalf("read empty-project Health: %v", err)
	}
	summary := envelope.Result.(blackboard.BlackboardHealthV1)
	if summary.CurrentGraph.Revision != 0 || summary.LatestRun != nil || summary.Overall != blackboard.HealthStatusUnknown {
		t.Fatalf("empty-project Health = %#v", summary)
	}
	run, err := graph.RunHealthExplicit(context.Background(), projectID, "empty-project-health", "quick")
	if err != nil {
		t.Fatalf("run empty-project Health: %v", err)
	}
	if run.CheckedGraphRevision != 0 {
		t.Fatalf("empty-project checked revision = %d", run.CheckedGraphRevision)
	}
}

func TestExplicitHealthVerifiesEvidenceArtifactsAndFilesystemChangesMakeRunStale(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	artifactRoot := t.TempDir()
	graph.WithArtifactRoot(artifactRoot)
	if err := os.MkdirAll(filepath.Join(artifactRoot, "artifacts"), 0o700); err != nil {
		t.Fatalf("create Artifact Root: %v", err)
	}
	artifactPath := filepath.Join(artifactRoot, "artifacts", "evidence.txt")
	if err := os.WriteFile(artifactPath, []byte("original evidence"), 0o600); err != nil {
		t.Fatalf("write Evidence artifact: %v", err)
	}
	sum := sha256.Sum256([]byte("original evidence"))
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u03:artifact-health", Context: execCtx, Operations: []blackboard.Operation{{OpID: "evidence", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:artifact-health"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"artifact_type": "file", "managed_path": "artifacts/evidence.txt", "sha256": hex.EncodeToString(sum[:]), "summary": "Artifact Health fixture", "status": "available"}}}}}); err != nil {
		t.Fatalf("seed Evidence artifact: %v", err)
	}
	run, err := graph.RunHealthExplicit(context.Background(), projectID, "artifact-health", "quick")
	if err != nil {
		t.Fatalf("run Artifact Health: %v", err)
	}
	if run.ArtifactScanStatus != "completed" || healthHasCode(run, "evidence_missing") {
		t.Fatalf("Artifact Health run = %#v", run)
	}
	if err := os.WriteFile(artifactPath, []byte("changed evidence"), 0o600); err != nil {
		t.Fatalf("change Evidence artifact: %v", err)
	}
	envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).WithArtifactRoot(artifactRoot).Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindBlackboardHealthV1, BlackboardHealth: &blackboard.BlackboardHealthRequest{}})
	if err != nil {
		t.Fatalf("read stale Artifact Health: %v", err)
	}
	latest := envelope.Result.(blackboard.BlackboardHealthV1).LatestRun
	if latest == nil || !latest.Stale || latest.Overall != run.Status {
		t.Fatalf("filesystem-stale Health = %#v", latest)
	}
}

func TestFailedExplicitHealthRunIsPersistedWithoutChangingGraph(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	action, err := graph.StartHealthRun(context.Background(), projectID, "cancelled-health", "quick")
	if err != nil {
		t.Fatalf("start explicit Health: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	failed, err := graph.CompleteHealthRun(cancelled, projectID, action.RunID, "quick")
	if err == nil {
		t.Fatal("expected cancelled Health completion to fail")
	}
	if failed.RunID != action.RunID || failed.RunStatus != "cancelled" || failed.Status != blackboard.HealthStatusUnknown || !healthHasCode(failed, "budget_unknown") {
		t.Fatalf("persisted failed Health = %#v", failed)
	}
	var revision int
	if err := graph.DBForTesting().QueryRow(`SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&revision); err != nil {
		t.Fatalf("read graph revision after failed Health: %v", err)
	}
	if revision != 0 {
		t.Fatalf("failed Health changed graph revision to %d", revision)
	}
}
