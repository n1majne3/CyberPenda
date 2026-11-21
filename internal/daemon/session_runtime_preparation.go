package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/modelprovider"
	"pentest/internal/modeskill"
	"pentest/internal/preflight"
	"pentest/internal/runner"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
)

func (server *Server) resolveSessionModelSnapshotForTurn(profile runtimeprofile.Profile, input sessionRuntimeInput, prior *runtimeconfig.RuntimeConfigurationSnapshot) (*modelprovider.Snapshot, error) {
	if profile.Provider == runtimeprofile.ProviderFake {
		return nil, nil
	}
	providerID := strings.TrimSpace(input.ModelProviderID)
	if prior != nil && (providerID == "" || providerID == prior.TurnSelection.ModelProviderID) {
		captured := prior.ModelProvider
		captured.Model = firstNonBlank(input.selectedModel(), prior.TurnSelection.Model, captured.Model)
		return &captured, nil
	}
	if providerID == "" {
		providerID = strings.TrimSpace(profile.Fields.ModelProviderID)
	}
	if providerID == "" {
		return nil, nil
	}
	profile.Fields.ModelProviderID = providerID
	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: profile, Providers: server.modelProviders, Plugins: server.runtimePlugins,
		Credentials: server.creds, ProjectID: "", CheckEnv: true,
		LaunchModelOverride: input.selectedModel(),
		CapabilityCache:     server.capabilityCache,
	})
	if err != nil {
		return nil, err
	}
	if snapshot.ModelProviderID == "" {
		return nil, nil
	}
	return &snapshot, nil
}

type sessionRuntimePreparation struct {
	Profile       runtimeprofile.Profile
	Runner        session.Runner
	RuntimeConfig map[string]any
}

func (server *Server) prepareSessionRuntime(ctx context.Context, mode session.BlackboardMode, input sessionRuntimeInput, previous *session.Continuation) (sessionRuntimePreparation, error) {
	profile, err := server.resolveSessionRuntimeProfile(input, previous)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	run, err := resolveSessionRunner(input, profile, previous)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	var priorSnapshot *runtimeconfig.RuntimeConfigurationSnapshot
	if previous != nil {
		versions, versionErr := server.sessions.RuntimeConfigVersions(previous.SessionID)
		if versionErr != nil || len(versions) == 0 {
			return sessionRuntimePreparation{}, errors.New("runtime configuration snapshot is missing")
		}
		captured, decodeErr := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
		if decodeErr != nil {
			return sessionRuntimePreparation{}, decodeErr
		}
		priorSnapshot = &captured
	}
	preflightRequest := preflightRequestForSession(server, profile, input, run, input.selectedModel())
	preflightRequest.BlackboardMode = modeskill.Mode(mode)
	if priorSnapshot != nil && (strings.TrimSpace(input.ModelProviderID) == "" || strings.TrimSpace(input.ModelProviderID) == priorSnapshot.TurnSelection.ModelProviderID) {
		captured := priorSnapshot.ModelProvider
		captured.Model = firstNonBlank(input.selectedModel(), priorSnapshot.TurnSelection.Model, captured.Model)
		preflightRequest.ModelProviderSnapshot = &captured
	}
	if priorSnapshot != nil {
		preflightRequest.CapturedSkillIDs = append([]string{}, priorSnapshot.EnabledSkillIDs...)
	}
	preflightResult := server.preflight.Run(ctx, preflightRequest)
	if priorSnapshot != nil {
		input.HostBrowserEnabled = priorSnapshot.HostBrowserEnabled
	}
	server.checkHostBrowser(&preflightResult, input.HostBrowserEnabled, profile.Provider)
	if !preflightResult.Pass {
		fails := make([]string, 0, len(preflightResult.Checks))
		for _, check := range preflightResult.Checks {
			if check.Status == preflight.CheckFail {
				detail := strings.TrimSpace(check.Detail)
				if detail == "" {
					fails = append(fails, check.Name)
					continue
				}
				fails = append(fails, check.Name+": "+detail)
			}
		}
		if len(fails) == 0 {
			return sessionRuntimePreparation{}, fmt.Errorf("Session Runtime preflight failed")
		}
		return sessionRuntimePreparation{}, fmt.Errorf("Session Runtime preflight failed: %s", strings.Join(fails, "; "))
	}
	modelSnapshot, err := server.resolveSessionModelSnapshotForTurn(profile, input, priorSnapshot)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	continuationProfile := profile
	resolvedModelSnapshot := modelprovider.Snapshot{}
	if modelSnapshot != nil {
		resolvedModelSnapshot = *modelSnapshot
		continuationProfile = runner.BlackboardV2ProfileWithModelSnapshot(profile, *modelSnapshot)
	}
	runtimeConfig, err := server.resolveSessionSnapshot(continuationProfile, run, input, resolvedModelSnapshot, previous)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}

	return sessionRuntimePreparation{
		Profile: profile, Runner: run, RuntimeConfig: runtimeConfig,
	}, nil
}
