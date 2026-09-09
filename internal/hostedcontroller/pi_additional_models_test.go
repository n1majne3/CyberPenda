package hostedcontroller_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"pentest/internal/hostedcontroller"
)

func validPiEnv() map[string]string {
	env := validHostedEnv()
	env["CYBERPENDA_RUNTIME"] = "pi"
	env["CYBERPENDA_MODEL_PROTOCOL"] = "openai_chat_completions"
	return env
}

func TestHostedPIAdditionalModelSlotsParseAndInheritIndependently(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []hostedcontroller.PIAdditionalModel
	}{
		{
			name: "absent slots stay unused",
			env:  map[string]string{},
			want: nil,
		},
		{
			name: "slot one alone inherits the parent provider",
			env:  map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one"},
			want: []hostedcontroller.PIAdditionalModel{{
				Slot: 1, Model: "pi-extra-one", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key",
			}},
		},
		{
			name: "slot two alone",
			env:  map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_2": "pi-extra-two"},
			want: []hostedcontroller.PIAdditionalModel{{
				Slot: 2, Model: "pi-extra-two", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key",
			}},
		},
		{
			name: "slot three alone",
			env:  map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_3": "pi-extra-three"},
			want: []hostedcontroller.PIAdditionalModel{{
				Slot: 3, Model: "pi-extra-three", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key",
			}},
		},
		{
			name: "all three slots in order",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_3": "pi-extra-three",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2": "pi-extra-two",
			},
			want: []hostedcontroller.PIAdditionalModel{
				{Slot: 1, Model: "pi-extra-one", Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key"},
				{Slot: 2, Model: "pi-extra-two", Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key"},
				{Slot: 3, Model: "pi-extra-three", Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key"},
			},
		},
		{
			name: "sparse slots parse independently",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_3": "pi-extra-three",
			},
			want: []hostedcontroller.PIAdditionalModel{
				{Slot: 1, Model: "pi-extra-one", Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key"},
				{Slot: 3, Model: "pi-extra-three", Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key"},
			},
		},
		{
			name: "each override inherits separately from the parent",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.tsecbench.gw/v1",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1":          "pi-extra-one",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2":          "pi-extra-two",
			},
			want: []hostedcontroller.PIAdditionalModel{
				{Slot: 1, Model: "pi-extra-one", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "key"},
				{Slot: 2, Model: "pi-extra-two", Protocol: "openai_chat_completions", BaseURL: "http://model.tsecbench.gw/v1", APIKey: "key"},
			},
		},
		{
			name: "full override keeps its own provider tuple",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2":          "pi-second-provider-model",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2_PROTOCOL": "openai_responses",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2_BASE_URL": "http://second.tsecbench.gw:8080/prefix",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_2_API_KEY":  "second-key",
			},
			want: []hostedcontroller.PIAdditionalModel{{
				Slot: 2, Model: "pi-second-provider-model", Protocol: "openai_responses",
				BaseURL: "http://second.tsecbench.gw:8080/prefix", APIKey: "second-key",
			}},
		},
		{
			name: "key-only override",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1":         "pi-extra-keyed",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1_API_KEY": "second-key",
			},
			want: []hostedcontroller.PIAdditionalModel{{
				Slot: 1, Model: "pi-extra-keyed", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "second-key",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validPiEnv()
			for key, value := range test.env {
				env[key] = value
			}
			config, err := hostedcontroller.ConfigFromEnv(env)
			if err != nil {
				t.Fatalf("ConfigFromEnv error = %v", err)
			}
			got := config.PIAdditionalModels
			if len(got) != len(test.want) {
				t.Fatalf("additional models = %#v, want %#v", got, test.want)
			}
			for index, want := range test.want {
				if got[index] != want {
					t.Fatalf("additional model %d = %#v, want %#v", index, got[index], want)
				}
			}
			evaluation := hostedcontroller.EvaluationForConfig(config)
			if len(evaluation.Runtime.PIAdditionalModels) != len(test.want) {
				t.Fatalf("bootstrap additional models = %#v", evaluation.Runtime.PIAdditionalModels)
			}
		})
	}
}

func TestHostedPIAdditionalModelAcceptsEveryPiProtocol(t *testing.T) {
	for _, protocol := range []string{"openai_chat_completions", "openai_responses", "anthropic_messages"} {
		t.Run(protocol, func(t *testing.T) {
			env := validPiEnv()
			env["CYBERPENDA_PI_ADDITIONAL_MODEL_1"] = "pi-extra-one"
			env["CYBERPENDA_PI_ADDITIONAL_MODEL_1_PROTOCOL"] = protocol
			config, err := hostedcontroller.ConfigFromEnv(env)
			if err != nil {
				t.Fatalf("ConfigFromEnv error = %v", err)
			}
			if config.PIAdditionalModels[0].Protocol != protocol {
				t.Fatalf("effective protocol = %q, want %q", config.PIAdditionalModels[0].Protocol, protocol)
			}
		})
	}
}

func TestHostedPIAdditionalModelRejectsInvalidSlotValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "blank model id", env: map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_1": ""}},
		{name: "whitespace model id", env: map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_2": "  \t "}},
		{name: "blank protocol", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_PROTOCOL": ""}},
		{name: "blank base url", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": " "}},
		{name: "blank api key", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_API_KEY": ""}},
		{name: "orphan protocol", env: map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_1_PROTOCOL": "openai_responses"}},
		{name: "orphan base url", env: map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_2_BASE_URL": "http://second.tsecbench.gw/v1"}},
		{name: "orphan api key", env: map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_3_API_KEY": "second-key"}},
		{name: "unsupported protocol", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_PROTOCOL": "openai_realtime"}},
		{name: "https base url", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "https://second.tsecbench.gw/v1"}},
		{name: "foreign host", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.example.test/v1"}},
		{name: "chat operation suffix", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.tsecbench.gw/v1/chat/completions"}},
		{name: "responses operation suffix", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.tsecbench.gw/v1/responses"}},
		{name: "messages operation suffix", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.tsecbench.gw/v1/messages"}},
		{name: "query in base url", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.tsecbench.gw/v1?x=1"}},
		{name: "fragment in base url", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://second.tsecbench.gw/v1#f"}},
		{name: "userinfo in base url", env: map[string]string{
			"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one", "CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL": "http://user@second.tsecbench.gw/v1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validPiEnv()
			for key, value := range test.env {
				env[key] = value
			}
			_, err := hostedcontroller.ConfigFromEnv(env)
			if err == nil {
				t.Fatal("ConfigFromEnv accepted an invalid Pi additional-model value")
			}
			if !strings.Contains(err.Error(), "CYBERPENDA_PI_ADDITIONAL_MODEL") {
				t.Fatalf("error %q does not identify the additional-model variable", err)
			}
		})
	}
}

func TestHostedPIAdditionalModelRequiresPiRuntime(t *testing.T) {
	for name, runtime := range map[string]string{"explicit codex": "codex", "claude code": "claude_code", "omitted defaults to codex": ""} {
		t.Run(name, func(t *testing.T) {
			env := validHostedEnv()
			if name == "claude code" {
				env["CYBERPENDA_MODEL_PROTOCOL"] = "anthropic_messages"
			}
			delete(env, "CYBERPENDA_RUNTIME")
			if runtime != "" {
				env["CYBERPENDA_RUNTIME"] = runtime
			}
			env["CYBERPENDA_PI_ADDITIONAL_MODEL_1"] = "pi-extra-one"
			if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
				t.Fatal("ConfigFromEnv accepted a Pi additional model on a non-Pi Runtime")
			}
			env = validHostedEnv()
			if name == "claude code" {
				env["CYBERPENDA_MODEL_PROTOCOL"] = "anthropic_messages"
				env["CYBERPENDA_RUNTIME"] = "claude_code"
			} else if runtime != "" {
				env["CYBERPENDA_RUNTIME"] = runtime
			}
			env["CYBERPENDA_PI_ADDITIONAL_MODEL_1_BASE_URL"] = "http://second.tsecbench.gw/v1"
			if _, err := hostedcontroller.ConfigFromEnv(env); err == nil {
				t.Fatal("ConfigFromEnv accepted a Pi additional-model override on a non-Pi Runtime")
			}
		})
	}
}

func TestHostedPIAdditionalModelPlanGroupsProviderTuples(t *testing.T) {
	parent := hostedcontroller.HostedRuntimeBootstrap{
		ModelProtocol: "openai_chat_completions",
		ModelBaseURL:  "http://model.tsecbench.gw/v1",
		Model:         "parent-model",
		ModelAPIKey:   "parent-key",
	}
	tests := []struct {
		name       string
		additional []hostedcontroller.PIAdditionalModel
		want       []hostedcontroller.HostedModelGroup
	}{
		{
			name:       "no additional models keeps the single parent provider",
			additional: nil,
			want: []hostedcontroller.HostedModelGroup{{
				Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
				Models: []string{"parent-model"}, Parent: true,
			}},
		},
		{
			name: "inherited slot joins the parent catalog",
			additional: []hostedcontroller.PIAdditionalModel{{
				Slot: 1, Model: "inherited-model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
			}},
			want: []hostedcontroller.HostedModelGroup{{
				Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
				Models: []string{"parent-model", "inherited-model"}, Parent: true,
			}},
		},
		{
			name: "normalized endpoint duplicate shares the parent group",
			additional: []hostedcontroller.PIAdditionalModel{{
				Slot: 1, Model: "inherited-model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1/", APIKey: "parent-key",
			}},
			want: []hostedcontroller.HostedModelGroup{{
				Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
				Models: []string{"parent-model", "inherited-model"}, Parent: true,
			}},
		},
		{
			name: "second gateway becomes its own provider",
			additional: []hostedcontroller.PIAdditionalModel{{
				Slot: 1, Model: "second-model", Protocol: "openai_chat_completions",
				BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key",
			}},
			want: []hostedcontroller.HostedModelGroup{
				{Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
					BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
					Models: []string{"parent-model"}, Parent: true},
				{Name: "TSecBench Hosted Additional Model 1", Protocol: "openai_chat_completions",
					BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key",
					Models: []string{"second-model"}},
			},
		},
		{
			name: "key-only override separates the provider",
			additional: []hostedcontroller.PIAdditionalModel{{
				Slot: 1, Model: "keyed-model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "second-key",
			}},
			want: []hostedcontroller.HostedModelGroup{
				{Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
					BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
					Models: []string{"parent-model"}, Parent: true},
				{Name: "TSecBench Hosted Additional Model 1", Protocol: "openai_chat_completions",
					BaseURL: "http://model.tsecbench.gw/v1", APIKey: "second-key",
					Models: []string{"keyed-model"}},
			},
		},
		{
			name: "protocol-only override separates the provider",
			additional: []hostedcontroller.PIAdditionalModel{{
				Slot: 2, Model: "responses-model", Protocol: "openai_responses",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
			}},
			want: []hostedcontroller.HostedModelGroup{
				{Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
					BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
					Models: []string{"parent-model"}, Parent: true},
				{Name: "TSecBench Hosted Additional Model 2", Protocol: "openai_responses",
					BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
					Models: []string{"responses-model"}},
			},
		},
		{
			name: "identical duplicate model is projected once",
			additional: []hostedcontroller.PIAdditionalModel{
				{Slot: 1, Model: "second-model", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key"},
				{Slot: 3, Model: "second-model", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1/", APIKey: "second-key"},
			},
			want: []hostedcontroller.HostedModelGroup{
				{Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
					BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
					Models: []string{"parent-model"}, Parent: true},
				{Name: "TSecBench Hosted Additional Model 1", Protocol: "openai_chat_completions",
					BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key",
					Models: []string{"second-model"}},
			},
		},
		{
			name: "models sharing one additional tuple share that provider",
			additional: []hostedcontroller.PIAdditionalModel{
				{Slot: 1, Model: "second-a", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key"},
				{Slot: 2, Model: "second-b", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key"},
			},
			want: []hostedcontroller.HostedModelGroup{
				{Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
					BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
					Models: []string{"parent-model"}, Parent: true},
				{Name: "TSecBench Hosted Additional Model 1", Protocol: "openai_chat_completions",
					BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key",
					Models: []string{"second-a", "second-b"}},
			},
		},
		{
			name: "parent model repeated with identical settings is a no-op",
			additional: []hostedcontroller.PIAdditionalModel{{
				Slot: 1, Model: "parent-model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
			}},
			want: []hostedcontroller.HostedModelGroup{{
				Name: "TSecBench Hosted Model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "parent-key",
				Models: []string{"parent-model"}, Parent: true,
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := parent
			runtime.PIAdditionalModels = test.additional
			got, err := hostedcontroller.PlanHostedModelGroups(runtime)
			if err != nil {
				t.Fatalf("PlanHostedModelGroups error = %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("groups = %#v, want %#v", got, test.want)
			}
			for index, want := range test.want {
				if got[index].Name != want.Name || got[index].Protocol != want.Protocol ||
					got[index].BaseURL != want.BaseURL || got[index].APIKey != want.APIKey ||
					got[index].Parent != want.Parent || !equalStringSlices(got[index].Models, want.Models) {
					t.Fatalf("group %d = %#v, want %#v", index, got[index], want)
				}
			}
		})
	}
}

func TestHostedPIAdditionalModelPlanRejectsConflictingTuples(t *testing.T) {
	parent := hostedcontroller.HostedRuntimeBootstrap{
		ModelProtocol: "openai_chat_completions",
		ModelBaseURL:  "http://model.tsecbench.gw/v1",
		Model:         "parent-model",
		ModelAPIKey:   "parent-key",
	}
	tests := []struct {
		name       string
		additional []hostedcontroller.PIAdditionalModel
	}{
		{
			name: "same model id with a different protocol",
			additional: []hostedcontroller.PIAdditionalModel{
				{Slot: 1, Model: "conflicted", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key"},
				{Slot: 2, Model: "conflicted", Protocol: "openai_responses", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key"},
			},
		},
		{
			name: "same model id with a different base url",
			additional: []hostedcontroller.PIAdditionalModel{
				{Slot: 1, Model: "conflicted", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key"},
				{Slot: 2, Model: "conflicted", Protocol: "openai_chat_completions", BaseURL: "http://third.tsecbench.gw/v1", APIKey: "second-key"},
			},
		},
		{
			name: "same model id with a different api key",
			additional: []hostedcontroller.PIAdditionalModel{
				{Slot: 1, Model: "conflicted", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "second-key"},
				{Slot: 2, Model: "conflicted", Protocol: "openai_chat_completions", BaseURL: "http://second.tsecbench.gw/v1", APIKey: "third-key"},
			},
		},
		{
			name: "parent model repeated with different settings",
			additional: []hostedcontroller.PIAdditionalModel{{
				Slot: 1, Model: "parent-model", Protocol: "openai_chat_completions",
				BaseURL: "http://model.tsecbench.gw/v1", APIKey: "second-key",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := parent
			runtime.PIAdditionalModels = test.additional
			_, err := hostedcontroller.PlanHostedModelGroups(runtime)
			if err == nil {
				t.Fatal("PlanHostedModelGroups accepted a conflicting model tuple")
			}
			if !strings.Contains(err.Error(), "hosted configuration is invalid") {
				t.Fatalf("error %q is not an invalid Hosted Model Configuration", err)
			}
			for _, key := range []string{"parent-key", "second-key", "third-key"} {
				if strings.Contains(err.Error(), key) {
					t.Fatalf("conflict error disclosed the API key value %q", key)
				}
			}
			if !strings.Contains(err.Error(), "conflicted") && !strings.Contains(err.Error(), "parent-model") {
				t.Fatalf("conflict error %q does not identify the model", err)
			}
		})
	}
}

func TestHostedControllerMasksEveryDistinctAdditionalModelKey(t *testing.T) {
	env := validPiEnv()
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_1"] = "inherited-model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2"] = "second-model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2_BASE_URL"] = "http://second.tsecbench.gw/v1"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2_API_KEY"] = "second-key"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_3"] = "third-model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_3_API_KEY"] = "second-key"

	app := &hostedApp{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	if err := hostedcontroller.RunWithApp(ctx, env, app, &stdout, &stderr); err != nil {
		t.Fatalf("RunWithApp error = %v", err)
	}
	if len(app.started) != 1 {
		t.Fatalf("starts = %d, want one", len(app.started))
	}
	if len(app.waited) != 1 || len(app.secretSets) != 1 {
		t.Fatalf("waits = %d secret sets = %d, want one of each", len(app.waited), len(app.secretSets))
	}
	got := app.secretSets[0]
	want := []string{"token", "key", "second-key"}
	if len(got) != len(want) {
		t.Fatalf("mask secrets = %#v, want %#v", got, want)
	}
	for _, secret := range want {
		found := false
		for _, value := range got {
			if value == secret {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("mask secrets = %#v, want %q present once", got, secret)
		}
	}
}

func TestHostedControllerRejectsAdditionalModelProblemsBeforeBootstrap(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "blank model id", env: map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_1": ""}},
		{name: "orphan override", env: map[string]string{"CYBERPENDA_PI_ADDITIONAL_MODEL_1_API_KEY": "second-key"}},
		{
			name: "parent model conflict",
			env: map[string]string{
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1":         "model",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1_API_KEY": "second-key",
			},
		},
		{
			name: "non-pi runtime",
			env: map[string]string{
				"CYBERPENDA_RUNTIME":               "codex",
				"CYBERPENDA_PI_ADDITIONAL_MODEL_1": "pi-extra-one",
				"CYBERPENDA_MODEL_PROTOCOL":        "openai_responses",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validPiEnv()
			for key, value := range test.env {
				env[key] = value
			}
			app := &hostedApp{}
			var stdout, stderr bytes.Buffer
			err := hostedcontroller.RunWithApp(context.Background(), env, app, &stdout, &stderr)
			if err == nil {
				t.Fatal("RunWithApp error = nil, want invalid Hosted Model Configuration")
			}
			if len(app.started) != 0 || len(app.waited) != 0 {
				t.Fatalf("invalid input reached bootstrap: starts=%d waits=%d", len(app.started), len(app.waited))
			}
			for _, secret := range []string{"second-key", "key", "token"} {
				if strings.Contains(stderr.String(), secret) || strings.Contains(err.Error(), secret) {
					t.Fatalf("diagnostic disclosed %q", secret)
				}
			}
		})
	}
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index, value := range want {
		if got[index] != value {
			return false
		}
	}
	return true
}
