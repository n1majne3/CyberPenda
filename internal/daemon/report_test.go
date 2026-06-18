package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReportTriggerWithTaskReturnsFullMarkdown proves the report trigger renders
// a full report derived from stored state when a task id anchors context.
func TestReportTriggerWithTaskReturnsFullMarkdown(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"],"testing_limits":["business hours only"]}
	}`)

	// A confirmed finding via the finding upsert endpoint (PUT with key in path).
	postJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/findings/sqli-login", `{
		"title":"SQL injection in login",
		"status":"confirmed",
		"target":"https://example.com/login",
		"proof":"' or 1=1--",
		"impact":"auth bypass",
		"recommendation":"parameterize queries",
		"cvss_version":"4.0",
		"cvss_vector":"CVSS:4.0/AV:N/VC:H/VI:H"
	}`)
	// An unconfirmed finding.
	postJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/findings/info-leak", `{
		"title":"Verbose errors"
	}`)
	// A fact for context.
	postJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/facts/recon:subs", `{
		"summary":"Found 3 subdomains",
		"body":"api, admin, staging"
	}`)

	// A fake-runtime task to anchor runner/scope.
	taskID := launchTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":"fake",
		"runner":"sandbox"
	}`)

	body := []byte(`{"task_id":"` + taskID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected report status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var out struct {
		Markdown string `json:"markdown"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	md := out.Markdown
	for _, want := range []string{
		"Confirmed Findings",
		"Unconfirmed Findings",
		"SQL injection in login",
		"Verbose errors",
		"CVSS:4.0/AV:N/VC:H/VI:H",
		"example.com",
		"business hours only",
		"runner",
		"sandbox",
		"Found 3 subdomains",
	} {
		if !strings.Contains(strings.ToLower(md), strings.ToLower(want)) {
			t.Fatalf("report markdown missing %q\n---\n%s", want, md)
		}
	}
}

func postJSON(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, method, path, body string) {
	t.Helper()
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code >= 400 {
		t.Fatalf("%s %s expected <400, got %d: %s", method, path, resp.Code, resp.Body.String())
	}
}

func TestReportTriggerWithoutTaskUsesLatestTaskWhenPresent(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"]}
	}`)
	postJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/findings/info-leak", `{
		"title":"Verbose errors"
	}`)
	first := launchTask(t, server, projectID, `{
		"goal":"first task",
		"runtime_profile_id":"fake",
		"runner":"sandbox"
	}`)
	_ = first
	second := launchTask(t, server, projectID, `{
		"goal":"second task",
		"runtime_profile_id":"fake",
		"runner":"sandbox"
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/report", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var out struct {
		Status   string `json:"status"`
		Markdown string `json:"markdown"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status == "generated_stub" {
		t.Fatalf("expected full report, got stub")
	}
	if !strings.Contains(out.Markdown, "Verbose errors") || !strings.Contains(out.Markdown, "sandbox") {
		t.Fatalf("expected full report from latest task context, got:\n%s", out.Markdown)
	}
	_ = second
}

func launchTask(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, body string) string {
	t.Helper()
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("launch task expected 201, got %d: %s", resp.Code, resp.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return created.ID
}
