package hostedcontroller_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"pentest/internal/hostedcontroller"
	"pentest/internal/skill"
)

func TestHostedEvaluationPublishesOnlyItsTSecBenchSkillAndProjectsBenchmarkEnvironment(t *testing.T) {
	var skillRequest struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Source      map[string]string `json:"source_provenance"`
		Files       map[string]string `json:"files"`
	}
	var profileRequest map[string]any
	bindings := map[string]map[string]any{}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "PUT /api/skills/tsecbench-hosted-challenge-loop":
			_ = json.NewDecoder(request.Body).Decode(&skillRequest)
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"tsecbench-hosted-challenge-loop"}`)
		case "POST /api/model-providers":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"hosted-model","api_key_env":"HOSTED_MODEL_API_KEY"}`)
		case "POST /api/projects":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"project-1"}`)
		case "POST /api/runtime-profiles":
			_ = json.NewDecoder(request.Body).Decode(&profileRequest)
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"profile-1"}`)
		case "PUT /api/projects/project-1/credential-bindings":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			bindings[body["credential_ref"].(string)] = body
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/projects/project-1/tasks":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"task-1"}`)
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test",
		Client:  &http.Client{Transport: hostedSkillRoundTripper{handler: handler}},
	})
	config := hostedcontroller.Config{
		BenchmarkBaseURL: "http://benchmark.test", BenchmarkToken: "benchmark-secret",
		Runtime: "pi", ModelProtocol: "openai_chat_completions", ModelBaseURL: "http://model.tsecbench.gw/v1",
		Model: "hosted-model", ModelAPIKey: "model-secret",
	}

	if _, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config)); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if skillRequest.Name != "tsecbench-hosted-challenge-loop" || skillRequest.Source["kind"] != "hosted" {
		t.Fatalf("hosted Skill metadata = %#v", skillRequest)
	}
	instruction := skillRequest.Files["SKILL.md"]
	for _, required := range []string{
		"pentest-tsecbench-client list", "pentest-tsecbench-client start", "pentest-tsecbench-client hint",
		"pentest-tsecbench-client submit", "pentest-tsecbench-client close", "pentest-tsecbench-client abandon",
		"correct_flag_count", "total_flag_count", "At most three", "hint_cost_radio", "duplicate", "invalid_state",
		"Never chain", "read-only Working Snapshot", "first pass", "third slot",
		"over_budget", "elapsed_min", "attempt_n", "challenge-clock.json",
		"Do not invent a longer budget", "CYBERPENDA_CHALLENGE_ADAPTER", "/data/adapters",
	} {
		if !strings.Contains(instruction, required) {
			t.Errorf("hosted Skill instruction missing %q", required)
		}
	}
	for _, forbidden := range []string{"curl ", "set -x", "curl -v", "--trace", "printenv", "benchmark-secret", "blackboard.json >", "> .pentest/blackboard.json"} {
		if strings.Contains(instruction, forbidden) {
			t.Errorf("hosted Skill contains forbidden pattern %q", forbidden)
		}
	}
	fields, _ := profileRequest["fields"].(map[string]any)
	env, _ := fields["env"].(map[string]any)
	if env["BENCHMARK_BASE_URL"] != config.BenchmarkBaseURL {
		t.Fatalf("Runtime Profile env = %#v", env)
	}
	refs, _ := fields["credential_refs"].([]any)
	if !reflect.DeepEqual(refs, []any{"BENCHMARK_TOKEN"}) {
		t.Fatalf("Runtime Profile credential_refs = %#v", refs)
	}
	tokenSource, _ := bindings["BENCHMARK_TOKEN"]["source"].(map[string]any)
	if tokenSource["destination_env"] != "BENCHMARK_TOKEN" || tokenSource["value"] != config.BenchmarkToken {
		t.Fatalf("BENCHMARK_TOKEN binding = %#v", bindings["BENCHMARK_TOKEN"])
	}
	modelSource, _ := bindings["HOSTED_MODEL_API_KEY"]["source"].(map[string]any)
	if modelSource["destination_env"] != "HOSTED_MODEL_API_KEY" || modelSource["value"] != config.ModelAPIKey {
		t.Fatalf("model binding = %#v", bindings["HOSTED_MODEL_API_KEY"])
	}
}

func TestTSecBenchSkillUsesSeparateGuardedOperations(t *testing.T) {
	instruction := captureHostedSkillInstruction(t)
	for _, forbiddenChain := range []string{
		"submit &&", "submit;", "submit\nclose", "close &&", "close;", "close\nstart",
	} {
		if strings.Contains(instruction, forbiddenChain) {
			t.Fatalf("hosted Skill contains unsafe operation chain %q", forbiddenChain)
		}
	}
}

func TestTSecBenchSkillTreatsClientFailureAsLocalAndRecoverable(t *testing.T) {
	instruction := captureHostedSkillInstruction(t)
	for _, required := range []string{
		"command failure affects only that command", "Do not exit the Runtime", "Do not automatically retry a mutation",
		"refresh with `pentest-tsecbench-client list`", "move to another challenge",
	} {
		if !strings.Contains(instruction, required) {
			t.Errorf("hosted Skill failure procedure missing %q", required)
		}
	}
}

func TestNormalBuiltinSkillCatalogDoesNotContainTheHostedTSecBenchSkill(t *testing.T) {
	bundles, err := skill.BuiltinBundles()
	if err != nil {
		t.Fatal(err)
	}
	for _, bundle := range bundles {
		if bundle.Metadata.ID == "tsecbench-hosted-challenge-loop" {
			t.Fatal("hosted-only Skill leaked into the normal built-in catalog")
		}
	}
}

type hostedSkillRoundTripper struct {
	handler http.Handler
}

func (transport hostedSkillRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	response := httptest.NewRecorder()
	transport.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

func captureHostedSkillInstruction(t *testing.T) string {
	t.Helper()
	var instruction string
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "PUT /api/skills/tsecbench-hosted-challenge-loop":
			var body struct {
				Files map[string]string `json:"files"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			instruction = body.Files["SKILL.md"]
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/model-providers":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"provider","api_key_env":"MODEL_API_KEY"}`)
		case "POST /api/projects":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"project"}`)
		case "POST /api/runtime-profiles":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"profile"}`)
		case "PUT /api/projects/project/credential-bindings":
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/projects/project/tasks":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"task"}`)
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test", Client: &http.Client{Transport: hostedSkillRoundTripper{handler: handler}},
	})
	config := hostedcontroller.Config{
		BenchmarkBaseURL: "http://benchmark.test", BenchmarkToken: "benchmark-secret", Runtime: "pi",
		ModelProtocol: "openai_chat_completions", ModelBaseURL: "http://model.tsecbench.gw/v1",
		Model: "model", ModelAPIKey: "model-secret",
	}
	if _, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config)); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if strings.TrimSpace(instruction) == "" {
		t.Fatal("hosted Skill instruction is empty")
	}
	return instruction
}

func newHostedSkillTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			if os.Getenv("CYBERPENDA_REQUIRE_REAL_PI_ACCEPTANCE") == "1" {
				t.Fatalf("real Pi acceptance requires the loopback fake-platform listener: %v", err)
			}
			t.Skipf("sandbox does not permit the loopback fake-platform listener: %v", err)
		}
		t.Fatalf("listen for fake TSecBench platform: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	return server
}
