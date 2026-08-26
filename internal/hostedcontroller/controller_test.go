package hostedcontroller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentest/internal/hostedcontroller"
)

type hostedApp struct {
	started []hostedcontroller.HostedEvaluationBootstrap
	waited  []hostedcontroller.HostedEvaluationReference
}

func (app *hostedApp) Start(_ context.Context, evaluation hostedcontroller.HostedEvaluationBootstrap) (hostedcontroller.HostedEvaluationReference, error) {
	app.started = append(app.started, evaluation)
	return hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, nil
}

func (app *hostedApp) Wait(ctx context.Context, run hostedcontroller.HostedEvaluationReference, _ io.Writer, _ []string) error {
	app.waited = append(app.waited, run)
	<-ctx.Done()
	return nil
}

func TestHostedControllerRejectsInvalidConfigurationBeforeBootstrap(t *testing.T) {
	var stdout, stderr bytes.Buffer
	env := map[string]string{
		"BENCHMARK_BASE_URL":        "http://benchmark.tsecbench.gw/openapi/v1",
		"BENCHMARK_TOKEN":           "benchmark-secret",
		"CYBERPENDA_MODEL_PROTOCOL": "openai_responses",
		"CYBERPENDA_MODEL_BASE_URL": "http://model.tsecbench.gw/v1/responses",
		"CYBERPENDA_MODEL":          "hosted-model",
		"CYBERPENDA_MODEL_API_KEY":  "model-secret",
	}

	err := hostedcontroller.Run(context.Background(), t.TempDir(), env, &stdout, &stderr)
	if err == nil {
		t.Fatal("Run error = nil, want invalid Hosted Model Configuration")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty JSONL stream", stdout.String())
	}
	for _, secret := range []string{"benchmark-secret", "model-secret"} {
		if strings.Contains(stderr.String(), secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("diagnostic disclosed %q", secret)
		}
	}
	if !strings.Contains(stderr.String(), "hosted configuration is invalid") {
		t.Fatalf("stderr = %q, want non-sensitive validation diagnostic", stderr.String())
	}
}

func TestHostedControllerStartsOnePiEvaluationAndOnlyObservesIt(t *testing.T) {
	env := map[string]string{
		"BENCHMARK_BASE_URL":        "http://benchmark.tsecbench.gw/openapi/v1",
		"BENCHMARK_TOKEN":           "benchmark-secret",
		"CYBERPENDA_MODEL_PROTOCOL": "openai_chat_completions",
		"CYBERPENDA_MODEL_BASE_URL": "http://model.tsecbench.gw/v1",
		"CYBERPENDA_MODEL":          "hosted-model",
		"CYBERPENDA_MODEL_API_KEY":  "model-secret",
	}
	app := &hostedApp{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer

	if err := hostedcontroller.RunWithApp(ctx, env, app, &stdout, &stderr); err != nil {
		t.Fatalf("RunWithApp error = %v", err)
	}
	if len(app.started) != 1 || len(app.waited) != 1 {
		t.Fatalf("starts=%d waits=%d, want one of each", len(app.started), len(app.waited))
	}
	evaluation := app.started[0]
	if evaluation.Project.Kind != "ctf_challenge" || evaluation.Task.Type != "ctf_challenge" {
		t.Fatalf("Project kind=%q Task type=%q", evaluation.Project.Kind, evaluation.Task.Type)
	}
	if evaluation.Task.Runner != "host" || !evaluation.Task.HostActivated {
		t.Fatalf("Task runner=%q host_activated=%v", evaluation.Task.Runner, evaluation.Task.HostActivated)
	}
	if !strings.Contains(evaluation.Project.ScopeNotes, "ephemeral target addresses returned") {
		t.Fatalf("Scope notes = %q, want Platform-Issued Scope", evaluation.Project.ScopeNotes)
	}
	if evaluation.Runtime.Provider != "pi" || evaluation.Runtime.Env["BENCHMARK_BASE_URL"] != env["BENCHMARK_BASE_URL"] {
		t.Fatalf("Runtime = %#v", evaluation.Runtime)
	}
	if evaluation.Runtime.Credentials["BENCHMARK_TOKEN"] != env["BENCHMARK_TOKEN"] || evaluation.Runtime.ModelAPIKey != env["CYBERPENDA_MODEL_API_KEY"] {
		t.Fatal("Runtime did not receive direct hosted credentials")
	}
	if evaluation.Task.Goal != hostedcontroller.HostedTaskGoal {
		t.Fatalf("Task Goal = %q", evaluation.Task.Goal)
	}
	if evaluation.Runtime.ReasoningEffort != "" {
		t.Fatalf("missing Reasoning Effort should stay empty, got %q", evaluation.Runtime.ReasoningEffort)
	}
}

func TestHostedConfigurationAcceptsOptionalReasoningEffortAndTaskGoalAppendix(t *testing.T) {
	env := validHostedEnv()
	env["CYBERPENDA_REASONING_EFFORT"] = "max"
	env["CYBERPENDA_TASK_GOAL_APPENDIX"] = "Prefer easy Benchmark Challenges first."

	config, err := hostedcontroller.ConfigFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if config.ReasoningEffort != "max" {
		t.Fatalf("Reasoning Effort = %q, want max", config.ReasoningEffort)
	}
	if config.TaskGoalAppendix != "Prefer easy Benchmark Challenges first." {
		t.Fatalf("Task Goal appendix = %q", config.TaskGoalAppendix)
	}

	evaluation := hostedcontroller.EvaluationForConfig(config)
	if evaluation.Runtime.ReasoningEffort != "max" {
		t.Fatalf("bootstrap Reasoning Effort = %q, want max", evaluation.Runtime.ReasoningEffort)
	}
	wantGoal := hostedcontroller.HostedTaskGoal + "\n\nPrefer easy Benchmark Challenges first."
	if evaluation.Task.Goal != wantGoal {
		t.Fatalf("Task Goal = %q, want required sentence plus appendix", evaluation.Task.Goal)
	}
}

func TestHostedConfigurationAcceptsOptionalCompactThresholdAndMaxOutputTokens(t *testing.T) {
	env := validHostedEnv()
	env["CYBERPENDA_AUTO_COMPACT_THRESHOLD"] = "80"
	env["CYBERPENDA_AUTO_COMPACT_WINDOW"] = "786432"
	env["CYBERPENDA_MAX_OUTPUT_TOKENS"] = "393216"
	env["CYBERPENDA_CONTEXT_WINDOW"] = "1048576"

	config, err := hostedcontroller.ConfigFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if config.AutoCompactThreshold != 80 {
		t.Fatalf("Auto Compact Threshold = %d, want 80", config.AutoCompactThreshold)
	}
	if config.AutoCompactWindow != 786432 {
		t.Fatalf("Auto Compact Window = %d, want 786432", config.AutoCompactWindow)
	}
	if config.MaxOutputTokens != 393216 {
		t.Fatalf("Max Output Tokens = %d, want 393216", config.MaxOutputTokens)
	}
	if config.ContextWindow != 1048576 {
		t.Fatalf("Context Window = %d, want 1048576", config.ContextWindow)
	}

	evaluation := hostedcontroller.EvaluationForConfig(config)
	if evaluation.Runtime.Env["CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"] != "80" {
		t.Fatalf("compact env = %#v, want CLAUDE_AUTOCOMPACT_PCT_OVERRIDE=80", evaluation.Runtime.Env)
	}
	if evaluation.Runtime.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "786432" {
		t.Fatalf("compact window env = %#v, want CLAUDE_CODE_AUTO_COMPACT_WINDOW=786432", evaluation.Runtime.Env)
	}
	if evaluation.Runtime.Env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != "393216" {
		t.Fatalf("max output env = %#v, want CLAUDE_CODE_MAX_OUTPUT_TOKENS=393216", evaluation.Runtime.Env)
	}
	if evaluation.Runtime.MaxOutputTokens != 393216 || evaluation.Runtime.ContextWindow != 1048576 {
		t.Fatalf("catalog overrides = output %d window %d", evaluation.Runtime.MaxOutputTokens, evaluation.Runtime.ContextWindow)
	}
	if evaluation.Runtime.Env["BENCHMARK_BASE_URL"] != env["BENCHMARK_BASE_URL"] {
		t.Fatalf("BENCHMARK_BASE_URL was dropped: %#v", evaluation.Runtime.Env)
	}
}

func TestHostedConfigurationOmitsCompactAndMaxOutputEnvWhenUnset(t *testing.T) {
	config, err := hostedcontroller.ConfigFromEnv(validHostedEnv())
	if err != nil {
		t.Fatal(err)
	}
	if config.AutoCompactThreshold != 0 || config.AutoCompactWindow != 0 || config.MaxOutputTokens != 0 || config.ContextWindow != 0 {
		t.Fatalf("omitted knobs = threshold %d window %d output %d, want 0", config.AutoCompactThreshold, config.AutoCompactWindow, config.MaxOutputTokens)
	}
	evaluation := hostedcontroller.EvaluationForConfig(config)
	if _, ok := evaluation.Runtime.Env["CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"]; ok {
		t.Fatalf("unexpected compact env: %#v", evaluation.Runtime.Env)
	}
	if _, ok := evaluation.Runtime.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; ok {
		t.Fatalf("unexpected compact window env: %#v", evaluation.Runtime.Env)
	}
	if _, ok := evaluation.Runtime.Env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"]; ok {
		t.Fatalf("unexpected max output env: %#v", evaluation.Runtime.Env)
	}
}

func TestHostedConfigurationRejectsInvalidCompactThresholdAndMaxOutputTokens(t *testing.T) {
	t.Run("threshold not integer", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_AUTO_COMPACT_THRESHOLD"] = "0.8"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted non-integer Auto Compact Threshold")
		}
	})
	t.Run("threshold below one", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_AUTO_COMPACT_THRESHOLD"] = "0"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted Auto Compact Threshold 0")
		}
	})
	t.Run("threshold above 100", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_AUTO_COMPACT_THRESHOLD"] = "101"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted Auto Compact Threshold 101")
		}
	})
	t.Run("window not integer", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_AUTO_COMPACT_WINDOW"] = "768k"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted non-integer Auto Compact Window")
		}
	})
	t.Run("window zero", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_AUTO_COMPACT_WINDOW"] = "0"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted Auto Compact Window 0")
		}
	})
	t.Run("window above context", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_AUTO_COMPACT_WINDOW"] = "1048577"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted Auto Compact Window above 1048576")
		}
	})
	t.Run("max output not integer", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_MAX_OUTPUT_TOKENS"] = "128k"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted non-integer Max Output Tokens")
		}
	})
	t.Run("max output zero", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_MAX_OUTPUT_TOKENS"] = "0"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted Max Output Tokens 0")
		}
	})
	t.Run("max output above context", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_MAX_OUTPUT_TOKENS"] = "1048577"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted Max Output Tokens above 1048576")
		}
	})
}

func TestHostedConfigurationRejectsInvalidReasoningEffortAndTaskGoalAppendix(t *testing.T) {
	t.Run("unknown effort", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_REASONING_EFFORT"] = "auto"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted unknown Reasoning Effort")
		}
	})
	t.Run("nul appendix", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_TASK_GOAL_APPENDIX"] = "keep\x00going"
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted NUL Task Goal appendix")
		}
	})
	t.Run("oversized appendix", func(t *testing.T) {
		env := validHostedEnv()
		env["CYBERPENDA_TASK_GOAL_APPENDIX"] = strings.Repeat("a", hostedcontroller.MaxHostedTaskGoalAppendix+1)
		if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
			t.Fatal("accepted oversized Task Goal appendix")
		}
	})
}

func TestHTTPAppCreatesOneHostedProjectAndMatchingHostTask(t *testing.T) {
	var projectCreates, taskCreates int
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "PUT /api/skills/tsecbench-hosted-challenge-loop":
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/model-providers":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"hosted-model","api_key_env":"HOSTED_MODEL_API_KEY"}`)
		case "POST /api/projects":
			projectCreates++
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["kind"] != "ctf_challenge" {
				t.Errorf("Project kind = %v", body["kind"])
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"project-1"}`)
		case "POST /api/runtime-profiles":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"profile-1"}`)
		case "PUT /api/projects/project-1/credential-bindings":
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/projects/project-1/tasks":
			taskCreates++
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			controls, _ := body["run_controls"].(map[string]any)
			if body["type"] != "ctf_challenge" || body["runner"] != "host" || controls["host_activated"] != true {
				t.Errorf("Task request = %#v", body)
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"task-1"}`)
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	})
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})}

	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", Client: client})
	config, err := hostedcontroller.ConfigFromEnv(validHostedEnv())
	if err != nil {
		t.Fatal(err)
	}
	run, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config))
	if err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if run.ProjectID != "project-1" || run.TaskID != "task-1" || projectCreates != 1 || taskCreates != 1 {
		t.Fatalf("run=%#v Project creates=%d Task creates=%d", run, projectCreates, taskCreates)
	}
}

func TestHTTPAppProjectsHostedReasoningEffortAndAppendedTaskGoal(t *testing.T) {
	var profileRequest, taskRequest map[string]any
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "PUT /api/skills/tsecbench-hosted-challenge-loop":
			_, _ = io.WriteString(response, `{}`)
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
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/projects/project-1/tasks":
			_ = json.NewDecoder(request.Body).Decode(&taskRequest)
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"task-1"}`)
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	})
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})}

	env := validHostedEnv()
	env["CYBERPENDA_REASONING_EFFORT"] = "xhigh"
	env["CYBERPENDA_TASK_GOAL_APPENDIX"] = "Close a completed challenge before starting another."
	config, err := hostedcontroller.ConfigFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", Client: client})
	if _, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config)); err != nil {
		t.Fatal(err)
	}
	fields, _ := profileRequest["fields"].(map[string]any)
	if fields["reasoning_effort"] != "xhigh" {
		t.Fatalf("Runtime Profile Reasoning Effort = %#v, want xhigh", fields["reasoning_effort"])
	}
	if taskRequest["goal"] != hostedcontroller.HostedTaskGoal+"\n\nClose a completed challenge before starting another." {
		t.Fatalf("Task Goal = %#v", taskRequest["goal"])
	}
}

func TestHTTPAppProjectsHostedCompactThresholdAndMaxOutputTokens(t *testing.T) {
	var profileRequest map[string]any
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "PUT /api/skills/tsecbench-hosted-challenge-loop":
			_, _ = io.WriteString(response, `{}`)
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
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/projects/project-1/tasks":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"task-1"}`)
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	})
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})}

	env := validHostedEnv()
	env["CYBERPENDA_AUTO_COMPACT_THRESHOLD"] = "80"
	env["CYBERPENDA_AUTO_COMPACT_WINDOW"] = "786432"
	env["CYBERPENDA_MAX_OUTPUT_TOKENS"] = "393216"
	config, err := hostedcontroller.ConfigFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", Client: client})
	if _, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config)); err != nil {
		t.Fatal(err)
	}
	fields, _ := profileRequest["fields"].(map[string]any)
	profileEnv, _ := fields["env"].(map[string]any)
	if profileEnv["CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"] != "80" {
		t.Fatalf("Runtime Profile compact env = %#v, want 80", profileEnv)
	}
	if profileEnv["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "786432" {
		t.Fatalf("Runtime Profile compact window env = %#v, want 786432", profileEnv)
	}
	if profileEnv["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != "393216" {
		t.Fatalf("Runtime Profile max output env = %#v, want 393216", profileEnv)
	}
	if profileEnv["BENCHMARK_BASE_URL"] != env["BENCHMARK_BASE_URL"] {
		t.Fatalf("Runtime Profile dropped BENCHMARK_BASE_URL: %#v", profileEnv)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func validHostedEnv() map[string]string {
	return map[string]string{
		"BENCHMARK_BASE_URL": "http://benchmark.tsecbench.gw/openapi/v1", "BENCHMARK_TOKEN": "token",
		"CYBERPENDA_MODEL_PROTOCOL": "openai_chat_completions", "CYBERPENDA_MODEL_BASE_URL": "http://model.tsecbench.gw/v1",
		"CYBERPENDA_MODEL": "model", "CYBERPENDA_MODEL_API_KEY": "key",
	}
}
