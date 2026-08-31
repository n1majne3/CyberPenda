package runtimeconfig_test

import (
	"reflect"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeconfig"
)

func TestResolveDirectLaunchCapturesOnlyDirectConfiguration(t *testing.T) {
	snapshot, err := runtimeconfig.Resolve(runtimeconfig.ResolveRequest{
		Selection: runtimeconfig.LaunchSelection{
			RuntimePluginID: "codex",
			ModelProviderID: "provider-1",
			Model:           "gpt-5",
			ReasoningEffort: "high",
			Runner:          "sandbox",
		},
		StandardSettings: map[string]any{"sandbox_image": "kali:latest"},
		ModelProvider: modelprovider.Snapshot{
			ModelProviderID:   "provider-1",
			ModelProviderName: "Primary",
			EndpointBaseURL:   "https://example.test/v1",
			Model:             "gpt-5",
		},
		DefaultEnabledSkillIDs: []string{"recon", "report"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if snapshot.SnapshotVersion != runtimeconfig.SnapshotVersion {
		t.Fatalf("snapshot version = %d", snapshot.SnapshotVersion)
	}
	if snapshot.RuntimeProfile != nil {
		t.Fatalf("direct launch provenance = %#v, want nil", snapshot.RuntimeProfile)
	}
	if snapshot.RuntimePluginID != "codex" || snapshot.Runner != "sandbox" {
		t.Fatalf("direct snapshot = %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot.EnabledSkillIDs, []string{"recon", "report"}) {
		t.Fatalf("enabled skills = %#v", snapshot.EnabledSkillIDs)
	}
	if got := snapshot.Settings["sandbox_image"]; got != "kali:latest" {
		t.Fatalf("standard setting = %#v", got)
	}
	if _, exists := snapshot.Settings["mcp_servers"]; exists {
		t.Fatal("direct launch inherited profile MCP settings")
	}
}

func TestResolveExplicitProfileCopiesImmutableConfiguration(t *testing.T) {
	settings := map[string]any{
		"custom_args": []any{"--verbose"},
		"mcp_servers": []any{map[string]any{"name": "project"}},
	}
	skills := []string{"recon"}
	snapshot, err := runtimeconfig.Resolve(runtimeconfig.ResolveRequest{
		Selection: runtimeconfig.LaunchSelection{
			RuntimeProfileID: "profile-1",
			Model:            "launch-model",
			ReasoningEffort:  "xhigh",
		},
		Profile: &runtimeconfig.ProfileSource{
			ID:              "profile-1",
			Name:            "Advanced Codex",
			RuntimePluginID: "codex",
			Runner:          "host",
			ModelProviderID: "provider-1",
			Model:           "profile-model",
			ReasoningEffort: "medium",
			Settings:        settings,
			EnabledSkillIDs: skills,
		},
		ModelProvider: modelprovider.Snapshot{ModelProviderID: "provider-1", Model: "launch-model"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	settings["custom_args"].([]any)[0] = "--changed"
	skills[0] = "changed"

	if snapshot.RuntimeProfile == nil || snapshot.RuntimeProfile.ID != "profile-1" || snapshot.RuntimeProfile.Name != "Advanced Codex" {
		t.Fatalf("profile provenance = %#v", snapshot.RuntimeProfile)
	}
	if got := snapshot.Settings["custom_args"].([]any)[0]; got != "--verbose" {
		t.Fatalf("captured settings mutated to %#v", got)
	}
	if got := snapshot.EnabledSkillIDs[0]; got != "recon" {
		t.Fatalf("captured skills mutated to %q", got)
	}
	if snapshot.TurnSelection.ModelProviderID != "provider-1" || snapshot.TurnSelection.Model != "launch-model" || snapshot.TurnSelection.ReasoningEffort != "xhigh" {
		t.Fatalf("launch overrides = %#v", snapshot.TurnSelection)
	}
}

func TestResolveExplicitLegacyProfileDoesNotInventModelProvider(t *testing.T) {
	snapshot, err := runtimeconfig.Resolve(runtimeconfig.ResolveRequest{
		Selection: runtimeconfig.LaunchSelection{RuntimeProfileID: "profile-legacy"},
		Profile: &runtimeconfig.ProfileSource{
			ID: "profile-legacy", Name: "Legacy", RuntimePluginID: "codex",
			Settings: map[string]any{"endpoint": "https://legacy.example.test"},
		},
	})
	if err != nil {
		t.Fatalf("Resolve legacy profile: %v", err)
	}
	if snapshot.TurnSelection.ModelProviderID != "" || snapshot.ModelProvider.ModelProviderID != "" {
		t.Fatalf("legacy profile gained invented Model Provider: %#v", snapshot)
	}
	if _, err := runtimeconfig.CloneForTurn(snapshot, snapshot.TurnSelection, snapshot.ModelProvider); err != nil {
		t.Fatalf("CloneForTurn legacy profile: %v", err)
	}
}

func TestResolveRejectsMixedOrIncompleteLaunchSelection(t *testing.T) {
	tests := []runtimeconfig.LaunchSelection{
		{RuntimeProfileID: "profile-1", RuntimePluginID: "codex", ModelProviderID: "provider-1", Model: "gpt-5"},
		{RuntimePluginID: "codex", ModelProviderID: "provider-1"},
		{ModelProviderID: "provider-1", Model: "gpt-5"},
	}
	for _, selection := range tests {
		if _, err := runtimeconfig.Resolve(runtimeconfig.ResolveRequest{Selection: selection}); err == nil {
			t.Fatalf("Resolve(%#v) error = nil", selection)
		}
	}
}

func TestCloneForTurnKeepsOwnerConfigurationAndChangesOnlyTurnSelection(t *testing.T) {
	original := runtimeconfig.RuntimeConfigurationSnapshot{
		SnapshotVersion: runtimeconfig.SnapshotVersion,
		RuntimePluginID: "codex",
		Runner:          "sandbox",
		Settings:        map[string]any{"custom_args": []any{"--safe"}},
		EnabledSkillIDs: []string{"recon"},
		TurnSelection: runtimeconfig.RuntimeTurnSelection{
			ModelProviderID: "provider-1",
			Model:           "old",
			ReasoningEffort: "high",
		},
	}
	cloned, err := runtimeconfig.CloneForTurn(original, runtimeconfig.RuntimeTurnSelection{
		ModelProviderID: "provider-2",
		Model:           "new",
		ReasoningEffort: "low",
	}, modelprovider.Snapshot{ModelProviderID: "provider-2", Model: "new"})
	if err != nil {
		t.Fatalf("CloneForTurn() error = %v", err)
	}
	if cloned.TurnSelection.ModelProviderID != "provider-2" || cloned.TurnSelection.Model != "new" {
		t.Fatalf("turn selection = %#v", cloned.TurnSelection)
	}
	cloned.Settings["custom_args"].([]any)[0] = "--changed"
	if got := original.Settings["custom_args"].([]any)[0]; got != "--safe" {
		t.Fatalf("original settings mutated to %#v", got)
	}
}
