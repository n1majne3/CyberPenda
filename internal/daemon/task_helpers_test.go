package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"pentest/internal/daemon"
)

func getTaskEvents(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, taskID string) []map[string]any {
	t.Helper()
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/events", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected events status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	return body.Events
}

func waitForEventText(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, taskID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events := getTaskEvents(t, server, projectID, taskID)
		for _, event := range events {
			payload, _ := event["payload"].(map[string]any)
			text, _ := payload["text"].(string)
			if strings.Contains(text, want) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for event text %q", want)
}

func waitForDockerLogText(t *testing.T, logPath, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(raw), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, _ := os.ReadFile(logPath)
	t.Fatalf("timed out waiting for docker log text %q in\n%s", want, string(raw))
}

func hasTranscriptEntry(entries []map[string]any, kind, role, text string) bool {
	for _, entry := range entries {
		if entry["kind"] == kind && entry["role"] == role && entry["text"] == text {
			return true
		}
	}
	return false
}

func waitForTaskStatus(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, taskID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
		server.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected get task status 200, got %d with body %s", resp.Code, resp.Body.String())
		}
		var found struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&found); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		if found.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for task status %q", want)
}

func waitForInterruptSteerAvailable(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, taskID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last struct {
		RuntimeControls struct {
			InterruptSteerAvailable bool   `json:"interrupt_steer_available"`
			InterruptSteerReason    string `json:"interrupt_steer_reason"`
			NativeSessionCaptured   bool   `json:"native_session_captured"`
		} `json:"runtime_controls"`
	}
	for time.Now().Before(deadline) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
		server.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected get task status 200, got %d with body %s", resp.Code, resp.Body.String())
		}
		if err := json.NewDecoder(resp.Body).Decode(&last); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		if last.RuntimeControls.InterruptSteerAvailable {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for interrupt steer availability, last controls %#v", last.RuntimeControls)
}

func createTask(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, body string) string {
	t.Helper()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected create task status 201, got %d with body %s", resp.Code, resp.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return created.ID
}

func quoteJSON(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func createModelProvider(t *testing.T, server *daemon.Server, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected create model provider status 201, got %d with body %s", resp.Code, resp.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode model provider: %v", err)
	}
	return created.ID
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// findTargetRewriteEvent returns the lifecycle target_rewrite event for a task,
// or nil when none was recorded.
func findTargetRewriteEvent(events []map[string]any) map[string]any {
	for _, event := range events {
		if event["kind"] != "lifecycle" {
			continue
		}
		payload, _ := event["payload"].(map[string]any)
		if payload["phase"] == "target_rewrite" {
			return payload
		}
	}
	return nil
}

// TestCreateTaskRecordsLoopbackRewriteEvent proves that launching a sandbox
// task whose goal contains a loopback target records a lifecycle
// target_rewrite event, while host-runner tasks and loopback-free goals do not.
