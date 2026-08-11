package scopeexpansion_test

import (
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/project"
	"pentest/internal/scopeexpansion"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestScopeExpansionRequiresApprovalAndRetainsRuntimeTrustedOrigin(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.CreateWithKind("Engagement", "", project.KindPentest, project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "discover", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	service := scopeexpansion.NewService(db, projects)

	proposal, err := service.Propose(scopeexpansion.ProposeRequest{
		ProjectID:       createdProject.ID,
		Addition:        project.Scope{Domains: []string{"api.example.com"}, Ports: []string{"8443"}},
		DiscoverySource: "Project Fact fact:api", Reason: "New application endpoint", Risk: "Adds authenticated testing",
		Origin: scopeexpansion.TrustedOrigin{Kind: scopeexpansion.TrustedOriginRuntime, TaskID: createdTask.ID, ContinuationID: continuation.ID},
	})
	if err != nil {
		t.Fatalf("propose Scope Expansion: %v", err)
	}
	if proposal.Status != scopeexpansion.StatusProposed || proposal.TrustedOrigin.Kind != scopeexpansion.TrustedOriginRuntime || proposal.TrustedOrigin.ContinuationID != continuation.ID {
		t.Fatalf("proposal = %#v", proposal)
	}
	before, err := projects.Get(createdProject.ID)
	if err != nil || len(before.Scope.Domains) != 1 {
		t.Fatalf("Scope changed before approval: %#v, %v", before.Scope, err)
	}

	approved, updatedProject, err := service.Approve(createdProject.ID, proposal.ID)
	if err != nil {
		t.Fatalf("approve Scope Expansion: %v", err)
	}
	if approved.Status != scopeexpansion.StatusApproved || len(updatedProject.Scope.Domains) != 2 || updatedProject.Scope.Domains[1] != "api.example.com" || len(updatedProject.Scope.Ports) != 1 {
		t.Fatalf("approved proposal = %#v, Scope=%#v", approved, updatedProject.Scope)
	}
	if _, _, err := service.Approve(createdProject.ID, proposal.ID); !errors.Is(err, scopeexpansion.ErrNotProposed) {
		t.Fatalf("second approval error = %v", err)
	}
}

func TestScopeExpansionRejectsUnprovenRuntimeTrustedOrigin(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, _ := projects.CreateWithKind("Engagement", "", project.KindPentest, project.Scope{}, project.Defaults{})
	service := scopeexpansion.NewService(db, projects)

	_, err = service.Propose(scopeexpansion.ProposeRequest{
		ProjectID: createdProject.ID, Addition: project.Scope{Domains: []string{"api.example.com"}},
		DiscoverySource: "Project Fact", Reason: "discovered", Risk: "new host",
		Origin: scopeexpansion.TrustedOrigin{Kind: scopeexpansion.TrustedOriginRuntime, TaskID: "foreign", ContinuationID: "foreign"},
	})
	if !errors.Is(err, scopeexpansion.ErrTrustedOrigin) {
		t.Fatalf("unproven Trusted Origin error = %v", err)
	}
}
