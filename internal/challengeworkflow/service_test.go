package challengeworkflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/challengeworkflow"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

type platformStub struct {
	claimCalls    int
	submitCalls   int
	abandonCalls  int
	finalizeCalls int
}

func TestChallengeWorkflowRequiresCTFChallengeTaskTypeSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	proj, err := projects.CreateWithKind("Arena", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypeCTFChallenge, Goal: "solve", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := db.Exec(`UPDATE tasks SET task_type='pentest' WHERE id=?`, createdTask.ID); err != nil {
		t.Fatalf("simulate a historical Pentest Task after Project conversion: %v", err)
	}

	service := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": &platformStub{}}, nil)
	_, err = service.Claim(context.Background(), challengeworkflow.ClaimRequest{
		ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "claim-wrong-type", ChallengeID: "3121",
	})
	if !errors.Is(err, challengeworkflow.ErrTaskType) {
		t.Fatalf("expected CTF Challenge Task Type requirement, got %v", err)
	}
}

func TestAcceptedSubmissionCreatesVerifiedSolutionAndExactResponseEvidence(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	proj, err := projects.CreateWithKind("Arena", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypeCTFChallenge, Goal: "solve", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := tasks.CreateContinuation(createdTask.ID, "profile", "codex", task.RunnerSandbox); err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	if err := os.MkdirAll(filepath.Join(runtimeRoot, createdTask.ID, "workdir"), 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatalf("create artifact root: %v", err)
	}
	blackboard := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: artifactRoot})
	platform := &platformStub{}
	service := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, challengeworkflow.NewBlackboardRecorder(blackboard, tasks, runtimeRoot))

	claim, err := service.Claim(context.Background(), challengeworkflow.ClaimRequest{ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "claim-accepted", ChallengeID: "3121"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	result, err := service.Submit(context.Background(), challengeworkflow.SubmitRequest{ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "submit-accepted", ExternalAttemptID: claim.ExternalAttemptID, Candidate: "FLAG{ok}"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted submission")
	}

	solution, err := blackboard.ReadCurrent(context.Background(), proj.ID, "solution/arena/attempt-42")
	if err != nil || solution.Record.Status != "verified" || solution.Record.Value != "FLAG{ok}" {
		t.Fatalf("verified Solution = %#v, %v", solution, err)
	}
	evidence, err := blackboard.ReadCurrent(context.Background(), proj.ID, result.EvidenceKey)
	if err != nil || evidence.Record.ArtifactType != "api_response" || evidence.Record.MediaType != "application/json" || evidence.Record.SHA256 == "" {
		t.Fatalf("retained Evidence = %#v, %v", evidence, err)
	}
	if evidence.Record.SourcePath != filepath.Join("challenge-workflow", "submit-accepted.json") {
		t.Fatalf("Evidence source = %q", evidence.Record.SourcePath)
	}
}

func (stub *platformStub) Claim(_ context.Context, request challengeworkflow.PlatformClaimRequest) (challengeworkflow.PlatformClaimResponse, error) {
	stub.claimCalls++
	return challengeworkflow.PlatformClaimResponse{ExternalAttemptID: "attempt-42", ChallengeID: request.ChallengeID, Summary: "claimed", Rating: 2100}, nil
}

func (stub *platformStub) Submit(_ context.Context, request challengeworkflow.PlatformSubmitRequest) (challengeworkflow.PlatformSubmitResponse, error) {
	stub.submitCalls++
	return challengeworkflow.PlatformSubmitResponse{Accepted: request.Candidate == "FLAG{ok}", Summary: "checked", Rating: 2113}, nil
}

func (stub *platformStub) Abandon(context.Context, challengeworkflow.PlatformAbandonRequest) (challengeworkflow.PlatformAbandonResponse, error) {
	stub.abandonCalls++
	return challengeworkflow.PlatformAbandonResponse{Summary: "abandoned", Rating: 2090}, nil
}

func (stub *platformStub) Finalize(context.Context, challengeworkflow.PlatformFinalizeRequest) (challengeworkflow.PlatformFinalizeResponse, error) {
	stub.finalizeCalls++
	return challengeworkflow.PlatformFinalizeResponse{Summary: "finalized"}, nil
}

type recorderStub struct {
	claims      int
	submissions int
}

type failOnceRecorder struct {
	recorderStub
	failed bool
}

type alwaysFailRecorder struct{ recorderStub }

func (recorder *alwaysFailRecorder) RecordClaim(ctx context.Context, request challengeworkflow.RecordClaimRequest) error {
	recorder.recorderStub.RecordClaim(ctx, request)
	return errors.New("simulated durable recovery failure")
}

func (recorder *failOnceRecorder) RecordClaim(ctx context.Context, request challengeworkflow.RecordClaimRequest) error {
	recorder.recorderStub.RecordClaim(ctx, request)
	if !recorder.failed {
		recorder.failed = true
		return errors.New("simulated recorder crash window")
	}
	return nil
}

func (recorder *recorderStub) RecordClaim(context.Context, challengeworkflow.RecordClaimRequest) error {
	recorder.claims++
	return nil
}

func (recorder *recorderStub) RecordSubmission(context.Context, challengeworkflow.RecordSubmissionRequest) error {
	recorder.submissions++
	return nil
}

func (*recorderStub) RecordAbandon(context.Context, challengeworkflow.RecordAbandonRequest) error {
	return nil
}
func (*recorderStub) RecordFinalize(context.Context, challengeworkflow.RecordFinalizeRequest) error {
	return nil
}

func newFixture(t *testing.T, policy task.TaskPolicy) (*store.DB, *project.Service, *task.Service, project.Project, task.Task) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.CreateWithKind("Arena", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypeCTFChallenge, Goal: "solve", Runner: task.RunnerSandbox, RunControls: task.RunControls{Policy: policy}})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return db, projects, tasks, createdProject, createdTask
}

func TestClaimIsDurableAndIdempotentAcrossServiceRestart(t *testing.T) {
	db, projects, tasks, proj, createdTask := newFixture(t, task.TaskPolicy{MaxAttempts: 2})
	platform := &platformStub{}
	recorder := &recorderStub{}
	request := challengeworkflow.ClaimRequest{ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "claim-op-1", ChallengeID: "3121"}

	first := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, recorder)
	result, err := first.Claim(context.Background(), request)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if result.AttemptKey != "attempt/arena/attempt-42" || result.ExternalAttemptID != "attempt-42" {
		t.Fatalf("claim result = %#v", result)
	}

	restarted := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, recorder)
	replay, err := restarted.Claim(context.Background(), request)
	if err != nil || replay != result {
		t.Fatalf("replay = %#v, %v; want %#v", replay, err, result)
	}
	if platform.claimCalls != 1 || recorder.claims != 1 {
		t.Fatalf("calls = platform %d, recorder %d; want 1 each", platform.claimCalls, recorder.claims)
	}
}

func TestClaimRecoversRecordingStateAfterRecorderFailure(t *testing.T) {
	db, projects, tasks, proj, createdTask := newFixture(t, task.TaskPolicy{MaxAttempts: 1})
	platform := &platformStub{}
	recorder := &failOnceRecorder{}
	request := challengeworkflow.ClaimRequest{ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "recover-claim", ChallengeID: "3121"}
	service := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, recorder)
	if _, err := service.Claim(context.Background(), request); err == nil {
		t.Fatal("expected recorder failure")
	}
	restarted := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, recorder)
	failures := restarted.Recover(context.Background())
	if len(failures) != 0 {
		t.Fatalf("recovery failures: %#v", failures)
	}
	result, err := restarted.Claim(context.Background(), request)
	if err != nil {
		t.Fatalf("recover claim: %v", err)
	}
	if result.ExternalAttemptID != "attempt-42" {
		t.Fatalf("result = %#v", result)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM challenge_attempts WHERE task_id=?`, createdTask.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("Attempt count = %d, %v", count, err)
	}
	if _, err := restarted.Claim(context.Background(), request); err != nil {
		t.Fatalf("completed replay: %v", err)
	}
	if platform.claimCalls != 2 {
		t.Fatalf("platform calls = %d, want one initial and one recovery call", platform.claimCalls)
	}
}

func TestRecoveryFailureSettlesOperationToActionRequired(t *testing.T) {
	db, projects, tasks, proj, createdTask := newFixture(t, task.TaskPolicy{MaxAttempts: 1})
	platform := &platformStub{}
	recorder := &alwaysFailRecorder{}
	request := challengeworkflow.ClaimRequest{ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "recover-action-required", ChallengeID: "3121"}
	service := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, recorder)
	if _, err := service.Claim(context.Background(), request); err == nil {
		t.Fatal("expected initial recorder failure")
	}

	restarted := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, recorder)
	failures := restarted.Recover(context.Background())
	if len(failures) != 1 || failures[0].OperationID != request.OperationID {
		t.Fatalf("recovery failures = %#v", failures)
	}
	var state, recoveryError string
	if err := db.QueryRow(`SELECT state,recovery_error FROM challenge_operations WHERE task_id=? AND operation_id=?`, createdTask.ID, request.OperationID).Scan(&state, &recoveryError); err != nil {
		t.Fatalf("read recovered Challenge operation: %v", err)
	}
	if state != "action_required" || recoveryError == "" {
		t.Fatalf("recovered Challenge operation = state %q error %q", state, recoveryError)
	}

	if failures := restarted.Recover(context.Background()); len(failures) != 0 {
		t.Fatalf("action-required operation must not replay automatically: %#v", failures)
	}
	if platform.claimCalls != 2 || recorder.claims != 2 {
		t.Fatalf("calls after two restarts = platform %d recorder %d; want one initial and one recovery", platform.claimCalls, recorder.claims)
	}
}

func TestSubmitEnforcesWrongSubmissionPolicyBeforeExternalCall(t *testing.T) {
	db, projects, tasks, proj, createdTask := newFixture(t, task.TaskPolicy{MaxWrongSubmissions: 1})
	platform := &platformStub{}
	recorder := &recorderStub{}
	service := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, recorder)
	claim, err := service.Claim(context.Background(), challengeworkflow.ClaimRequest{ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "claim-op", ChallengeID: "3121"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	first, err := service.Submit(context.Background(), challengeworkflow.SubmitRequest{ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "submit-op-1", ExternalAttemptID: claim.ExternalAttemptID, Candidate: "wrong"})
	if err != nil || first.Accepted {
		t.Fatalf("first submit = %#v, %v", first, err)
	}
	_, err = service.Submit(context.Background(), challengeworkflow.SubmitRequest{ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "submit-op-2", ExternalAttemptID: claim.ExternalAttemptID, Candidate: "wrong-again"})
	var policyError *challengeworkflow.PolicyError
	if !errors.As(err, &policyError) || policyError.Code != challengeworkflow.PolicyMaxWrongSubmissions {
		t.Fatalf("expected wrong-submission policy error, got %v", err)
	}
	if platform.submitCalls != 1 || recorder.submissions != 1 {
		t.Fatalf("calls = platform %d, recorder %d; want 1 each", platform.submitCalls, recorder.submissions)
	}
}

func TestFirstClaimEnforcesNoProgressPolicyFromTaskCreation(t *testing.T) {
	db, projects, tasks, proj, createdTask := newFixture(t, task.TaskPolicy{MaxNoProgressSeconds: 1})
	oldCreatedAt := time.Now().UTC().Add(-2 * time.Second).Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE tasks SET created_at=? WHERE id=?`, oldCreatedAt, createdTask.ID); err != nil {
		t.Fatalf("age Task: %v", err)
	}
	platform := &platformStub{}
	service := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, &recorderStub{})

	_, err := service.Claim(context.Background(), challengeworkflow.ClaimRequest{
		ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "claim-after-no-progress", ChallengeID: "3121",
	})
	var policyError *challengeworkflow.PolicyError
	if !errors.As(err, &policyError) || policyError.Code != challengeworkflow.PolicyMaxNoProgress {
		t.Fatalf("first Claim error = %v", err)
	}
	if platform.claimCalls != 0 {
		t.Fatalf("platform Claim calls = %d, want 0", platform.claimCalls)
	}
}

func TestAbandonAndFinalizeEnforceTaskPolicyBeforeExternalCall(t *testing.T) {
	for _, operation := range []string{"abandon", "finalize"} {
		t.Run(operation, func(t *testing.T) {
			db, projects, tasks, proj, createdTask := newFixture(t, task.TaskPolicy{MaxWrongSubmissions: 1})
			platform := &platformStub{}
			service := challengeworkflow.NewService(db, projects, tasks, map[string]challengeworkflow.PlatformAdapter{"arena": platform}, &recorderStub{})
			claim, err := service.Claim(context.Background(), challengeworkflow.ClaimRequest{
				ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "claim-" + operation, ChallengeID: "3121",
			})
			if err != nil {
				t.Fatalf("Claim: %v", err)
			}
			if _, err := service.Submit(context.Background(), challengeworkflow.SubmitRequest{
				ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "submit-" + operation,
				ExternalAttemptID: claim.ExternalAttemptID, Candidate: "wrong",
			}); err != nil {
				t.Fatalf("Submit: %v", err)
			}

			switch operation {
			case "abandon":
				_, err = service.Abandon(context.Background(), challengeworkflow.AbandonRequest{
					ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "abandon-blocked",
					ExternalAttemptID: claim.ExternalAttemptID, Reason: "stop",
				})
			case "finalize":
				_, err = service.Finalize(context.Background(), challengeworkflow.FinalizeRequest{
					ProjectID: proj.ID, TaskID: createdTask.ID, Platform: "arena", OperationID: "finalize-blocked",
					ExternalAttemptID: claim.ExternalAttemptID,
				})
			}
			var policyError *challengeworkflow.PolicyError
			if !errors.As(err, &policyError) || policyError.Code != challengeworkflow.PolicyMaxWrongSubmissions {
				t.Fatalf("%s error = %v", operation, err)
			}
			if platform.abandonCalls != 0 || platform.finalizeCalls != 0 {
				t.Fatalf("platform calls after blocked %s = abandon %d finalize %d", operation, platform.abandonCalls, platform.finalizeCalls)
			}
		})
	}
}
