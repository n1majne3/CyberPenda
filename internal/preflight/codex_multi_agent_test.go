package preflight_test

import (
	"context"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/runtimeprofile"
)

func newCodexMultiAgentProfile(t *testing.T, svc services, settings *runtimeprofile.CodexMultiAgent) runtimeprofile.Profile {
	t.Helper()
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"mimo"}, DefaultModel: "mimo"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	profile, err := svc.profiles.Create("codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID,
		CodexMultiAgent: settings,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return profile
}

func TestRunPreviewsCodexMultiAgentToolsInheritByDefault(t *testing.T) {
	svc := newTestServices(t)
	profile := newCodexMultiAgentProfile(t, svc, nil)

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.CodexMultiAgent == nil {
		t.Fatalf("expected codex multi-agent preview, got none")
	}
	if result.CodexMultiAgent.State != "inherit" {
		t.Fatalf("expected inherit preview, got %#v", result.CodexMultiAgent)
	}
	if result.CodexMultiAgent.MaxConcurrentThreadsPerSession != 0 || result.CodexMultiAgent.MaxDepth != 0 {
		t.Fatalf("expected no caps in inherit preview, got %#v", result.CodexMultiAgent)
	}
	if !result.Pass {
		t.Fatalf("multi-agent preview must not affect pass state, got %#v", result.Checks)
	}
}

func TestRunPreviewsCodexMultiAgentToolsOnWithCaps(t *testing.T) {
	svc := newTestServices(t)
	enabled := true
	profile := newCodexMultiAgentProfile(t, svc, &runtimeprofile.CodexMultiAgent{
		Enabled:                        &enabled,
		MaxConcurrentThreadsPerSession: 4,
		MaxDepth:                       2,
	})

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.CodexMultiAgent == nil {
		t.Fatalf("expected codex multi-agent preview, got none")
	}
	if result.CodexMultiAgent.State != "on" {
		t.Fatalf("expected on preview, got %#v", result.CodexMultiAgent)
	}
	if result.CodexMultiAgent.MaxConcurrentThreadsPerSession != 4 || result.CodexMultiAgent.MaxDepth != 2 {
		t.Fatalf("expected caps in preview, got %#v", result.CodexMultiAgent)
	}
}

func TestRunPreviewsCodexMultiAgentToolsExplicitOff(t *testing.T) {
	svc := newTestServices(t)
	disabled := false
	profile := newCodexMultiAgentProfile(t, svc, &runtimeprofile.CodexMultiAgent{Enabled: &disabled})

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.CodexMultiAgent == nil {
		t.Fatalf("expected codex multi-agent preview, got none")
	}
	if result.CodexMultiAgent.State != "off" {
		t.Fatalf("expected off preview, got %#v", result.CodexMultiAgent)
	}
	if result.CodexMultiAgent.MaxConcurrentThreadsPerSession != 0 || result.CodexMultiAgent.MaxDepth != 0 {
		t.Fatalf("expected no caps in off preview, got %#v", result.CodexMultiAgent)
	}
}

func TestRunOmitsCodexMultiAgentPreviewForOtherRuntimes(t *testing.T) {
	svc := newTestServices(t)
	enabled := true
	// The structured field is Codex-only: storing it on another runtime
	// family is rejected at the source.
	if _, err := svc.profiles.Create("pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{Enabled: &enabled},
	}); err == nil {
		t.Fatalf("expected codex_multi_agent on a pi profile to be rejected")
	}

	profile, err := svc.profiles.Create("pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.CodexMultiAgent != nil {
		t.Fatalf("expected no codex multi-agent preview for pi profile, got %#v", result.CodexMultiAgent)
	}
}

func TestRunPreviewsCodexMultiAgentFromCustomConfigFile(t *testing.T) {
	svc := newTestServices(t)
	profile := newCodexMultiAgentProfile(t, svc, nil)
	profile, err := svc.profiles.Update(profile.ID, "", "", runtimeprofile.Fields{
		ModelProviderID: profile.Fields.ModelProviderID,
		CustomConfigFile: `
[features]
multi_agent = true

[agents]
enabled = true
max_concurrent_threads_per_session = 7
max_depth = 3
`,
	}, true, false)
	if err != nil {
		t.Fatalf("add custom config: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.CodexMultiAgent == nil || result.CodexMultiAgent.State != "on" {
		t.Fatalf("expected merged custom config to preview on, got %#v", result.CodexMultiAgent)
	}
	if result.CodexMultiAgent.MaxConcurrentThreadsPerSession != 7 || result.CodexMultiAgent.MaxDepth != 3 {
		t.Fatalf("expected merged caps, got %#v", result.CodexMultiAgent)
	}
}

func TestRunPreviewsOverlayCapsWhenStructuredControlLeavesThemUnset(t *testing.T) {
	svc := newTestServices(t)
	enabled := true
	profile := newCodexMultiAgentProfile(t, svc, &runtimeprofile.CodexMultiAgent{Enabled: &enabled})
	profile, err := svc.profiles.Update(profile.ID, "", "", runtimeprofile.Fields{
		ModelProviderID: profile.Fields.ModelProviderID,
		CodexMultiAgent: profile.Fields.CodexMultiAgent,
		CustomConfigFile: `
[agents]
max_concurrent_threads_per_session = 9
max_depth = 4
`,
	}, true, false)
	if err != nil {
		t.Fatalf("add custom caps: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.CodexMultiAgent == nil || result.CodexMultiAgent.State != "on" {
		t.Fatalf("expected structured on preview, got %#v", result.CodexMultiAgent)
	}
	if result.CodexMultiAgent.MaxConcurrentThreadsPerSession != 9 || result.CodexMultiAgent.MaxDepth != 4 {
		t.Fatalf("expected overlay caps, got %#v", result.CodexMultiAgent)
	}
}

func TestRunPreviewsMultiAgentV2CapsFromFinalConfig(t *testing.T) {
	svc := newTestServices(t)
	profile := newCodexMultiAgentProfile(t, svc, nil)
	profile, err := svc.profiles.Update(profile.ID, "", "", runtimeprofile.Fields{
		ModelProviderID: profile.Fields.ModelProviderID,
		CustomConfigFile: `
[features.multi_agent_v2]
enabled = true
max_concurrent_threads_per_session = 11

[agents]
enabled = false
max_depth = 5
`,
	}, true, false)
	if err != nil {
		t.Fatalf("add V2 config: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.CodexMultiAgent == nil || result.CodexMultiAgent.State != "on" {
		t.Fatalf("V2 must take precedence over agents.enabled, got %#v", result.CodexMultiAgent)
	}
	if result.CodexMultiAgent.MaxConcurrentThreadsPerSession != 11 || result.CodexMultiAgent.MaxDepth != 0 {
		t.Fatalf("expected V2 cap without the V1-only depth, got %#v", result.CodexMultiAgent)
	}
}

func TestRunDoesNotEnableMultiAgentV2FromCapsAlone(t *testing.T) {
	svc := newTestServices(t)
	profile := newCodexMultiAgentProfile(t, svc, nil)
	profile, err := svc.profiles.Update(profile.ID, "", "", runtimeprofile.Fields{
		ModelProviderID: profile.Fields.ModelProviderID,
		CustomConfigFile: `
[features.multi_agent_v2]
max_concurrent_threads_per_session = 11
`,
	}, true, false)
	if err != nil {
		t.Fatalf("add V2 caps without enabled: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.CodexMultiAgent == nil || result.CodexMultiAgent.State != "inherit" {
		t.Fatalf("V2 caps alone must not enable the feature, got %#v", result.CodexMultiAgent)
	}
	if result.CodexMultiAgent.MaxConcurrentThreadsPerSession != 0 || result.CodexMultiAgent.MaxDepth != 0 {
		t.Fatalf("inactive V2 caps must not be reported, got %#v", result.CodexMultiAgent)
	}
}
