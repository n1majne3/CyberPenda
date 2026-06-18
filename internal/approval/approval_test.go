package approval_test

import (
	"path/filepath"
	"testing"

	"pentest/internal/approval"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestRequestHighRiskActionCreatesPendingApprovalAndAuditEntry(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := approval.NewService(db)
	got, err := svc.RequestHighRiskAction(approval.Request{
		ProjectID:       "proj-1",
		TaskID:          "task-1",
		Requester:       "runtime",
		RequestedAction: "run nuclei against admin.example.com",
		Rationale:       "validate suspected XSS",
	})
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	if got.Status != approval.StatusPending || got.Kind != approval.KindHighRiskAction {
		t.Fatalf("unexpected approval: %#v", got)
	}

	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE project_id = ? AND kind = 'approval_requested'`, "proj-1").Scan(&auditCount); err != nil {
		t.Fatalf("count audit logs: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one audit entry, got %d", auditCount)
	}
}

func TestRequestScopeExpansionRequiresRequestedAction(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := approval.NewService(db)
	if _, err := svc.RequestScopeExpansion(approval.Request{ProjectID: "proj-1"}); err == nil {
		t.Fatal("expected missing requested action error")
	}
}

func TestDecideApprovalUpdatesStatusAndRecordsAudit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := approval.NewService(db)
	created, err := svc.RequestHighRiskAction(approval.Request{
		ProjectID:       "proj-1",
		RequestedAction: "exploit validation",
	})
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}

	decided, err := svc.Decide(approval.DecideRequest{
		ApprovalID: created.ID,
		Reviewer:   "operator",
		Decision:   approval.DecisionApprove,
		Notes:      "approved for staging only",
	})
	if err != nil {
		t.Fatalf("decide approval: %v", err)
	}
	if decided.Status != approval.StatusApproved || decided.Reviewer != "operator" {
		t.Fatalf("unexpected decided approval: %#v", decided)
	}

	var decidedAudit int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE project_id = ? AND kind = 'approval_decided'`, "proj-1").Scan(&decidedAudit); err != nil {
		t.Fatalf("count decided audit logs: %v", err)
	}
	if decidedAudit != 1 {
		t.Fatalf("expected one decided audit entry, got %d", decidedAudit)
	}

	if _, err := svc.Decide(approval.DecideRequest{
		ApprovalID: created.ID,
		Reviewer:   "operator",
		Decision:   approval.DecisionReject,
	}); err == nil {
		t.Fatal("expected already decided error")
	}
}

func TestApplyScopeExpansionMergesAssetPayload(t *testing.T) {
	scope := project.Scope{Domains: []string{"example.com"}}
	expanded := approval.ApplyScopeExpansion(scope, map[string]any{
		"asset_type": "domain",
		"asset":      "api.example.com",
	})
	if len(expanded.Domains) != 2 || expanded.Domains[1] != "api.example.com" {
		t.Fatalf("expected merged domains, got %#v", expanded.Domains)
	}
}

func TestListPendingApprovalsAndAuditLog(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := approval.NewService(db)
	if _, err := svc.RequestScopeExpansion(approval.Request{
		ProjectID:       "proj-1",
		RequestedAction: "add subdomain api.example.com",
	}); err != nil {
		t.Fatalf("request scope expansion: %v", err)
	}

	pending, err := svc.ListByProject("proj-1", approval.StatusPending)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}

	count, err := svc.CountPending("proj-1")
	if err != nil || count != 1 {
		t.Fatalf("expected 1 pending count, got %d err=%v", count, err)
	}

	logs, err := svc.ListAudit("proj-1", 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(logs) != 1 || logs[0].Kind != "approval_requested" {
		t.Fatalf("unexpected audit logs: %#v", logs)
	}
}