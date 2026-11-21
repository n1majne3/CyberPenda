package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

func TestClaudeCodeRunningTaskAllowsInterruptSteer(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	binary := filepath.Join(t.TempDir(), "claude-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"sess-claude\"}\\n'; sleep 5\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Claude Test", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "claude-sonnet-4",
	})
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "running")
	waitForInterruptSteerAvailable(t, server, projectID, taskID)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected get task status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var found struct {
		RuntimeControls struct {
			InterruptSteerAvailable bool   `json:"interrupt_steer_available"`
			InterruptSteerReason    string `json:"interrupt_steer_reason"`
			RuntimeProvider         string `json:"runtime_provider"`
		} `json:"runtime_controls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&found); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if !found.RuntimeControls.InterruptSteerAvailable {
		t.Fatalf("expected claude_code interrupt steer available, got %#v", found.RuntimeControls)
	}
	if found.RuntimeControls.RuntimeProvider != "claude_code" {
		t.Fatalf("expected claude_code runtime provider, got %#v", found.RuntimeControls)
	}
}

func TestPiRunningTaskAllowsInterruptSteer(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	binary := filepath.Join(t.TempDir(), "pi-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '{\"type\":\"session\",\"version\":3,\"id\":\"sess-pi\",\"cwd\":\"/task/workdir\"}\\n'; sleep 5\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Pi Test", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "DeepSeek-V4-Pro",
	})
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "running")
	waitForInterruptSteerAvailable(t, server, projectID, taskID)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected get task status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var found struct {
		RuntimeControls struct {
			InterruptSteerAvailable bool   `json:"interrupt_steer_available"`
			InterruptSteerReason    string `json:"interrupt_steer_reason"`
			RuntimeProvider         string `json:"runtime_provider"`
		} `json:"runtime_controls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&found); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if !found.RuntimeControls.InterruptSteerAvailable {
		t.Fatalf("expected pi interrupt steer available, got %#v", found.RuntimeControls)
	}
	if found.RuntimeControls.RuntimeProvider != "pi" {
		t.Fatalf("expected pi runtime provider, got %#v", found.RuntimeControls)
	}
}

func TestPiTaskDetailDiscoversPersistedNativeSession(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	binary := filepath.Join(t.TempDir(), "pi-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Pi Test", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "DeepSeek-V4-Pro",
	})
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	sessionPath := filepath.Join(runtimeRoot, taskID, "runtime-home", "pi", "agent", "sessions", "--task-workdir--", "2026-07-04T00-00-00-000Z_pi.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("mkdir session path: %v", err)
	}
	sessionLine := `{"type":"session","version":3,"id":"sess-pi-file","cwd":"/task/workdir"}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(sessionLine), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected get task status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var found struct {
		RuntimeControls struct {
			NativeResumeAvailable bool `json:"native_resume_available"`
			NativeSessionCaptured bool `json:"native_session_captured"`
		} `json:"runtime_controls"`
		LatestContinuation *struct {
			NativeSessionID   string `json:"native_session_id"`
			NativeSessionPath string `json:"native_session_path"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&found); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if !found.RuntimeControls.NativeResumeAvailable || !found.RuntimeControls.NativeSessionCaptured {
		t.Fatalf("expected persisted pi session to enable native resume, got %#v", found.RuntimeControls)
	}
	if found.LatestContinuation == nil || found.LatestContinuation.NativeSessionID != "sess-pi-file" {
		t.Fatalf("expected latest continuation to capture pi session, got %#v", found.LatestContinuation)
	}
	if found.LatestContinuation.NativeSessionPath != sessionPath {
		t.Fatalf("expected pi session path %q, got %#v", sessionPath, found.LatestContinuation)
	}
}

func TestRunningNativeRuntimeWithoutSessionDisablesInterruptSteer(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
		ProviderSessionFactory: testScriptProviderSessionFactory{
			capabilities: &runtimeplugin.Capabilities{PersistentSession: true},
		},
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	binary := filepath.Join(t.TempDir(), "codex-no-session")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Codex No Session", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "running")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected get task status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var found struct {
		RuntimeControls struct {
			InterruptSteerAvailable bool   `json:"interrupt_steer_available"`
			InterruptSteerReason    string `json:"interrupt_steer_reason"`
			NativeSessionCaptured   bool   `json:"native_session_captured"`
		} `json:"runtime_controls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&found); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if found.RuntimeControls.InterruptSteerAvailable {
		t.Fatalf("expected interrupt steer disabled without native session, got %#v", found.RuntimeControls)
	}
	if found.RuntimeControls.NativeSessionCaptured {
		t.Fatalf("expected no native session captured, got %#v", found.RuntimeControls)
	}
	if !strings.Contains(found.RuntimeControls.InterruptSteerReason, "native session") {
		t.Fatalf("expected native session reason, got %#v", found.RuntimeControls)
	}
}
