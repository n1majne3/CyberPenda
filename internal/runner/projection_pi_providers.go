package runner

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

// piProjectedProvider is one launch-ready Model Provider prepared for Pi
// models.json / auth.json projection.
type piProjectedProvider struct {
	Provider modelprovider.Provider
	Protocol modelprovider.Protocol
	Endpoint modelprovider.Endpoint
	Models   []string
	APIKey   string
}

func listPiLaunchReadyProviders(profile runtimeprofile.Profile, req ProjectionRequest) ([]piProjectedProvider, error) {
	switch profile.Provider {
	case runtimeprofile.ProviderPi:
	default:
		return nil, nil
	}
	plugin, ok := runtimePluginForProvider(profile.Provider, req.RuntimePlugins)
	if !ok {
		return nil, nil
	}
	all, err := globalModelProvidersForProjection(req)
	if err != nil {
		return nil, err
	}
	if all == nil {
		return nil, nil
	}
	// Stable order by ID so models.json is deterministic across launches.
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	out := make([]piProjectedProvider, 0, len(all))
	for _, provider := range all {
		entry, ready := piLaunchReadyProvider(provider, plugin, req)
		if !ready {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

func globalModelProvidersForProjection(req ProjectionRequest) ([]modelprovider.Provider, error) {
	if req.GlobalModelProviderSnapshot != nil {
		// Non-nil snapshot owns the set (may be empty). Copy so callers cannot
		// mutate the request snapshot while filtering.
		return append([]modelprovider.Provider(nil), req.GlobalModelProviderSnapshot.Providers...), nil
	}
	if req.ModelProviders == nil {
		return nil, nil
	}
	lister, ok := req.ModelProviders.(modelprovider.ProviderLister)
	if !ok {
		return nil, nil
	}
	all, err := lister.List()
	if err != nil {
		return nil, fmt.Errorf("list model providers for pi projection: %w", err)
	}
	return all, nil
}

// piLaunchReadyProvider returns true when a global Model Provider has a
// Pi-compatible endpoint, at least one configured catalog model, and a
// configured API key. Drafts and incomplete providers are skipped.
func piLaunchReadyProvider(provider modelprovider.Provider, plugin runtimeplugin.Plugin, req ProjectionRequest) (piProjectedProvider, bool) {
	models := provider.Catalog.Models()
	if len(models) == 0 {
		return piProjectedProvider{}, false
	}
	protocol, err := resolvePiProtocol(provider, plugin)
	if err != nil {
		return piProjectedProvider{}, false
	}
	endpoint, ok := provider.EndpointFor(protocol)
	if !ok || strings.TrimSpace(endpoint.BaseURL) == "" {
		return piProjectedProvider{}, false
	}
	apiKey, ok := resolveModelProviderAPIKeyValue(provider.APIKeyEnv, req)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return piProjectedProvider{}, false
	}
	return piProjectedProvider{
		Provider: provider,
		Protocol: protocol,
		Endpoint: endpoint,
		Models:   models,
		APIKey:   apiKey,
	}, true
}

func resolvePiProtocol(provider modelprovider.Provider, plugin runtimeplugin.Plugin) (modelprovider.Protocol, error) {
	supported := map[modelprovider.Protocol]bool{}
	for _, protocol := range plugin.ModelProvider.SupportedProtocols {
		supported[modelprovider.Protocol(protocol)] = true
	}
	for _, preferred := range plugin.ModelProvider.ProtocolPreference {
		protocol := modelprovider.Protocol(preferred)
		if supported[protocol] && provider.Supports(protocol) {
			if _, ok := provider.EndpointFor(protocol); ok {
				return protocol, nil
			}
		}
	}
	return "", modelprovider.ErrIncompatibleProtocol
}

func resolveModelProviderAPIKeyValue(envName string, req ProjectionRequest) (string, bool) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return "", false
	}
	// A non-nil MaterializedCredentials map is the complete launch snapshot.
	// Missing keys mean "not configured" without re-entering the credential
	// service — BindGrant runs under CreateContinuation's open SQLite TX, so
	// Resolve would deadlock and draft/non-ready providers must stay skippable.
	if req.MaterializedCredentials != nil {
		value := strings.TrimSpace(req.MaterializedCredentials[envName])
		return value, value != ""
	}
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value, true
	}
	if req.Credentials == nil {
		return "", false
	}
	resolution, err := req.Credentials.Resolve(envName, req.Owner.ProjectID)
	if err != nil || !resolution.Found || resolution.Disabled || resolution.Source == nil {
		return "", false
	}
	value, err := credential.Materialize(*resolution.Source)
	if err != nil {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

func mergePiProjectedCredentials(materialized map[string]string, projected []piProjectedProvider, req ProjectionRequest) (map[string]string, error) {
	if len(projected) == 0 {
		return materialized, nil
	}
	out := cloneMaterializedCredentials(materialized)
	if out == nil {
		out = map[string]string{}
	}
	for _, entry := range projected {
		envName := strings.TrimSpace(entry.Provider.APIKeyEnv)
		if envName == "" {
			continue
		}
		if strings.TrimSpace(out[envName]) != "" {
			continue
		}
		value := strings.TrimSpace(entry.APIKey)
		if value == "" {
			resolved, ok := resolveModelProviderAPIKeyValue(envName, req)
			if !ok {
				// Non-initial providers are skipped earlier; keep defensive.
				continue
			}
			value = resolved
		}
		out[envName] = value
	}
	return out, nil
}

func buildPiModelsFromProjected(projected []piProjectedProvider, req ProjectionRequest) map[string]any {
	providers := make(map[string]any, len(projected))
	for _, entry := range projected {
		models := make([]map[string]any, 0, len(entry.Models))
		for _, modelID := range entry.Models {
			models = append(models, piModelEntry(modelID, entry.Provider.Catalog, req.CapabilityCache))
		}
		provider := map[string]any{
			"baseUrl": strings.TrimRight(entry.Endpoint.BaseURL, "/"),
			"api":     piAPIForProtocol(entry.Protocol),
			"models":  models,
		}
		if envName := strings.TrimSpace(entry.Provider.APIKeyEnv); envName != "" {
			provider["apiKey"] = "$" + envName
		}
		providers[entry.Provider.ID] = provider
	}
	return map[string]any{"providers": providers}
}

func buildPiAuthFromProjected(projected []piProjectedProvider, materialized map[string]string) map[string]map[string]string {
	if len(projected) == 0 {
		return nil
	}
	auth := make(map[string]map[string]string, len(projected))
	for _, entry := range projected {
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			if envName := strings.TrimSpace(entry.Provider.APIKeyEnv); envName != "" {
				key = strings.TrimSpace(materialized[envName])
			}
		}
		if key == "" {
			continue
		}
		auth[entry.Provider.ID] = map[string]string{
			"type": "api_key",
			"key":  key,
		}
	}
	if len(auth) == 0 {
		return nil
	}
	return auth
}
