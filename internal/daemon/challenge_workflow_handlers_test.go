package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"strings"
	"testing"
)

func TestRetiredChallengeWorkflowPreservesHistoryAcrossRestart(t *testing.T) {
	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "db.sqlite"), RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"), AuthToken: "operator-test", DisableBuiltinSkills: true}
	server, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if server != nil {
			_ = server.Close()
		}
	})
	proj, err := server.projects.CreateWithKind("Arena", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypeCTFChallenge, Goal: "Inspect", Runner: task.RunnerSandbox, RuntimeProfileID: profile.ID, RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox)})
	if err != nil {
		t.Fatal(err)
	}
	empty := serveChallenge(t, server, http.MethodGet, "/api/projects/"+proj.ID+"/tasks/"+created.ID, "operator-test", "")
	if empty.Code != http.StatusOK || strings.Contains(empty.Body.String(), `"challenge_history_available":true`) {
		t.Fatalf("new Task has Challenge history: %d %s", empty.Code, empty.Body.String())
	}
	stamp := "2026-09-01T00:00:00Z"
	if _, err := server.db.Exec(`INSERT INTO challenge_attempts (project_id,task_id,platform,external_attempt_id,challenge_id,attempt_key,objective_key,status,last_progress_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'open',?,?,?)`, proj.ID, created.ID, "arena", "attempt-42", "42", "attempt:42", "objective:42", stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "recording", "action_required", "completed"} {
		if _, err := server.db.Exec(`INSERT INTO challenge_operations (task_id,operation_id,project_id,platform,kind,request_hash,request_json,state,external_attempt_id,response_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, created.ID, state, proj.ID, "arena", "submit", strings.Repeat("0", 64), `{"candidate":"do-not-expose"}`, state, "attempt-42", `{"private":"do-not-expose"}`, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server, err = NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/projects/" + proj.ID + "/tasks/" + created.ID
	history := serveChallenge(t, server, http.MethodGet, base+"/challenges", "operator-test", "")
	if history.Code != http.StatusOK {
		t.Fatalf("history: %d %s", history.Code, history.Body.String())
	}
	var body struct {
		Retired    bool `json:"retired"`
		Operations []struct {
			ID    string `json:"operation_id"`
			State string `json:"state"`
		} `json:"operations"`
		Attempts []struct {
			Status string `json:"status"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Retired || len(body.Operations) != 4 || len(body.Attempts) != 1 || body.Attempts[0].Status != "open" {
		t.Fatalf("history: %s", history.Body.String())
	}
	for _, op := range body.Operations {
		if op.ID != op.State {
			t.Fatalf("restart changed operation: %+v", op)
		}
	}
	if strings.Contains(history.Body.String(), "do-not-expose") {
		t.Fatal("history exposed raw payload")
	}
	detail := serveChallenge(t, server, http.MethodGet, base, "operator-test", "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"challenge_history_available":true`) {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
	}
	for _, operation := range []string{"claim", "submit", "abandon", "finalize"} {
		result := serveChallenge(t, server, http.MethodPost, base+"/challenges/"+operation, "operator-test", `{}`)
		if result.Code != http.StatusGone {
			t.Fatalf("%s = %d %s", operation, result.Code, result.Body.String())
		}
	}
	denied := serveChallenge(t, server, http.MethodGet, base+"/challenges", "", "")
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous history: %d", denied.Code)
	}
	wrongProject, err := server.projects.Create("Other", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	cross := serveChallenge(t, server, http.MethodGet, "/api/projects/"+wrongProject.ID+"/tasks/"+created.ID+"/challenges", "operator-test", "")
	if cross.Code != http.StatusNotFound {
		t.Fatalf("cross-project history: %d", cross.Code)
	}
	readiness := serveChallenge(t, server, http.MethodGet, base+"/finish-readiness", "operator-test", "")
	if readiness.Code != http.StatusOK || !strings.Contains(readiness.Body.String(), `"ready_to_finish":true`) {
		t.Fatalf("retired history blocks finish: %s", readiness.Body.String())
	}
}

func serveChallenge(t *testing.T, server *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}
