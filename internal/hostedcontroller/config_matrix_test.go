package hostedcontroller_test

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"pentest/internal/hostedcontroller"
)

func TestHostedConfigurationRequiresInputsAndDefaultsToPi(t *testing.T) {
	required := []string{
		"BENCHMARK_BASE_URL", "BENCHMARK_TOKEN", "CYBERPENDA_MODEL_PROTOCOL",
		"CYBERPENDA_MODEL_BASE_URL", "CYBERPENDA_MODEL", "CYBERPENDA_MODEL_API_KEY",
	}
	for _, key := range required {
		t.Run(key, func(t *testing.T) {
			env := hostedMatrixEnv()
			delete(env, key)
			if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
				t.Fatalf("ConfigFromEnv accepted missing %s", key)
			}
		})
	}
	config, err := hostedcontroller.ConfigFromEnv(hostedMatrixEnv())
	if err != nil {
		t.Fatal(err)
	}
	if config.Runtime != "pi" {
		t.Fatalf("default Runtime = %q, want pi", config.Runtime)
	}
}

func TestHostedConfigurationAcceptsOnlyTheRuntimeProtocolMatrix(t *testing.T) {
	tests := []struct {
		runtime, protocol string
		valid             bool
	}{
		{"pi", "openai_chat_completions", true},
		{"pi", "openai_responses", true},
		{"pi", "anthropic_messages", true},
		{"codex", "openai_chat_completions", false},
		{"codex", "openai_responses", true},
		{"codex", "anthropic_messages", false},
		{"claude_code", "openai_chat_completions", false},
		{"claude_code", "openai_responses", false},
		{"claude_code", "anthropic_messages", true},
		{"unknown", "openai_responses", false},
	}
	for _, test := range tests {
		t.Run(test.runtime+"/"+test.protocol, func(t *testing.T) {
			env := hostedMatrixEnv()
			env["CYBERPENDA_RUNTIME"] = test.runtime
			env["CYBERPENDA_MODEL_PROTOCOL"] = test.protocol
			_, err := hostedcontroller.ConfigFromEnv(env)
			if (err == nil) != test.valid {
				t.Fatalf("ConfigFromEnv error = %v, valid=%v", err, test.valid)
			}
		})
	}
}

func TestHostedConfigurationRequiresAnOperationFreeHTTPGatewayBaseURL(t *testing.T) {
	tests := []struct {
		name, baseURL string
		valid         bool
	}{
		{"gateway base", "http://model.tsecbench.gw/v1", true},
		{"gateway base with port", "http://model.tsecbench.gw:8080/proxy/v1", true},
		{"https", "https://model.tsecbench.gw/v1", false},
		{"foreign host", "http://model.example.test/v1", false},
		{"suffix confusion", "http://model.tsecbench.gw.example.test/v1", false},
		{"missing gateway name", "http://tsecbench.gw/v1", false},
		{"chat operation", "http://model.tsecbench.gw/v1/chat/completions", false},
		{"chat operation slashes", "http://model.tsecbench.gw/v1/chat/completions///", false},
		{"responses operation", "http://model.tsecbench.gw/v1/responses", false},
		{"messages operation", "http://model.tsecbench.gw/v1/messages", false},
		{"query", "http://model.tsecbench.gw/v1?operation=responses", false},
		{"fragment", "http://model.tsecbench.gw/v1#responses", false},
		{"userinfo", "http://user@model.tsecbench.gw/v1", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := hostedMatrixEnv()
			env["CYBERPENDA_MODEL_BASE_URL"] = test.baseURL
			_, err := hostedcontroller.ConfigFromEnv(env)
			if (err == nil) != test.valid {
				t.Fatalf("ConfigFromEnv(%q) error = %v, valid=%v", test.baseURL, err, test.valid)
			}
		})
	}
}

func TestHostedStartProjectsEachRuntimeThroughNormalProviderAndCredentialInputs(t *testing.T) {
	tests := []struct {
		runtime, protocol, binary string
		customArgs                []any
	}{
		{"pi", "openai_chat_completions", "/opt/bin/pi", []any{"--approve"}},
		{"codex", "openai_responses", "/opt/bin/codex", nil},
		{"claude_code", "anthropic_messages", "/opt/bin/claude", nil},
	}
	for _, test := range tests {
		t.Run(test.runtime, func(t *testing.T) {
			var providerRequest, profileRequest, taskRequest map[string]any
			var bindingRequests []map[string]any
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				switch request.Method + " " + request.URL.Path {
				case "PUT /api/skills/tsecbench-hosted-challenge-loop":
					_, _ = io.WriteString(response, `{}`)
				case "POST /api/model-providers":
					_ = json.NewDecoder(request.Body).Decode(&providerRequest)
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
					bindingRequests = append(bindingRequests, body)
					_, _ = io.WriteString(response, `{}`)
				case "POST /api/projects/project-1/tasks":
					_ = json.NewDecoder(request.Body).Decode(&taskRequest)
					response.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(response, `{"id":"task-1"}`)
				default:
					http.Error(response, "unexpected request", http.StatusNotFound)
				}
			})
			app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
				BaseURL: "http://hosted.test", Client: hostedMatrixHTTPClient(handler), RuntimeBinary: test.binary,
			})
			env := hostedMatrixEnv()
			env["CYBERPENDA_RUNTIME"] = test.runtime
			env["CYBERPENDA_MODEL_PROTOCOL"] = test.protocol
			config, err := hostedcontroller.ConfigFromEnv(env)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config)); err != nil {
				t.Fatal(err)
			}

			endpoints, _ := providerRequest["endpoints"].([]any)
			endpoint, _ := endpoints[0].(map[string]any)
			if endpoint["protocol"] != test.protocol || endpoint["base_url"] != env["CYBERPENDA_MODEL_BASE_URL"] {
				t.Fatalf("Model Provider request = %#v", providerRequest)
			}
			fields, _ := profileRequest["fields"].(map[string]any)
			if profileRequest["provider"] != test.runtime || fields["binary_path"] != test.binary ||
				fields["model_provider_protocol"] != test.protocol || fields["model_provider_id"] != "hosted-model" ||
				fields["model_override"] != "model" {
				t.Fatalf("Runtime Profile request = %#v", profileRequest)
			}
			if !maps.Equal(fields["env"].(map[string]any), map[string]any{"BENCHMARK_BASE_URL": env["BENCHMARK_BASE_URL"]}) {
				t.Fatalf("Runtime Profile env = %#v", fields["env"])
			}
			customArgs, found := fields["custom_args"]
			if test.customArgs == nil && found {
				t.Fatalf("Runtime Profile added unexpected custom arguments: %#v", fields)
			}
			if test.customArgs != nil && !found {
				t.Fatalf("Runtime Profile did not trust projected Pi resources: %#v", fields)
			}
			if found && !slices.Equal(customArgs.([]any), test.customArgs) {
				t.Fatalf("Runtime Profile custom arguments = %#v, want %#v", customArgs, test.customArgs)
			}
			if controls, _ := taskRequest["run_controls"].(map[string]any); controls["yolo"] != nil {
				t.Fatalf("Task added synthetic YOLO control: %#v", taskRequest)
			}
			assertHostedLiteralBinding(t, bindingRequests, "BENCHMARK_TOKEN", "token")
			assertHostedLiteralBinding(t, bindingRequests, "HOSTED_MODEL_API_KEY", "key")
		})
	}
}

func hostedMatrixHTTPClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: hostedMatrixRoundTripper(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})}
}

type hostedMatrixRoundTripper func(*http.Request) (*http.Response, error)

func (fn hostedMatrixRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func assertHostedLiteralBinding(t *testing.T, requests []map[string]any, credentialRef, value string) {
	t.Helper()
	for _, request := range requests {
		if request["credential_ref"] != credentialRef {
			continue
		}
		source, _ := request["source"].(map[string]any)
		if source["kind"] != "literal" || source["value"] != value || source["destination_env"] != credentialRef {
			t.Fatalf("binding %s = %#v", credentialRef, request)
		}
		return
	}
	t.Fatalf("binding %s not found in %#v", credentialRef, requests)
}

func hostedMatrixEnv() map[string]string {
	return map[string]string{
		"BENCHMARK_BASE_URL": "http://benchmark.tsecbench.gw/openapi/v1", "BENCHMARK_TOKEN": "token",
		"CYBERPENDA_MODEL_PROTOCOL": "openai_chat_completions", "CYBERPENDA_MODEL_BASE_URL": "http://model.tsecbench.gw/v1",
		"CYBERPENDA_MODEL": "model", "CYBERPENDA_MODEL_API_KEY": "key",
	}
}
