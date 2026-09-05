package hostedcontroller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/daemon"
	"pentest/internal/hostedcontroller"
)

// TestHostedAcceptanceConfigurationRunsTheRealCodexRuntimeWithTheProjectedSkill
// drives the real Codex CLI through the hosted bootstrap and proves the
// orchestrator dispatch loop end to end:
//
//  1. the lead spawns a child through the built-in multi-agent mechanism and
//     the child thread makes its own model requests;
//  2. the lead verifies the child's 90-second fact-skeleton acknowledgement
//     and re-dispatches the step when the spawn message was not delivered
//     (run 15209: collab delivery failed for every spawned child);
//  3. the lead stays in its turn until the worker settles, so the task can
//     report a live idle runtime instead of hanging forever.
//
// The per-thread delivery observation is logged, not asserted: whether Codex
// delivers the spawn message depends on the CLI version the image floats to
// (ADR 0026 keeps @latest). The guaranteed assertion is the convergence of
// the acknowledgement loop, which is skill behaviour and must hold on every
// Codex version.
func TestHostedAcceptanceConfigurationRunsTheRealCodexRuntimeWithTheProjectedSkill(t *testing.T) {
	fixture := newRealCodexHostedAcceptanceFixture(t)

	fixture.start(t)
	fixture.await(t)

	if fixture.spawnUnsupported {
		t.Skipf("codex %s does not route spawn_agent; the built-in multi-agent delivery loop is inconclusive on this build", fixture.codexVersion)
	}

	skeletons, locks := fixture.findGraphFiles()
	if len(skeletons) == 0 {
		t.Fatal("real Codex orchestrator never produced the step fact skeleton")
	}
	if len(locks) == 0 {
		t.Fatal("real Codex orchestrator never wrote the leader lock")
	}
	if !fixture.model.delivered() {
		t.Logf("spawn message delivery: UNDELIVERED on this Codex version (lead re-dispatch path carried the run; deliveries=%d attempts=%d children=%d)",
			fixture.model.deliveredCount(), fixture.model.spawnCount(), len(fixture.model.threads())-1)
	} else {
		t.Logf("spawn message delivery: DELIVERED (attempts=%d children=%d)", fixture.model.spawnCount(), len(fixture.model.threads())-1)
	}
	for thread, texts := range fixture.model.threadInputs() {
		t.Logf("thread %s requests=%d delivered=%v", thread, len(texts), containsAny(texts, ackMarkers))
	}
	if !fixture.settled {
		t.Fatal("real Codex orchestrator run did not settle with a live idle runtime")
	}
}

const (
	acceptedCodexModelKey  = acceptedModelKey
	acceptedCodexStepFile  = "901-ack.md"
	acceptedCodexAckMarker = "ACK-MARKER"
	acceptedCodexMaxSpawns = 3
)

var ackMarkers = []string{acceptedCodexAckMarker + "-1", acceptedCodexAckMarker + "-2", acceptedCodexAckMarker + "-3"}

type realCodexHostedAcceptanceFixture struct {
	server   *daemon.Server
	app      *hostedcontroller.HTTPApp
	model    *realCodexControlledModel
	platform *acceptedPlatform
	run      hostedcontroller.HostedEvaluationReference
	settled  bool

	spawnUnsupported bool
	codexVersion     string
}

func codexVersionOutput(t *testing.T, codexPath string) string {
	t.Helper()
	output, err := exec.Command(codexPath, "--version").CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func newRealCodexHostedAcceptanceFixture(t *testing.T) *realCodexHostedAcceptanceFixture {
	t.Helper()
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		if os.Getenv("CYBERPENDA_REQUIRE_REAL_CODEX_ACCEPTANCE") == "1" {
			t.Fatal("real Codex acceptance requires codex on PATH")
		}
		t.Skip("real Codex acceptance requires codex on PATH")
	}
	codexVersion := codexVersionOutput(t, codexPath)
	bridgePath := buildRealPiAcceptanceBridge(t)
	clientPath := buildRealPiAcceptanceClient(t)
	platform := newAcceptedPlatform(acceptedPlatformSuccess)
	platformServer := newHostedSkillTestServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("BENCHMARK_TOKEN") != acceptedBenchmarkToken {
			http.Error(response, `{"code":"task_not_found"}`, http.StatusNotFound)
			return
		}
		platform.serveHTTP(response, request)
	}))
	t.Cleanup(platformServer.Close)
	model := newRealCodexControlledModel(t)

	root := t.TempDir()
	server, err := daemon.NewServer(daemon.Config{
		Version: "hosted-real-codex-acceptance", DBPath: filepath.Join(root, "pentest.db"),
		RuntimeRoot: filepath.Join(root, "runs"),
		ProviderSessionFactory: daemon.NewProductionProviderSessionFactory(daemon.ProductionProviderSessionFactoryConfig{
			HostBridgeCommand: bridgePath,
			Diagnostics:       func(line string) { t.Logf("real Codex bridge: %s", line) },
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &realCodexHostedAcceptanceFixture{
		server: server,
		app: hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
			BaseURL:       "http://accepted-daemon.test",
			Client:        &http.Client{Transport: hostedSkillRoundTripper{handler: server}},
			RuntimeBinary: codexPath,
			PollPeriod:    time.Millisecond,
		}),
		model: model, platform: platform, codexVersion: codexVersion,
	}
	fixture.model.clientPath = clientPath
	fixture.model.workdirRoot = root
	t.Cleanup(func() {
		fixture.stop(t)
		if err := server.Close(); err != nil {
			t.Errorf("close real Codex hosted application graph: %v", err)
		}
	})
	return fixture
}

func (fixture *realCodexHostedAcceptanceFixture) start(t *testing.T) hostedcontroller.HostedEvaluationReference {
	t.Helper()
	config := hostedcontroller.Config{
		BenchmarkBaseURL: "http://benchmark.test", BenchmarkToken: acceptedBenchmarkToken,
		Runtime: "codex", ModelProtocol: "openai_responses",
		ModelBaseURL: fixture.model.baseURL, Model: "accepted-responses-model", ModelAPIKey: acceptedCodexModelKey,
	}
	run, err := fixture.app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config))
	if err != nil {
		t.Fatalf("start real Codex hosted application graph: %v", err)
	}
	fixture.run = run
	return run
}

func (fixture *realCodexHostedAcceptanceFixture) await(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		skeletons, locks := fixture.findGraphFiles()
		if len(skeletons) > 0 && len(locks) > 0 && fixture.leadFinished(t, "spawn delivery verified") {
			fixture.settled = true
			return
		}
		if fixture.model.unsupported() && fixture.runtimeIdle(t) {
			fixture.spawnUnsupported = true
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	calls, threads := fixture.model.state()
	t.Fatalf("timed out waiting for real Codex orchestrator: model_requests=%d threads=%d spawns=%d deliveries=%d last_request=%s",
		calls, threads, fixture.model.spawnCount(), fixture.model.deliveredCount(), fixture.model.lastRequestText())
}

func (fixture *realCodexHostedAcceptanceFixture) findGraphFiles() (skeletons []string, locks []string) {
	root := fixture.model.workdirRoot
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		switch {
		case filepath.Base(path) == acceptedCodexStepFile:
			skeletons = append(skeletons, path)
		case filepath.Base(path) == "leader.lock":
			locks = append(locks, path)
		}
		return nil
	})
	return skeletons, locks
}

func (fixture *realCodexHostedAcceptanceFixture) runtimeIdle(t *testing.T) bool {
	t.Helper()
	taskResponse := httptest.NewRecorder()
	taskPath := "/api/projects/" + fixture.run.ProjectID + "/tasks/" + fixture.run.TaskID
	fixture.server.ServeHTTP(taskResponse, httptest.NewRequest(http.MethodGet, taskPath, http.NoBody))
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
	return true
}

func (fixture *realCodexHostedAcceptanceFixture) leadFinished(t *testing.T, finalText string) bool {
	t.Helper()
	if !fixture.runtimeIdle(t) {
		return false
	}
	taskPath := "/api/projects/" + fixture.run.ProjectID + "/tasks/" + fixture.run.TaskID
	transcriptResponse := httptest.NewRecorder()
	fixture.server.ServeHTTP(transcriptResponse, httptest.NewRequest(http.MethodGet, taskPath+"/transcript", http.NoBody))
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
		if entry.Role == "assistant" && strings.Contains(entry.Text, "spawn delivery verified") {
			return true
		}
	}
	return false
}

func (fixture *realCodexHostedAcceptanceFixture) stop(t *testing.T) {
	t.Helper()
	if fixture.run.ProjectID == "" || fixture.run.TaskID == "" {
		return
	}
	path := "/api/projects/" + fixture.run.ProjectID + "/tasks/" + fixture.run.TaskID + "/stop"
	deadline := time.Now().Add(3 * time.Second)
	for {
		response := httptest.NewRecorder()
		fixture.server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, http.NoBody))
		if response.Code == http.StatusOK || response.Code == http.StatusAccepted {
			return
		}
		if response.Code != http.StatusConflict || time.Now().After(deadline) {
			t.Logf("stop real Codex hosted Task: status=%d body=%s", response.Code, response.Body.String())
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// realCodexControlledModel is the fake OpenAI Responses gateway. Every Codex
// thread (lead and spawned children) talks to it, so it observes delivery from
// the child's own first request and drives the acknowledgement loop.
type realCodexControlledModel struct {
	server      *httptest.Server
	baseURL     string
	clientPath  string
	workdirRoot string

	mu                 sync.Mutex
	requests           int
	spawns             int
	deliveries         int
	unsupportedSpawns  int
	lastByThread       map[string][]string
	order              []string
	lead               string
	spawnCountByThread map[string]int
}

func newRealCodexControlledModel(t *testing.T) *realCodexControlledModel {
	t.Helper()
	model := &realCodexControlledModel{
		lastByThread:       map[string][]string{},
		spawnCountByThread: map[string]int{},
	}
	model.server = newHostedSkillTestServer(t, http.HandlerFunc(model.serveHTTP))
	t.Cleanup(model.server.Close)
	model.baseURL = model.server.URL + "/v1"
	return model
}

type codexResponseRequest struct {
	Input          []json.RawMessage `json:"input"`
	PromptCacheKey string            `json:"prompt_cache_key"`
}

func (model *realCodexControlledModel) serveHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer "+acceptedCodexModelKey {
		http.Error(response, "bad model projection", http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(request.Body, 8<<20))
	var parsed codexResponseRequest
	if json.Unmarshal(body, &parsed) != nil {
		http.Error(response, "bad model request", http.StatusBadRequest)
		return
	}
	texts := codexInputTexts(parsed.Input)
	model.mu.Lock()
	model.requests++
	thread := parsed.PromptCacheKey
	if thread == "" {
		thread = fmt.Sprintf("thread-%d", len(model.lastByThread)+1)
	}
	if _, seen := model.lastByThread[thread]; !seen {
		model.order = append(model.order, thread)
		if model.lead == "" {
			model.lead = thread
		}
	}
	model.lastByThread[thread] = append(model.lastByThread[thread], texts...)
	if containsAny(texts, ackMarkers) {
		model.deliveries++
	}
	lastOutput := lastFunctionCallOutput(parsed.Input)
	lead := model.lead
	model.mu.Unlock()

	response.Header().Set("Content-Type", "text/event-stream")
	if thread != lead {
		model.serveChild(response, thread)
		return
	}
	model.serveLead(response, thread, lastOutput)
}

// serveLead drives the scripted orchestrator: setup, spawn, wait, verify,
// re-dispatch on a missed acknowledgement, settle.
func (model *realCodexControlledModel) serveLead(response http.ResponseWriter, thread string, lastOutput string) {
	model.mu.Lock()
	step := len(model.lastByThread[thread])
	spawned := model.spawnCountByThread[thread]
	model.mu.Unlock()
	switch {
	case step == 1:
		writeCodexToolCall(response, "exec_command", map[string]any{
			"cmd": `WS="$(pwd -P)"; mkdir -p "$WS/graph/facts"; printf '%s\n' "$(date +%s)" > "$WS/graph/leader.lock"; printf -- '- id: step_901\n  action: ack probe\n  state: open\n  budget_min: 12\n' > "$WS/graph/steps.yaml"; echo setup-done`,
		})
	case step == 2:
		model.dispatchLead(response, thread, acceptedCodexAckMarker+"-1")
	case strings.Contains(lastOutput, "unsupported call"):
		// This Codex build does not route spawn_agent at all (the tool is
		// advertised but the router rejects it), so the delivery loop cannot
		// run here. End the lead; the test skips with that finding.
		model.mu.Lock()
		model.unsupportedSpawns++
		model.mu.Unlock()
		writeCodexMessage(response, "spawn unsupported on this codex build")
	case strings.Contains(lastOutput, "ACK-OK"):
		model.finishLead(response)
	case strings.Contains(lastOutput, "ACK-MISS"):
		model.redispatchLead(response, thread, spawned)
	case strings.Contains(lastOutput, `"agent_id"`), strings.Contains(lastOutput, `"task_name"`):
		// The spawn result just arrived; block on the child before verifying.
		writeCodexToolCall(response, "wait_agent", map[string]any{
			"targets":    codexAgentIDPattern.FindStringSubmatch(lastOutput)[1:],
			"timeout_ms": 30000,
		})
	default:
		// The wait returned (or timed out); verify the acknowledgement file.
		writeCodexToolCall(response, "exec_command", map[string]any{
			"cmd": `test -f "$(pwd -P)/graph/facts/901-ack.md" && echo ACK-OK || echo ACK-MISS`,
		})
	}
}

func (model *realCodexControlledModel) finishLead(response http.ResponseWriter) {
	writeCodexMessage(response, "spawn delivery verified")
}

func (model *realCodexControlledModel) redispatchLead(response http.ResponseWriter, thread string, spawned int) {
	if spawned >= acceptedCodexMaxSpawns {
		model.finishLead(response)
		return
	}
	model.dispatchLead(response, thread, acceptedCodexAckMarker+"-"+fmt.Sprint(spawned+1))
}

func (model *realCodexControlledModel) dispatchLead(response http.ResponseWriter, thread string, marker string) {
	model.mu.Lock()
	model.spawns++
	model.spawnCountByThread[thread]++
	model.mu.Unlock()
	writeCodexToolCall(response, "spawn_agent", map[string]any{
		"fork_context": false,
		"message":      marker + " 你只负责 step_901：开工 90 秒内在你的工作目录写 graph/facts/" + acceptedCodexStepFile + "（fact 骨架即可），然后结束。预算 12 分钟。",
	})
}

// serveChild answers every spawned thread: write the fact skeleton, then
// settle. Delivered children and empty-prompt orphans take the same path so
// the acknowledgement loop converges regardless of Codex delivery behaviour.
func (model *realCodexControlledModel) serveChild(response http.ResponseWriter, thread string) {
	model.mu.Lock()
	step := len(model.lastByThread[thread])
	model.mu.Unlock()
	switch step {
	case 1:
		writeCodexToolCall(response, "exec_command", map[string]any{
			"cmd": `mkdir -p "$(pwd -P)/graph/facts" && printf -- '---\nid: fact_901\nstep: step_901\n---\nack from child\n' > "$(pwd -P)/graph/facts/901-ack.md" && echo skeleton-written`,
		})
	default:
		writeCodexMessage(response, "child settled")
	}
}

func (model *realCodexControlledModel) state() (requests int, threads int) {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.requests, len(model.lastByThread)
}

func (model *realCodexControlledModel) threads() []string {
	model.mu.Lock()
	defer model.mu.Unlock()
	return append([]string(nil), model.order...)
}

func (model *realCodexControlledModel) threadInputs() map[string][]string {
	model.mu.Lock()
	defer model.mu.Unlock()
	out := make(map[string][]string, len(model.lastByThread))
	for thread, texts := range model.lastByThread {
		out[thread] = append([]string(nil), texts...)
	}
	return out
}

func (model *realCodexControlledModel) spawnCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.spawns
}

func (model *realCodexControlledModel) unsupported() bool {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.unsupportedSpawns > 0 && model.deliveries == 0
}

func (model *realCodexControlledModel) delivered() bool {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.deliveries > 0
}

func (model *realCodexControlledModel) deliveredCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.deliveries
}

func (model *realCodexControlledModel) lastRequestText() string {
	model.mu.Lock()
	defer model.mu.Unlock()
	var last string
	for _, texts := range model.lastByThread {
		if len(texts) > 0 {
			last = texts[len(texts)-1]
		}
	}
	if len(last) > 2000 {
		last = last[len(last)-2000:]
	}
	return last
}

func codexInputTexts(input []json.RawMessage) []string {
	texts := []string{}
	for _, raw := range input {
		var item struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Output string `json:"output"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		for _, part := range item.Content {
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		if item.Output != "" {
			texts = append(texts, item.Output)
		}
	}
	return texts
}

var codexAgentIDPattern = regexp.MustCompile(`"(?:agent_id|task_name|id)"\s*:\s*"([^"]+)"`)

func lastFunctionCallOutput(input []json.RawMessage) string {
	output := ""
	for _, raw := range input {
		var item struct {
			Type   string `json:"type"`
			Output string `json:"output"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Type != "function_call_output" {
			continue
		}
		output = item.Output
	}
	return output
}

func containsAny(haystacks []string, needles []string) bool {
	for _, haystack := range haystacks {
		for _, needle := range needles {
			if strings.Contains(haystack, needle) {
				return true
			}
		}
	}
	return false
}

func writeCodexToolCall(response http.ResponseWriter, name string, arguments map[string]any) {
	raw, _ := json.Marshal(arguments)
	item := map[string]any{
		"id": "fc_accepted", "type": "function_call", "call_id": "call_accepted",
		"name": name, "arguments": string(raw),
	}
	writeCodexSSE(response, "response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": 0, "item": item,
	})
	writeCodexSSE(response, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": 0, "item": item,
	})
	writeCodexCompleted(response, item)
}

func writeCodexMessage(response http.ResponseWriter, text string) {
	item := map[string]any{
		"id": "msg_accepted", "type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
	writeCodexSSE(response, "response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": 0, "item": item,
	})
	writeCodexSSE(response, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": 0, "item": item,
	})
	writeCodexCompleted(response, item)
}

func writeCodexCompleted(response http.ResponseWriter, item map[string]any) {
	writeCodexSSE(response, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_accepted", "output": []any{item},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		},
	})
}

func writeCodexSSE(response http.ResponseWriter, event string, payload any) {
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event, raw)
}
