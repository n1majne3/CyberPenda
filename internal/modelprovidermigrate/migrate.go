// Package modelprovidermigrate moves legacy runtime-profile model-service fields
// into reusable model providers with explicit user confirmation.
package modelprovidermigrate

import (
	"errors"
	"fmt"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

type Action string

const (
	ActionCreate Action = "create"
	ActionReuse  Action = "reuse"
)

var (
	ErrNotFound            = errors.New("runtime profile not found")
	ErrNotEligible         = errors.New("runtime profile is not eligible for model provider migration")
	ErrMissingProviderID   = errors.New("provider_id is required when reusing an existing model provider")
	ErrProviderNotFound    = errors.New("model provider not found")
	ErrIncompatibleProfile = errors.New("runtime profile provider does not require model provider migration")
)

type APIKeySourcePreview struct {
	Kind          string `json:"kind"`
	CredentialRef string `json:"credential_ref,omitempty"`
	EnvVar        string `json:"env_var,omitempty"`
	Configured    bool   `json:"configured"`
}

type ProposedProvider struct {
	Name              string                   `json:"name"`
	BaseURL           string                   `json:"base_url"`
	Model             string                   `json:"model,omitempty"`
	Protocols         []modelprovider.Protocol `json:"protocols"`
	Endpoints         []modelprovider.Endpoint `json:"endpoints,omitempty"`
	SuggestedProtocol modelprovider.Protocol   `json:"suggested_protocol,omitempty"`
	APIKeyEnv         string                   `json:"api_key_env,omitempty"`
}

type ProviderMatch struct {
	Provider modelprovider.Provider `json:"provider"`
}

type Preview struct {
	ProfileID         string                `json:"profile_id"`
	ProfileName       string                `json:"profile_name"`
	RuntimeProvider   string                `json:"runtime_provider"`
	Eligible          bool                  `json:"eligible"`
	Reason            string                `json:"reason,omitempty"`
	Proposed          ProposedProvider      `json:"proposed"`
	Matches           []ProviderMatch       `json:"matches"`
	APIKeySources     []APIKeySourcePreview `json:"api_key_sources"`
}

type ApplyRequest struct {
	ProfileID     string
	Action        Action
	ProviderID    string
	ProviderName  string
	MigrateAPIKey bool
}

type ApplyResult struct {
	Profile  runtimeprofile.Profile  `json:"profile"`
	Provider modelprovider.Provider  `json:"provider"`
}

type Service struct {
	profiles  *runtimeprofile.Service
	providers *modelprovider.Service
	creds     *credential.Service
	plugins   *runtimeplugin.Registry
}

func NewService(
	profiles *runtimeprofile.Service,
	providers *modelprovider.Service,
	creds *credential.Service,
	plugins *runtimeplugin.Registry,
) *Service {
	if plugins == nil {
		plugins = runtimeplugin.MustBuiltinRegistry()
	}
	return &Service{
		profiles:  profiles,
		providers: providers,
		creds:     creds,
		plugins:   plugins,
	}
}

func (s *Service) Preview(profileID string) (Preview, error) {
	profile, plugin, err := s.loadProfile(profileID)
	if err != nil {
		return Preview{}, err
	}
	return buildPreview(profile, plugin, s.listProviders(), s.creds), nil
}

func (s *Service) Apply(req ApplyRequest) (ApplyResult, error) {
	profile, plugin, err := s.loadProfile(req.ProfileID)
	if err != nil {
		return ApplyResult{}, err
	}
	preview := buildPreview(profile, plugin, s.listProviders(), s.creds)
	if !preview.Eligible {
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrNotEligible, preview.Reason)
	}

	var provider modelprovider.Provider
	switch req.Action {
	case ActionReuse:
		if strings.TrimSpace(req.ProviderID) == "" {
			return ApplyResult{}, ErrMissingProviderID
		}
		provider, err = s.providers.Get(req.ProviderID)
		if err != nil {
			if errors.Is(err, modelprovider.ErrNotFound) {
				return ApplyResult{}, ErrProviderNotFound
			}
			return ApplyResult{}, err
		}
	case ActionCreate, "":
		name := strings.TrimSpace(req.ProviderName)
		if name == "" {
			name = preview.Proposed.Name
		}
		provider, err = s.providers.Create(modelprovider.CreateRequest{
			Name:      name,
			BaseURL:   preview.Proposed.BaseURL,
			Protocols: preview.Proposed.Protocols,
			Catalog: modelprovider.Catalog{
				Manual:       manualCatalog(preview.Proposed.Model),
				DefaultModel: preview.Proposed.Model,
			},
		})
		if err != nil {
			return ApplyResult{}, err
		}
	default:
		return ApplyResult{}, fmt.Errorf("unsupported migration action %q", req.Action)
	}

	if req.MigrateAPIKey {
		if err := s.migrateAPIKey(profile, plugin, provider); err != nil {
			return ApplyResult{}, err
		}
	}

	updatedFields := ClearLegacyModelFields(profile.Fields, profile.Provider)
	updatedFields.ModelProviderID = provider.ID
	if preview.Proposed.Model != "" && preview.Proposed.Model != provider.Catalog.DefaultModel {
		updatedFields.ModelOverride = preview.Proposed.Model
	}

	updated, err := s.profiles.ReplaceFields(profile.ID, updatedFields)
	if err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{Profile: updated, Provider: provider}, nil
}

func (s *Service) loadProfile(profileID string) (runtimeprofile.Profile, runtimeplugin.Plugin, error) {
	profile, err := s.profiles.Get(profileID)
	if err != nil {
		if errors.Is(err, runtimeprofile.ErrNotFound) {
			return runtimeprofile.Profile{}, runtimeplugin.Plugin{}, ErrNotFound
		}
		return runtimeprofile.Profile{}, runtimeplugin.Plugin{}, err
	}
	plugin, ok := s.plugins.Get(string(profile.Provider))
	if !ok {
		return runtimeprofile.Profile{}, runtimeplugin.Plugin{}, ErrIncompatibleProfile
	}
	return profile, plugin, nil
}

func (s *Service) listProviders() []modelprovider.Provider {
	providers, err := s.providers.List()
	if err != nil {
		return nil
	}
	return providers
}

func buildPreview(
	profile runtimeprofile.Profile,
	plugin runtimeplugin.Plugin,
	providers []modelprovider.Provider,
	creds *credential.Service,
) Preview {
	out := Preview{
		ProfileID:       profile.ID,
		ProfileName:     profile.Name,
		RuntimeProvider: string(profile.Provider),
		Matches:         []ProviderMatch{},
		APIKeySources:   []APIKeySourcePreview{},
	}
	if plugin.ModelProvider.Requirement != "required" {
		out.Reason = "runtime does not require a model provider"
		return out
	}
	if strings.TrimSpace(profile.Fields.ModelProviderID) != "" {
		out.Reason = "runtime profile already references a model provider"
		return out
	}
	baseURL := extractBaseURL(profile)
	model := extractModel(profile)
	if baseURL == "" {
		out.Reason = "no legacy endpoint/base URL found to migrate"
		return out
	}
	protocols := suggestedProtocols(plugin)
	suggested := modelprovider.Protocol("")
	if len(protocols) > 0 {
		suggested = protocols[0]
	}
	endpoints, err := modelprovider.BackfillEndpoints(baseURL, protocols)
	if err != nil {
		out.Reason = fmt.Sprintf("could not derive provider endpoints: %s", err)
		return out
	}
	proposedName := strings.TrimSpace(profile.Name)
	if proposedName == "" {
		proposedName = "Migrated provider"
	}
	out.Eligible = true
	out.Proposed = ProposedProvider{
		Name:              proposedName,
		BaseURL:           baseURL,
		Model:             model,
		Protocols:         protocols,
		Endpoints:         endpoints,
		SuggestedProtocol: suggested,
	}
	out.APIKeySources = nonNilAPIKeySources(extractAPIKeySources(profile, plugin, creds))
	out.Matches = nonNilMatches(findMatches(baseURL, providers))
	return out
}

func nonNilMatches(matches []ProviderMatch) []ProviderMatch {
	if matches == nil {
		return []ProviderMatch{}
	}
	return matches
}

func nonNilAPIKeySources(sources []APIKeySourcePreview) []APIKeySourcePreview {
	if sources == nil {
		return []APIKeySourcePreview{}
	}
	return sources
}

func extractBaseURL(profile runtimeprofile.Profile) string {
	if endpoint := strings.TrimSpace(profile.Fields.Endpoint); endpoint != "" {
		normalized, err := modelprovider.NormalizeBaseURL(endpoint)
		if err == nil {
			return normalized
		}
		return strings.TrimRight(endpoint, "/")
	}
	switch profile.Provider {
	case runtimeprofile.ProviderCodex:
		if value := strings.TrimSpace(profile.Fields.Env["OPENAI_BASE_URL"]); value != "" {
			normalized, err := modelprovider.NormalizeBaseURL(value)
			if err == nil {
				return normalized
			}
			return strings.TrimRight(value, "/")
		}
	case runtimeprofile.ProviderClaudeCode:
		if value := strings.TrimSpace(profile.Fields.Env["ANTHROPIC_BASE_URL"]); value != "" {
			normalized, err := modelprovider.NormalizeBaseURL(value)
			if err == nil {
				return normalized
			}
			return strings.TrimRight(value, "/")
		}
	}
	return ""
}

func extractModel(profile runtimeprofile.Profile) string {
	if model := strings.TrimSpace(profile.Fields.Model); model != "" {
		return model
	}
	switch profile.Provider {
	case runtimeprofile.ProviderClaudeCode:
		return strings.TrimSpace(profile.Fields.Env["ANTHROPIC_MODEL"])
	}
	return ""
}

func suggestedProtocols(plugin runtimeplugin.Plugin) []modelprovider.Protocol {
	var out []modelprovider.Protocol
	for _, protocol := range plugin.ModelProvider.SupportedProtocols {
		out = append(out, modelprovider.Protocol(protocol))
	}
	return out
}

func extractAPIKeySources(profile runtimeprofile.Profile, plugin runtimeplugin.Plugin, creds *credential.Service) []APIKeySourcePreview {
	var out []APIKeySourcePreview
	inline := runtimeprofile.MaterializedAPIKeys(profile)
	for _, envName := range plugin.CredentialEnv {
		envName = strings.TrimSpace(envName)
		if envName == "" {
			continue
		}
		if value, ok := inline[envName]; ok && strings.TrimSpace(value) != "" {
			out = append(out, APIKeySourcePreview{
				Kind:       "inline_api_key",
				EnvVar:     envName,
				Configured: true,
			})
			continue
		}
		if creds != nil {
			if resolution, err := creds.Resolve(envName, ""); err == nil && resolution.Found && !resolution.Disabled && resolution.Source != nil {
				out = append(out, APIKeySourcePreview{
					Kind:          "credential_binding",
					CredentialRef: envName,
					Configured:    true,
				})
				continue
			}
		}
		out = append(out, APIKeySourcePreview{
			Kind:   "env_var",
			EnvVar: envName,
		})
	}
	return out
}

func findMatches(baseURL string, providers []modelprovider.Provider) []ProviderMatch {
	var out []ProviderMatch
	for _, provider := range providers {
		if provider.BaseURL == baseURL {
			out = append(out, ProviderMatch{Provider: provider})
		}
	}
	return out
}

func manualCatalog(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	return []string{model}
}

func (s *Service) migrateAPIKey(profile runtimeprofile.Profile, plugin runtimeplugin.Plugin, provider modelprovider.Provider) error {
	inline := runtimeprofile.MaterializedAPIKeys(profile)
	for _, envName := range plugin.CredentialEnv {
		envName = strings.TrimSpace(envName)
		if value, ok := inline[envName]; ok && strings.TrimSpace(value) != "" {
			_, err := s.creds.Upsert(provider.APIKeyEnv, credential.ScopeGlobal, "", credential.Source{
				Kind:  credential.SourceLiteral,
				Value: strings.TrimSpace(value),
			}, false)
			return err
		}
	}
	return nil
}

// ClearLegacyModelFields removes migrated model-service fields while keeping
// unrelated runtime profile configuration.
func ClearLegacyModelFields(fields runtimeprofile.Fields, provider runtimeprofile.Provider) runtimeprofile.Fields {
	next := fields
	next.Endpoint = ""
	next.Model = ""
	next.APIKeys = clearModelAPIKeys(next.APIKeys, provider)
	next.Env = clearModelEnvKeys(next.Env, provider)
	next.CredentialRefs = removeModelCredentialRefs(next.CredentialRefs, provider)
	return next
}

func clearModelAPIKeys(keys map[string]string, provider runtimeprofile.Provider) map[string]string {
	if len(keys) == 0 {
		return nil
	}
	legacy := legacyCredentialEnvNames(provider)
	out := make(map[string]string, len(keys))
	for key, value := range keys {
		if legacy[strings.ToUpper(strings.TrimSpace(key))] {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func clearModelEnvKeys(env map[string]string, provider runtimeprofile.Provider) map[string]string {
	if len(env) == 0 {
		return nil
	}
	legacy := legacyEnvKeys(provider)
	out := make(map[string]string, len(env))
	for key, value := range env {
		if legacy[key] {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func removeModelCredentialRefs(refs []string, provider runtimeprofile.Provider) []string {
	if len(refs) == 0 {
		return nil
	}
	legacy := legacyCredentialEnvNames(provider)
	var out []string
	for _, ref := range refs {
		if legacy[strings.ToUpper(strings.TrimSpace(ref))] {
			continue
		}
		out = append(out, ref)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func legacyEnvKeys(provider runtimeprofile.Provider) map[string]bool {
	keys := map[string]bool{}
	for _, key := range []string{
		"OPENAI_BASE_URL",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_MODEL",
		"CODEX_MODEL_PROVIDER",
		"CODEX_WIRE_API",
		"CODEX_PROVIDER_NAME",
		"PI_PROVIDER_ID",
		"PI_API",
	} {
		keys[key] = true
	}
	_ = provider
	return keys
}

func legacyCredentialEnvNames(provider runtimeprofile.Provider) map[string]bool {
	names := map[string]bool{}
	for _, envName := range []string{
		runtimeprofile.DefaultAPIKeyEnv(provider),
		"OPENAI_API_KEY",
		"CODEX_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_API_KEY",
	} {
		if envName = strings.ToUpper(strings.TrimSpace(envName)); envName != "" {
			names[envName] = true
		}
	}
	return names
}

