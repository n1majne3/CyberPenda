package daemon_test

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/daemon"
)

// TestRequestLogMiddlewareLogsEachRequest proves the daemon emits one
// structured log line per request containing method, path, status, and a
// duration field. This is the visibility the daemon previously lacked.
func TestRequestLogMiddlewareLogsEachRequest(t *testing.T) {
	var captured bytes.Buffer
	logger := log.New(&captured, "", 0)

	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	line := captured.String()
	if !strings.Contains(line, "GET") || !strings.Contains(line, "/health") {
		t.Fatalf("expected log line to contain method and path, got %q", line)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("expected log line to contain status 200, got %q", line)
	}
	if !strings.Contains(line, "ms") {
		t.Fatalf("expected log line to contain a duration, got %q", line)
	}
}

// TestRequestLogMiddlewareCapturesErrorStatus proves the recorded status
// reflects what the handler actually wrote, including non-2xx responses.
func TestRequestLogMiddlewareCapturesErrorStatus(t *testing.T) {
	var captured bytes.Buffer
	logger := log.New(&captured, "", 0)

	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	// Listing tasks for a project that does not exist yields 404 from
	// requireProject, exercising a real error path through the recorder.
	req := httptest.NewRequest(http.MethodGet, "/api/projects/does-not-exist/tasks", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.Code)
	}
	if !strings.Contains(captured.String(), "404") {
		t.Fatalf("expected log line to contain status 404, got %q", captured.String())
	}
}

// TestTaskLifecycleLogsLaunchAndCompletion proves the daemon logs task
// launch and completion, so `make dev` shows runtime activity.
func TestTaskLifecycleLogsLaunchAndCompletion(t *testing.T) {
	var captured bytes.Buffer
	logger := log.New(&captured, "", 0)

	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	waitForTaskStatus(t, server, projectID, taskID, "completed")

	output := captured.String()
	if !strings.Contains(output, "task launched") || !strings.Contains(output, "task completed") {
		t.Fatalf("expected launch and completion log lines, got:\n%s", output)
	}
	if !strings.Contains(output, taskID) {
		t.Fatalf("expected task id %q in logs, got:\n%s", taskID, output)
	}
}

