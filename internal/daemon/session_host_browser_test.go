package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"pentest/internal/daemon"
)

func TestSessionLaunchCapturesHostBrowserChoiceWithoutChangingRunner(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default_off", true: "explicit_on"}[enabled], func(t *testing.T) {
			server := newDaemon(t)
			profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
			input := map[string]any{"input": "verify browser selection", "runtime_profile_id": profileID, "runner": "sandbox"}
			if enabled {
				input["host_browser_enabled"] = true
			}
			body, _ := json.Marshal(input)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body)))
			var created struct {
				ID string
			}
			if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &created) != nil {
				t.Fatalf("Session launch: %d %s", response.Code, response.Body.String())
			}
			detail := httptest.NewRecorder()
			server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID, nil))
			var found struct {
				Configuration struct {
					HostBrowserEnabled bool `json:"host_browser_enabled"`
					Runner             string
				} `json:"runtime_configuration"`
			}
			if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &found) != nil || found.Configuration.HostBrowserEnabled != enabled || found.Configuration.Runner != "sandbox" {
				t.Fatalf("Session must capture browser choice without Host Runner activation: %s", detail.Body.String())
			}
		})
	}
}

func TestSessionHostBrowserServiceStopsAndResumeUsesCapturedChoice(t *testing.T) {
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
	server := newDaemonWithConfig(t, daemon.Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"), DisableBuiltinSkills: true,
		HostBrowserMCPBinary: mcp, HostBrowserExecutable: "/usr/bin/true"})
	profileID := createRuntimeProfile(t, server, `{"name":"Codex fixture","provider":"codex","fields":{"binary_path":`+quoteJSON(provider)+`,"model":"test"}}`)
	body := []byte(`{"input":"verify browser","runtime_profile_id":` + quoteJSON(profileID) + `,"runner":"host","host_activated":true,"host_browser_enabled":true,"run_controls":{"blackboard_mode":"disabled"}}`)
	preflight := httptest.NewRecorder()
	server.ServeHTTP(preflight, httptest.NewRequest(http.MethodPost, "/api/sessions/preflight", bytes.NewReader(body)))
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), `"name":"host_browser","status":"pass"`) {
		t.Fatalf("Host Browser Preflight: %d %s", preflight.Code, preflight.Body.String())
	}
	defaultResponse := httptest.NewRecorder()
	server.ServeHTTP(defaultResponse, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(strings.Replace(string(body), `,"host_browser_enabled":true`, "", 1))))
	var defaultSession struct{ ID string }
	if defaultResponse.Code != http.StatusCreated || json.Unmarshal(defaultResponse.Body.Bytes(), &defaultSession) != nil {
		t.Fatalf("default launch: %d %s", defaultResponse.Code, defaultResponse.Body.String())
	}
	waitForBrowserSession(t, server, defaultSession.ID, 1)
	if _, err := os.Stat(startsPath); !os.IsNotExist(err) {
		t.Fatalf("Preflight or default-off Session started host MCP: %v", err)
	}
	defaultStop := httptest.NewRecorder()
	server.ServeHTTP(defaultStop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+defaultSession.ID+"/stop", nil))
	if defaultStop.Code != http.StatusOK {
		t.Fatalf("default Session stop: %d %s", defaultStop.Code, defaultStop.Body.String())
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("launch: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	waitForBrowserSession(t, server, created.ID, 1)
	configPath := filepath.Join(root, "sessions", created.ID, ".runtime", "runtime-home", "codex", "config.toml")
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
		server.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
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
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/messages", strings.NewReader(`{"message":"resume browser verification","host_activated":true}`)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", response.Code, response.Body.String())
	}
	waitForBrowserSession(t, server, created.ID, 2)
	detail := httptest.NewRecorder()
	server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID, nil))
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

func waitForBrowserSession(t *testing.T, server *daemon.Server, id string, number int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id, nil))
		last = response.Body.String()
		var detail struct {
			Latest struct {
				Number int
				Status string
			} `json:"latest_continuation"`
		}
		if json.Unmarshal(response.Body.Bytes(), &detail) == nil && detail.Latest.Number == number && detail.Latest.Status == "running" {
			events := httptest.NewRecorder()
			server.ServeHTTP(events, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/events", nil))
			if strings.Contains(events.Body.String(), "browser-runtime-ready") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Session continuation %d did not run: %s", number, last)
}
