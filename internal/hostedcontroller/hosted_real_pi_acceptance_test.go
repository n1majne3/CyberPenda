package hostedcontroller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/daemon"
	"pentest/internal/hostedcontroller"
)

func TestHostedAcceptanceConfigurationRunsTheRealPiRuntimeWithTheProjectedSkill(t *testing.T) {
	fixture := newRealPiHostedAcceptanceFixture(t)

	run := fixture.start(t)
	result := fixture.await(t)

	if !result.hostedSkillRead {
		t.Fatal("real Pi Runtime did not read the projected hosted-only TSecBench Skill")
	}
	if !result.modelCalled {
		t.Fatal("real Pi Runtime did not call the controlled openai_chat_completions model")
	}
	want := []string{
		"GET /openapi/v1/challenges",
		"POST /openapi/v1/challenges/start?unique_code=multi",
		"GET /openapi/v1/challenges/hint?unique_code=multi",
		"POST /openapi/v1/challenges/submit flag-one",
		"POST /openapi/v1/challenges/submit flag-two",
		"POST /openapi/v1/challenges/close?unique_code=multi",
	}
	if !equalAcceptedRequests(result.platformRequests, want) {
		t.Fatalf("real Pi Runtime challenge requests = %#v, want %#v", result.platformRequests, want)
	}
	if result.correctFlagCount != 2 {
		t.Fatalf("real Pi Runtime correct flag count = %d, want 2", result.correctFlagCount)
	}
	if !result.runtimeTurnSettled {
		t.Fatal("real Pi Runtime result returned before the Task reported a live idle turn and retained the final assistant message")
	}
	if run.ProjectID == "" || run.TaskID == "" {
		t.Fatalf("real Hosted Controller run reference = %#v", run)
	}
}

type realPiHostedAcceptanceFixture struct {
	server   *daemon.Server
	app      *hostedcontroller.HTTPApp
	model    *realPiControlledModel
	platform *acceptedPlatform
	run      hostedcontroller.HostedEvaluationReference
}

type realPiHostedAcceptanceResult struct {
	hostedSkillRead    bool
	modelCalled        bool
	platformRequests   []string
	correctFlagCount   int
	runtimeTurnSettled bool
}

func newRealPiHostedAcceptanceFixture(t *testing.T) *realPiHostedAcceptanceFixture {
	t.Helper()
	piPath, err := exec.LookPath("pi")
	if err != nil {
		if os.Getenv("CYBERPENDA_REQUIRE_REAL_PI_ACCEPTANCE") == "1" {
			t.Fatal("real Pi acceptance requires pi on PATH")
		}
		t.Skip("real Pi acceptance requires pi on PATH")
	}
	for _, program := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(program); err != nil {
			if os.Getenv("CYBERPENDA_REQUIRE_REAL_PI_ACCEPTANCE") == "1" {
				t.Fatalf("real Pi acceptance requires %s on PATH", program)
			}
			t.Skipf("real Pi acceptance requires %s on PATH", program)
		}
	}
	bridgePath := buildRealPiAcceptanceBridge(t)
	platform := newAcceptedPlatform(acceptedPlatformSuccess)
	platformServer := newHostedSkillTestServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("BENCHMARK_TOKEN") != acceptedBenchmarkToken {
			http.Error(response, `{"code":"task_not_found"}`, http.StatusNotFound)
			return
		}
		platform.serveHTTP(response, request)
	}))
	t.Cleanup(platformServer.Close)
	model := newRealPiControlledModel(t)

	root := t.TempDir()
	server, err := daemon.NewServer(daemon.Config{
		Version: "hosted-real-pi-acceptance", DBPath: filepath.Join(root, "pentest.db"),
		RuntimeRoot: filepath.Join(root, "runs"),
		ProviderSessionFactory: daemon.NewProductionProviderSessionFactory(daemon.ProductionProviderSessionFactoryConfig{
			HostBridgeCommand: bridgePath,
			Diagnostics:       func(line string) { t.Logf("real Pi bridge: %s", line) },
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &realPiHostedAcceptanceFixture{
		server: server,
		app: hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
			BaseURL:       "http://accepted-daemon.test",
			Client:        &http.Client{Transport: hostedSkillRoundTripper{handler: server}},
			RuntimeBinary: piPath,
			PollPeriod:    time.Millisecond,
		}),
		model: model, platform: platform,
	}
	fixture.model.baseURL = model.server.URL + "/v1"
	fixture.model.platformBaseURL = platformServer.URL
	t.Cleanup(func() {
		fixture.stop(t)
		if err := server.Close(); err != nil {
			t.Errorf("close real Pi hosted application graph: %v", err)
		}
	})
	return fixture
}

func (fixture *realPiHostedAcceptanceFixture) start(t *testing.T) hostedcontroller.HostedEvaluationReference {
	t.Helper()
	config := hostedcontroller.Config{
		BenchmarkBaseURL: fixture.model.platformBaseURL, BenchmarkToken: acceptedBenchmarkToken,
		Runtime: "pi", ModelProtocol: "openai_chat_completions",
		ModelBaseURL: fixture.model.baseURL, Model: "accepted-chat-model", ModelAPIKey: acceptedModelKey,
	}
	run, err := fixture.app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config))
	if err != nil {
		t.Fatalf("start real Pi hosted application graph: %v", err)
	}
	fixture.run = run
	return run
}

func (fixture *realPiHostedAcceptanceFixture) await(t *testing.T) realPiHostedAcceptanceResult {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		calls, read, finished := fixture.model.state()
		requests := fixture.platform.requestsCopy()
		correct := fixture.platform.correctCount()
		settled := false
		if finished && len(requests) == 6 && correct == 2 {
			settled = fixture.runtimeTurnSettled(t)
		}
		if settled {
			return realPiHostedAcceptanceResult{
				hostedSkillRead: read, modelCalled: calls > 0,
				platformRequests: requests, correctFlagCount: correct, runtimeTurnSettled: true,
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls, read, _ := fixture.model.state()
	t.Fatalf("timed out waiting for real Pi Runtime: model_calls=%d skill_read=%v requests=%#v last_model_request=%s", calls, read, fixture.platform.requestsCopy(), fixture.model.lastRequestText())
	return realPiHostedAcceptanceResult{}
}

func (fixture *realPiHostedAcceptanceFixture) runtimeTurnSettled(t *testing.T) bool {
	t.Helper()
	taskResponse := httptest.NewRecorder()
	taskPath := "/api/projects/" + fixture.run.ProjectID + "/tasks/" + fixture.run.TaskID
	fixture.server.ServeHTTP(taskResponse, httptest.NewRequest(http.MethodGet, taskPath, nil))
	if taskResponse.Code != http.StatusOK {
		return false
	}
	var taskState struct {
		RuntimeActivity struct {
			Liveness     string `json:"liveness"`
			TurnActivity string `json:"turn_activity"`
		} `json:"runtime_activity"`
	}
	if json.Unmarshal(taskResponse.Body.Bytes(), &taskState) != nil ||
		taskState.RuntimeActivity.Liveness != "live" || taskState.RuntimeActivity.TurnActivity != "idle" {
		return false
	}

	transcriptResponse := httptest.NewRecorder()
	fixture.server.ServeHTTP(transcriptResponse, httptest.NewRequest(http.MethodGet, taskPath+"/transcript", nil))
	if transcriptResponse.Code != http.StatusOK {
		return false
	}
	var page struct {
		Entries []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"entries"`
	}
	if json.Unmarshal(transcriptResponse.Body.Bytes(), &page) != nil {
		return false
	}
	for _, entry := range page.Entries {
		if entry.Role == "assistant" && strings.Contains(entry.Text, "all challenge flags complete") {
			return true
		}
	}
	return false
}

func (fixture *realPiHostedAcceptanceFixture) stop(t *testing.T) {
	t.Helper()
	if fixture.run.ProjectID == "" || fixture.run.TaskID == "" {
		return
	}
	path := "/api/projects/" + fixture.run.ProjectID + "/tasks/" + fixture.run.TaskID + "/stop"
	deadline := time.Now().Add(3 * time.Second)
	for {
		response := httptest.NewRecorder()
		fixture.server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code == http.StatusOK || response.Code == http.StatusAccepted {
			return
		}
		if response.Code != http.StatusConflict || time.Now().After(deadline) {
			t.Errorf("stop real Pi hosted Task: status=%d body=%s", response.Code, response.Body.String())
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func buildRealPiAcceptanceBridge(t *testing.T) string {
	t.Helper()
	if bridge := strings.TrimSpace(os.Getenv("CYBERPENDA_REAL_PI_ACCEPTANCE_BRIDGE")); bridge != "" {
		if info, err := os.Stat(bridge); err != nil || info.IsDir() {
			t.Fatalf("real Pi acceptance bridge %q is unavailable", bridge)
		}
		return bridge
	}
	repository := acceptanceRepositoryRoot(t)
	bridge := filepath.Join(t.TempDir(), "pentest-provider-bridge")
	command := exec.Command("go", "build", "-o", bridge, "./cmd/pentest-provider-bridge")
	command.Dir = repository
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "go-cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build real provider bridge: %v\n%s", err, output)
	}
	return bridge
}

func acceptanceRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("find repository root for real Pi acceptance")
		}
		directory = parent
	}
}

type realPiControlledModel struct {
	server          *httptest.Server
	baseURL         string
	platformBaseURL string

	mu          sync.Mutex
	calls       int
	skillRead   bool
	finished    bool
	lastRequest []byte
}

func newRealPiControlledModel(t *testing.T) *realPiControlledModel {
	t.Helper()
	model := &realPiControlledModel{}
	model.server = newHostedSkillTestServer(t, http.HandlerFunc(model.serveHTTP))
	t.Cleanup(model.server.Close)
	return model
}

func (model *realPiControlledModel) serveHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer "+acceptedModelKey {
		http.Error(response, "bad model projection", http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(request.Body, 4<<20))
	model.mu.Lock()
	model.calls++
	call := model.calls
	model.lastRequest = append([]byte(nil), body...)
	if bytes.Contains(body, []byte("The hosted platform supplies its network")) {
		model.skillRead = true
	}
	model.mu.Unlock()

	response.Header().Set("Content-Type", "text/event-stream")
	switch call {
	case 1:
		writeRealPiToolCall(response, "accepted-read", "read", map[string]any{
			"path": ".agents/skills/tsecbench-hosted-challenge-loop/SKILL.md",
		})
	case 2:
		writeRealPiToolCall(response, "accepted-bash", "bash", map[string]any{
			"command": realPiChallengeCommand(), "timeout": 10,
		})
	default:
		writeRealPiText(response, "TSecBench reports all challenge flags complete.")
		model.mu.Lock()
		model.finished = true
		model.mu.Unlock()
	}
}

func (model *realPiControlledModel) state() (calls int, skillRead, finished bool) {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls, model.skillRead, model.finished
}

func (model *realPiControlledModel) lastRequestText() string {
	model.mu.Lock()
	defer model.mu.Unlock()
	text := string(model.lastRequest)
	if len(text) > 4000 {
		text = text[len(text)-4000:]
	}
	return text
}

func writeRealPiToolCall(response io.Writer, id, name string, arguments map[string]any) {
	raw, _ := json.Marshal(arguments)
	writeRealPiSSE(response, map[string]any{
		"id": "chatcmpl-accepted", "object": "chat.completion.chunk", "created": 1, "model": "accepted-chat-model",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)},
			}},
		}, "finish_reason": nil}},
	})
	writeRealPiSSE(response, map[string]any{
		"id": "chatcmpl-accepted", "object": "chat.completion.chunk", "created": 1, "model": "accepted-chat-model",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
		"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
	_, _ = io.WriteString(response, "data: [DONE]\n\n")
}

func writeRealPiText(response io.Writer, content string) {
	writeRealPiSSE(response, map[string]any{
		"id": "chatcmpl-accepted-final", "object": "chat.completion.chunk", "created": 1, "model": "accepted-chat-model",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}, "finish_reason": nil}},
	})
	writeRealPiSSE(response, map[string]any{
		"id": "chatcmpl-accepted-final", "object": "chat.completion.chunk", "created": 1, "model": "accepted-chat-model",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
	_, _ = io.WriteString(response, "data: [DONE]\n\n")
}

func writeRealPiSSE(response io.Writer, payload any) {
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(response, "data: %s\n\n", raw)
}

func realPiChallengeCommand() string {
	return strings.TrimSpace(`
api_base="${BENCHMARK_BASE_URL%/}/openapi/v1/challenges"
curl --fail-with-body --silent --show-error --header "BENCHMARK_TOKEN: $BENCHMARK_TOKEN" "$api_base"
code=multi
curl --fail-with-body --silent --show-error --request POST --header "BENCHMARK_TOKEN: $BENCHMARK_TOKEN" --url "$api_base/start" --url-query "unique_code=$code"
curl --fail-with-body --silent --show-error --get --header "BENCHMARK_TOKEN: $BENCHMARK_TOKEN" --url "$api_base/hint" --url-query "unique_code=$code"
for flag in flag-one flag-two; do
  jq -n --arg code "$code" --arg flag "$flag" '{unique_code: $code, flag: $flag}' |
  curl --fail-with-body --silent --show-error --request POST --header "BENCHMARK_TOKEN: $BENCHMARK_TOKEN" --header "Content-Type: application/json" --data-binary @- "$api_base/submit"
done
curl --fail-with-body --silent --show-error --request POST --header "BENCHMARK_TOKEN: $BENCHMARK_TOKEN" --url "$api_base/close" --url-query "unique_code=$code"
`)
}

func equalAcceptedRequests(got, want []string) bool {
	return reflect.DeepEqual(got, want)
}
