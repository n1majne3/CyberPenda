package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/runtimeprofile"
)

func TestResumeTaskUsesCodexNativeResumeWhenSessionExists(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	binary := filepath.Join(t.TempDir(), "codex-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho codex-provider:$*\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Codex Test", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})

	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	sessionPath := filepath.Join(runtimeRoot, taskID, "runtime-home", "codex", "sessions", "2026", "07", "04", "rollout-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("mkdir session path: %v", err)
	}
	sessionMeta := `{"timestamp":"2026-07-04T00:00:00Z","type":"session_meta","payload":{"session_id":"sess-123","cwd":"` + runtimeRoot + `"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(sessionMeta), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resp.Code, resp.Body.String())
	}

	waitForEventText(t, server, projectID, taskID, "resume sess-123")
	waitForTaskStatus(t, server, projectID, taskID, "completed")
}

func TestResumeTaskUsesContinuationModelSelectionWithoutDroppingRuntimeFields(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	binary := filepath.Join(t.TempDir(), "codex-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho codex-provider:$*\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Codex Test", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	sessionPath := filepath.Join(runtimeRoot, taskID, "runtime-home", "codex", "sessions", "2026", "07", "04", "rollout-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("mkdir session path: %v", err)
	}
	sessionMeta := `{"timestamp":"2026-07-04T00:00:00Z","type":"session_meta","payload":{"session_id":"sess-456","cwd":"` + runtimeRoot + `"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(sessionMeta), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	t.Setenv("MIMO_API_KEY", "sk-test")
	providerID := createModelProvider(t, server, `{
		"name":"MiMo",
		"base_url":"https://api.example.test/v1",
		"protocols":["openai_responses"],
		"catalog":{"manual":["mimo-v2-pro"],"default_model":"mimo-v2-pro"}
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", bytes.NewReader([]byte(`{
		"model_provider_id":`+quoteJSON(providerID)+`,
		"model_override":"mimo-v2-pro"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resp.Code, resp.Body.String())
	}
	waitForEventText(t, server, projectID, taskID, "resume sess-456")
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	detailResp := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("expected get task status 200, got %d with body %s", detailResp.Code, detailResp.Body.String())
	}
	var resumed struct {
		LatestContinuation *struct {
			RuntimeProfileID string `json:"runtime_profile_id"`
		} `json:"latest_continuation"`
		RuntimeConfiguration *struct {
			ModelProviderID string `json:"model_provider_id"`
			Model           string `json:"model"`
		} `json:"runtime_configuration"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&resumed); err != nil {
		t.Fatalf("decode resumed task: %v", err)
	}
	if resumed.LatestContinuation == nil || resumed.LatestContinuation.RuntimeProfileID != profileID {
		t.Fatalf("expected immutable profile provenance %q, got %#v", profileID, resumed.LatestContinuation)
	}
	if resumed.RuntimeConfiguration == nil || resumed.RuntimeConfiguration.ModelProviderID != providerID || resumed.RuntimeConfiguration.Model != "mimo-v2-pro" {
		t.Fatalf("expected continuation model selection in Snapshot summary, got %#v", resumed.RuntimeConfiguration)
	}
}

func TestResumeTaskFallsBackToFreshContinuationWhenNativeSessionIsMissing(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	binary := filepath.Join(t.TempDir(), "codex-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho codex-provider:$*\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Codex Test", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})

	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected fresh resume status 202 without native session, got %d with body %s", resp.Code, resp.Body.String())
	}
	var resumed struct {
		LatestContinuation *struct {
			Number int `json:"number"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resumed); err != nil {
		t.Fatalf("decode fresh resume response: %v", err)
	}
	if resumed.LatestContinuation == nil || resumed.LatestContinuation.Number != 2 {
		t.Fatalf("fresh resume continuation = %#v", resumed.LatestContinuation)
	}
}
