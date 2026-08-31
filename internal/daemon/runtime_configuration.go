package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/skill"
)

type resolvedLaunchConfiguration struct {
	Profile  runtimeprofile.Profile
	Snapshot runtimeconfig.RuntimeConfigurationSnapshot
}

func (server *Server) resolveLaunchConfiguration(selection runtimeconfig.LaunchSelection, projectID string) (resolvedLaunchConfiguration, error) {
	profileID := strings.TrimSpace(selection.RuntimeProfileID)
	direct := strings.TrimSpace(selection.RuntimePluginID) != "" || strings.TrimSpace(selection.ModelProviderID) != ""
	if (profileID != "") == direct {
		return resolvedLaunchConfiguration{}, runtimeconfig.ErrInvalidLaunchSelection
	}

	var profile runtimeprofile.Profile
	var profileSource *runtimeconfig.ProfileSource
	var standardSettings map[string]any
	var configProjection map[string]any
	if profileID != "" {
		found, err := server.profiles.Get(profileID)
		if err != nil {
			return resolvedLaunchConfiguration{}, err
		}
		profile = found
		configProjection = server.runtimePluginConfigProjection(string(found.Provider))
		profileSource = &runtimeconfig.ProfileSource{
			ID: found.ID, Name: found.Name, RuntimePluginID: string(found.Provider),
			Runner: found.Fields.DefaultRunner, ModelProviderID: found.Fields.ModelProviderID,
			Model: firstRuntimeModel(found.Fields), ReasoningEffort: found.Fields.ReasoningEffort,
			Settings: runtimeProfileFieldsMap(found.Fields), ConfigProjection: configProjection,
		}
	} else {
		model := strings.TrimSpace(selection.Model)
		if model == "" {
			model = strings.TrimSpace(selection.ModelOverride)
		}
		profile = runtimeprofile.Profile{Provider: runtimeprofile.Provider(strings.TrimSpace(selection.RuntimePluginID)), Fields: runtimeprofile.Fields{
			ModelProviderID: strings.TrimSpace(selection.ModelProviderID), ModelOverride: model,
			ReasoningEffort: strings.TrimSpace(selection.ReasoningEffort), DefaultRunner: strings.TrimSpace(selection.Runner),
		}}
		standardSettings = server.runtimePluginStandardSettings(string(profile.Provider))
		configProjection = server.runtimePluginConfigProjection(string(profile.Provider))
	}

	var modelSnapshot modelprovider.Snapshot
	resolveStructuredProvider := profileID == "" || strings.TrimSpace(profile.Fields.ModelProviderID) != ""
	if resolveStructuredProvider {
		var err error
		modelSnapshot, err = modelprovider.Resolve(modelprovider.ResolveRequest{
			Profile: profile, Providers: server.modelProviders, Plugins: server.runtimePlugins,
			Credentials: server.creds, ProjectID: projectID, CheckEnv: false,
			LaunchModelOverride: firstNonBlank(selection.Model, selection.ModelOverride),
			CapabilityCache:     server.capabilityCache,
		})
		if err != nil {
			return resolvedLaunchConfiguration{}, err
		}
	} else {
		modelSnapshot.Model = firstNonBlank(selection.Model, selection.ModelOverride, firstRuntimeModel(profile.Fields))
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		modelSnapshot.ModelProviderID = "none"
		modelSnapshot.ModelProviderName = "No Model Provider"
		modelSnapshot.Model = firstNonBlank(firstRuntimeModel(profile.Fields), "fake")
		if profileSource != nil {
			profileSource.ModelProviderID = modelSnapshot.ModelProviderID
			profileSource.Model = modelSnapshot.Model
		} else {
			selection.ModelProviderID = modelSnapshot.ModelProviderID
			selection.Model = modelSnapshot.Model
		}
	}
	skillProfileID := ""
	if profileSource != nil {
		skillProfileID = profileSource.ID
	}
	enabled, err := server.skills.EnabledSkills(skillProfileID)
	if err != nil {
		return resolvedLaunchConfiguration{}, fmt.Errorf("resolve enabled Skills: %w", err)
	}
	skillIDs := make([]string, 0, len(enabled))
	for _, item := range enabled {
		skillIDs = append(skillIDs, item.ID)
	}
	if profileSource != nil {
		profileSource.EnabledSkillIDs = skillIDs
	}
	snapshot, err := runtimeconfig.Resolve(runtimeconfig.ResolveRequest{
		Selection: selection, Profile: profileSource, ModelProvider: modelSnapshot,
		StandardSettings: standardSettings, DefaultEnabledSkillIDs: skillIDs, ConfigProjection: configProjection,
	})
	if err != nil {
		return resolvedLaunchConfiguration{}, err
	}
	return resolvedLaunchConfiguration{Profile: profile, Snapshot: snapshot}, nil
}

func (server *Server) runtimePluginStandardSettings(pluginID string) map[string]any {
	plugin, ok := server.runtimePlugins.Get(pluginID)
	if !ok {
		return map[string]any{}
	}
	settings := map[string]any{}
	if field := strings.TrimSpace(plugin.Binary.ProfileField); field != "" {
		settings[field] = plugin.Binary.Default
	}
	return settings
}

func (server *Server) runtimePluginConfigProjection(pluginID string) map[string]any {
	plugin, ok := server.runtimePlugins.Get(pluginID)
	if !ok {
		return map[string]any{}
	}
	encoded, _ := json.Marshal(plugin.ConfigProjection)
	var projection map[string]any
	_ = json.Unmarshal(encoded, &projection)
	return projection
}

func runtimeProfileFieldsMap(fields runtimeprofile.Fields) map[string]any {
	encoded, _ := json.Marshal(fields)
	var values map[string]any
	_ = json.Unmarshal(encoded, &values)
	if values == nil {
		values = map[string]any{}
	}
	delete(values, "api_keys")
	return values
}

func runtimeSnapshotMap(snapshot runtimeconfig.RuntimeConfigurationSnapshot) map[string]any {
	encoded, _ := json.Marshal(snapshot)
	var values map[string]any
	_ = json.Unmarshal(encoded, &values)
	return values
}

func runtimeProfileFromSnapshot(config map[string]any) (runtimeprofile.Profile, error) {
	snapshot, err := decodeRuntimeSnapshot(config)
	if err != nil {
		return runtimeprofile.Profile{}, err
	}
	settings, _ := json.Marshal(snapshot.Settings)
	var fields runtimeprofile.Fields
	if err := json.Unmarshal(settings, &fields); err != nil {
		return runtimeprofile.Profile{}, err
	}
	fields.ModelProviderID = snapshot.TurnSelection.ModelProviderID
	fields.ModelOverride = snapshot.TurnSelection.Model
	fields.ReasoningEffort = snapshot.TurnSelection.ReasoningEffort
	fields.DefaultRunner = snapshot.Runner
	profile := runtimeprofile.Profile{Provider: runtimeprofile.Provider(snapshot.RuntimePluginID), Fields: fields}
	if snapshot.RuntimeProfile != nil {
		profile.ID = snapshot.RuntimeProfile.ID
		profile.Name = snapshot.RuntimeProfile.Name
	}
	return profile, nil
}

func decodeRuntimeSnapshot(config map[string]any) (runtimeconfig.RuntimeConfigurationSnapshot, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return runtimeconfig.RuntimeConfigurationSnapshot{}, err
	}
	var snapshot runtimeconfig.RuntimeConfigurationSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return runtimeconfig.RuntimeConfigurationSnapshot{}, err
	}
	if snapshot.SnapshotVersion != runtimeconfig.SnapshotVersion {
		return runtimeconfig.RuntimeConfigurationSnapshot{}, errors.New("runtime configuration snapshot is missing")
	}
	return snapshot, nil
}

func firstRuntimeModel(fields runtimeprofile.Fields) string {
	return firstNonBlank(fields.ModelOverride, fields.Model)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (server *Server) resolveSessionSnapshot(profile runtimeprofile.Profile, run session.Runner, input sessionRuntimeInput, provider modelprovider.Snapshot, previous *session.Continuation) (map[string]any, error) {
	turn := runtimeconfig.RuntimeTurnSelection{
		ModelProviderID: firstNonBlank(input.ModelProviderID, profile.Fields.ModelProviderID, provider.ModelProviderID),
		Model:           firstNonBlank(input.selectedModel(), profile.Fields.ModelOverride, profile.Fields.Model, provider.Model),
		ReasoningEffort: firstNonBlank(input.ReasoningEffort, profile.Fields.ReasoningEffort),
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		turn.ModelProviderID = firstNonBlank(turn.ModelProviderID, "none")
		turn.Model = firstNonBlank(turn.Model, "fake")
		provider.ModelProviderID = turn.ModelProviderID
		provider.Model = turn.Model
	}
	if previous != nil {
		versions, err := server.sessions.RuntimeConfigVersions(previous.SessionID)
		if err != nil || len(versions) == 0 {
			return nil, errors.New("runtime configuration snapshot is missing")
		}
		encoded, _ := json.Marshal(versions[len(versions)-1].Config)
		var prior runtimeconfig.RuntimeConfigurationSnapshot
		if err := json.Unmarshal(encoded, &prior); err != nil || prior.SnapshotVersion != runtimeconfig.SnapshotVersion {
			return nil, errors.New("runtime configuration snapshot is missing")
		}
		for _, skillID := range prior.EnabledSkillIDs {
			if _, err := server.skills.Get(skillID); err != nil {
				return nil, fmt.Errorf("captured Skill %s is unavailable", skillID)
			}
		}
		cloned, err := runtimeconfig.CloneForTurn(prior, turn, provider)
		if err != nil {
			return nil, err
		}
		cloned.Runner = string(run)
		return runtimeSnapshotMap(cloned), nil
	}

	selection := runtimeconfig.LaunchSelection{
		RuntimeProfileID: input.RuntimeProfileID,
		RuntimePluginID:  string(profile.Provider),
		ModelProviderID:  turn.ModelProviderID,
		Model:            turn.Model,
		ReasoningEffort:  turn.ReasoningEffort,
		Runner:           string(run),
	}
	var source *runtimeconfig.ProfileSource
	if strings.TrimSpace(profile.ID) != "" {
		selection.RuntimePluginID = ""
		selection.ModelProviderID = ""
		selection.Model = ""
		selection.ReasoningEffort = ""
		enabled, err := server.skills.EnabledSkills(profile.ID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(enabled))
		for _, item := range enabled {
			ids = append(ids, item.ID)
		}
		source = &runtimeconfig.ProfileSource{
			ID: profile.ID, Name: profile.Name, RuntimePluginID: string(profile.Provider), Runner: string(run),
			ModelProviderID: turn.ModelProviderID, Model: turn.Model, ReasoningEffort: turn.ReasoningEffort,
			Settings: runtimeProfileFieldsMap(profile.Fields), EnabledSkillIDs: ids,
			ConfigProjection: server.runtimePluginConfigProjection(string(profile.Provider)),
		}
	}
	defaultSkills := []string{}
	if source == nil {
		enabled, err := server.skills.EnabledSkills("")
		if err != nil {
			return nil, err
		}
		for _, item := range enabled {
			defaultSkills = append(defaultSkills, item.ID)
		}
	}
	snapshot, err := runtimeconfig.Resolve(runtimeconfig.ResolveRequest{
		Selection: selection, Profile: source, ModelProvider: provider,
		StandardSettings:       server.runtimePluginStandardSettings(string(profile.Provider)),
		DefaultEnabledSkillIDs: defaultSkills,
		ConfigProjection:       server.runtimePluginConfigProjection(string(profile.Provider)),
	})
	if err != nil {
		return nil, err
	}
	return runtimeSnapshotMap(snapshot), nil
}

func (server *Server) taskSnapshotSkillBundles(foundTaskID string) ([]skill.Bundle, error) {
	versions, err := server.tasks.RuntimeConfigVersions(foundTaskID)
	if err != nil || len(versions) == 0 {
		return nil, errors.New("runtime configuration snapshot is missing")
	}
	snapshot, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return nil, err
	}
	return server.runtimeSnapshotSkillBundles(snapshot)
}

func (server *Server) runtimeSnapshotSkillBundles(snapshot runtimeconfig.RuntimeConfigurationSnapshot) ([]skill.Bundle, error) {
	bundles := make([]skill.Bundle, 0, len(snapshot.EnabledSkillIDs))
	for _, skillID := range snapshot.EnabledSkillIDs {
		captured, err := server.skills.Get(skillID)
		if err != nil {
			return nil, fmt.Errorf("captured Skill %s is unavailable", skillID)
		}
		bundles = append(bundles, skill.Bundle{ID: captured.ID, Name: captured.Name, Source: captured.Source, Path: captured.BundlePath})
	}
	return bundles, nil
}

func (server *Server) latestSessionRuntimeSnapshot(sessionID string) (*runtimeconfig.RuntimeConfigurationSnapshot, error) {
	versions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, nil
	}
	snapshot, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (server *Server) cloneSnapshotForTurn(prior runtimeconfig.RuntimeConfigurationSnapshot, projectID string, turn runtimeconfig.RuntimeTurnSelection) (runtimeconfig.RuntimeConfigurationSnapshot, error) {
	profileMap := runtimeSnapshotMap(prior)
	profile, err := runtimeProfileFromSnapshot(profileMap)
	if err != nil {
		return runtimeconfig.RuntimeConfigurationSnapshot{}, err
	}
	turn.ModelProviderID = firstNonBlank(turn.ModelProviderID, prior.TurnSelection.ModelProviderID)
	turn.Model = firstNonBlank(turn.Model, prior.TurnSelection.Model)
	turn.ReasoningEffort = firstNonBlank(turn.ReasoningEffort, prior.TurnSelection.ReasoningEffort)
	profile.Fields.ModelProviderID = turn.ModelProviderID
	profile.Fields.ModelOverride = turn.Model
	profile.Fields.ReasoningEffort = turn.ReasoningEffort
	if turn.ModelProviderID == prior.TurnSelection.ModelProviderID {
		provider := prior.ModelProvider
		provider.Model = turn.Model
		return runtimeconfig.CloneForTurn(prior, turn, provider)
	}
	provider, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: profile, Providers: server.modelProviders, Plugins: server.runtimePlugins,
		Credentials: server.creds, ProjectID: projectID, CheckEnv: true,
		LaunchModelOverride: turn.Model, CapabilityCache: server.capabilityCache,
	})
	if err != nil {
		return runtimeconfig.RuntimeConfigurationSnapshot{}, err
	}
	return runtimeconfig.CloneForTurn(prior, turn, provider)
}
