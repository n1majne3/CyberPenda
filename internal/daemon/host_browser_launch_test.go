package daemon_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"pentest/internal/daemon"
)

func TestTaskLaunchCapturesExplicitHostBrowserEnablement(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default_off", true: "explicit_on"}[enabled], func(t *testing.T) {
			server := newDaemon(t)
			projectID := createProject(t, server, `{"name":"Browser test","scope":{"domains":["example.test"]}}`)
			profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
			input := map[string]any{"type": "pentest", "goal": "inspect example.test", "runtime_profile_id": profileID, "runner": "sandbox"}
			if enabled {
				input["host_browser_enabled"] = true
			}
			body, _ := json.Marshal(input)
			request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader(body))
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("launch: %d %s", response.Code, response.Body.String())
			}
			var created struct {
				ID            string `json:"id"`
				Runner        string `json:"runner"`
				Configuration struct {
					HostBrowserEnabled bool `json:"host_browser_enabled"`
				} `json:"runtime_configuration"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if created.Configuration.HostBrowserEnabled != enabled || created.Runner != "sandbox" {
				t.Fatalf("explicit browser choice must not change Runner: %s", response.Body.String())
			}
			waitForTaskStatus(t, server, projectID, created.ID, "completed")
			response = httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID, nil))
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if created.Configuration.HostBrowserEnabled != enabled {
				t.Fatalf("Continuation lost browser choice: %s", response.Body.String())
			}
		})
	}
}

func TestHostBrowserMCPHelper(t *testing.T) {
	if os.Getenv("CYBERPENDA_TEST_BROWSER_HELPER") != "1" {
		return
	}
	if path := os.Getenv("CYBERPENDA_TEST_BROWSER_STARTS"); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = file.WriteString("started\n")
		_ = file.Close()
	}
	port := ""
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(2)
	}
	_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pid": os.Getpid(), "args": os.Args, "automatic_token": os.Getenv("PLAYWRIGHT_MCP_EXTENSION_TOKEN") != ""})
	}))
	os.Exit(0)
}

func TestTaskHostBrowserServiceStopsAndResumeUsesCapturedChoice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test process fixture")
	}
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(root, "playwright-mcp")
	if err := os.WriteFile(mcp, []byte("#!/bin/sh\nexec "+strconv.Quote(executable)+" -test.run=^TestHostBrowserMCPHelper$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(root, "codex")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"browser-runtime-ready\"}}'\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CYBERPENDA_TEST_BROWSER_HELPER", "1")
	startsPath := filepath.Join(root, "mcp-starts")
	t.Setenv("CYBERPENDA_TEST_BROWSER_STARTS", startsPath)
	t.Setenv("PLAYWRIGHT_MCP_EXTENSION_TOKEN", "must-not-auto-authorize")
	server := newDaemonWithConfig(t, daemon.Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true,
		HostBrowserMCPBinary: mcp, HostBrowserExecutable: "/usr/bin/true"})
	projectID := createProject(t, server, `{"name":"Browser lifecycle","scope":{"domains":["example.test"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex fixture","provider":"codex","fields":{"binary_path":`+quoteJSON(provider)+`,"model":"test"}}`)
	body := []byte(`{"type":"pentest","goal":"inspect example.test","runtime_profile_id":` + quoteJSON(profileID) + `,"runner":"host","host_browser_enabled":true,"run_controls":{"host_activated":true,"blackboard_mode":"disabled"}}`)
	preflight := httptest.NewRecorder()
	server.ServeHTTP(preflight, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body)))
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), `"name":"host_browser","status":"pass"`) {
		t.Fatalf("Host Browser Preflight: %d %s", preflight.Code, preflight.Body.String())
	}
	defaultResponse := httptest.NewRecorder()
	server.ServeHTTP(defaultResponse, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", strings.NewReader(strings.Replace(string(body), `,"host_browser_enabled":true`, "", 1))))
	var defaultTask struct{ ID string }
	if defaultResponse.Code != http.StatusCreated || json.Unmarshal(defaultResponse.Body.Bytes(), &defaultTask) != nil {
		t.Fatalf("default launch: %d %s", defaultResponse.Code, defaultResponse.Body.String())
	}
	waitForEventText(t, server, projectID, defaultTask.ID, "browser-runtime-ready")
	if _, err := os.Stat(startsPath); !os.IsNotExist(err) {
		t.Fatalf("Preflight or default-off Task started host MCP: %v", err)
	}
	defaultStop := httptest.NewRecorder()
	server.ServeHTTP(defaultStop, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+defaultTask.ID+"/stop", nil))
	if defaultStop.Code != http.StatusOK {
		t.Fatalf("default Task stop: %d %s", defaultStop.Code, defaultStop.Body.String())
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("launch: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	waitForEventText(t, server, projectID, created.ID, "browser-runtime-ready")
	configPath := filepath.Join(root, "runs", created.ID, "runtime-home", "codex", "config.toml")
	readEndpoint := func() string {
		t.Helper()
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "url = \"http://127.0.0.1:") {
				return strings.Trim(strings.TrimPrefix(line, "url = "), "\"")
			}
		}
		t.Fatalf("Host Browser missing from native config: %s", data)
		return ""
	}
	endpoint := readEndpoint()
	checkService := func(endpoint string) {
		t.Helper()
		resp, err := http.Get(strings.TrimSuffix(endpoint, "/mcp") + "/fixture")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var state struct {
			Args           []string `json:"args"`
			AutomaticToken bool     `json:"automatic_token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
			t.Fatal(err)
		}
		if state.AutomaticToken || !strings.Contains(strings.Join(state.Args, " "), "--extension --browser msedge") {
			t.Fatalf("unexpected Host Browser launch: %#v", state)
		}
	}
	checkService(endpoint)
	stop := func() {
		t.Helper()
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("stop: %d %s", resp.Code, resp.Body.String())
		}
	}
	stop()
	if resp, err := http.Get(endpoint); err == nil {
		resp.Body.Close()
		t.Fatal("Stop left the host service running")
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/resume", strings.NewReader(`{}`)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", response.Code, response.Body.String())
	}
	waitForTaskStatus(t, server, projectID, created.ID, "running")
	detail := httptest.NewRecorder()
	server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID, nil))
	var resumed struct {
		Configuration struct {
			HostBrowserEnabled bool `json:"host_browser_enabled"`
		} `json:"runtime_configuration"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &resumed); err != nil || !resumed.Configuration.HostBrowserEnabled {
		t.Fatalf("Resume lost captured Host Browser choice: %s (%v)", detail.Body.String(), err)
	}
	endpoint = readEndpoint()
	checkService(endpoint)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.Get(endpoint); err == nil {
		resp.Body.Close()
		t.Fatal("Daemon shutdown left the host service running")
	}
}

func TestHostBrowserPreflightChecksOnlyExplicitEnablement(t *testing.T) {
	root := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, HostBrowserMCPBinary: filepath.Join(root, "not-installed")})
	projectID := createProject(t, server, `{"name":"Browser Preflight","scope":{"domains":["example.test"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex","fields":{"binary_path":"/usr/bin/true","model":"test"}}`)
	for _, enabled := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"runtime_profile_id": profileID, "runner": "host", "host_browser_enabled": enabled,
			"run_controls": map[string]any{"host_activated": true}})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body)))
		var result struct {
			Checks []struct{ Name, Status, Detail string }
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatalf("Preflight: %d %s", response.Code, response.Body.String())
		}
		found := false
		for _, check := range result.Checks {
			if check.Name == "host_browser" {
				found = true
				if check.Status != "fail" || !strings.Contains(check.Detail, "playwright-mcp") {
					t.Fatalf("missing MCP binary must fail Preflight: %s", response.Body.String())
				}
			}
		}
		if found != enabled {
			t.Fatalf("Preflight checked a non-selected capability: %s", response.Body.String())
		}
	}
}

func TestSessionHostBrowserPreflightChecksExplicitEnablement(t *testing.T) {
	root := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, HostBrowserMCPBinary: filepath.Join(root, "not-installed")})
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex","fields":{"binary_path":"/usr/bin/true","model":"test"}}`)
	for _, enabled := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"runtime_profile_id": profileID, "runner": "host", "host_activated": true, "host_browser_enabled": enabled})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/preflight", bytes.NewReader(body)))
		if response.Code != http.StatusOK {
			t.Fatalf("Preflight: %d %s", response.Code, response.Body.String())
		}
		found := strings.Contains(response.Body.String(), `"name":"host_browser","status":"fail"`)
		if found != enabled {
			t.Fatalf("Session Preflight must check only explicit enablement: %s", response.Body.String())
		}
	}
}
