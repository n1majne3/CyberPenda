package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/approval"
	"pentest/internal/project"
)

func TestListDecideApprovalsAndAuditLogHTTP(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	createdProject, err := server.projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	approvalRecord, err := server.approvals.RequestScopeExpansion(approval.Request{
		ProjectID:       createdProject.ID,
		RequestedAction: "add api.example.com",
		Payload: map[string]any{
			"asset_type": "domain",
			"asset":      "api.example.com",
		},
	})
	if err != nil {
		t.Fatalf("request scope expansion: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+createdProject.ID+"/approvals?status=pending", nil)
	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list approvals status %d body %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Approvals []approval.Approval `json:"approvals"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Approvals) != 1 || listed.Approvals[0].ID != approvalRecord.ID {
		t.Fatalf("unexpected approvals: %#v", listed.Approvals)
	}

	body, _ := json.Marshal(map[string]any{"decision": "approve", "reviewer": "tester"})
	decideReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/approvals/"+approvalRecord.ID+"/decide", bytes.NewReader(body))
	decideReq.Header.Set("Content-Type", "application/json")
	decideRec := httptest.NewRecorder()
	server.ServeHTTP(decideRec, decideReq)
	if decideRec.Code != http.StatusOK {
		t.Fatalf("decide status %d body %s", decideRec.Code, decideRec.Body.String())
	}

	got, err := server.projects.Get(createdProject.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(got.Scope.Domains) != 2 {
		t.Fatalf("expected expanded scope with 2 domains, got %#v", got.Scope.Domains)
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+createdProject.ID+"/audit-log", nil)
	auditRec := httptest.NewRecorder()
	server.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("audit log status %d body %s", auditRec.Code, auditRec.Body.String())
	}
	var audit struct {
		Entries []approval.AuditEntry `json:"entries"`
	}
	if err := json.NewDecoder(auditRec.Body).Decode(&audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Entries) < 2 {
		t.Fatalf("expected approval + scope audit entries, got %#v", audit.Entries)
	}
}
