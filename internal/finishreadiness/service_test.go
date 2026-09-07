package finishreadiness_test

import (
	"context"
	"path/filepath"
	"testing"

	"pentest/internal/fgs"
	"pentest/internal/finishreadiness"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestFGSRejectedUpdateBlocksFinishUntilWithdrawn(t *testing.T) {
	db, _, tasks, proj, created, _ := fixture(t)
	service := fgs.NewService(db)
	c := created.OwnerContract(t.TempDir())
	_, err := service.Apply(t.Context(), c, "fgs-continuation", fgs.Update{Schema: fgs.Schema, ID: "intent_00000001", Sequence: 1, Operations: []fgs.Operation{{Op: "step.create", Key: "step:a", Goal: "goal:missing", Action: "Check"}}})
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := finishreadiness.NewService(db, tasks).Evaluate(t.Context(), proj.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, blocker := range readiness.Blockers {
		if blocker.Code == "fgs_action_required" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing FGS blocker: %+v", readiness)
	}
	_, err = service.Apply(t.Context(), c, "fgs-continuation", fgs.Update{Schema: fgs.Schema, ID: "intent_00000002", Sequence: 2, Resolves: &fgs.Identity{ContinuationID: "fgs-continuation", IntentID: "intent_00000001"}, WithdrawalReason: "Not needed"})
	if err != nil {
		t.Fatal(err)
	}
	readiness, err = finishreadiness.NewService(db, tasks).Evaluate(t.Context(), proj.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, blocker := range readiness.Blockers {
		if blocker.Code == "fgs_action_required" {
			t.Fatalf("withdrawn update still blocks finish: %+v", readiness)
		}
	}
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

func TestRetiredChallengeHistoryDoesNotBlockDisabledFinish(t *testing.T) {
	db, _, tasks, proj, created, _ := fixtureWithBlackboardMode(t, task.BlackboardModeDisabled)
	// Retired records keep their original state and do not block Task Finish.
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
	if !readiness.ReadyToFinish {
		t.Fatalf("blockers = %#v", readiness.Blockers)
	}
}

func TestRetiredChallengeOriginDoesNotHideOrdinaryOpenAttempt(t *testing.T) {
	db, _, tasks, proj, created, _ := fixture(t)
	continuation, err := tasks.LatestContinuation(created.ID)
	if err != nil || continuation == nil {
		t.Fatalf("continuation: %v %v", continuation, err)
	}
	stamp := "2026-09-01T00:00:00Z"
	for _, key := range []string{"attempt:retired", "attempt:ordinary"} {
		if _, err := db.Exec(`INSERT INTO blackboard_v2_records(project_id,key,type,version,record_json,created_at,updated_at) VALUES (?,?,'attempt',1,?,?,?)`, proj.ID, key, `{"status":"open"}`, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO blackboard_v2_attempt_origins(project_id,key,continuation_id,created_at) VALUES (?,?,?,?)`, proj.ID, key, continuation.ID, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO challenge_attempts(project_id,task_id,platform,external_attempt_id,challenge_id,attempt_key,objective_key,status,last_progress_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'open',?,?,?)`, proj.ID, created.ID, "arena", "42", "42", "attempt:retired", "objective:retired", stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	readiness, err := finishreadiness.NewService(db, tasks).Evaluate(t.Context(), proj.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.ReadyToFinish || len(readiness.Blockers) != 1 || readiness.Blockers[0].Code != finishreadiness.BlockerOpenAttempts || readiness.Blockers[0].Count != 1 {
		t.Fatalf("only ordinary Attempt must block: %+v", readiness)
	}
}
