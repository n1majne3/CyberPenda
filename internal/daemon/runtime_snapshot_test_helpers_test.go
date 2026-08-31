package daemon

import (
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
)

// testTaskRuntimeSnapshot gives low-level daemon tests the same owner-local
// boundary that the HTTP create flow writes atomically in production.
func testTaskRuntimeSnapshot(t *testing.T, server *Server, profile runtimeprofile.Profile, runner task.Runner) map[string]any {
	return testRuntimeSnapshot(t, server, profile, string(runner))
}

func testSessionProfileRuntimeSnapshot(t *testing.T, server *Server, profile runtimeprofile.Profile, runner session.Runner) map[string]any {
	return testRuntimeSnapshot(t, server, profile, string(runner))
}

func testRuntimeSnapshot(t *testing.T, server *Server, profile runtimeprofile.Profile, runner string) map[string]any {
	t.Helper()
	model := firstNonBlank(profile.Fields.ModelOverride, profile.Fields.Model, "test-model")
	provider, err := server.modelProviders.Get(profile.Fields.ModelProviderID)
	createdProvider := false
	if profile.Fields.ModelProviderID == "" || err != nil {
		provider, err = server.modelProviders.Create(modelprovider.CreateRequest{
			Name: string(profile.Provider) + " test provider", BaseURL: "https://api.example.test/v1",
			Protocols: []modelprovider.Protocol{
				modelprovider.ProtocolOpenAIResponses,
				modelprovider.ProtocolOpenAIChatCompletions,
				modelprovider.ProtocolAnthropicMessages,
			},
			Catalog: modelprovider.Catalog{Manual: []string{model}, DefaultModel: model},
		})
		if err != nil {
			t.Fatalf("create test Model Provider: %v", err)
		}
		createdProvider = true
	}
	if createdProvider {
		t.Setenv(provider.APIKeyEnv, "sk-test")
	}
	providerID := provider.ID
	profile.Fields.ModelProviderID = providerID
	profile.Fields.ModelOverride = model
	enabled, err := server.skills.EnabledSkills(profile.ID)
	if err != nil {
		t.Fatalf("resolve profile Skills: %v", err)
	}
	skillIDs := make([]string, 0, len(enabled))
	for _, item := range enabled {
		skillIDs = append(skillIDs, item.ID)
	}
	snapshot := runtimeconfig.RuntimeConfigurationSnapshot{
		SnapshotVersion: runtimeconfig.SnapshotVersion,
		RuntimeProfile:  &runtimeconfig.ProfileProvenance{ID: profile.ID, Name: profile.Name},
		RuntimePluginID: string(profile.Provider), Runner: runner,
		TurnSelection: runtimeconfig.RuntimeTurnSelection{
			ModelProviderID: providerID, Model: model, ReasoningEffort: profile.Fields.ReasoningEffort,
		},
		Settings: runtimeProfileFieldsMap(profile.Fields), EnabledSkillIDs: skillIDs,
		ConfigProjection: map[string]any{},
	}
	snapshot.ModelProvider.ModelProviderID = providerID
	snapshot.ModelProvider.ModelProviderName = provider.Name
	snapshot.ModelProvider.BaseURL = provider.BaseURL
	snapshot.ModelProvider.Model = model
	return runtimeSnapshotMap(snapshot)
}

func createSnapshotTestModelProvider(t *testing.T, server *Server, model string) modelprovider.Provider {
	t.Helper()
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "snapshot test provider", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{
			modelprovider.ProtocolOpenAIResponses,
			modelprovider.ProtocolOpenAIChatCompletions,
			modelprovider.ProtocolAnthropicMessages,
		},
		Catalog: modelprovider.Catalog{Manual: []string{model}, DefaultModel: model},
	})
	if err != nil {
		t.Fatalf("create snapshot test Model Provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	return provider
}
