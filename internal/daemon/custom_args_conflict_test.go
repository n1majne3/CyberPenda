package daemon_test

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/store"
)

func TestCreateRuntimeProfileRejectsConflictingCustomArgsAndLogs(t *testing.T) {
	var captured bytes.Buffer
	logger := log.New(&captured, "", 0)
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	body := `{"name":"Codex Conflict","provider":"codex","fields":{"model":"gpt-5","custom_args":["--model","sk-abcdefghijklmnopqrstuv","OPENAI_API_KEY=sk-secret-value-123456"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body %s", err, rec.Body.String())
	}
	if !strings.Contains(payload.Error, "--model") {
		t.Fatalf("error must name --model, got %q", payload.Error)
	}
	if !strings.Contains(payload.Error, "model") {
		t.Fatalf("error must name structured field model, got %q", payload.Error)
	}
	if strings.Contains(payload.Error, "sk-abcdefghijklmnopqrstuv") {
		t.Fatalf("API error must not leak secret model value: %q", payload.Error)
	}

	// Nothing persisted under list.
	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles", nil)
	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, listReq)
	if strings.Contains(listRec.Body.String(), "Codex Conflict") {
		t.Fatalf("conflicting create must not persist profile, body %s", listRec.Body.String())
	}

	logLine := captured.String()
	if !strings.Contains(logLine, "custom_args conflict") {
		t.Fatalf("expected diagnostic log for custom_args conflict, got:\n%s", logLine)
	}
	if !strings.Contains(logLine, `argument="--model [REDACTED]"`) && !strings.Contains(logLine, `argument="--model`) {
		t.Fatalf("diagnostic must name complete offending argument, got:\n%s", logLine)
	}
	if !strings.Contains(logLine, `flag="--model"`) {
		t.Fatalf("diagnostic must name flag, got:\n%s", logLine)
	}
	if !strings.Contains(logLine, "structured_field=model") {
		t.Fatalf("diagnostic must name structured field, got:\n%s", logLine)
	}
	if strings.Contains(logLine, "sk-abcdefghijklmnopqrstuv") || strings.Contains(logLine, "sk-secret-value-123456") {
		t.Fatalf("diagnostic must redact secrets, got:\n%s", logLine)
	}
}

func TestUpdateRuntimeProfileRejectsConflictingCustomArgsWithoutPersisting(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	profileID := createRuntimeProfile(t, server, `{"name":"Claude Clean","provider":"claude_code","fields":{"model":"claude-sonnet","custom_args":["--verbose"]}}`)

	body := `{"fields":{"model":"claude-sonnet","custom_args":["--effort","high"]}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/runtime-profiles/"+profileID, bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "--effort") {
		t.Fatalf("error must name --effort: %s", rec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+profileID, nil)
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, getReq)
	if !strings.Contains(getRec.Body.String(), `"--verbose"`) {
		t.Fatalf("stored custom args must remain unchanged: %s", getRec.Body.String())
	}
	if strings.Contains(getRec.Body.String(), `"--effort"`) {
		t.Fatalf("conflicting update must not persist --effort: %s", getRec.Body.String())
	}
}

func TestPreflightRejectsLegacyConflictingCustomArgsAndLogs(t *testing.T) {
	var captured bytes.Buffer
	logger := log.New(&captured, "", 0)
	dbPath := filepath.Join(t.TempDir(), "pentest.db")
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  dbPath,
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Pi Legacy","provider":"pi","fields":{"model":"m","custom_args":["--debug"]}}`)

	// Simulate a legacy profile that stored conflicting Custom Args before
	// structured-field validation existed.
	forceProfileCustomArgs(t, dbPath, profileID, `{"model":"m","custom_args":["--thinking","medium","OPENAI_API_KEY=sk-abcdefghijklmnopqrstuv"]}`)

	body := `{"runtime_profile_id":"` + profileID + `","runner":"sandbox"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status %d body %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Pass   bool `json:"pass"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Pass {
		t.Fatal("expected preflight pass=false")
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "custom_args" && check.Status == "fail" {
			found = true
			if !strings.Contains(check.Detail, "--thinking") {
				t.Fatalf("detail must name --thinking: %q", check.Detail)
			}
			if !strings.Contains(check.Detail, "reasoning_effort") && !strings.Contains(strings.ToLower(check.Detail), "reasoning effort") {
				t.Fatalf("detail must name reasoning effort field: %q", check.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("expected custom_args fail check, got %#v", result.Checks)
	}

	logLine := captured.String()
	if !strings.Contains(logLine, "custom_args conflict") {
		t.Fatalf("expected diagnostic log, got:\n%s", logLine)
	}
	if strings.Contains(logLine, "sk-abcdefghijklmnopqrstuv") {
		t.Fatalf("diagnostic must redact secrets, got:\n%s", logLine)
	}
	if !strings.Contains(logLine, "[REDACTED]") {
		t.Fatalf("diagnostic must include redaction marker, got:\n%s", logLine)
	}
}

func TestTaskLaunchRejectsConflictingCustomArgsBeforeLaunch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pentest.db")
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  dbPath,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex Launch","provider":"codex","fields":{"model":"gpt-test","custom_args":["--json"],"api_keys":{"OPENAI_API_KEY":"sk-test"}}}`)
	// Legacy conflict stored under the profile.
	forceProfileCustomArgs(t, dbPath, profileID, `{"model":"gpt-test","custom_args":["-c","model_reasoning_effort=high","--json"],"api_keys":{"OPENAI_API_KEY":"sk-test"}}`)

	body := `{"goal":"inspect example.com","runtime_profile_id":"` + profileID + `","runner":"sandbox"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("launch status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "preflight failed") && !strings.Contains(rec.Body.String(), "custom_args") {
		t.Fatalf("launch must fail via preflight custom_args: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model_reasoning_effort") && !strings.Contains(rec.Body.String(), "reasoning_effort") {
		t.Fatalf("launch error must name effort conflict: %s", rec.Body.String())
	}

	// No task persisted.
	listReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks", nil)
	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, listReq)
	var listed struct {
		Tasks []any `json:"tasks"`
	}
	_ = json.Unmarshal(listRec.Body.Bytes(), &listed)
	if len(listed.Tasks) != 0 {
		t.Fatalf("task must not be created when custom args conflict, got %s", listRec.Body.String())
	}
}

func forceProfileCustomArgs(t *testing.T, dbPath, profileID, fieldsJSON string) {
	t.Helper()
	inspectionDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open inspection db: %v", err)
	}
	t.Cleanup(func() { _ = inspectionDB.Close() })
	if _, err := inspectionDB.Exec(
		`UPDATE runtime_profiles SET fields_json = ? WHERE id = ?`,
		fieldsJSON, profileID,
	); err != nil {
		t.Fatalf("force legacy custom args: %v", err)
	}
}
