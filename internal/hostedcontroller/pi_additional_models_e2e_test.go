package hostedcontroller_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/daemon"
	"pentest/internal/hostedcontroller"
	"pentest/internal/runtime"
)

// This test drives the same real application graph as the accepted Hosted
// Evaluation Run seam: a real daemon, real Host Runner launch, real Project
// Credential Bindings, and the production pre-transaction Global Model
// Provider projection. Only the external Pi bridge, the model gateways, and
// TSecBench are controlled boundaries.
func TestHostedPIAdditionalModelsCompleteTheRealProjectionAndLaunchPath(t *testing.T) {
	const (
		parentModel    = "accepted-parent-model"
		inheritedModel = "accepted-inherited-model"
		secondModel    = "accepted-second-model"
		parentKey      = "accepted-parent-key"
		secondKey      = "accepted-second-key"
		parentGateway  = "http://accepted-model.tsecbench.gw/v1"
		secondGateway  = "http://second-model.tsecbench.gw/v1"
	)

	var modelCalls struct {
		mu      sync.Mutex
		bearers map[string]int
	}
	modelCalls.bearers = map[string]int{}
	secondGatewayHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		modelCalls.mu.Lock()
		modelCalls.bearers[request.Header.Get("Authorization")]++
		modelCalls.mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"choices":[{"message":{"role":"assistant","content":"second gateway replied"}}]}`)
	})

	starter := &additionalModelsPiStarter{
		done: make(chan additionalModelsPiResult, 1),
		want: additionalModelsPiExpectation{
			parentModel: parentModel, inheritedModel: inheritedModel, secondModel: secondModel,
			parentGateway: parentGateway, secondGateway: secondGateway,
			parentKey: parentKey, secondKey: secondKey,
		},
		secondGateway: secondGatewayHandler,
		modelBearers:  &modelCalls.bearers,
		modelMutex:    &modelCalls.mu,
	}
	root := t.TempDir()
	factory := daemon.NewProductionProviderSessionFactory(daemon.ProductionProviderSessionFactoryConfig{
		HostBridgeCommand: "/accepted/fake-pi-wire-bridge",
		HostStarter:       starter,
	})
	server, err := daemon.NewServer(daemon.Config{
		Version: "hosted-additional-models", DBPath: filepath.Join(root, "pentest.db"),
		RuntimeRoot: filepath.Join(root, "runs"), ProviderSessionFactory: factory,
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := server.Close(); err != nil {
			t.Errorf("close real hosted application graph: %v", err)
		}
	}()
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL:       "http://accepted-daemon.test",
		Client:        &http.Client{Transport: hostedSkillRoundTripper{handler: server}},
		RuntimeBinary: "/accepted/fake-pi",
		PollPeriod:    time.Millisecond,
	})

	config := hostedcontroller.Config{
		BenchmarkBaseURL: "http://accepted-platform.test", BenchmarkToken: acceptedBenchmarkToken,
		Runtime: "pi", ModelProtocol: "openai_chat_completions",
		ModelBaseURL: parentGateway, Model: parentModel, ModelAPIKey: parentKey,
		ContextWindow: 1048576, MaxOutputTokens: 393216,
		PIAdditionalModels: []hostedcontroller.PIAdditionalModel{
			{Slot: 1, Model: inheritedModel, Protocol: "openai_chat_completions", BaseURL: parentGateway, APIKey: parentKey},
			{Slot: 2, Model: secondModel, Protocol: "openai_chat_completions", BaseURL: secondGateway, APIKey: secondKey},
		},
	}
	run, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config))
	if err != nil {
		t.Fatalf("start real hosted application graph: %v", err)
	}
	stopPath := "/api/projects/" + run.ProjectID + "/tasks/" + run.TaskID + "/stop"
	defer func() {
		deadline := time.Now().Add(2 * time.Second)
		for {
			request := httptest.NewRequest(http.MethodPost, stopPath, nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code == http.StatusOK || response.Code == http.StatusAccepted || time.Now().After(deadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	var result additionalModelsPiResult
	select {
	case result = <-starter.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the fake Pi Runtime")
	}
	if result.err != nil {
		t.Fatalf("fake Pi Runtime failed: %v", result.err)
	}
	if !result.parentModelLaunched || result.turnModel != parentModel {
		t.Fatalf("parent session model = %q (parent launched=%v, provider %q), want %q", result.turnModel, result.parentModelLaunched, result.turnProvider, parentModel)
	}
	if !result.secondGatewayCalled {
		t.Fatal("the additional provider model call never reached its own gateway")
	}
	modelCalls.mu.Lock()
	bearers := make(map[string]int, len(modelCalls.bearers))
	for key, count := range modelCalls.bearers {
		bearers[key] = count
	}
	modelCalls.mu.Unlock()
	if bearers["Bearer "+secondKey] < 1 {
		t.Fatalf("second gateway bearers = %#v, want the additional key", bearers)
	}
	if bearers["Bearer "+parentKey] < 1 {
		t.Fatalf("parent gateway bearers = %#v, want the parent key", bearers)
	}
}

type additionalModelsPiExpectation struct {
	parentModel, inheritedModel, secondModel string
	parentGateway, secondGateway             string
	parentKey, secondKey                     string
}

type additionalModelsPiResult struct {
	err                 error
	turnModel           string
	turnProvider        string
	parentModelLaunched bool
	secondGatewayCalled bool
}

// projectedPiProvider is one provider entry in the task-local Pi models.json.
type projectedPiProvider struct {
	BaseURL string `json:"baseUrl"`
	API     string `json:"api"`
	APIKey  string `json:"apiKey"`
	Models  []struct {
		ID            string
		ContextWindow int
		MaxTokens     int
	}
}

type additionalModelsPiStarter struct {
	want          additionalModelsPiExpectation
	done          chan additionalModelsPiResult
	once          sync.Once
	secondGateway http.Handler
	modelBearers  *map[string]int
	modelMutex    *sync.Mutex
}

func (starter *additionalModelsPiStarter) Start(_ context.Context, spec runtime.HostProcessSpec) (runtime.HostProcessHandle, error) {
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	diagnosticsR, diagnosticsW := io.Pipe()
	go starter.serve(spec, inputR, outputW)
	return runtime.HostProcessHandle{
		IO: runtime.SandboxBridgeIO{
			Stdin: inputW, Stdout: outputR, Diagnostics: diagnosticsR,
			Wait: func() error { return nil },
		},
		ProcessGroupID: 42421,
		KillProcessGroup: func(context.Context) error {
			_ = inputR.Close()
			_ = outputW.Close()
			_ = diagnosticsW.Close()
			return nil
		},
	}, nil
}

// serve answers the bridge protocol until the daemon closes the pipe. It
// never exits while the daemon may still write: a dead pipe would block the
// synchronous task-creation handshake. Protocol failures are recorded in the
// result instead of stopping the loop.
func (starter *additionalModelsPiStarter) serve(spec runtime.HostProcessSpec, input io.Reader, output io.Writer) {
	result := additionalModelsPiResult{}
	defer func() { starter.once.Do(func() { starter.done <- result }) }()
	if spec.Program != "/accepted/fake-pi-wire-bridge" || !containsAcceptedArgs(spec.Args, "--provider", "pi") {
		result.err = fmt.Errorf("Production Provider Session Factory did not launch the Pi wire bridge")
		_, _ = io.Copy(io.Discard, input)
		return
	}
	if !containsArg(spec.Args, "--approve") {
		result.err = fmt.Errorf("Pi --approve trust flag disappeared from launch arguments: %v", spec.Args)
	}

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var request runtime.SandboxBridgeRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		responseResult := map[string]any{"ok": true}
		switch request.Method {
		case "pi/get_state":
			responseResult = map[string]any{"session_id": "additional-models-pi-session", "session_path": filepath.Join(spec.Env["PI_CODING_AGENT_SESSION_DIR"], "accepted.jsonl")}
		case "pi/set_model":
			// Runtime Turn Selection: the parent session model arrives as an
			// RPC before the first prompt, not as a launch argument.
			var params struct {
				Provider string `json:"provider"`
				Model    string `json:"modelId"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Model == "" {
				var alt struct {
					Model string `json:"model"`
				}
				_ = json.Unmarshal(request.Params, &alt)
				params.Model = alt.Model
			}
			result.turnModel = params.Model
			result.turnProvider = params.Provider
		case "pi/prompt":
			responseResult = map[string]any{"session_id": "additional-models-pi-session", "turn_id": request.ID, "status": "started"}
		}
		writeAcceptedRPCResponse(output, request.ID, responseResult)
		if request.Method != "pi/prompt" {
			continue
		}
		if result.turnModel != starter.want.parentModel {
			result.err = fmt.Errorf("parent session model selection = %q (provider %q), want %q", result.turnModel, result.turnProvider, starter.want.parentModel)
		}
		if err := starter.verifyProjection(spec); err != nil {
			result.err = err
		} else {
			result.parentModelLaunched = true
			if err := starter.callModels(spec); err != nil {
				result.err = err
			} else {
				result.secondGatewayCalled = true
			}
		}
		status := "completed"
		if result.err != nil {
			status = "failed"
		}
		writeAcceptedEvent(output, "pi/agent_end", map[string]any{
			"session_id": "additional-models-pi-session", "turn_id": request.ID, "status": status,
		})
		starter.once.Do(func() { starter.done <- result })
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		result.err = err
	}
}

// verifyProjection inspects the real Config Projection output: task-local
// models.json, auth.json, the Pi settings file, and the launch environment.
func (starter *additionalModelsPiStarter) verifyProjection(spec runtime.HostProcessSpec) error {
	agentDir := spec.Env["PI_CODING_AGENT_DIR"]
	if !filepath.IsAbs(agentDir) {
		agentDir = filepath.Clean(filepath.Join(spec.Workdir, agentDir))
	}
	rawModels, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if err != nil {
		return fmt.Errorf("read real Pi model projection: %w", err)
	}
	var modelsDoc struct {
		Providers map[string]projectedPiProvider `json:"providers"`
	}
	if err := json.Unmarshal(rawModels, &modelsDoc); err != nil {
		return fmt.Errorf("decode real Pi model projection: %w", err)
	}
	var parentProvider, secondProvider projectedPiProvider
	parentKeyEnv, secondKeyEnv := "", ""
	for _, entry := range modelsDoc.Providers {
		switch entry.BaseURL {
		case starter.want.parentGateway:
			parentProvider = entry
			parentKeyEnv = strings.TrimPrefix(entry.APIKey, "$")
		case starter.want.secondGateway:
			secondProvider = entry
			secondKeyEnv = strings.TrimPrefix(entry.APIKey, "$")
		}
	}
	if parentProvider.BaseURL == "" {
		return fmt.Errorf("parent provider missing from Pi projection: %s", rawModels)
	}
	if secondProvider.BaseURL == "" {
		return fmt.Errorf("additional provider missing from Pi projection: %s", rawModels)
	}
	if !containsModel(parentProvider.Models, starter.want.parentModel) || !containsModel(parentProvider.Models, starter.want.inheritedModel) {
		return fmt.Errorf("parent provider models = %#v, want the parent and inherited model", parentProvider.Models)
	}
	if len(secondProvider.Models) != 1 || secondProvider.Models[0].ID != starter.want.secondModel {
		return fmt.Errorf("additional provider models = %#v", secondProvider.Models)
	}
	for _, entry := range append(parentProvider.Models, secondProvider.Models...) {
		if entry.ContextWindow != 1048576 || entry.MaxTokens != 393216 {
			return fmt.Errorf("model %s limits = %d/%d, want 1048576/393216", entry.ID, entry.ContextWindow, entry.MaxTokens)
		}
	}
	if parentProvider.API != "openai-completions" || secondProvider.API != "openai-completions" {
		return fmt.Errorf("projected protocol APIs = %q / %q", parentProvider.API, secondProvider.API)
	}
	if spec.Env[parentKeyEnv] != starter.want.parentKey || spec.Env[secondKeyEnv] != starter.want.secondKey {
		names := make([]string, 0, len(spec.Env))
		for name := range spec.Env {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("projected credential env missing keys: parent env %q second env %q; env names = %v", parentKeyEnv, secondKeyEnv, names)
	}

	rawAuth, err := os.ReadFile(filepath.Join(agentDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("read real Pi auth projection: %w", err)
	}
	if !strings.Contains(string(rawAuth), starter.want.parentKey) || !strings.Contains(string(rawAuth), starter.want.secondKey) {
		return fmt.Errorf("projected auth.json does not hold both provider keys: %s", redactKeys(string(rawAuth), starter.want.parentKey, starter.want.secondKey))
	}

	// Hosted never writes plugin selection from configuration.
	if rawSettings, err := os.ReadFile(filepath.Join(agentDir, "settings.json")); err == nil {
		var settings map[string]any
		if json.Unmarshal(rawSettings, &settings) == nil {
			if _, exists := settings["subagents"]; exists {
				return fmt.Errorf("projection wrote a subagents configuration: %s", rawSettings)
			}
		}
	}
	return nil
}

// callModels proves each projected provider serves requests on its own base
// URL with its own credential, through the projected environment.
func (starter *additionalModelsPiStarter) callModels(spec runtime.HostProcessSpec) error {
	agentDir := spec.Env["PI_CODING_AGENT_DIR"]
	if !filepath.IsAbs(agentDir) {
		agentDir = filepath.Clean(filepath.Join(spec.Workdir, agentDir))
	}
	rawModels, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if err != nil {
		return err
	}
	var modelsDoc struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rawModels, &modelsDoc); err != nil {
		return err
	}
	for _, entry := range modelsDoc.Providers {
		model := starter.want.parentModel
		key := starter.want.parentKey
		if entry.BaseURL == starter.want.secondGateway {
			model = starter.want.secondModel
			key = starter.want.secondKey
		}
		body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"verify additional model projection"}]}`, model)
		request, _ := http.NewRequest(http.MethodPost, entry.BaseURL+"/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		response := httptest.NewRecorder()
		if entry.BaseURL == starter.want.secondGateway {
			starter.secondGateway.ServeHTTP(response, request)
		} else {
			// The parent gateway accepts any bearer; record it for the test.
			starter.modelMutex.Lock()
			(*starter.modelBearers)[request.Header.Get("Authorization")]++
			starter.modelMutex.Unlock()
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"choices":[{"message":{"role":"assistant","content":"parent gateway replied"}}]}`)
		}
		if response.Code != http.StatusOK {
			return fmt.Errorf("model call to %s failed: HTTP %d", entry.BaseURL, response.Code)
		}
	}
	return nil
}

func containsArg(args []string, arg string) bool {
	for _, value := range args {
		if value == arg {
			return true
		}
	}
	return false
}

func containsModel(models []struct {
	ID            string
	ContextWindow int
	MaxTokens     int
}, modelID string) bool {
	for _, model := range models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

func redactKeys(text string, keys ...string) string {
	for _, key := range keys {
		text = strings.ReplaceAll(text, key, "[REDACTED]")
	}
	return text
}
