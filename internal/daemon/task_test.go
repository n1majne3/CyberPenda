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
	"pentest/internal/runtimeprofile"
)

// TestLaunchTaskRunsFakeRuntimeAndStreamsEvents proves the Slice 3 tracer bullet
// through HTTP: launching a fake-runtime task from a project captures the goal,
// runtime profile, runner, and scope snapshot, and the task emits events that
// can be read back.
func TestLaunchTaskRunsFakeRuntimeAndStreamsEvents(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"],"notes":"in scope"}
	}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	body := []byte(`{
		"goal":"enumerate example.com",
		"runtime_profile_id":` + quoteJSON(profileID) + `,
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected create task status 201, got %d with body %s", resp.Code, resp.Body.String())
	}

	var created struct {
		ID                 string `json:"id"`
		ProjectID          string `json:"project_id"`
		Goal               string `json:"goal"`
		Runner             string `json:"runner"`
		RuntimeProfileID   string `json:"runtime_profile_id"`
		LatestContinuation *struct {
			Number           int    `json:"number"`
			RuntimeProfileID string `json:"runtime_profile_id"`
			RuntimeProvider  string `json:"runtime_provider"`
			Runner           string `json:"runner"`
			Status           string `json:"status"`
		} `json:"latest_continuation"`
		ScopeSnapshot struct {
			Domains []string `json:"domains"`
			Notes   string   `json:"notes"`
		} `json:"scope_snapshot"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected task id")
	}
	if created.Goal != "enumerate example.com" {
		t.Fatalf("expected goal, got %q", created.Goal)
	}
	if created.Runner != "sandbox" {
		t.Fatalf("expected sandbox runner, got %q", created.Runner)
	}
	if created.RuntimeProfileID != profileID {
		t.Fatalf("expected runtime profile id, got %q", created.RuntimeProfileID)
	}
	if created.LatestContinuation == nil {
		t.Fatal("expected latest continuation in create response")
	}
	if created.LatestContinuation.Number != 1 {
		t.Fatalf("expected first continuation number 1, got %d", created.LatestContinuation.Number)
	}
	if created.LatestContinuation.RuntimeProvider != "fake" {
		t.Fatalf("expected runtime provider fake, got %q", created.LatestContinuation.RuntimeProvider)
	}
	if created.LatestContinuation.Runner != "sandbox" {
		t.Fatalf("expected continuation runner sandbox, got %q", created.LatestContinuation.Runner)
	}
	// Scope snapshot is captured at launch.
	if got := created.ScopeSnapshot.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope snapshot domain, got %#v", got)
	}
	if created.ScopeSnapshot.Notes != "in scope" {
		t.Fatalf("expected scope snapshot notes, got %q", created.ScopeSnapshot.Notes)
	}

	// The runtime runs in the background, so poll until its output is visible.
	waitForEventText(t, server, projectID, created.ID, "enumerating in-scope assets")
	events := getTaskEvents(t, server, projectID, created.ID)
	kinds := map[string]bool{}
	for _, e := range events {
		kinds[e["kind"].(string)] = true
	}
	if !kinds["lifecycle"] || !kinds["runtime_output"] {
		t.Fatalf("expected lifecycle and runtime_output events, got %#v", kinds)
	}
}

func TestGetTaskIncludesLatestContinuation(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected get task status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var found struct {
		ID                 string `json:"id"`
		LatestContinuation *struct {
			Number           int    `json:"number"`
			RuntimeProfileID string `json:"runtime_profile_id"`
			RuntimeProvider  string `json:"runtime_provider"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&found); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if found.ID != taskID {
		t.Fatalf("expected task id %q, got %q", taskID, found.ID)
	}
	if found.LatestContinuation == nil {
		t.Fatal("expected latest continuation in task detail")
	}
	if found.LatestContinuation.Number != 1 {
		t.Fatalf("expected latest continuation number 1, got %d", found.LatestContinuation.Number)
	}
	if found.LatestContinuation.RuntimeProvider != "fake" {
		t.Fatalf("expected latest continuation provider fake, got %q", found.LatestContinuation.RuntimeProvider)
	}
}

func TestTaskTranscriptEndpointProjectsRetainedEvents(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"map app",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	waitForEventText(t, server, projectID, taskID, "enumerating in-scope assets")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/transcript", nil)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected transcript status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		TaskID  string           `json:"task_id"`
		Entries []map[string]any `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if body.TaskID != taskID {
		t.Fatalf("expected task id %q, got %q", taskID, body.TaskID)
	}
	if !hasTranscriptEntry(body.Entries, "message", "user", "map app") {
		t.Fatalf("expected goal message in transcript, got %#v", body.Entries)
	}
	if !hasTranscriptEntry(body.Entries, "runtime_output", "runtime", "enumerating in-scope assets") {
		t.Fatalf("expected retained runtime output in transcript, got %#v", body.Entries)
	}
}

func TestTaskTranscriptEndpointRejectsCrossProjectTask(t *testing.T) {
	server := newDaemon(t)
	projectA := createProject(t, server, `{"name":"A","scope":{"domains":["a.example"]}}`)
	projectB := createProject(t, server, `{"name":"B","scope":{"domains":["b.example"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectA, `{
		"goal":"map app",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectB+"/tasks/"+taskID+"/transcript", nil)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected transcript cross-project status 404, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func TestLaunchTaskFailsPreflightWhenRuntimeProfileMissing(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(`{
		"goal":"enumerate example.com",
		"runtime_profile_id":"missing-profile",
		"runner":"sandbox"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected task launch preflight failure status 400, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Error     string `json:"error"`
		Preflight struct {
			Pass   bool `json:"pass"`
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Detail string `json:"detail"`
			} `json:"checks"`
		} `json:"preflight"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode preflight launch failure: %v", err)
	}
	if body.Error != "preflight failed" {
		t.Fatalf("expected preflight failed error, got %q", body.Error)
	}
	if body.Preflight.Pass {
		t.Fatalf("expected preflight pass=false, got %#v", body.Preflight)
	}
	if !checkNamed(body.Preflight.Checks, "runtime_profile", "fail") {
		t.Fatalf("expected runtime_profile check to fail, got %#v", body.Preflight.Checks)
	}

	listResp := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks", nil)
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected list tasks status 200, got %d with body %s", listResp.Code, listResp.Body.String())
	}
	var listed struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode tasks list: %v", err)
	}
	if len(listed.Tasks) != 0 {
		t.Fatalf("preflight failure must not create a task, got %#v", listed.Tasks)
	}
}

func TestLaunchTaskUsesProjectDefaultsWhenRuntimeControlsAreOmitted(t *testing.T) {
	server := newDaemon(t)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"]},
		"defaults":{"runtime_profile":`+quoteJSON(profileID)+`,"runner":"sandbox"}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(`{
		"goal":"enumerate example.com"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected create task status 201, got %d with body %s", resp.Code, resp.Body.String())
	}
	var created struct {
		ID               string `json:"id"`
		RuntimeProfileID string `json:"runtime_profile_id"`
		Runner           string `json:"runner"`
		Status           string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	if created.RuntimeProfileID != profileID {
		t.Fatalf("expected default runtime profile %q, got %q", profileID, created.RuntimeProfileID)
	}
	if created.Runner != "sandbox" {
		t.Fatalf("expected default runner sandbox, got %q", created.Runner)
	}
	waitForTaskStatus(t, server, projectID, created.ID, "completed")
}

func TestLaunchTaskUsesRuntimeProfileProviderAdapter(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"]}
	}`)

	binary := filepath.Join(t.TempDir(), "codex-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho codex-provider:$*\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}

	profileID := createLocalRuntimeProfile(t, server, "Codex Test", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})

	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)

	waitForEventText(t, server, projectID, taskID, "codex-provider:exec")
	events := getTaskEvents(t, server, projectID, taskID)
	var sawCodexLifecycle bool
	var sawProviderOutput bool
	for _, event := range events {
		if event["kind"] == "lifecycle" {
			payload := event["payload"].(map[string]any)
			if payload["adapter"] == "codex" {
				sawCodexLifecycle = true
			}
		}
		if event["kind"] == "runtime_output" {
			payload := event["payload"].(map[string]any)
			text, _ := payload["text"].(string)
			if strings.Contains(text, "codex-provider:exec --model gpt-test") &&
				strings.Contains(text, "enumerate example.com") {
				sawProviderOutput = true
			}
		}
	}
	if !sawCodexLifecycle {
		t.Fatalf("expected codex lifecycle adapter, got %#v", events)
	}
	if !sawProviderOutput {
		t.Fatalf("expected provider stdout in task events, got %#v", events)
	}
}

func TestLaunchTaskReturnsBeforeRuntimeProcessCompletes(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	binary := filepath.Join(t.TempDir(), "slow-codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho slow-provider-started\nsleep 2\necho slow-provider-completed\n"), 0o700); err != nil {
		t.Fatalf("write slow provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Slow Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})

	start := time.Now()
	taskID := createTask(t, server, projectID, `{
		"goal":"run slow provider",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("task launch blocked on runtime process for %s", elapsed)
	}

	waitForEventText(t, server, projectID, taskID, "slow-provider-started")

	stopResp := httptest.NewRecorder()
	stopReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/stop", nil)
	server.ServeHTTP(stopResp, stopReq)
	if stopResp.Code != http.StatusOK {
		t.Fatalf("expected stop status 200, got %d with body %s", stopResp.Code, stopResp.Body.String())
	}
}

func TestLaunchTaskWrapsProviderCommandInSandboxRunner(t *testing.T) {
	containerCLI := filepath.Join(t.TempDir(), "fake-docker")
	if err := os.WriteFile(containerCLI, []byte("#!/bin/sh\necho sandbox-command:$*\n"), 0o700); err != nil {
		t.Fatalf("write fake container cli: %v", err)
	}
	server, err := daemon.NewServer(daemon.Config{
		Version:      "test-version",
		DBPath:       filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:  t.TempDir(),
		SandboxImage: "pentest-kali:test",
		ContainerCLI: containerCLI,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createLocalRuntimeProfile(t, server, "Codex Sandbox", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test",
	})

	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	waitForEventText(t, server, projectID, taskID, "sandbox-command:run --rm -i")
	events := getTaskEvents(t, server, projectID, taskID)
	var sawSandboxCommand bool
	for _, event := range events {
		if event["kind"] != "runtime_output" {
			continue
		}
		payload := event["payload"].(map[string]any)
		text, _ := payload["text"].(string)
		if strings.Contains(text, "sandbox-command:run --rm -i") &&
			strings.Contains(text, "pentest-kali:test codex exec --model gpt-test") &&
			strings.Contains(text, "enumerate example.com") {
			sawSandboxCommand = true
		}
	}
	if !sawSandboxCommand {
		t.Fatalf("expected sandbox-wrapped provider command, got %#v", events)
	}
}

func TestSandboxResumeRebuildsContainerWithPersistentTaskMountAndRuntimeHome(t *testing.T) {
	dir := t.TempDir()
	runtimeRoot := filepath.Join(dir, "runtime-root")
	logPath := filepath.Join(dir, "docker.log")
	countPath := filepath.Join(dir, "docker-count")
	containerCLI := filepath.Join(dir, "fake-docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"cidfile=\"\"\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--cidfile\" ]; then cidfile=\"$arg\"; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"count=$(cat " + shellQuote(countPath) + " 2>/dev/null || echo 0)\n" +
		"count=$((count + 1))\n" +
		"echo \"$count\" > " + shellQuote(countPath) + "\n" +
		"if [ -n \"$cidfile\" ]; then echo \"ctr-$count\" > \"$cidfile\"; fi\n" +
		"echo sandbox-command:$*\n"
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
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resp.Code, resp.Body.String())
	}
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	rawLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	logText := string(rawLog)
	taskMount := filepath.Join(runtimeRoot, taskID) + ":/task"
	if got := strings.Count(logText, "run --rm -i --cidfile"); got != 2 {
		t.Fatalf("expected two sandbox container launches, got %d in log:\n%s", got, logText)
	}
	if got := strings.Count(logText, taskMount); got != 2 {
		t.Fatalf("expected both launches to reuse task mount %q, got log:\n%s", taskMount, logText)
	}
	var launchLines []string
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		if strings.HasPrefix(line, "run --rm") {
			launchLines = append(launchLines, line)
		}
	}
	if len(launchLines) != 2 {
		t.Fatalf("expected two docker launch log lines, got %d in log:\n%s", len(launchLines), logText)
	}
	for _, line := range launchLines {
		if !strings.Contains(line, "CODEX_HOME=/task/runtime-home/codex") {
			t.Fatalf("expected launch to use persistent runtime home, got line:\n%s", line)
		}
	}

	detailResp := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("expected task detail status 200, got %d with body %s", detailResp.Code, detailResp.Body.String())
	}
	var detailed struct {
		LatestContinuation *struct {
			Number      int    `json:"number"`
			ContainerID string `json:"container_id"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&detailed); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if detailed.LatestContinuation == nil {
		t.Fatal("expected latest continuation")
	}
	if detailed.LatestContinuation.Number != 2 {
		t.Fatalf("expected resumed continuation number 2, got %d", detailed.LatestContinuation.Number)
	}
	if detailed.LatestContinuation.ContainerID != "ctr-2" {
		t.Fatalf("expected latest continuation container id ctr-2, got %q", detailed.LatestContinuation.ContainerID)
	}
}

func TestTaskSummaryUpdatesAreAcceptedAndVersioned(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	putTaskSummary(t, server, projectID, taskID, `{
		"summary":"Found example.com as the primary target.",
		"submitted_by":"fake"
	}`)
	putTaskSummary(t, server, projectID, taskID, `{
		"summary":"Found example.com and confirmed no subdomains yet.",
		"submitted_by":"fake"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/summary", nil)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected summary status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var body struct {
		Summary struct {
			Version int    `json:"version"`
			Summary string `json:"summary"`
		} `json:"summary"`
		Versions []struct {
			Version int    `json:"version"`
			Summary string `json:"summary"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if body.Summary.Version != 2 {
		t.Fatalf("expected latest version 2, got %d", body.Summary.Version)
	}
	if body.Summary.Summary != "Found example.com and confirmed no subdomains yet." {
		t.Fatalf("expected latest summary, got %q", body.Summary.Summary)
	}
	if len(body.Versions) != 2 {
		t.Fatalf("expected 2 summary versions, got %d", len(body.Versions))
	}
}

func TestSteerTaskRecordsDirectiveAndRuntimeProfileSwitch(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileA := createRuntimeProfile(t, server, `{"name":"Fake A","provider":"fake"}`)
	profileB := createRuntimeProfile(t, server, `{"name":"Fake B","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileA)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus on http services before dns brute force",
		"runtime_profile_id":`+quoteJSON(profileB)+`,
		"submitted_by":"operator"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected steer status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var steered struct {
		Event struct {
			Kind    string         `json:"kind"`
			Payload map[string]any `json:"payload"`
		} `json:"event"`
		RuntimeConfigVersion struct {
			Version          int    `json:"version"`
			RuntimeProfileID string `json:"runtime_profile_id"`
		} `json:"runtime_config_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&steered); err != nil {
		t.Fatalf("decode steer response: %v", err)
	}
	if steered.Event.Kind != "steering" {
		t.Fatalf("expected steering event, got %q", steered.Event.Kind)
	}
	if steered.Event.Payload["directive"] != "focus on http services before dns brute force" {
		t.Fatalf("expected directive payload, got %#v", steered.Event.Payload)
	}
	if steered.RuntimeConfigVersion.Version != 2 {
		t.Fatalf("expected second runtime config version, got %d", steered.RuntimeConfigVersion.Version)
	}
	if steered.RuntimeConfigVersion.RuntimeProfileID != profileB {
		t.Fatalf("expected switched profile, got %q", steered.RuntimeConfigVersion.RuntimeProfileID)
	}

	events := getTaskEvents(t, server, projectID, taskID)
	sawSteering := false
	for _, event := range events {
		if event["kind"] == "steering" {
			sawSteering = true
			break
		}
	}
	if !sawSteering {
		t.Fatalf("expected steering event, got %#v", events)
	}
}

func TestTaskContinuationReturnsSummaryOrMechanicalHandoff(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"],"notes":"approved only"}
	}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/continuation", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected continuation status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var handoff struct {
		Mode    string `json:"mode"`
		Summary *struct {
			Summary string `json:"summary"`
		} `json:"summary"`
		Handoff struct {
			Goal             string   `json:"goal"`
			RuntimeProfileID string   `json:"runtime_profile_id"`
			ScopeDomains     []string `json:"scope_domains"`
		} `json:"handoff"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&handoff); err != nil {
		t.Fatalf("decode handoff: %v", err)
	}
	if handoff.Mode != "mechanical_handoff" {
		t.Fatalf("expected mechanical handoff, got %q", handoff.Mode)
	}
	if handoff.Summary != nil {
		t.Fatalf("expected no summary, got %#v", handoff.Summary)
	}
	if handoff.Handoff.Goal != "enumerate example.com" {
		t.Fatalf("expected task goal in handoff, got %q", handoff.Handoff.Goal)
	}
	if got := handoff.Handoff.ScopeDomains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope domains in handoff, got %#v", got)
	}

	putTaskSummary(t, server, projectID, taskID, `{
		"summary":"Enumerated example.com and found one web service.",
		"submitted_by":"fake"
	}`)

	resp = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/continuation", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected continuation with summary status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var summarized struct {
		Mode    string `json:"mode"`
		Summary struct {
			Summary string `json:"summary"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&summarized); err != nil {
		t.Fatalf("decode summarized continuation: %v", err)
	}
	if summarized.Mode != "summary" {
		t.Fatalf("expected summary mode, got %q", summarized.Mode)
	}
	if summarized.Summary.Summary != "Enumerated example.com and found one web service." {
		t.Fatalf("expected latest summary, got %q", summarized.Summary.Summary)
	}
}

// TestTaskRoutesRejectUnknownProject pins the cross-cutting invariant that
// every project-scoped task route returns 404 for a project that does not
// exist, the same way the blackboard / credential / dashboard routes do.
// Without an explicit project-exists check the list route returns 200 with an
// empty body and the {task_id} routes only guard against cross-project access
// to a *real* task, never against a bogus project id.
func TestTaskRoutesRejectUnknownProject(t *testing.T) {
	server := newDaemon(t)
	const bogus = "does-not-exist"

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list tasks", http.MethodGet, "/api/projects/" + bogus + "/tasks", ""},
		{"create task", http.MethodPost, "/api/projects/" + bogus + "/tasks", `{"goal":"x","runner":"sandbox"}`},
		{"get task", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything", ""},
		{"task events", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything/events", ""},
		{"stop task", http.MethodPost, "/api/projects/" + bogus + "/tasks/anything/stop", ""},
		{"steer task", http.MethodPost, "/api/projects/" + bogus + "/tasks/anything/steer", `{"directive":"focus"}`},
		{"task continuation", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything/continuation", ""},
		{"put task summary", http.MethodPut, "/api/projects/" + bogus + "/tasks/anything/summary", `{"summary":"s"}`},
		{"get task summary", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything/summary", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *bytes.Reader
			if tc.body == "" {
				body = bytes.NewReader(nil)
			} else {
				body = bytes.NewReader([]byte(tc.body))
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			server.ServeHTTP(resp, req)

			if resp.Code != http.StatusNotFound {
				t.Fatalf("expected 404 for %s on unknown project, got %d with body %s", tc.name, resp.Code, resp.Body.String())
			}
		})
	}
}

// getTaskEvents reads the task timeline as a list of generic maps.
func getTaskEvents(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, taskID string) []map[string]any {
	t.Helper()
	// server is *daemon.Server; use a type assertion-free path via httptest.
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
func TestCreateTaskRecordsLoopbackRewriteEvent(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	taskID := createTask(t, server, projectID, `{
		"goal":"recon http://127.0.0.1:3000 and find the score board",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	events := getTaskEvents(t, server, projectID, taskID)
	payload := findTargetRewriteEvent(events)
	if payload == nil {
		t.Fatalf("expected a target_rewrite event, got events: %#v", events)
	}
	if from, _ := payload["from"].(string); !strings.Contains(from, "127.0.0.1:3000") {
		t.Fatalf("expected from to contain the loopback goal, got %q", from)
	}
	to, _ := payload["to"].(string)
	if !strings.Contains(to, "host.docker.internal:3000") || strings.Contains(to, "127.0.0.1") {
		t.Fatalf("expected to to be rewritten to host.docker.internal, got %q", to)
	}
}

func TestCreateTaskOmitsRewriteEventForHostRunner(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	taskID := createTask(t, server, projectID, `{
		"goal":"recon http://127.0.0.1:3000",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)

	events := getTaskEvents(t, server, projectID, taskID)
	if findTargetRewriteEvent(events) != nil {
		t.Fatalf("expected no target_rewrite event for host runner, got events: %#v", events)
	}
}

func TestCreateTaskOmitsRewriteEventWhenGoalHasNoLoopback(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com only",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	events := getTaskEvents(t, server, projectID, taskID)
	if findTargetRewriteEvent(events) != nil {
		t.Fatalf("expected no target_rewrite event for loopback-free goal, got events: %#v", events)
	}
}

func TestResumeTaskEnrichesPromptWithFindingsAndProgressFacts(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	upsertFact(t, server, projectID, "progress:juice-shop", `{
		"summary":"53 of 112 challenges solved",
		"body":"{\"solved\":53,\"total\":112}"
	}`)
	upsertFinding(t, server, projectID, "sqli-login", `{
		"title":"SQL injection in login",
		"status":"unconfirmed"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resp.Code, resp.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	var sawEnrichedGoal bool
	for time.Now().Before(deadline) {
		events := getTaskEvents(t, server, projectID, taskID)
		for _, event := range events {
			if event["kind"] != "runtime_output" {
				continue
			}
			payload := event["payload"].(map[string]any)
			goal, _ := payload["goal"].(string)
			if strings.Contains(goal, "sqli-login") &&
				strings.Contains(goal, "Current findings:") &&
				strings.Contains(goal, `"solved":53`) {
				sawEnrichedGoal = true
				break
			}
		}
		if sawEnrichedGoal {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawEnrichedGoal {
		t.Fatalf("expected resumed runtime to receive enriched handoff prompt")
	}
}

func TestSteerTaskRejectsRuntimeProviderChange(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	fakeProfileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	codexProfileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex","fields":{"model":"gpt-test"}}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(fakeProfileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"switch runtimes",
		"runtime_profile_id":`+quoteJSON(codexProfileID)+`
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected steer status 400 for provider change, got %d with body %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "runtime provider") {
		t.Fatalf("expected provider-change error, got %s", resp.Body.String())
	}
}

func TestResumeTaskUsesSteeredRuntimeProfileWhenProviderMatches(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileA := createRuntimeProfile(t, server, `{"name":"Fake A","provider":"fake"}`)
	profileB := createRuntimeProfile(t, server, `{"name":"Fake B","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileA)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	steerResp := httptest.NewRecorder()
	steerReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"use profile b next",
		"runtime_profile_id":`+quoteJSON(profileB)+`
	}`)))
	steerReq.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(steerResp, steerReq)
	if steerResp.Code != http.StatusOK {
		t.Fatalf("expected steer status 200, got %d with body %s", steerResp.Code, steerResp.Body.String())
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resp.Code, resp.Body.String())
	}

	var resumed struct {
		LatestContinuation *struct {
			RuntimeProfileID string `json:"runtime_profile_id"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resumed); err != nil {
		t.Fatalf("decode resumed task: %v", err)
	}
	if resumed.LatestContinuation == nil {
		t.Fatal("expected latest continuation in resume response")
	}
	if resumed.LatestContinuation.RuntimeProfileID != profileB {
		t.Fatalf("expected resumed continuation profile %q, got %q", profileB, resumed.LatestContinuation.RuntimeProfileID)
	}
	waitForTaskStatus(t, server, projectID, taskID, "completed")
}

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
		"goal":"enumerate example.com",
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
		"case \"$*\" in\n" +
		"  *resume*) exit 0 ;;\n" +
		"esac\n" +
		"exec sleep 5\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Codex Slow", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})

	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForEventText(t, server, projectID, taskID, "codex-provider:run")

	sessionPath := filepath.Join(runtimeRoot, taskID, "runtime-home", "codex", "sessions", "2026", "07", "04", "rollout-live.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("mkdir session path: %v", err)
	}
	sessionMeta := `{"timestamp":"2026-07-04T00:00:00Z","type":"session_meta","payload":{"session_id":"sess-live","cwd":"` + runtimeRoot + `"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(sessionMeta), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus admin.example.com"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected steer interrupt status 202, got %d with body %s", resp.Code, resp.Body.String())
	}

	waitForEventText(t, server, projectID, taskID, "resume sess-live")
	waitForEventText(t, server, projectID, taskID, "focus admin.example.com")
	waitForTaskStatus(t, server, projectID, taskID, "completed")
}

func TestSandboxSteerConfirmsContainerExitBeforeNativeResume(t *testing.T) {
	dir := t.TempDir()
	runtimeRoot := filepath.Join(dir, "runtime-root")
	dockerLog := filepath.Join(dir, "docker.log")
	inspectLog := filepath.Join(dir, "inspect.log")
	countPath := filepath.Join(dir, "docker-count")
	containerCLI := filepath.Join(dir, "fake-docker")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"inspect\" ]; then\n" +
		"  echo \"$*\" >> " + shellQuote(inspectLog) + "\n" +
		"  echo false\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"$*\" >> " + shellQuote(dockerLog) + "\n" +
		"cidfile=\"\"\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--cidfile\" ]; then cidfile=\"$arg\"; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"count=$(cat " + shellQuote(countPath) + " 2>/dev/null || echo 0)\n" +
		"count=$((count + 1))\n" +
		"echo \"$count\" > " + shellQuote(countPath) + "\n" +
		"if [ -n \"$cidfile\" ]; then echo \"ctr-$count\" > \"$cidfile\"; fi\n" +
		"echo sandbox-command:$*\n" +
		"case \"$*\" in\n" +
		"  *resume*) exit 0 ;;\n" +
		"esac\n" +
		"exec sleep 5\n"
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
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForEventText(t, server, projectID, taskID, "sandbox-command:run")

	sessionPath := filepath.Join(runtimeRoot, taskID, "runtime-home", "codex", "sessions", "2026", "07", "04", "sandbox-live.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("mkdir session path: %v", err)
	}
	sessionMeta := `{"timestamp":"2026-07-04T00:00:00Z","type":"session_meta","payload":{"session_id":"sess-sandbox","cwd":"` + runtimeRoot + `"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(sessionMeta), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus admin.example.com"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected steer interrupt status 202, got %d with body %s", resp.Code, resp.Body.String())
	}

	inspectRaw, err := os.ReadFile(inspectLog)
	if err != nil {
		t.Fatalf("read inspect log: %v", err)
	}
	if !strings.Contains(string(inspectRaw), "inspect -f {{.State.Running}} ctr-1") {
		t.Fatalf("expected steer to inspect stopped container ctr-1, got %s", string(inspectRaw))
	}
	waitForEventText(t, server, projectID, taskID, "resume sess-sandbox")
	waitForEventText(t, server, projectID, taskID, "focus admin.example.com")
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	dockerRaw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	if got := strings.Count(string(dockerRaw), "run --rm -i --cidfile"); got != 2 {
		t.Fatalf("expected initial and resumed sandbox launches, got %d in log:\n%s", got, string(dockerRaw))
	}
}

func putTaskSummary(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, taskID, body string) {
	t.Helper()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/tasks/"+taskID+"/summary", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected put summary status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
}
