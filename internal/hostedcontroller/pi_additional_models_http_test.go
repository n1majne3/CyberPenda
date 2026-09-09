package hostedcontroller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/hostedcontroller"
)

type recordedHostedEndpoint struct {
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
}

type recordedHostedCatalog struct {
	Manual       []string                               `json:"manual"`
	DefaultModel string                                 `json:"default_model"`
	Limits       map[string]recordedHostedCatalogLimits `json:"limits"`
}

type recordedHostedCatalogLimits struct {
	ContextWindow   int `json:"context_window"`
	MaxOutputTokens int `json:"max_output_tokens"`
}

// recordedHostedProvider captures one POST /api/model-providers body.
type recordedHostedProvider struct {
	Name      string                   `json:"name"`
	Endpoints []recordedHostedEndpoint `json:"endpoints"`
	Catalog   recordedHostedCatalog    `json:"catalog"`
}

type hostedBootstrapRecorder struct {
	providerRequests []recordedHostedProvider
	providerBodies   []string
	profileRequest   map[string]any
	taskRequest      map[string]any
	bindingRequests  []map[string]any
	taskCreates      int
	requestCount     int
	failProviderAt   int // 1-based ordinal of provider creation to fail
	failBindings     bool
}

func (recorder *hostedBootstrapRecorder) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		recorder.requestCount++
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "PUT /api/skills/ctf-orchestrator":
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/model-providers":
			raw, _ := io.ReadAll(request.Body)
			var providerRequest recordedHostedProvider
			if err := json.Unmarshal(raw, &providerRequest); err != nil {
				t.Errorf("decode provider request: %v", err)
			}
			recorder.providerRequests = append(recorder.providerRequests, providerRequest)
			recorder.providerBodies = append(recorder.providerBodies, string(raw))
			if recorder.failProviderAt == len(recorder.providerRequests) {
				http.Error(response, "controlled provider failure", http.StatusBadGateway)
				return
			}
			response.WriteHeader(http.StatusCreated)
			ordinal := len(recorder.providerRequests)
			fmt.Fprintf(response, `{"id":"hosted-provider-%d","api_key_env":"HOSTED_PROVIDER_%d_API_KEY"}`, ordinal, ordinal)
		case "POST /api/projects":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"project-1"}`)
		case "POST /api/runtime-profiles":
			_ = json.NewDecoder(request.Body).Decode(&recorder.profileRequest)
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"profile-1"}`)
		case "PUT /api/projects/project-1/credential-bindings":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			recorder.bindingRequests = append(recorder.bindingRequests, body)
			if recorder.failBindings {
				http.Error(response, "controlled binding failure", http.StatusBadGateway)
				return
			}
			_, _ = io.WriteString(response, `{}`)
		case "POST /api/projects/project-1/tasks":
			recorder.taskCreates++
			_ = json.NewDecoder(request.Body).Decode(&recorder.taskRequest)
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"task-1"}`)
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	})
}

func startHostedBootstrap(t *testing.T, env map[string]string, recorder *hostedBootstrapRecorder) error {
	t.Helper()
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		recorder.handler(t).ServeHTTP(response, request)
		return response.Result(), nil
	})}
	config, err := hostedcontroller.ConfigFromEnv(env)
	if err != nil {
		t.Fatalf("ConfigFromEnv error = %v", err)
	}
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", Client: client})
	_, err = app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config))
	return err
}

func TestHTTPAppStartProjectsAdditionalModelGroups(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantGroups  []recordedHostedProvider
		wantBinding map[string]string // credential_ref -> literal value
	}{
		{
			name: "inherited slot joins the parent provider catalog",
			env:  map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "inherited-model"},
			wantGroups: []recordedHostedProvider{{
				Name: "TSecBench Hosted Model", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1"}},
				Catalog: recordedHostedCatalog{Manual: []string{"parent-model", "inherited-model"}, DefaultModel: "parent-model"},
			}},
			wantBinding: map[string]string{"HOSTED_PROVIDER_1_API_KEY": "parent-key"},
		},
		{
			name: "second gateway with its own key is a separate provider",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1":          "second-model",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.tsecbench.gw/v1",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1_API_KEY":  "second-key",
			},
			wantGroups: []recordedHostedProvider{
				{
					Name: "TSecBench Hosted Model", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1"}},
					Catalog: recordedHostedCatalog{Manual: []string{"parent-model"}, DefaultModel: "parent-model"},
				},
				{
					Name: "TSecBench Hosted Additional Model 1", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1"}},
					Catalog: recordedHostedCatalog{Manual: []string{"second-model"}, DefaultModel: "second-model"},
				},
			},
			wantBinding: map[string]string{
				"HOSTED_PROVIDER_1_API_KEY": "parent-key",
				"HOSTED_PROVIDER_2_API_KEY": "second-key",
			},
		},
		{
			name: "key-only override separates provider and binding",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1":         "keyed-model",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1_API_KEY": "second-key",
			},
			wantGroups: []recordedHostedProvider{
				{
					Name: "TSecBench Hosted Model", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1"}},
					Catalog: recordedHostedCatalog{Manual: []string{"parent-model"}, DefaultModel: "parent-model"},
				},
				{
					Name: "TSecBench Hosted Additional Model 1", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1"}},
					Catalog: recordedHostedCatalog{Manual: []string{"keyed-model"}, DefaultModel: "keyed-model"},
				},
			},
			wantBinding: map[string]string{
				"HOSTED_PROVIDER_1_API_KEY": "parent-key",
				"HOSTED_PROVIDER_2_API_KEY": "second-key",
			},
		},
		{
			name: "protocol-only override separates the provider",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2":          "responses-model",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2_PROTOCOL": "openai_responses",
			},
			wantGroups: []recordedHostedProvider{
				{
					Name: "TSecBench Hosted Model", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1"}},
					Catalog: recordedHostedCatalog{Manual: []string{"parent-model"}, DefaultModel: "parent-model"},
				},
				{
					Name: "TSecBench Hosted Additional Model 2", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_responses", BaseURL: "http://model.tsecbench.gw/v1"}},
					Catalog: recordedHostedCatalog{Manual: []string{"responses-model"}, DefaultModel: "responses-model"},
				},
			},
			wantBinding: map[string]string{"HOSTED_PROVIDER_1_API_KEY": "parent-key", "HOSTED_PROVIDER_2_API_KEY": "parent-key"},
		},
		{
			name: "models sharing one additional tuple share that provider",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1":          "second-a",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.tsecbench.gw/v1",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1_API_KEY":  "second-key",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2":          "second-b",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2_BASE_URL": "http://second.tsecbench.gw/v1",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2_API_KEY":  "second-key",
			},
			wantGroups: []recordedHostedProvider{
				{
					Name: "TSecBench Hosted Model", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1"}},
					Catalog: recordedHostedCatalog{Manual: []string{"parent-model"}, DefaultModel: "parent-model"},
				},
				{
					Name: "TSecBench Hosted Additional Model 1", Endpoints: []recordedHostedEndpoint{{Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1"}},
					Catalog: recordedHostedCatalog{Manual: []string{"second-a", "second-b"}, DefaultModel: "second-a"},
				},
			},
			wantBinding: map[string]string{
				"HOSTED_PROVIDER_1_API_KEY": "parent-key",
				"HOSTED_PROVIDER_2_API_KEY": "second-key",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validPiEnv()
			env["CYBERPENDA_MODEL"] = "parent-model"
			env["CYBERPENDA_MODEL_API_KEY"] = "parent-key"
			for key, value := range test.env {
				env[key] = value
			}
			recorder := &hostedBootstrapRecorder{}
			if err := startHostedBootstrap(t, env, recorder); err != nil {
				t.Fatalf("Start error = %v", err)
			}
			if len(recorder.providerRequests) != len(test.wantGroups) {
				t.Fatalf("provider creates = %#v, want %d groups", recorder.providerRequests, len(test.wantGroups))
			}
			for index, want := range test.wantGroups {
				got := recorder.providerRequests[index]
				if got.Name != want.Name {
					t.Fatalf("provider %d name = %q, want %q", index, got.Name, want.Name)
				}
				if len(got.Endpoints) != 1 || got.Endpoints[0].Protocol != want.Endpoints[0].Protocol ||
					got.Endpoints[0].BaseURL != want.Endpoints[0].BaseURL {
					t.Fatalf("provider %d endpoints = %#v, want %#v", index, got.Endpoints, want.Endpoints)
				}
				if strings.Join(got.Catalog.Manual, ",") != strings.Join(want.Catalog.Manual, ",") {
					t.Fatalf("provider %d catalog manual = %#v, want %#v", index, got.Catalog.Manual, want.Catalog.Manual)
				}
				if got.Catalog.DefaultModel != want.Catalog.DefaultModel {
					t.Fatalf("provider %d default = %q, want %q", index, got.Catalog.DefaultModel, want.Catalog.DefaultModel)
				}
			}
			// The parent stays selected: profile pins provider 1 and the parent model.
			fields, _ := recorder.profileRequest["fields"].(map[string]any)
			if fields["model_provider_id"] != "hosted-provider-1" || fields["model_override"] != "parent-model" ||
				fields["model_provider_protocol"] != "openai_chat_completions" {
				t.Fatalf("Runtime Profile fields = %#v, want the parent provider and model", fields)
			}
			if recorder.taskCreates != 1 {
				t.Fatalf("Task creates = %d, want 1", recorder.taskCreates)
			}
			gotBindings := map[string]string{}
			for _, request := range recorder.bindingRequests {
				ref, _ := request["credential_ref"].(string)
				source, _ := request["source"].(map[string]any)
				value, _ := source["value"].(string)
				if source["kind"] != "literal" || source["destination_env"] != ref {
					t.Fatalf("binding %s = %#v", ref, request)
				}
				if _, duplicate := gotBindings[ref]; duplicate {
					t.Fatalf("duplicate binding for %s", ref)
				}
				gotBindings[ref] = value
			}
			if _, ok := gotBindings["BENCHMARK_TOKEN"]; !ok {
				t.Fatalf("BENCHMARK_TOKEN binding missing: %#v", recorder.bindingRequests)
			}
			delete(gotBindings, "BENCHMARK_TOKEN")
			if len(gotBindings) != len(test.wantBinding) {
				t.Fatalf("model key bindings = %#v, want %#v", gotBindings, test.wantBinding)
			}
			for ref, want := range test.wantBinding {
				if gotBindings[ref] != want {
					t.Fatalf("binding %s = %q, want %q", ref, gotBindings[ref], want)
				}
			}
			// Raw keys never enter provider, profile, or task payloads.
			for _, body := range recorder.providerBodies {
				if strings.Contains(body, "parent-key") || strings.Contains(body, "second-key") {
					t.Fatalf("provider payload disclosed an API key: %s", body)
				}
			}
			if raw, err := json.Marshal(recorder.profileRequest); err == nil && strings.Contains(string(raw), "parent-key") {
				t.Fatalf("Runtime Profile payload disclosed an API key: %s", raw)
			}
			if raw, err := json.Marshal(recorder.taskRequest); err == nil && strings.Contains(string(raw), "parent-key") {
				t.Fatalf("Task payload disclosed an API key: %s", raw)
			}
		})
	}
}

func TestHTTPAppStartAppliesHostedLimitsToEveryAdditionalModel(t *testing.T) {
	env := validPiEnv()
	env["CYBERPENDA_MODEL"] = "parent-model"
	env["CYBERPENDA_MODEL_API_KEY"] = "parent-key"
	env["CYBERPENDA_CONTEXT_WINDOW"] = "1048576"
	env["CYBERPENDA_MAX_OUTPUT_TOKENS"] = "393216"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_1"] = "inherited-model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2"] = "second-model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2_BASE_URL"] = "http://second.tsecbench.gw/v1"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2_API_KEY"] = "second-key"

	recorder := &hostedBootstrapRecorder{}
	if err := startHostedBootstrap(t, env, recorder); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if len(recorder.providerRequests) != 2 {
		t.Fatalf("provider creates = %d, want 2", len(recorder.providerRequests))
	}
	wantModels := map[int][]string{0: {"parent-model", "inherited-model"}, 1: {"second-model"}}
	for provider, models := range wantModels {
		limits := recorder.providerRequests[provider].Catalog.Limits
		if len(limits) != len(models) {
			t.Fatalf("provider %d limits = %#v, want one entry per model %#v", provider, limits, models)
		}
		for _, model := range models {
			got, ok := limits[model]
			if !ok || got.ContextWindow != 1048576 || got.MaxOutputTokens != 393216 {
				t.Fatalf("provider %d limits[%s] = %+v, want 1048576/393216", provider, model, got)
			}
		}
	}
}

func TestHTTPAppStartKeepsSingleProviderWithoutAdditionalModels(t *testing.T) {
	recorder := &hostedBootstrapRecorder{}
	if err := startHostedBootstrap(t, validPiEnv(), recorder); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if len(recorder.providerRequests) != 1 {
		t.Fatalf("provider creates = %d, want 1", len(recorder.providerRequests))
	}
	if len(recorder.bindingRequests) != 2 {
		t.Fatalf("binding creates = %d, want 2 (token plus parent key)", len(recorder.bindingRequests))
	}
	if recorder.taskCreates != 1 {
		t.Fatalf("Task creates = %d, want 1", recorder.taskCreates)
	}
}

func TestHTTPAppStartStopsBeforeTaskWhenProviderOrBindingFails(t *testing.T) {
	tests := []struct {
		name           string
		failProviderAt int
		failBindings   bool
	}{
		{name: "additional provider failure", failProviderAt: 2},
		{name: "credential binding failure", failBindings: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validPiEnv()
			env["CYBERPENDA_PI_ADDITIONAL_MODEL_1"] = "second-model"
			env["CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL"] = "http://second.tsecbench.gw/v1"
			env["CYBERPENDA_PI_ADDITIONAL_MODEL_1_API_KEY"] = "second-key"
			recorder := &hostedBootstrapRecorder{failProviderAt: test.failProviderAt, failBindings: test.failBindings}
			if err := startHostedBootstrap(t, env, recorder); err == nil {
				t.Fatal("Start error = nil, want controlled bootstrap failure")
			}
			if recorder.taskCreates != 0 {
				t.Fatalf("Task creates = %d, want 0 after a failed bootstrap step", recorder.taskCreates)
			}
		})
	}
}

func TestHTTPAppStartRejectsConflictingAdditionalModelsBeforeHTTPWrites(t *testing.T) {
	recorder := &hostedBootstrapRecorder{}
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		recorder.handler(t).ServeHTTP(response, request)
		return response.Result(), nil
	})}
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", Client: client})
	var evaluation hostedcontroller.HostedEvaluationBootstrap
	evaluation.Runtime.Provider = "pi"
	evaluation.Runtime.ModelProtocol = "openai_chat_completions"
	evaluation.Runtime.ModelBaseURL = "http://model.tsecbench.gw/v1"
	evaluation.Runtime.Model = "parent-model"
	evaluation.Runtime.ModelAPIKey = "parent-key"
	evaluation.Runtime.PIAdditionalModels = []hostedcontroller.PIAdditionalModel{
		{Slot: 1, Model: "conflicted", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key"},
		{Slot: 2, Model: "conflicted", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "third-key"},
	}
	_, err := app.Start(context.Background(), evaluation)
	if err == nil {
		t.Fatal("Start error = nil, want conflicting additional models rejected")
	}
	if recorder.requestCount != 0 {
		t.Fatalf("daemon requests = %d, want 0 before validation", recorder.requestCount)
	}
	if strings.Contains(err.Error(), "second-key") || strings.Contains(err.Error(), "third-key") {
		t.Fatalf("Start error disclosed an API key: %v", err)
	}
}
