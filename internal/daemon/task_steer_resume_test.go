package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/daemon"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

func TestSteerTaskInterruptsActiveRunAndLaunchesResumedContinuation(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	binary := filepath.Join(t.TempDir(), "codex-slow")
	script := "#!/bin/sh\n" +
		"echo codex-provider:$*\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"resume\" ]; then exit 0; fi\n" +
		"done\n" +
		"exec sleep 5\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Codex Slow", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})

	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForEventText(t, server, projectID, taskID, "codex-provider:exec")

	sessionPath := filepath.Join(runtimeRoot, taskID, "runtime-home", "codex", "sessions", "2026", "07", "04", "rollout-live.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("mkdir session path: %v", err)
	}
	sessionMeta := `{"timestamp":"2026-07-04T00:00:00Z","type":"session_meta","payload":{"session_id":"sess-live","cwd":"` + runtimeRoot + `"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(sessionMeta), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	stopResp := httptest.NewRecorder()
	server.ServeHTTP(stopResp, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/stop", nil))
	if stopResp.Code != http.StatusOK {
		t.Fatalf("expected stop status 200, got %d with body %s", stopResp.Code, stopResp.Body.String())
	}
	waitForTaskStatus(t, server, projectID, taskID, "stopped")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus admin.example.com"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected steer queue status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	resumeResp := httptest.NewRecorder()
	server.ServeHTTP(resumeResp, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil))
	if resumeResp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resumeResp.Code, resumeResp.Body.String())
	}

	waitForEventText(t, server, projectID, taskID, "resume sess-live")
	waitForEventText(t, server, projectID, taskID, "focus admin.example.com")
	waitForTaskStatus(t, server, projectID, taskID, "completed")
}

func TestClaudeSteerNativeResumeKeepsSettingsAndMCPArgs(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	binary := filepath.Join(t.TempDir(), "claude-steer")
	script := "#!/bin/sh\n" +
		"echo claude-provider:$*\n" +
		"case \"$*\" in\n" +
		"  *--resume*) exit 0 ;;\n" +
		"esac\n" +
		"printf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"sess-claude\"}\\n'\n" +
		"exec sleep 5\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Claude Steer", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "claude-sonnet-4",
	})

	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	deadline := time.Now().Add(2 * time.Second)
	for {
		detailResp := httptest.NewRecorder()
		server.ServeHTTP(detailResp, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil))
		var detail struct {
			RuntimeControls struct {
				NativeSessionCaptured bool `json:"native_session_captured"`
			} `json:"runtime_controls"`
		}
		if detailResp.Code == http.StatusOK && json.NewDecoder(detailResp.Body).Decode(&detail) == nil && detail.RuntimeControls.NativeSessionCaptured {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for native session capture")
		}
		time.Sleep(10 * time.Millisecond)
	}

	stopResp := httptest.NewRecorder()
	server.ServeHTTP(stopResp, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/stop", nil))
	if stopResp.Code != http.StatusOK {
		t.Fatalf("expected stop status 200, got %d with body %s", stopResp.Code, stopResp.Body.String())
	}
	waitForTaskStatus(t, server, projectID, taskID, "stopped")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus admin.example.com"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected steer queue status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	resumeResp := httptest.NewRecorder()
	server.ServeHTTP(resumeResp, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil))
	if resumeResp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resumeResp.Code, resumeResp.Body.String())
	}

	waitForEventText(t, server, projectID, taskID, "claude-provider:--resume sess-claude")
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	events := getTaskEvents(t, server, projectID, taskID)
	var resumeLine string
	for _, event := range events {
		if event["kind"] != "runtime_output" {
			continue
		}
		payload, _ := event["payload"].(map[string]any)
		text, _ := payload["text"].(string)
		if strings.Contains(text, "claude-provider:--resume sess-claude") {
			resumeLine = text
			break
		}
	}
	if resumeLine == "" {
		t.Fatalf("expected resumed claude command in events, got %#v", events)
	}
	for _, want := range []string{"--settings", "settings.json"} {
		if !strings.Contains(resumeLine, want) {
			t.Fatalf("expected resumed claude command to contain %q, got %q", want, resumeLine)
		}
	}
	for _, retired := range []string{"--strict-mcp-config", "--mcp-config", ".mcp.json"} {
		if strings.Contains(resumeLine, retired) {
			t.Fatalf("resumed claude command retained retired built-in MCP argument %q: %q", retired, resumeLine)
		}
	}
}

func TestSteerTaskRejectsActiveRunWithoutNativeSessionBeforeStopping(t *testing.T) {
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
	script := "#!/bin/sh\n" +
		"echo codex-provider:$*\n" +
		"exec sleep 5\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
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
	waitForEventText(t, server, projectID, taskID, "codex-provider:exec")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus admin.example.com"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected steer status 409 without native steer capability, got %d with body %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "provider session does not support") {
		t.Fatalf("expected provider session capability error, got %s", resp.Body.String())
	}

	detailResp := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("expected task detail status 200, got %d with body %s", detailResp.Code, detailResp.Body.String())
	}
	var detailed struct {
		Status             string `json:"status"`
		ActiveContinuation *struct {
			Status string `json:"status"`
		} `json:"active_continuation"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&detailed); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if detailed.Status != "running" {
		t.Fatalf("expected task to remain running, got %q", detailed.Status)
	}
	if detailed.ActiveContinuation == nil || detailed.ActiveContinuation.Status != "running" {
		t.Fatalf("expected active continuation to remain running, got %#v", detailed.ActiveContinuation)
	}
}

func TestSandboxSteerConfirmsContainerExitBeforeNativeResume(t *testing.T) {
	dir := t.TempDir()
	runtimeRoot := filepath.Join(dir, "runtime-root")
	dockerLog := filepath.Join(dir, "docker.log")
	countPath := filepath.Join(dir, "docker-count")
	stoppedPath := filepath.Join(dir, "stopped")
	containerCLI := filepath.Join(dir, "fake-docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(dockerLog) + "\n" +
		"if [ \"$1\" = \"start\" ]; then\n" +
		"  id=\"$3\"\n" +
		"  create_file=" + shellQuote(dir) + "/$id.create\n" +
		"  echo sandbox-command:$(cat \"$create_file\")\n" +
		"  if [ \"$id\" = \"ctr-2\" ]; then exit 0; fi\n" +
		"  while [ ! -f " + shellQuote(stoppedPath) + " ]; do sleep 0.05; done\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"stop\" ]; then touch " + shellQuote(stoppedPath) + "; exit 0; fi\n" +
		"if [ \"$1\" = \"kill\" ]; then touch " + shellQuote(stoppedPath) + "; exit 0; fi\n" +
		"if [ \"$1\" = \"rm\" ]; then exit 0; fi\n" +
		"if [ \"$1\" != \"create\" ]; then exit 1; fi\n" +
		"cidfile=\"\"\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--cidfile\" ]; then cidfile=\"$arg\"; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"count=$(cat " + shellQuote(countPath) + " 2>/dev/null || echo 0)\n" +
		"count=$((count + 1))\n" +
		"echo \"$count\" > " + shellQuote(countPath) + "\n" +
		"echo \"$*\" > " + shellQuote(dir) + "/ctr-$count.create\n" +
		"if [ -n \"$cidfile\" ]; then echo \"ctr-$count\" > \"$cidfile\"; fi\n" +
		"echo ctr-$count\n"
	if err := os.WriteFile(containerCLI, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake container cli: %v", err)
	}
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		SandboxImage:         "pentest-kali:test",
		ContainerCLI:         containerCLI,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createLocalRuntimeProfile(t, server, "Codex Sandbox", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test",
	})

	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForEventText(t, server, projectID, taskID, "sandbox-command:create")

	sessionPath := filepath.Join(runtimeRoot, taskID, "runtime-home", "codex", "sessions", "2026", "07", "04", "sandbox-live.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("mkdir session path: %v", err)
	}
	sessionMeta := `{"timestamp":"2026-07-04T00:00:00Z","type":"session_meta","payload":{"session_id":"sess-sandbox","cwd":"` + runtimeRoot + `"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(sessionMeta), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	stopResp := httptest.NewRecorder()
	server.ServeHTTP(stopResp, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/stop", nil))
	if stopResp.Code != http.StatusOK {
		t.Fatalf("expected stop status 200, got %d with body %s", stopResp.Code, stopResp.Body.String())
	}
	waitForDockerLogText(t, dockerLog, "stop ctr-1")
	waitForTaskStatus(t, server, projectID, taskID, "stopped")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus admin.example.com"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected steer queue status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	resumeResp := httptest.NewRecorder()
	server.ServeHTTP(resumeResp, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil))
	if resumeResp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resumeResp.Code, resumeResp.Body.String())
	}

	waitForTaskStatus(t, server, projectID, taskID, "completed")

	dockerRaw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	if got := strings.Count(string(dockerRaw), "create --cidfile"); got != 2 {
		t.Fatalf("expected initial and resumed sandbox launches, got %d in log:\n%s", got, string(dockerRaw))
	}
}
