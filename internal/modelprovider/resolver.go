package modelprovider

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

type ProviderGetter interface {
	Get(id string) (Provider, error)
}

// ProviderLister lists every global Model Provider. Used by Pi Config
// Projection to project all launch-ready providers into one runtime.
type ProviderLister interface {
	ProviderGetter
	List() ([]Provider, error)
}

type ResolveRequest struct {
	Profile             runtimeprofile.Profile
	Providers           ProviderGetter
	Plugins             *runtimeplugin.Registry
	Credentials         *credential.Service
	ProjectID           string
	CheckEnv            bool
	LaunchModelOverride string
}

type Snapshot struct {
	ModelProviderID   string   `json:"model_provider_id"`
	ModelProviderName string   `json:"model_provider_name"`
	EndpointBaseURL   string   `json:"endpoint_base_url"`
	BaseURL           string   `json:"base_url"`
	Protocol          Protocol `json:"protocol"`
	Model             string   `json:"model"`
	APIKeyEnv         string   `json:"api_key_env"`
	APIKeySource      string   `json:"api_key_source"`
	ProjectionTarget  string   `json:"projection_target"`
}

var (
	ErrMissingProvider      = errors.New("model provider is required")
	ErrIncompatibleProtocol = errors.New("model provider protocol is incompatible with runtime")
	ErrMissingModel         = errors.New("model provider model is required")
	ErrMissingAPIKeyEnv     = errors.New("model provider API key environment variable is not configured")
)

func Resolve(req ResolveRequest) (Snapshot, error) {
	plugin, ok := runtimePluginForProfile(req.Profile, req.Plugins)
	if !ok || plugin.ModelProvider.Requirement == "" || plugin.ModelProvider.Requirement == "none" {
		return Snapshot{}, nil
	}
	if strings.TrimSpace(req.Profile.Fields.ModelProviderID) == "" {
		return Snapshot{}, ErrMissingProvider
	}
	if req.Providers == nil {
		return Snapshot{}, ErrMissingProvider
	}
	provider, err := req.Providers.Get(req.Profile.Fields.ModelProviderID)
	if err != nil {
		return Snapshot{}, err
	}
	protocol, err := resolveProtocol(provider, plugin, req.Profile.Fields.ModelProviderProtocol)
	if err != nil {
		return Snapshot{}, err
	}
	endpoint, ok := provider.EndpointFor(protocol)
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrIncompatibleProtocol, protocol)
	}
	model := strings.TrimSpace(req.LaunchModelOverride)
	if model == "" {
		model = strings.TrimSpace(req.Profile.Fields.ModelOverride)
	}
	if model == "" {
		model = strings.TrimSpace(provider.Catalog.DefaultModel)
	}
	if model == "" || !provider.Catalog.Contains(model) {
		return Snapshot{}, fmt.Errorf("%w: %q", ErrMissingModel, model)
	}
	if req.CheckEnv && !apiKeySourceAvailable(req.Credentials, req.ProjectID, provider.APIKeyEnv) {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrMissingAPIKeyEnv, provider.APIKeyEnv)
	}
	return Snapshot{
		ModelProviderID:   provider.ID,
		ModelProviderName: provider.Name,
		EndpointBaseURL:   endpoint.BaseURL,
		BaseURL:           endpoint.BaseURL,
		Protocol:          protocol,
		Model:             model,
		APIKeyEnv:         provider.APIKeyEnv,
		APIKeySource:      "generated_env",
		ProjectionTarget:  plugin.ConfigProjection.Primitive,
	}, nil
}

func apiKeySourceAvailable(credentials *credential.Service, projectID, envName string) bool {
	if strings.TrimSpace(os.Getenv(envName)) != "" {
		return true
	}
	if credentials == nil {
		return false
	}
	resolution, err := credentials.Resolve(envName, projectID)
	if err != nil || !resolution.Found || resolution.Disabled || resolution.Source == nil {
		return false
	}
	value, err := credential.Materialize(*resolution.Source)
	return err == nil && strings.TrimSpace(value) != ""
}

func resolveProtocol(provider Provider, plugin runtimeplugin.Plugin, pin string) (Protocol, error) {
	supported := map[Protocol]bool{}
	for _, protocol := range plugin.ModelProvider.SupportedProtocols {
		supported[Protocol(protocol)] = true
	}
	if pin = strings.TrimSpace(pin); pin != "" {
		protocol := Protocol(pin)
		if supported[protocol] && provider.Supports(protocol) {
			return protocol, nil
		}
		return "", fmt.Errorf("%w: %s", ErrIncompatibleProtocol, pin)
	}
	for _, preferred := range plugin.ModelProvider.ProtocolPreference {
		protocol := Protocol(preferred)
		if supported[protocol] && provider.Supports(protocol) {
			return protocol, nil
		}
	}
	return "", ErrIncompatibleProtocol
}

func runtimePluginForProfile(profile runtimeprofile.Profile, registry *runtimeplugin.Registry) (runtimeplugin.Plugin, bool) {
	if registry != nil {
		return registry.Get(string(profile.Provider))
	}
	return runtimeplugin.MustBuiltinRegistry().Get(string(profile.Provider))
}
