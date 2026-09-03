package finishreadiness_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/challengeworkflow"
	"pentest/internal/finishreadiness"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

type adapter struct{}

func (adapter) Claim(_ context.Context, request challengeworkflow.PlatformClaimRequest) (challengeworkflow.PlatformClaimResponse, error) {
	return challengeworkflow.PlatformClaimResponse{ExternalAttemptID: "42", ChallengeID: request.ChallengeID, Summary: "claimed"}, nil
}
func (adapter) Submit(_ context.Context, request challengeworkflow.PlatformSubmitRequest) (challengeworkflow.PlatformSubmitResponse, error) {
	return challengeworkflow.PlatformSubmitResponse{Accepted: true, Summary: "accepted"}, nil
}
func (adapter) Abandon(context.Context, challengeworkflow.PlatformAbandonRequest) (challengeworkflow.PlatformAbandonResponse, error) {
	return challengeworkflow.PlatformAbandonResponse{Summary: "abandoned"}, nil
}
func (adapter) Finalize(context.Context, challengeworkflow.PlatformFinalizeRequest) (challengeworkflow.PlatformFinalizeResponse, error) {
	return challengeworkflow.PlatformFinalizeResponse{Summary: "finalized"}, nil
}

type noOpRecorder struct{}

func (noOpRecorder) RecordClaim(context.Context, challengeworkflow.RecordClaimRequest) error {
	return nil
}
func (noOpRecorder) RecordSubmission(context.Context, challengeworkflow.RecordSubmissionRequest) error {
	return nil
}
func (noOpRecorder) RecordAbandon(context.Context, challengeworkflow.RecordAbandonRequest) error {
	return nil
}
func (noOpRecorder) RecordFinalize(context.Context, challengeworkflow.RecordFinalizeRequest) error {
	return nil
}

func fixture(t *testing.T) (*store.DB, *project.Service, *task.Service, project.Project, task.Task, string) {
	return fixtureWithBlackboardMode(t, task.BlackboardModeInteractive)
}

func fixtureWithBlackboardMode(t *testing.T, mode task.BlackboardMode) (*store.DB, *project.Service, *task.Service, project.Project, task.Task, string) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	proj, err := projects.CreateWithKind("Arena", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	tasks := task.NewService(db, projects)
	created, err := tasks.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypeCTFChallenge,
		Goal:      "solve",
		Runner:    task.RunnerSandbox,
		RunControls: task.RunControls{
			BlackboardMode: mode,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.CreateContinuation(created.ID, "profile", "codex", task.RunnerSandbox); err != nil {
		t.Fatal(err)
	}
	return db, projects, tasks, proj, created, root
}

func TestReadinessReportsOpenWorkflowAndMissingEvidence(t *testing.T) {
	db, projects, tasks, proj, created, _ := fixture(t)
	workflow := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": adapter{}}, noOpRecorder{})
	if _, err := workflow.Claim(context.Background(), challengeworkflow.ClaimRequest{ProjectID: proj.ID, TaskID: created.ID, Platform: "arena", OperationID: "claim-1", ChallengeID: "3121"}); err != nil {
		t.Fatal(err)
	}
	readiness, err := finishreadiness.NewService(db, tasks).Evaluate(context.Background(), proj.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.ReadyToFinish {
		t.Fatalf("expected blockers, got %#v", readiness)
	}
	if !hasCode(readiness, finishreadiness.BlockerOpenChallengeAttempts) || !hasCode(readiness, finishreadiness.BlockerMissingChallengeEvidence) {
		t.Fatalf("blockers = %#v", readiness.Blockers)
	}
}

func TestDisabledReadinessRetainsChallengeWorkflowBlockers(t *testing.T) {
	db, _, tasks, proj, created, _ := fixtureWithBlackboardMode(t, task.BlackboardModeDisabled)
	// Disabled rejects new Challenge Workflow writes. Seed durable legacy state
	// directly so this test only specifies how Finish treats existing blockers.
	if _, err := db.Exec(`INSERT INTO challenge_attempts (
		project_id,task_id,platform,external_attempt_id,challenge_id,attempt_key,objective_key,
		status,last_progress_at,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,'open',?,?,?)`,
		proj.ID, created.ID, "arena", "42", "3121", "attempt:arena:42", "objective:arena:42",
		"2026-08-30T00:00:00Z", "2026-08-30T00:00:00Z", "2026-08-30T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO challenge_operations (
		task_id,operation_id,project_id,platform,kind,request_hash,request_json,state,
		external_attempt_id,response_json,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,'completed',?,?,?,?)`,
		created.ID, "disabled-legacy-claim", proj.ID, "arena", "claim",
		"0000000000000000000000000000000000000000000000000000000000000000", "{}", "42", "{}",
		"2026-08-30T00:00:00Z", "2026-08-30T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	readiness, err := finishreadiness.NewService(db, tasks).Evaluate(context.Background(), proj.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.ReadyToFinish {
		t.Fatalf("expected challenge workflow blockers, got %#v", readiness)
	}
	if !hasCode(readiness, finishreadiness.BlockerOpenChallengeAttempts) || !hasCode(readiness, finishreadiness.BlockerMissingChallengeEvidence) {
		t.Fatalf("blockers = %#v", readiness.Blockers)
	}
}

func TestReadinessIsReadyAfterAcceptedWorkflow(t *testing.T) {
	db, projects, tasks, proj, created, root := fixture(t)
	runtimeRoot := filepath.Join(root, "runtime")
	if err := os.MkdirAll(filepath.Join(runtimeRoot, created.ID, "workdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	blackboard := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: artifactRoot})
	workflow := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": adapter{}}, challengeworkflow.NewBlackboardRecorder(blackboard, tasks, runtimeRoot))
	claim, err := workflow.Claim(context.Background(), challengeworkflow.ClaimRequest{ProjectID: proj.ID, TaskID: created.ID, Platform: "arena", OperationID: "claim-ready", ChallengeID: "3121"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Submit(context.Background(), challengeworkflow.SubmitRequest{ProjectID: proj.ID, TaskID: created.ID, Platform: "arena", OperationID: "submit-ready", ExternalAttemptID: claim.ExternalAttemptID, Candidate: "FLAG{ok}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Finalize(context.Background(), challengeworkflow.FinalizeRequest{ProjectID: proj.ID, TaskID: created.ID, Platform: "arena", OperationID: "finalize-ready", ExternalAttemptID: claim.ExternalAttemptID}); err != nil {
		t.Fatal(err)
	}
	readiness, err := finishreadiness.NewService(db, tasks).Evaluate(context.Background(), proj.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.ReadyToFinish {
		t.Fatalf("unexpected blockers: %#v", readiness.Blockers)
	}
}

func hasCode(readiness finishreadiness.Readiness, code string) bool {
	for _, blocker := range readiness.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}
