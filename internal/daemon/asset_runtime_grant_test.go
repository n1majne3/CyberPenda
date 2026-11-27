package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/projectinterface"
)

func TestReadOnlyBlackboardGrantCanReportAssetsWhileOpen(t *testing.T) {
	server, err := NewServer(Config{Version: "test", DBPath: ":memory:", ListenAddr: "127.0.0.1:8787"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	created, err := server.projects.Create("Asset Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := server.db.Exec(`INSERT INTO tasks (id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at)
		VALUES('task-1',?,?,?,?,?,?,?,?,?)`, created.ID, "goal", "running", "pi", "profile-1", `{}`, `{}`, stamp, stamp); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := server.db.Exec(`INSERT INTO task_continuations (id,task_id,number,runtime_profile_id,runtime_provider,runner,status,started_at,updated_at)
		VALUES('cont-1','task-1',1,'profile-1','provider','pi','running',?,?)`, stamp, stamp); err != nil {
		t.Fatalf("insert continuation: %v", err)
	}
	token, _, err := server.projectInterfaceGrants.Issue(context.Background(), projectinterface.IssueGrantRequest{
		ProjectID: created.ID, TaskID: "task-1", ContinuationID: "cont-1",
		RuntimeConfigVersionID: "config-1", RuntimeProfileID: "profile-1",
		RuntimePluginID: "provider", Runner: "pi", Access: projectinterface.GrantAccessReadOnly,
	})
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v2/projects/"+created.ID+"/assets/upsert", strings.NewReader(`{"assets":[{"type":"ip","value":"192.0.2.1"}]}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("asset write status = %d body=%s", response.Code, response.Body.String())
	}
}
