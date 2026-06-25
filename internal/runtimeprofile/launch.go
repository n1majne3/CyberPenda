package runtimeprofile

import (
	"fmt"
	"strings"
)

// LaunchSelection is the minimal launch-time runtime configuration most tasks need.
type LaunchSelection struct {
	Provider        Provider
	ModelProviderID string
	ModelOverride   string
}

// LaunchResolution is the profile used for a launch selection.
type LaunchResolution struct {
	Profile Profile
	Created bool
}

func normalizeLaunchSelection(selection LaunchSelection) (LaunchSelection, error) {
	provider := Provider(strings.TrimSpace(string(selection.Provider)))
	if !providers[provider] || provider == ProviderFake {
		return LaunchSelection{}, fmt.Errorf("unsupported launch runtime %q", selection.Provider)
	}
	modelProviderID := strings.TrimSpace(selection.ModelProviderID)
	if modelProviderID == "" {
		return LaunchSelection{}, fmt.Errorf("model provider is required")
	}
	return LaunchSelection{
		Provider:        provider,
		ModelProviderID: modelProviderID,
		ModelOverride:   strings.TrimSpace(selection.ModelOverride),
	}, nil
}

// MatchesLaunch reports whether a profile satisfies a launch selection. Profiles
// with additional MCP or extension configuration still match.
func MatchesLaunch(profile Profile, selection LaunchSelection) bool {
	normalized, err := normalizeLaunchSelection(selection)
	if err != nil {
		return false
	}
	if profile.Provider != normalized.Provider {
		return false
	}
	if strings.TrimSpace(profile.Fields.ModelProviderID) != normalized.ModelProviderID {
		return false
	}
	if strings.TrimSpace(profile.Fields.ModelOverride) != normalized.ModelOverride {
		return false
	}
	return true
}

// FindLaunchProfile returns the first profile that matches the launch selection.
func FindLaunchProfile(profiles []Profile, selection LaunchSelection) (Profile, bool) {
	normalized, err := normalizeLaunchSelection(selection)
	if err != nil {
		return Profile{}, false
	}
	for _, profile := range profiles {
		if MatchesLaunch(profile, normalized) {
			return profile, true
		}
	}
	return Profile{}, false
}

// ResolveLaunchProfile finds or creates a profile for a launch selection.
func (s *Service) ResolveLaunchProfile(selection LaunchSelection, providerName string) (LaunchResolution, error) {
	normalized, err := normalizeLaunchSelection(selection)
	if err != nil {
		return LaunchResolution{}, err
	}
	profiles, err := s.List()
	if err != nil {
		return LaunchResolution{}, err
	}
	if found, ok := FindLaunchProfile(profiles, normalized); ok {
		return LaunchResolution{Profile: found, Created: false}, nil
	}
	name := LaunchProfileName(normalized, providerName)
	created, err := s.Create(name, normalized.Provider, Fields{
		ModelProviderID: normalized.ModelProviderID,
		ModelOverride:   normalized.ModelOverride,
		DefaultRunner:   "sandbox",
	})
	if err != nil {
		return LaunchResolution{}, err
	}
	return LaunchResolution{Profile: created, Created: true}, nil
}

func LaunchProfileName(selection LaunchSelection, modelProviderName string) string {
	normalized, err := normalizeLaunchSelection(selection)
	if err != nil {
		return "launch"
	}
	runtimeLabel := string(normalized.Provider)
	switch normalized.Provider {
	case ProviderCodex:
		runtimeLabel = "Codex"
	case ProviderClaudeCode:
		runtimeLabel = "Claude Code"
	case ProviderPi:
		runtimeLabel = "Pi"
	}
	providerLabel := strings.TrimSpace(modelProviderName)
	if providerLabel == "" {
		providerLabel = normalized.ModelProviderID
	}
	if normalized.ModelOverride != "" {
		return fmt.Sprintf("%s · %s · %s", runtimeLabel, providerLabel, normalized.ModelOverride)
	}
	return fmt.Sprintf("%s · %s", runtimeLabel, providerLabel)
}