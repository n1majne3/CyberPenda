package approval_test

import (
	"path/filepath"
	"testing"

	"pentest/internal/approval"
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
