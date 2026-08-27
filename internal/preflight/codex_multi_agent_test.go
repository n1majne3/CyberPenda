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
