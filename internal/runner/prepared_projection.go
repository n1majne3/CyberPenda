package runner

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

// ConfigProjectionInput selects the captured dependencies for one launch.
// A non-nil CapturedSkillIDs uses exactly the owner Snapshot's Skill selection.
type ConfigProjectionInput struct {
	ProjectionRequest
	Skills           *skill.Service
	CapturedSkillIDs []string
	BlackboardV2     bool
}

// ProjectionBinding supplies only the authority and paths created after capture.
type ProjectionBinding struct {
	InterfaceToken       string
	ContinuationID       string
	WorkingGraphRoot     string
	WorkingGraphOutbox   string
	WorkingGraphReceipts string
}

// PreparedConfigProjection retains one launch's configuration and secrets in
// memory. Render does not query the Store and does not roll back file writes.
type PreparedConfigProjection struct {
	profile      runtimeprofile.Profile
	request      ProjectionRequest
	blackboardV2 bool
}

func PrepareConfigProjection(profile runtimeprofile.Profile, input ConfigProjectionInput) (*PreparedConfigProjection, error) {
	profile = cloneProjectionProfile(profile)
	req := input.ProjectionRequest
	if err := validateProjectionOwner(req.Owner); err != nil {
		return nil, err
	}
	if req.ModelSnapshot == nil && req.ModelProviders != nil && strings.TrimSpace(profile.Fields.ModelProviderID) != "" {
		snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
			Profile: profile, Providers: req.ModelProviders, Plugins: req.RuntimePlugins,
			Credentials: req.Credentials, ProjectID: req.Owner.ProjectID, CheckEnv: true,
			LaunchModelOverride: req.LaunchModelOverride, CapabilityCache: req.CapabilityCache,
		})
		if err != nil {
			return nil, err
		}
		if snapshot.ModelProviderID != "" {
			req.ModelSnapshot = &snapshot
		}
	}
	if req.ModelSnapshot != nil {
		snapshot := *req.ModelSnapshot
		req.ModelSnapshot = &snapshot
		profile = profileWithModelSnapshot(profile, snapshot)
	}
	if req.GlobalModelProviderSnapshot == nil {
		if lister, ok := req.ModelProviders.(modelprovider.ProviderLister); ok {
			providers, err := lister.List()
			if err != nil {
				return nil, fmt.Errorf("list model providers for launch snapshot: %w", err)
			}
			req.GlobalModelProviderSnapshot = CloneGlobalModelProviderSnapshot(providers)
		}
	} else {
		req.GlobalModelProviderSnapshot = CloneGlobalModelProviderSnapshot(req.GlobalModelProviderSnapshot.Providers)
	}
	if req.GlobalModelProviderSnapshot == nil {
		req.GlobalModelProviderSnapshot = CloneGlobalModelProviderSnapshot(nil)
	}
	catalog := launchModelCatalog(profile, req)
	catalog.Manual = slices.Clone(catalog.Manual)
	catalog.Refreshed = slices.Clone(catalog.Refreshed)
	catalog.Limits = maps.Clone(catalog.Limits)
	req.capturedModelCatalog = &catalog
	if req.CapabilityCache != nil {
		limits := make(map[string]modelprovider.CatalogLimits)
		capture := func(modelID string) {
			if value, ok := req.CapabilityCache.Lookup(modelID); ok {
				limits[strings.TrimSpace(modelID)] = value
			}
		}
		capture(profile.Fields.Model)
		capture(req.LaunchModelOverride)
		for _, modelID := range append(slices.Clone(catalog.Manual), catalog.Refreshed...) {
			capture(modelID)
		}
		if req.GlobalModelProviderSnapshot != nil {
			for _, provider := range req.GlobalModelProviderSnapshot.Providers {
				capture(provider.Catalog.DefaultModel)
				for _, modelID := range append(slices.Clone(provider.Catalog.Manual), provider.Catalog.Refreshed...) {
					capture(modelID)
				}
			}
		}
		req.CapabilityCache = modelprovider.NewCapabilityCache(limits, "", nil)
	}
	materialized, err := MaterializeLaunchCredentials(profile, req)
	if err != nil {
		return nil, err
	}
	req.MaterializedCredentials = materialized
	if input.Skills == nil && len(input.CapturedSkillIDs) > 0 {
		return nil, fmt.Errorf("Runtime skills service is unavailable")
	}
	if input.Skills != nil {
		if input.CapturedSkillIDs == nil {
			req.SkillBundles, err = input.Skills.EnabledSkillBundles(profile.ID)
			if err != nil {
				return nil, err
			}
		} else {
			req.SkillBundles = make([]skill.Bundle, 0, len(input.CapturedSkillIDs))
			for _, id := range input.CapturedSkillIDs {
				captured, err := input.Skills.Get(id)
				if errors.Is(err, skill.ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("resolve captured Skill %s: %w", id, err)
				}
				req.SkillBundles = append(req.SkillBundles, skill.Bundle{
					ID: captured.ID, Name: captured.Name, Source: captured.Source, Path: captured.BundlePath,
				})
			}
		}
	}
	req.SkillBundles = slices.Clone(req.SkillBundles)
	for i := range req.SkillBundles {
		if imported := req.SkillBundles[i].Source.LastImportedAt; imported != nil {
			value := *imported
			req.SkillBundles[i].Source.LastImportedAt = &value
		}
	}
	req.CredentialEnvNames = slices.Clone(req.CredentialEnvNames)
	req.ScopeSnapshot.Domains = slices.Clone(req.ScopeSnapshot.Domains)
	req.ScopeSnapshot.IPs = slices.Clone(req.ScopeSnapshot.IPs)
	req.ScopeSnapshot.CIDRs = slices.Clone(req.ScopeSnapshot.CIDRs)
	req.ScopeSnapshot.URLs = slices.Clone(req.ScopeSnapshot.URLs)
	req.ScopeSnapshot.Ports = slices.Clone(req.ScopeSnapshot.Ports)
	req.ScopeSnapshot.Excluded = slices.Clone(req.ScopeSnapshot.Excluded)
	req.ScopeSnapshot.TestingLimits = slices.Clone(req.ScopeSnapshot.TestingLimits)
	req.ScopeSnapshot.Capabilities = slices.Clone(req.ScopeSnapshot.Capabilities)
	req.Credentials = nil
	req.ModelProviders = nil
	req.AuthToken = ""
	return &PreparedConfigProjection{profile: profile, request: req, blackboardV2: input.BlackboardV2}, nil
}

func (p *PreparedConfigProjection) Profile() runtimeprofile.Profile {
	return cloneProjectionProfile(p.profile)
}

func (p *PreparedConfigProjection) ModelSnapshot() *modelprovider.Snapshot {
	if p.request.ModelSnapshot == nil {
		return nil
	}
	snapshot := *p.request.ModelSnapshot
	return &snapshot
}

// Credentials returns a separate copy for launch-error redaction, not storage.
func (p *PreparedConfigProjection) Credentials() map[string]string {
	return maps.Clone(p.request.MaterializedCredentials)
}

func (p *PreparedConfigProjection) Render(layout Layout, binding ProjectionBinding) (ConfigProjection, map[string]string, error) {
	profile := p.Profile()
	req := p.request
	req.ModelSnapshot = p.ModelSnapshot()
	req.AuthToken = binding.InterfaceToken
	var projection ConfigProjection
	var err error
	if p.blackboardV2 {
		projection, err = ProjectBlackboardV2RuntimeConfig(layout, profile, req)
	} else {
		projection, err = ProjectRuntimeConfig(layout, profile, req)
	}
	if err != nil {
		return ConfigProjection{}, nil, err
	}
	if projection.ResolvedProfile.Provider != "" {
		profile = projection.ResolvedProfile
	}
	ctx := RuntimeOwnerContext{
		Owner: req.Owner, BlackboardProtocol: req.BlackboardProtocol, BlackboardMode: string(req.BlackboardMode),
		Sandbox: req.Sandbox, ContinuationID: binding.ContinuationID,
		WorkingGraphRoot: binding.WorkingGraphRoot, WorkingGraphOutbox: binding.WorkingGraphOutbox,
		WorkingGraphReceipts: binding.WorkingGraphReceipts,
	}
	if req.BlackboardProjection != BlackboardProjectionOmitted && binding.InterfaceToken != "" {
		ctx.InterfaceToken = binding.InterfaceToken
		ctx.APIURL = APIEndpointURL(req.DaemonAddr, req.Sandbox)
	}
	req.ModelSnapshot = projection.ModelSnapshot
	env, err := LaunchProcessEnvWithCredentials(layout, profile, req.Sandbox, ctx, req)
	if err != nil {
		return ConfigProjection{}, nil, err
	}
	if p.blackboardV2 {
		env = BlackboardV2ProcessEnv(env, layout, req.Sandbox)
	}
	return projection, env, nil
}

func cloneProjectionProfile(profile runtimeprofile.Profile) runtimeprofile.Profile {
	fields := &profile.Fields
	fields.Env = maps.Clone(fields.Env)
	fields.APIKeys = maps.Clone(fields.APIKeys)
	fields.CustomArgs = slices.Clone(fields.CustomArgs)
	fields.CredentialRefs = slices.Clone(fields.CredentialRefs)
	fields.MCPServers = slices.Clone(fields.MCPServers)
	for i := range fields.MCPServers {
		fields.MCPServers[i].Args = slices.Clone(fields.MCPServers[i].Args)
		fields.MCPServers[i].Env = maps.Clone(fields.MCPServers[i].Env)
	}
	fields.RuntimeExtensions = slices.Clone(fields.RuntimeExtensions)
	for i := range fields.RuntimeExtensions {
		fields.RuntimeExtensions[i].Config = maps.Clone(fields.RuntimeExtensions[i].Config)
		if enabled := fields.RuntimeExtensions[i].Enabled; enabled != nil {
			value := *enabled
			fields.RuntimeExtensions[i].Enabled = &value
		}
	}
	if fields.CodexMultiAgent != nil {
		settings := *fields.CodexMultiAgent
		if settings.Enabled != nil {
			enabled := *settings.Enabled
			settings.Enabled = &enabled
		}
		fields.CodexMultiAgent = &settings
	}
	return profile
}
