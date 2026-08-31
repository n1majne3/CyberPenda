// Package runtimeconfig owns immutable, Runtime Owner-local configuration.
// It does not persist or resolve global Runtime Profiles. Callers adapt an
// explicitly selected Profile into ProfileSource once, at owner creation.
package runtimeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/modelprovider"
)

const SnapshotVersion = 1

var (
	ErrInvalidLaunchSelection = errors.New("select either a runtime profile or direct runtime configuration")
	ErrMissingProfileSource   = errors.New("selected runtime profile is unavailable")
	ErrInvalidSnapshot        = errors.New("runtime configuration snapshot is invalid")
)

// LaunchSelection is the mutually exclusive input accepted by Preflight and
// Runtime Owner creation. ModelOverride is only a legacy request alias.
type LaunchSelection struct {
	RuntimeProfileID string `json:"runtime_profile_id,omitempty"`
	RuntimePluginID  string `json:"runtime_plugin_id,omitempty"`
	ModelProviderID  string `json:"model_provider_id,omitempty"`
	Model            string `json:"model,omitempty"`
	ModelOverride    string `json:"model_override,omitempty"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
	Runner           string `json:"runner,omitempty"`
}

// RuntimeTurnSelection is the only mutable selection at a continuation or
// steering boundary.
type RuntimeTurnSelection struct {
	ModelProviderID string `json:"model_provider_id"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"requested_reasoning_effort,omitempty"`
}

type ProfileProvenance struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProfileSource is the one-time adapter input for an explicitly selected
// Runtime Profile. Settings must contain only non-secret values.
type ProfileSource struct {
	ID               string
	Name             string
	RuntimePluginID  string
	Runner           string
	ModelProviderID  string
	Model            string
	ReasoningEffort  string
	Settings         map[string]any
	EnabledSkillIDs  []string
	ConfigProjection map[string]any
}

// RuntimeConfigurationSnapshot is the complete non-secret configuration used
// by one Runtime Owner configuration version.
type RuntimeConfigurationSnapshot struct {
	SnapshotVersion  int                    `json:"snapshot_version"`
	RuntimeProfile   *ProfileProvenance     `json:"runtime_profile,omitempty"`
	RuntimePluginID  string                 `json:"runtime_plugin_id"`
	Runner           string                 `json:"runner"`
	ModelProvider    modelprovider.Snapshot `json:"model_provider_snapshot"`
	TurnSelection    RuntimeTurnSelection   `json:"runtime_turn_selection"`
	Settings         map[string]any         `json:"settings"`
	EnabledSkillIDs  []string               `json:"enabled_skill_ids"`
	ConfigProjection map[string]any         `json:"config_projection,omitempty"`
}

// Summary is the stable Task/Session API view of the latest Snapshot.
type Summary struct {
	RuntimeProfileID   string `json:"runtime_profile_id,omitempty"`
	RuntimeProfileName string `json:"runtime_profile_name,omitempty"`
	RuntimePluginID    string `json:"runtime_plugin_id"`
	Runner             string `json:"runner"`
	ModelProviderID    string `json:"model_provider_id"`
	ModelProviderName  string `json:"model_provider_name,omitempty"`
	Model              string `json:"model"`
	ReasoningEffort    string `json:"requested_reasoning_effort,omitempty"`
}

func Summarize(snapshot RuntimeConfigurationSnapshot) Summary {
	summary := Summary{
		RuntimePluginID: snapshot.RuntimePluginID, Runner: snapshot.Runner,
		ModelProviderID:   snapshot.TurnSelection.ModelProviderID,
		ModelProviderName: snapshot.ModelProvider.ModelProviderName,
		Model:             snapshot.TurnSelection.Model, ReasoningEffort: snapshot.TurnSelection.ReasoningEffort,
	}
	if snapshot.RuntimeProfile != nil {
		summary.RuntimeProfileID = snapshot.RuntimeProfile.ID
		summary.RuntimeProfileName = snapshot.RuntimeProfile.Name
	}
	return summary
}

type ResolveRequest struct {
	Selection              LaunchSelection
	Profile                *ProfileSource
	StandardSettings       map[string]any
	ModelProvider          modelprovider.Snapshot
	DefaultEnabledSkillIDs []string
	ConfigProjection       map[string]any
}

// Resolve creates a detached Snapshot. It performs no database writes.
func Resolve(req ResolveRequest) (RuntimeConfigurationSnapshot, error) {
	selection := normalizeSelection(req.Selection)
	profileSelected := selection.RuntimeProfileID != ""
	directSelected := selection.RuntimePluginID != "" || selection.ModelProviderID != ""
	if profileSelected == directSelected {
		return RuntimeConfigurationSnapshot{}, ErrInvalidLaunchSelection
	}

	var snapshot RuntimeConfigurationSnapshot
	snapshot.SnapshotVersion = SnapshotVersion
	snapshot.ModelProvider = req.ModelProvider
	if profileSelected {
		if req.Profile == nil || strings.TrimSpace(req.Profile.ID) != selection.RuntimeProfileID {
			return RuntimeConfigurationSnapshot{}, ErrMissingProfileSource
		}
		snapshot.RuntimeProfile = &ProfileProvenance{ID: strings.TrimSpace(req.Profile.ID), Name: strings.TrimSpace(req.Profile.Name)}
		snapshot.RuntimePluginID = strings.TrimSpace(req.Profile.RuntimePluginID)
		snapshot.Runner = firstNonEmpty(selection.Runner, req.Profile.Runner)
		snapshot.Settings = cloneMap(req.Profile.Settings)
		snapshot.EnabledSkillIDs = cloneStrings(req.Profile.EnabledSkillIDs)
		snapshot.ConfigProjection = cloneMap(req.Profile.ConfigProjection)
		snapshot.TurnSelection = RuntimeTurnSelection{
			ModelProviderID: firstNonEmpty(selection.ModelProviderID, req.Profile.ModelProviderID, req.ModelProvider.ModelProviderID),
			Model:           firstNonEmpty(selection.Model, req.Profile.Model, req.ModelProvider.Model),
			ReasoningEffort: firstNonEmpty(selection.ReasoningEffort, req.Profile.ReasoningEffort),
		}
	} else {
		if selection.RuntimePluginID == "" || selection.ModelProviderID == "" || selection.Model == "" {
			return RuntimeConfigurationSnapshot{}, ErrInvalidLaunchSelection
		}
		snapshot.RuntimePluginID = selection.RuntimePluginID
		snapshot.Runner = selection.Runner
		snapshot.Settings = cloneMap(req.StandardSettings)
		snapshot.EnabledSkillIDs = cloneStrings(req.DefaultEnabledSkillIDs)
		snapshot.ConfigProjection = cloneMap(req.ConfigProjection)
		snapshot.TurnSelection = RuntimeTurnSelection{
			ModelProviderID: selection.ModelProviderID,
			Model:           selection.Model,
			ReasoningEffort: selection.ReasoningEffort,
		}
	}
	if snapshot.Settings == nil {
		snapshot.Settings = map[string]any{}
	}
	if snapshot.EnabledSkillIDs == nil {
		snapshot.EnabledSkillIDs = []string{}
	}
	return snapshot, validate(snapshot)
}

// CloneForTurn copies a prior Snapshot and replaces only Runtime Turn
// Selection plus its resolved Model Provider Snapshot.
func CloneForTurn(prior RuntimeConfigurationSnapshot, turn RuntimeTurnSelection, provider modelprovider.Snapshot) (RuntimeConfigurationSnapshot, error) {
	if err := validate(prior); err != nil {
		return RuntimeConfigurationSnapshot{}, err
	}
	cloned, err := cloneSnapshot(prior)
	if err != nil {
		return RuntimeConfigurationSnapshot{}, err
	}
	turn.ModelProviderID = strings.TrimSpace(turn.ModelProviderID)
	turn.Model = strings.TrimSpace(turn.Model)
	turn.ReasoningEffort = strings.TrimSpace(turn.ReasoningEffort)
	if cloned.RuntimeProfile == nil && (turn.ModelProviderID == "" || turn.Model == "") {
		return RuntimeConfigurationSnapshot{}, ErrInvalidLaunchSelection
	}
	cloned.TurnSelection = turn
	cloned.ModelProvider = provider
	return cloned, nil
}

func normalizeSelection(selection LaunchSelection) LaunchSelection {
	selection.RuntimeProfileID = strings.TrimSpace(selection.RuntimeProfileID)
	selection.RuntimePluginID = strings.TrimSpace(selection.RuntimePluginID)
	selection.ModelProviderID = strings.TrimSpace(selection.ModelProviderID)
	selection.Model = firstNonEmpty(selection.Model, selection.ModelOverride)
	selection.ModelOverride = ""
	selection.ReasoningEffort = strings.TrimSpace(selection.ReasoningEffort)
	selection.Runner = strings.TrimSpace(selection.Runner)
	return selection
}

func validate(snapshot RuntimeConfigurationSnapshot) error {
	if snapshot.SnapshotVersion != SnapshotVersion || strings.TrimSpace(snapshot.RuntimePluginID) == "" {
		return ErrInvalidSnapshot
	}
	// Direct configuration is always a complete structured selection. An
	// explicit legacy Runtime Profile can instead carry provider-native fields
	// in Settings; keep that captured state readable without inventing a global
	// Model Provider identity.
	if snapshot.RuntimeProfile == nil && (strings.TrimSpace(snapshot.TurnSelection.ModelProviderID) == "" || strings.TrimSpace(snapshot.TurnSelection.Model) == "") {
		return ErrInvalidSnapshot
	}
	return nil
}

func cloneSnapshot(snapshot RuntimeConfigurationSnapshot) (RuntimeConfigurationSnapshot, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return RuntimeConfigurationSnapshot{}, fmt.Errorf("encode runtime configuration snapshot: %w", err)
	}
	var cloned RuntimeConfigurationSnapshot
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return RuntimeConfigurationSnapshot{}, fmt.Errorf("decode runtime configuration snapshot: %w", err)
	}
	return cloned, nil
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil
	}
	return cloned
}

func cloneStrings(input []string) []string {
	if input == nil {
		return nil
	}
	return append([]string(nil), input...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
