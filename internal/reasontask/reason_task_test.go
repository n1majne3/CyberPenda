package reasontask_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/reasontask"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestReasonTaskProposalCannotMutateBlackboardBeforeApproval(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, _ := projects.CreateWithKind("Engagement", "", project.KindPentest, project.Scope{}, project.Defaults{})
	tasks := task.NewService(db, projects)
	planningTask, err := tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Type: task.TypePentest,
		Goal: "Prepare an approval-required Reason Task proposal", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create planning Task: %v", err)
	}
	board := blackboardv2.NewService(db)
	service := reasontask.NewService(db, board)
	if err := service.Register(createdProject.ID, planningTask.ID); err != nil {
		t.Fatalf("register Reason Task: %v", err)
	}

	proposal, err := service.Propose(reasontask.ProposeRequest{
		ProjectID: createdProject.ID, ReasonTaskID: planningTask.ID,
		NextTaskGoals:               []string{"Confirm the new administration surface"},
		ExplorationObjectiveChanges: []string{"Replace the broad discovery objective with targeted validation"},
		ReadinessJudgment:           "The Project is ready for targeted validation after consolidation.",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "objective:targeted-validation", Type: "objective",
			Record: map[string]any{"status": "open", "objective": "Validate the administration surface"},
		}},
	})
	if err != nil {
		t.Fatalf("store Reason Task proposal: %v", err)
	}
	if proposal.Status != reasontask.StatusProposed {
		t.Fatalf("proposal = %#v", proposal)
	}
	if _, err := board.ReadCurrent(context.Background(), createdProject.ID, "objective:targeted-validation"); err == nil {
		t.Fatalf("proposal mutated Blackboard before approval: %v", err)
	}

	approved, result, err := service.Approve(context.Background(), createdProject.ID, proposal.ID)
	if err != nil {
		t.Fatalf("approve Reason Task proposal: %v", err)
	}
	if approved.Status != reasontask.StatusApproved || result.Revision < 1 {
		t.Fatalf("approved proposal = %#v, result=%#v", approved, result)
	}
	if _, err := board.ReadCurrent(context.Background(), createdProject.ID, "objective:targeted-validation"); err != nil {
		t.Fatalf("approved Blackboard change missing: %v", err)
	}
	if _, _, err := service.Approve(context.Background(), createdProject.ID, proposal.ID); !errors.Is(err, reasontask.ErrNotProposed) {
		t.Fatalf("second approval error = %v", err)
	}
}
