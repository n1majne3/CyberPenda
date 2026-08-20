package hostedcontroller_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/daemon"
	"pentest/internal/hostedcontroller"
	"pentest/internal/runtime"
	"pentest/internal/tsecbenchclient"
)

// This is the accepted Hosted Evaluation Run reference seam. HTTPApp calls a
// real daemon application graph through an in-process HTTP transport because
// some test sandboxes forbid listeners. The real Production Provider Session
// Factory, Host Runner, Config Projection, Runtime Profile, credential binding,
// and hosted Skill projection remain active. Only the external Pi bridge,
// model endpoint, and TSecBench platform are controlled test boundaries.
func TestHostedAcceptanceConfigurationCompletesTheRuntimeManagedChallengeLoop(t *testing.T) {
	platform := newAcceptedPlatform(acceptedPlatformSuccess)
	fixture := newHostedAcceptanceFixture(t, platform)

	run := fixture.start(t)
	result := fixture.await(t)

	if result.err != nil {
		t.Fatalf("fake Pi Runtime loop failed: %v", result.err)
	}
	if !result.hostedSkillProjected {
		t.Fatal("hosted-only TSecBench Skill was not projected into the real Host Runner layout")
	}
	if result.modelAPI != "openai-completions" || !result.modelCalled {
		t.Fatalf("Hosted Acceptance Configuration model projection = API %q called=%v", result.modelAPI, result.modelCalled)
	}
	want := []string{
		"GET /openapi/v1/challenges",
		"POST /openapi/v1/challenges/start?unique_code=multi",
		"GET /openapi/v1/challenges/hint?unique_code=multi",
		"POST /openapi/v1/challenges/submit flag-one",
		"POST /openapi/v1/challenges/submit flag-two",
		"GET /openapi/v1/challenges",
		"POST /openapi/v1/challenges/close?unique_code=multi",
	}
	if !reflect.DeepEqual(result.platformRequests, want) {
		t.Fatalf("Runtime-managed challenge requests = %#v, want %#v", result.platformRequests, want)
	}
	if result.correctFlagCount != 2 {
		t.Fatalf("correct flag count = %d, want 2", result.correctFlagCount)
	}
	if run.ProjectID == "" || run.TaskID == "" {
		t.Fatalf("real Hosted Controller run reference = %#v", run)
	}
}

func TestHostedAcceptanceConfigurationMakesPlatformFailuresVisibleToTheRuntime(t *testing.T) {
	tests := []struct {
		name     string
		scenario acceptedPlatformScenario
		want     string
	}{
		{name: "malformed response", scenario: acceptedPlatformMalformed, want: "malformed JSON"},
		{name: "transport failure", scenario: acceptedPlatformTransportFailure, want: "request failed"},
		{name: "rate limit", scenario: acceptedPlatformRateLimited, want: "HTTP 429"},
		{name: "other client error", scenario: acceptedPlatformOtherClientError, want: "HTTP 422"},
		{name: "server error", scenario: acceptedPlatformServerError, want: "HTTP 500"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHostedAcceptanceFixture(t, newAcceptedPlatform(test.scenario))
			fixture.start(t)
			result := fixture.await(t)
			if result.err == nil || !strings.Contains(result.err.Error(), test.want) {
				t.Fatalf("Runtime-visible failure = %v, want %q", result.err, test.want)
			}
			if strings.Contains(result.err.Error(), acceptedBenchmarkToken) {
				t.Fatal("Runtime-visible failure disclosed BENCHMARK_TOKEN")
			}
		})
	}
}

const (
	acceptedBenchmarkToken = "accepted-one-use-benchmark-token"
	acceptedModelKey       = "accepted-dedicated-model-key"
)

type hostedAcceptanceFixture struct {
	server  *daemon.Server
	app     *hostedcontroller.HTTPApp
	starter *acceptedPiStarter
	run     hostedcontroller.HostedEvaluationReference
}

func newHostedAcceptanceFixture(t *testing.T, platform *acceptedPlatform) *hostedAcceptanceFixture {
	t.Helper()
	root := t.TempDir()
	starter := &acceptedPiStarter{platform: platform, done: make(chan acceptedPiResult, 1)}
	factory := daemon.NewProductionProviderSessionFactory(daemon.ProductionProviderSessionFactoryConfig{
		HostBridgeCommand: "/accepted/fake-pi-wire-bridge",
		HostStarter:       starter,
	})
	server, err := daemon.NewServer(daemon.Config{
		Version: "hosted-acceptance", DBPath: filepath.Join(root, "pentest.db"),
		RuntimeRoot: filepath.Join(root, "runs"), ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL:       "http://accepted-daemon.test",
		Client:        &http.Client{Transport: hostedSkillRoundTripper{handler: server}},
		RuntimeBinary: "/accepted/fake-pi",
		PollPeriod:    time.Millisecond,
	})
	fixture := &hostedAcceptanceFixture{server: server, app: app, starter: starter}
	t.Cleanup(func() {
		fixture.stop(t)
		if err := server.Close(); err != nil {
			t.Errorf("close real hosted application graph: %v", err)
		}
	})
	return fixture
}

func (fixture *hostedAcceptanceFixture) start(t *testing.T) hostedcontroller.HostedEvaluationReference {
	t.Helper()
	config := hostedcontroller.Config{
		BenchmarkBaseURL: "http://accepted-platform.test", BenchmarkToken: acceptedBenchmarkToken,
		Runtime: "pi", ModelProtocol: "openai_chat_completions",
		ModelBaseURL: "http://accepted-model.tsecbench.gw/v1", Model: "accepted-chat-model",
		ModelAPIKey: acceptedModelKey,
	}
	run, err := fixture.app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config))
	if err != nil {
		t.Fatalf("start real hosted application graph: %v", err)
	}
	fixture.run = run
	return run
}

func (fixture *hostedAcceptanceFixture) stop(t *testing.T) {
	t.Helper()
	if fixture.run.ProjectID == "" || fixture.run.TaskID == "" {
		return
	}
	path := "/api/projects/" + fixture.run.ProjectID + "/tasks/" + fixture.run.TaskID + "/stop"
	deadline := time.Now().Add(2 * time.Second)
	for {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		response := httptest.NewRecorder()
		fixture.server.ServeHTTP(response, request)
		if response.Code == http.StatusOK || response.Code == http.StatusAccepted {
			return
		}
		if response.Code != http.StatusConflict || time.Now().After(deadline) {
			t.Errorf("stop real hosted Task: status=%d body=%s", response.Code, response.Body.String())
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (fixture *hostedAcceptanceFixture) await(t *testing.T) acceptedPiResult {
	t.Helper()
	select {
	case result := <-fixture.starter.done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake Pi Runtime")
		return acceptedPiResult{}
	}
}

type acceptedPiResult struct {
	err                  error
	hostedSkillProjected bool
	modelAPI             string
	modelCalled          bool
	platformRequests     []string
	correctFlagCount     int
}

type acceptedPiStarter struct {
	platform *acceptedPlatform
	done     chan acceptedPiResult
	once     sync.Once
}

func (starter *acceptedPiStarter) Start(_ context.Context, spec runtime.HostProcessSpec) (runtime.HostProcessHandle, error) {
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	diagnosticsR, diagnosticsW := io.Pipe()
	go starter.serve(spec, inputR, outputW)
	return runtime.HostProcessHandle{
		IO: runtime.SandboxBridgeIO{
			Stdin: inputW, Stdout: outputR, Diagnostics: diagnosticsR,
			Wait: func() error { return nil },
		},
		ProcessGroupID: 42420,
		KillProcessGroup: func(context.Context) error {
			_ = inputR.Close()
			_ = outputW.Close()
			_ = diagnosticsW.Close()
			return nil
		},
	}, nil
}

func (starter *acceptedPiStarter) serve(spec runtime.HostProcessSpec, input io.Reader, output io.Writer) {
	result := acceptedPiResult{}
	defer func() { starter.once.Do(func() { starter.done <- result }) }()
	if spec.Program != "/accepted/fake-pi-wire-bridge" || !containsAcceptedArgs(spec.Args, "--provider", "pi") {
		result.err = fmt.Errorf("Production Provider Session Factory did not launch the Pi wire bridge")
		return
	}
	skillPath := filepath.Join(filepath.Dir(spec.Workdir), "skills", "tsecbench-hosted-challenge-loop", "SKILL.md")
	if instruction, err := os.ReadFile(skillPath); err == nil {
		result.hostedSkillProjected = bytes.Contains(instruction, []byte("pentest-tsecbench-client list"))
	}

	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		var request runtime.SandboxBridgeRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		responseResult := map[string]any{"ok": true}
		switch request.Method {
		case "pi/get_state":
			responseResult = map[string]any{"session_id": "accepted-pi-session", "session_path": filepath.Join(spec.Env["PI_CODING_AGENT_SESSION_DIR"], "accepted.jsonl")}
		case "pi/prompt":
			responseResult = map[string]any{"session_id": "accepted-pi-session", "turn_id": request.ID, "status": "started"}
		}
		writeAcceptedRPCResponse(output, request.ID, responseResult)
		if request.Method == "pi/get_state" {
			continue
		}
		if request.Method != "pi/prompt" {
			continue
		}
		agentDir := spec.Env["PI_CODING_AGENT_DIR"]
		if !filepath.IsAbs(agentDir) {
			agentDir = filepath.Clean(filepath.Join(spec.Workdir, agentDir))
		}
		modelsRaw, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
		if err != nil {
			result.err = fmt.Errorf("read real Pi model projection: %w", err)
			writeAcceptedEvent(output, "pi/agent_end", map[string]any{
				"session_id": "accepted-pi-session", "turn_id": request.ID, "status": "failed",
			})
			starter.once.Do(func() { starter.done <- result })
			continue
		}
		var models struct {
			Providers map[string]struct {
				BaseURL string `json:"baseUrl"`
				API     string `json:"api"`
				APIKey  string `json:"apiKey"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(modelsRaw, &models); err != nil {
			result.err = fmt.Errorf("decode real Pi model projection")
			writeAcceptedEvent(output, "pi/agent_end", map[string]any{
				"session_id": "accepted-pi-session", "turn_id": request.ID, "status": "failed",
			})
			starter.once.Do(func() { starter.done <- result })
			continue
		}
		var projected struct {
			BaseURL string
			API     string
			APIKey  string
		}
		for _, provider := range models.Providers {
			if provider.BaseURL == "http://accepted-model.tsecbench.gw/v1" {
				projected.BaseURL, projected.API, projected.APIKey = provider.BaseURL, provider.API, provider.APIKey
				break
			}
		}
		if projected.BaseURL == "" {
			result.err = fmt.Errorf("hosted Model Provider is absent from Pi projection")
			writeAcceptedEvent(output, "pi/agent_end", map[string]any{
				"session_id": "accepted-pi-session", "turn_id": request.ID, "status": "failed",
			})
			starter.once.Do(func() { starter.done <- result })
			continue
		}
		result.modelAPI = projected.API

		keyEnv := strings.TrimPrefix(projected.APIKey, "$")
		modelRequest, _ := http.NewRequest(http.MethodPost, projected.BaseURL+"/chat/completions", strings.NewReader(`{"model":"accepted-chat-model","messages":[{"role":"user","content":"execute hosted loop"}]}`))
		modelRequest.Header.Set("Authorization", "Bearer "+spec.Env[keyEnv])
		modelResponse := httptestResponse(starter.platform.modelHandler, modelRequest)
		if modelResponse.Code != http.StatusOK {
			result.err = fmt.Errorf("model call failed: HTTP %d", modelResponse.Code)
		} else {
			result.modelCalled = true
			result.err = starter.runChallengeLoop(spec.Env)
		}
		result.platformRequests = starter.platform.requestsCopy()
		result.correctFlagCount = starter.platform.correctCount()
		status := "completed"
		if result.err != nil {
			status = "failed"
		}
		writeAcceptedEvent(output, "pi/agent_end", map[string]any{
			"session_id": "accepted-pi-session", "turn_id": request.ID, "status": status,
		})
		starter.once.Do(func() { starter.done <- result })
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		result.err = err
	}
}

func (starter *acceptedPiStarter) runChallengeLoop(env map[string]string) error {
	client, err := tsecbenchclient.New(tsecbenchclient.Config{
		BaseURL: env["BENCHMARK_BASE_URL"], Token: env["BENCHMARK_TOKEN"],
		Client: &http.Client{Transport: acceptedPlatformTransport{platform: starter.platform}}, Timeout: time.Second,
	})
	if err != nil {
		return err
	}
	state, err := client.List(context.Background())
	if err != nil {
		return err
	}
	if len(state.Challenges) == 0 {
		return fmt.Errorf("empty challenge list")
	}
	code := state.Challenges[0].UniqueCode
	if _, err := client.Start(context.Background(), code); err != nil {
		return err
	}
	if _, err := client.Hint(context.Background(), code); err != nil {
		return err
	}
	if _, err := client.Submit(context.Background(), code, "flag-one"); err != nil {
		return err
	}
	if _, err := client.Submit(context.Background(), code, "flag-two"); err != nil {
		return err
	}
	_, err = client.Close(context.Background(), tsecbenchclient.CloseRequest{UniqueCode: code})
	return err
}

type acceptedPlatformTransport struct{ platform *acceptedPlatform }

func (transport acceptedPlatformTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	status, body, err := transport.platform.do(request)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request,
	}, nil
}

func writeAcceptedRPCResponse(output io.Writer, id string, result any) {
	_ = json.NewEncoder(output).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeAcceptedEvent(output io.Writer, method string, params any) {
	_ = json.NewEncoder(output).Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func containsAcceptedArgs(args []string, option, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == option && args[index+1] == value {
			return true
		}
	}
	return false
}

type acceptedPlatformScenario string

const (
	acceptedPlatformSuccess          acceptedPlatformScenario = "success"
	acceptedPlatformMalformed        acceptedPlatformScenario = "malformed"
	acceptedPlatformTransportFailure acceptedPlatformScenario = "transport"
	acceptedPlatformRateLimited      acceptedPlatformScenario = "rate_limited"
	acceptedPlatformOtherClientError acceptedPlatformScenario = "other_4xx"
	acceptedPlatformServerError      acceptedPlatformScenario = "server_error"
)

type acceptedPlatform struct {
	scenario     acceptedPlatformScenario
	mu           sync.Mutex
	requests     []string
	correct      int
	modelHandler http.Handler
}

func newAcceptedPlatform(scenario acceptedPlatformScenario) *acceptedPlatform {
	platform := &acceptedPlatform{scenario: scenario}
	platform.modelHandler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer "+acceptedModelKey {
			http.Error(response, "bad model projection", http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"choices":[{"message":{"role":"assistant","content":"use projected hosted Skill"}}]}`)
	})
	return platform
}

func (platform *acceptedPlatform) do(request *http.Request) (int, []byte, error) {
	if platform.scenario == acceptedPlatformTransportFailure {
		return 0, nil, errors.New("controlled transport failure")
	}
	if request.Header.Get("BENCHMARK_TOKEN") != acceptedBenchmarkToken {
		return http.StatusNotFound, []byte(`{"code":"task_not_found"}`), nil
	}
	if request.Method == http.MethodGet && request.URL.Path == "/openapi/v1/challenges" {
		switch platform.scenario {
		case acceptedPlatformMalformed:
			return http.StatusOK, []byte(`{not-json`), nil
		case acceptedPlatformRateLimited:
			return http.StatusTooManyRequests, []byte(`{"code":"rate_limited"}`), nil
		case acceptedPlatformOtherClientError:
			return http.StatusUnprocessableEntity, []byte(`{"code":"invalid_request"}`), nil
		case acceptedPlatformServerError:
			return http.StatusInternalServerError, []byte(`{"code":"internal_error"}`), nil
		}
	}
	recorder := httptestResponse(http.HandlerFunc(platform.serveHTTP), request)
	return recorder.Code, recorder.Body.Bytes(), nil
}

func (platform *acceptedPlatform) serveHTTP(response http.ResponseWriter, request *http.Request) {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	entry := request.Method + " " + request.URL.RequestURI()
	response.Header().Set("Content-Type", "application/json")
	switch request.Method + " " + request.URL.Path {
	case "GET /openapi/v1/challenges":
		platform.requests = append(platform.requests, entry)
		completed := platform.correct >= 2
		_, _ = fmt.Fprintf(response, `[{"unique_code":"multi","is_completed":%t,"container_status":"stopped","flag_count":2,"correct_flag_count":%d}]`, completed, platform.correct)
	case "POST /openapi/v1/challenges/start":
		platform.requests = append(platform.requests, entry)
		_, _ = io.WriteString(response, `{"container_addr":["10.0.0.2:31337"]}`)
	case "GET /openapi/v1/challenges/hint":
		platform.requests = append(platform.requests, entry)
		_, _ = io.WriteString(response, `{"hint":"score-costing hint"}`)
	case "POST /openapi/v1/challenges/submit":
		var input struct {
			Flag string `json:"flag"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		platform.correct++
		platform.requests = append(platform.requests, entry+" "+input.Flag)
		_, _ = fmt.Fprintf(response, `{"correct":true,"correct_flag_count":%d,"total_flag_count":2}`, platform.correct)
	case "POST /openapi/v1/challenges/close":
		platform.requests = append(platform.requests, entry)
		_, _ = io.WriteString(response, `{"closed":true}`)
	default:
		http.Error(response, `{"code":"not_found"}`, http.StatusNotFound)
	}
}

func (platform *acceptedPlatform) requestsCopy() []string {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	return append([]string(nil), platform.requests...)
}

func (platform *acceptedPlatform) correctCount() int {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	return platform.correct
}

type acceptedRecorder struct {
	HeaderMap http.Header
	Body      bytes.Buffer
	Code      int
}

func httptestResponse(handler http.Handler, request *http.Request) *acceptedRecorder {
	recorder := &acceptedRecorder{HeaderMap: make(http.Header), Code: http.StatusOK}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func (recorder *acceptedRecorder) Header() http.Header { return recorder.HeaderMap }
func (recorder *acceptedRecorder) Write(payload []byte) (int, error) {
	return recorder.Body.Write(payload)
}
func (recorder *acceptedRecorder) WriteHeader(status int) { recorder.Code = status }
