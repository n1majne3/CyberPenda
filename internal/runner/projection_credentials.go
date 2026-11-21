package runner

import (
	"fmt"
	"os"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/owner"
	"pentest/internal/runtimeprofile"
)

func resolveMaterializedCredentials(profile runtimeprofile.Profile, req ProjectionRequest) (map[string]string, error) {
	// Global Environment Variables: every active global Credential Binding
	// projects into every Runtime as a base layer, independent of the
	// profile's credential_refs. Profile-scoped sources below override these
	// so explicit references still win.
	//
	// Deadlock constraint: this function runs BOTH outside Store transactions
	// (via MaterializeLaunchCredentials) AND inside them (the
	// MaterializedCredentials snapshot path). ResolveGlobalEnv queries SQLite,
	// so it must only run on the pre-transaction path — once global env lands in
	// the MaterializedCredentials snapshot, the in-transaction path reuses it
	// and never touches the Service again.
	env := make(map[string]string)
	if req.MaterializedCredentials != nil {
		// In-transaction path: the snapshot already contains global env vars
		// (merged by MaterializeLaunchCredentials before the TX began).
		for key, value := range req.MaterializedCredentials {
			env[key] = value
		}
		if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" && strings.TrimSpace(env[req.ModelSnapshot.APIKeyEnv]) == "" {
			return nil, fmt.Errorf("model provider API key env %s is not configured", req.ModelSnapshot.APIKeyEnv)
		}
		return credentialsForBlackboardProjection(env, req.BlackboardProjection), nil
	}
	if req.Credentials != nil {
		globalEnv, err := req.Credentials.ResolveGlobalEnv()
		if err != nil {
			return nil, err
		}
		for key, value := range globalEnv {
			env[key] = value
		}
	}
	if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" {
		if value, ok := materializeModelProviderAPIKey(req); ok {
			env[req.ModelSnapshot.APIKeyEnv] = value
		} else {
			value := strings.TrimSpace(os.Getenv(req.ModelSnapshot.APIKeyEnv))
			if value == "" {
				return nil, fmt.Errorf("model provider API key env %s is not configured", req.ModelSnapshot.APIKeyEnv)
			}
			env[req.ModelSnapshot.APIKeyEnv] = value
		}
	}
	inline := runtimeprofile.MaterializedAPIKeys(profile)
	for key, value := range inline {
		env[key] = value
	}
	if req.Credentials != nil && req.Owner.ProjectID != "" && len(profile.Fields.CredentialRefs) > 0 {
		referenced, err := req.Credentials.ResolveMaterializedEnv(req.Owner.ProjectID, profile.Fields.CredentialRefs)
		if err != nil {
			return nil, err
		}
		for key, value := range referenced {
			env[key] = value
		}
	}
	// Pre-transaction parity with MaterializeLaunchCredentials: every
	// launch-ready global Model Provider's key must reach the Runtime process
	// environment, not only the profile-selected parent. models.json apiKey
	// entries reference these generated env names, so a Pi child without them
	// cannot authenticate to its additional providers.
	if profile.Provider == runtimeprofile.ProviderPi {
		merged, err := mergePiGlobalProviderCredentials(env, profile, req)
		if err != nil {
			return nil, err
		}
		env = merged
	}
	if len(env) == 0 {
		return nil, nil
	}
	return credentialsForBlackboardProjection(env, req.BlackboardProjection), nil
}

func profileForBlackboardProjection(profile runtimeprofile.Profile, projection BlackboardProjection) runtimeprofile.Profile {
	if projection != BlackboardProjectionOmitted {
		return profile
	}
	filtered := profile
	filtered.Fields.Env = credentialsForBlackboardProjection(profile.Fields.Env, projection)
	filtered.Fields.APIKeys = credentialsForBlackboardProjection(profile.Fields.APIKeys, projection)
	filtered.Fields.CredentialRefs = make([]string, 0, len(profile.Fields.CredentialRefs))
	for _, ref := range profile.Fields.CredentialRefs {
		if !isBlackboardAuthorityEnv(ref) {
			filtered.Fields.CredentialRefs = append(filtered.Fields.CredentialRefs, ref)
		}
	}
	filtered.Fields.MCPServers = make([]runtimeprofile.MCPServer, 0, len(profile.Fields.MCPServers))
	for _, server := range profile.Fields.MCPServers {
		if server.Mode == runtimeprofile.MCPServerTrusted {
			continue
		}
		server.Env = credentialsForBlackboardProjection(server.Env, projection)
		filtered.Fields.MCPServers = append(filtered.Fields.MCPServers, server)
	}
	return filtered
}

func credentialsForBlackboardProjection(credentials map[string]string, projection BlackboardProjection) map[string]string {
	if projection != BlackboardProjectionOmitted || credentials == nil {
		return credentials
	}
	filtered := make(map[string]string, len(credentials))
	for key, value := range credentials {
		if !isBlackboardAuthorityEnv(key) {
			filtered[key] = value
		}
	}
	return filtered
}

func isBlackboardAuthorityEnv(key string) bool {
	switch strings.TrimSpace(key) {
	case "PENTEST_BLACKBOARD_PROTOCOL", "PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_SESSION_ID", "PENTEST_CONTINUATION_ID",
		"PENTEST_MCP_URL", "PENTEST_API_URL", "PENTEST_AUTH_TOKEN", "PENTEST_INTERFACE_TOKEN",
		"PENTEST_BLACKBOARD_MODE", "PENTEST_WORKING_GRAPH_ROOT", "PENTEST_WORKING_GRAPH_OUTBOX",
		"PENTEST_WORKING_GRAPH_RECEIPTS", "PENTEST_DISABLE_TRUSTED_MCP":
		return true
	default:
		return false
	}
}

// MaterializeLaunchCredentials resolves every credential needed by one launch
// before callers enter a Store transaction. The returned values are secret and
// must remain in memory; they are never part of generated config previews.
// For Pi, this includes API keys for every launch-ready global Model Provider
// so cross-provider turns can authenticate without restart.
func MaterializeLaunchCredentials(profile runtimeprofile.Profile, req ProjectionRequest) (map[string]string, error) {
	if err := validateProjectionOwner(req.Owner); err != nil {
		return nil, err
	}
	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return nil, err
	}
	if profile.Provider == runtimeprofile.ProviderPi {
		materialized, err = mergePiGlobalProviderCredentials(materialized, profile, req)
		if err != nil {
			return nil, err
		}
	}
	if materialized == nil {
		return map[string]string{}, nil
	}
	return cloneMaterializedCredentials(materialized), nil
}

// mergePiGlobalProviderCredentials merges every launch-ready global Model
// Provider's key into a materialized credential map. It is idempotent: keys
// already present keep their value, so the pre-transaction and snapshot paths
// can both run it.
func mergePiGlobalProviderCredentials(materialized map[string]string, profile runtimeprofile.Profile, req ProjectionRequest) (map[string]string, error) {
	projected, err := listPiLaunchReadyProviders(profile, req)
	if err != nil {
		return nil, err
	}
	return mergePiProjectedCredentials(materialized, projected, req)
}

func validateProjectionOwner(contract owner.Contract) error {
	if contract.ID == "" {
		return nil
	}
	if err := contract.Validate(); err != nil {
		return fmt.Errorf("invalid Runtime owner contract: %w", err)
	}
	return nil
}

func cloneMaterializedCredentials(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func materializeModelProviderAPIKey(req ProjectionRequest) (string, bool) {
	if req.ModelSnapshot == nil || strings.TrimSpace(req.ModelSnapshot.APIKeyEnv) == "" {
		return "", false
	}
	if req.MaterializedCredentials != nil {
		value := strings.TrimSpace(req.MaterializedCredentials[req.ModelSnapshot.APIKeyEnv])
		return value, value != ""
	}
	if req.Credentials == nil {
		return "", false
	}
	resolution, err := req.Credentials.Resolve(req.ModelSnapshot.APIKeyEnv, req.Owner.ProjectID)
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
