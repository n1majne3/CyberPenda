package daemon_test

import (
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
		"type":"pentest","goal":"enumerate example.com",
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
		"type":"pentest","goal":"run slow provider",
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
	dir := t.TempDir()
	createLog := filepath.Join(dir, "create.log")
	containerCLI := filepath.Join(dir, "fake-docker")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  create) echo \"$*\" > " + shellQuote(createLog) + "; echo ctr-1 ;;\n" +
		"  start) echo sandbox-command:$(cat " + shellQuote(createLog) + ") ;;\n" +
		"  rm) exit 0 ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(containerCLI, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake container cli: %v", err)
	}
	server, err := daemon.NewServer(daemon.Config{
		Version:                "test-version",
		DBPath:                 filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:            t.TempDir(),
		SandboxImage:           "pentest-kali:test",
		ContainerCLI:           containerCLI,
		ProviderSessionFactory: testScriptProviderSessionFactory{},
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
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	waitForEventText(t, server, projectID, taskID, "sandbox-command:create --cidfile")
	events := getTaskEvents(t, server, projectID, taskID)
	var sawSandboxCommand bool
	for _, event := range events {
		if event["kind"] != "runtime_output" {
			continue
		}
		payload := event["payload"].(map[string]any)
		text, _ := payload["text"].(string)
		if strings.Contains(text, "sandbox-command:create --cidfile") &&
			strings.Contains(text, "pentest-kali:test codex") {
			sawSandboxCommand = true
		}
	}
	if !sawSandboxCommand {
		t.Fatalf("expected sandbox-wrapped provider command, got %#v", events)
	}
}

func TestLaunchTaskCreatesHostProxyOnlySandboxNetworkBeforeContainerStart(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker.log")
	networkPath := filepath.Join(dir, "network-created")
	createLog := filepath.Join(dir, "create.log")
	containerCLI := filepath.Join(dir, "fake-docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(dockerLog) + "\n" +
		"if [ \"$1 $2\" = \"network inspect\" ]; then\n" +
		"  if [ -f " + shellQuote(networkPath) + " ]; then echo 'bridge|false'; exit 0; fi\n" +
		"  exit 1\n" +
		"fi\n" +
		"if [ \"$1 $2\" = \"network create\" ]; then touch " + shellQuote(networkPath) + "; echo network-id; exit 0; fi\n" +
		"case \"$1\" in\n" +
		"  create) echo \"$*\" > " + shellQuote(createLog) + "; echo ctr-1 ;;\n" +
		"  start)\n" +
		"    if [ ! -f " + shellQuote(networkPath) + " ]; then echo 'network pentest-host-proxy-only not found' >&2; exit 1; fi\n" +
		"    echo sandbox-command:$(cat " + shellQuote(createLog) + ") ;;\n" +
		"  rm) exit 0 ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(containerCLI, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake container cli: %v", err)
	}
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          t.TempDir(),
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
		"runner":"sandbox",
		"run_controls":{"sandbox_network":"host_proxy_only"}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	raw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	logText := string(raw)
	createNetwork := "network create --driver bridge pentest-host-proxy-only"
	if !strings.Contains(logText, createNetwork) {
		t.Fatalf("expected host-proxy-only network creation, got log:\n%s", logText)
	}
	if strings.Index(logText, createNetwork) > strings.Index(logText, "create --cidfile") {
		t.Fatalf("expected network creation before container creation, got log:\n%s", logText)
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
		"if [ \"$1\" != \"create\" ]; then exit 0; fi\n" +
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
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resp.Code, resp.Body.String())
	}
	// Wait for the second container creation directly: the task status may
	// still read "completed" from the first run while the resume launch is
	// still in flight, and reading the log before the second create races.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rawCount, err := os.ReadFile(countPath)
		if err == nil && strings.TrimSpace(string(rawCount)) == "2" {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out waiting for second container creation")
		}
		time.Sleep(20 * time.Millisecond)
	}

	rawLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	logText := string(rawLog)
	taskMount := "type=bind,src=" + filepath.Join(runtimeRoot, taskID) + ",dst=/task"
	if got := strings.Count(logText, "create --cidfile"); got != 2 {
		t.Fatalf("expected two sandbox container launches, got %d in log:\n%s", got, logText)
	}
	if got := strings.Count(logText, taskMount); got != 2 {
		t.Fatalf("expected both launches to reuse task mount %q, got log:\n%s", taskMount, logText)
	}
	var launchLines []string
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		if strings.HasPrefix(line, "create --cidfile") {
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

	// Container creation precedes durable Continuation metadata. Wait for the
	// public detail needed by this assertion, not only the CLI fixture log.
	var detailed struct {
		LatestContinuation *struct {
			Number      int    `json:"number"`
			ContainerID string `json:"container_id"`
		} `json:"latest_continuation"`
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		detailResp := httptest.NewRecorder()
		detailReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
		server.ServeHTTP(detailResp, detailReq)
		if detailResp.Code != http.StatusOK {
			t.Fatalf("expected task detail status 200, got %d with body %s", detailResp.Code, detailResp.Body.String())
		}
		if err := json.NewDecoder(detailResp.Body).Decode(&detailed); err != nil {
			t.Fatalf("decode task detail: %v", err)
		}
		if detailed.LatestContinuation != nil && detailed.LatestContinuation.Number == 2 && detailed.LatestContinuation.ContainerID == "ctr-2" {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("expected durable Continuation 2 with container ctr-2, got %s", detailResp.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCreateTaskRecordsLoopbackRewriteEvent(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"recon http://127.0.0.1:3000 and find the score board",
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
		"type":"pentest","goal":"recon http://127.0.0.1:3000",
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
		"type":"pentest","goal":"enumerate example.com only",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	events := getTaskEvents(t, server, projectID, taskID)
	if findTargetRewriteEvent(events) != nil {
		t.Fatalf("expected no target_rewrite event for loopback-free goal, got events: %#v", events)
	}
}
