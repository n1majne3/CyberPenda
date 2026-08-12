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
	if !strings.Contains(evaluation.Task.Goal, "TSecBench") {
		t.Fatalf("Task Goal = %q", evaluation.Task.Goal)
	}
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
