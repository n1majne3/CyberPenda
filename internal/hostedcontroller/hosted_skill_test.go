package hostedcontroller_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
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
		"/openapi/v1/challenges", "/start", "/hint", "/submit", "/close",
		"BENCHMARK_TOKEN: $BENCHMARK_TOKEN", "correct_flag_count", "total_flag_count",
		"At most three", "hint_cost_radio", "duplicate", "invalid_state", "HTTP 422", "HTTP 429",
		"transport errors", "HTTP 5xx", "malformed", "compatible request shape",
	} {
		if !strings.Contains(instruction, required) {
			t.Errorf("hosted Skill instruction missing %q", required)
		}
	}
	for _, block := range shellBlocks(instruction) {
		for _, forbidden := range []string{"set -x", "curl -v", "--trace", "printenv", "benchmark-secret"} {
			if strings.Contains(block, forbidden) {
				t.Errorf("hosted Skill shell example contains forbidden disclosure pattern %q: %s", forbidden, block)
			}
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

func TestTSecBenchSkillCurlExamplesWorkAgainstTheChallengePlatformContract(t *testing.T) {
	instruction := captureHostedSkillInstruction(t)
	blocks := shellBlocks(instruction)
	for _, marker := range []string{"api_base=", "\"$api_base\"", "$api_base/start", "$api_base/hint", "$api_base/submit", "$api_base/close"} {
		if shellBlockContaining(blocks, marker) == "" {
			t.Fatalf("hosted Skill has no shell example containing %q", marker)
		}
	}

	var mu sync.Mutex
	requests := []string{}
	accepted := map[string]bool{}
	active := 0
	platform := newHostedSkillTestServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("BENCHMARK_TOKEN") != "benchmark-secret" {
			t.Errorf("BENCHMARK_TOKEN header = %q", request.Header.Get("BENCHMARK_TOKEN"))
		}
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /openapi/v1/challenges":
			_, _ = io.WriteString(response, `[{"unique_code":"multi","description":"two flags","difficulty":"easy","level":1,"total_score":100,"flag_count":2,"correct_flag_count":0,"is_completed":false,"container_status":"stopped","container_addr":[]}]`)
		case "POST /openapi/v1/challenges/start":
			if request.URL.Query().Get("unique_code") != "multi" {
				t.Errorf("start query = %q", request.URL.RawQuery)
			}
			active++
			if active > 3 {
				t.Errorf("active challenge count = %d", active)
			}
			_, _ = io.WriteString(response, `{"unique_code":"multi","container_addr":["127.0.0.1:31337"]}`)
		case "GET /openapi/v1/challenges/hint":
			_, _ = io.WriteString(response, `{"unique_code":"multi","hint":"costly hint"}`)
		case "POST /openapi/v1/challenges/submit":
			var body struct {
				UniqueCode string `json:"unique_code"`
				Flag       string `json:"flag"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body.UniqueCode != "multi" {
				t.Errorf("submit body = %#v", body)
			}
			if accepted[body.Flag] {
				response.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(response, `{"code":"duplicate","message":"already accepted","detail":{}}`)
				return
			}
			accepted[body.Flag] = true
			count := len(accepted)
			_, _ = fmt.Fprintf(response, `{"correct":true,"awarded":50,"cumulative_score":%d,"correct_flag_count":%d,"total_flag_count":2,"matched_flag_index":%d}`, count*50, count, count-1)
		case "POST /openapi/v1/challenges/close":
			active--
			_, _ = io.WriteString(response, `{"unique_code":"multi","closed":true}`)
		default:
			http.Error(response, `{"code":"not_found"}`, http.StatusNotFound)
		}
	}))
	defer platform.Close()

	initBlock := shellBlockContaining(blocks, "api_base=")
	listBlock := shellBlockContaining(blocks, "\"$api_base\"")
	startBlock := shellBlockContaining(blocks, "$api_base/start")
	hintBlock := shellBlockContaining(blocks, "$api_base/hint")
	submitBlock := shellBlockContaining(blocks, "$api_base/submit")
	closeBlock := shellBlockContaining(blocks, "$api_base/close")
	script := strings.Join([]string{
		initBlock, listBlock, "code=multi", startBlock, hintBlock,
		"flag=flag-one", submitBlock, "flag=flag-one", submitBlock,
		"flag=flag-two", submitBlock, closeBlock,
	}, "\n")
	command := exec.Command("/bin/sh", "-c", script)
	command.Env = append(os.Environ(), "BENCHMARK_BASE_URL="+platform.URL, "BENCHMARK_TOKEN=benchmark-secret")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Skill curl examples failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `"code":"duplicate"`) || !strings.Contains(string(output), `"correct_flag_count":2`) {
		t.Fatalf("curl output did not preserve duplicate and multi-flag results: %s", output)
	}
	want := []string{
		"GET /openapi/v1/challenges", "POST /openapi/v1/challenges/start?unique_code=multi",
		"GET /openapi/v1/challenges/hint?unique_code=multi", "POST /openapi/v1/challenges/submit",
		"POST /openapi/v1/challenges/submit", "POST /openapi/v1/challenges/submit",
		"POST /openapi/v1/challenges/close?unique_code=multi",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("challenge request sequence = %#v, want %#v", requests, want)
	}
}

func TestTSecBenchSkillCurlMakesPlatformFailuresVisibleToTheRuntime(t *testing.T) {
	listBlock := shellBlockContaining(shellBlocks(captureHostedSkillInstruction(t)), "\"$api_base\"")
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"invalid token", http.StatusNotFound, `{"code":"task_not_found","message":"invalid token","detail":{}}`},
		{"invalid state", http.StatusConflict, `{"code":"invalid_state","message":"evaluation ended","detail":{}}`},
		{"other client error", http.StatusNotFound, `{"code":"challenge_not_found","message":"unknown code","detail":{}}`},
		{"rate limit", http.StatusTooManyRequests, `{"code":"rate_limited","message":"wait","detail":{}}`},
		{"server failure", http.StatusInternalServerError, `{"code":"internal_error","message":"retry","detail":{}}`},
		{"resource unavailable", http.StatusServiceUnavailable, `{"code":"resource_unavailable","message":"busy","detail":{}}`},
		{"malformed success", http.StatusOK, `{not-json`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := newHostedSkillTestServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Header.Get("BENCHMARK_TOKEN") != "benchmark-secret" {
					t.Errorf("BENCHMARK_TOKEN header = %q", request.Header.Get("BENCHMARK_TOKEN"))
				}
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))
			defer platform.Close()
			command := exec.Command("/bin/sh", "-c", `api_base="${BENCHMARK_BASE_URL%/}/openapi/v1/challenges"`+"\n"+listBlock)
			command.Env = append(os.Environ(), "BENCHMARK_BASE_URL="+platform.URL, "BENCHMARK_TOKEN=benchmark-secret")
			output, err := command.CombinedOutput()
			if test.status >= 400 && err == nil {
				t.Fatal("curl error = nil, want HTTP failure")
			}
			if test.status == http.StatusOK && err != nil {
				t.Fatalf("curl error = %v, want transport success", err)
			}
			if !strings.Contains(string(output), test.body) {
				t.Fatalf("curl output = %q, want response body %q", output, test.body)
			}
		})
	}

	t.Run("transport error", func(t *testing.T) {
		platform := newHostedSkillTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		baseURL := platform.URL
		platform.Close()
		command := exec.Command("/bin/sh", "-c", `api_base="${BENCHMARK_BASE_URL%/}/openapi/v1/challenges"`+"\n"+listBlock)
		command.Env = append(os.Environ(), "BENCHMARK_BASE_URL="+baseURL, "BENCHMARK_TOKEN=benchmark-secret")
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "curl:") {
			t.Fatalf("transport result error=%v output=%q", err, output)
		}
	})
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

func shellBlocks(markdown string) []string {
	var blocks []string
	for {
		start := strings.Index(markdown, "```sh\n")
		if start < 0 {
			return blocks
		}
		markdown = markdown[start+len("```sh\n"):]
		end := strings.Index(markdown, "\n```")
		if end < 0 {
			return blocks
		}
		blocks = append(blocks, markdown[:end])
		markdown = markdown[end+len("\n```"):]
	}
}

func shellBlockContaining(blocks []string, marker string) string {
	for _, block := range blocks {
		if strings.Contains(block, marker) {
			return block
		}
	}
	return ""
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
