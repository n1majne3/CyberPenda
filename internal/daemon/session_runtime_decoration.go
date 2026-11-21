package daemon

import (
	"sort"

	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
)

func (server *Server) decorateSession(found session.Session) (session.Session, error) {
	decorated, err := server.decorateSessions([]session.Session{found})
	if err != nil {
		return session.Session{}, err
	}
	return decorated[0], nil
}

func (server *Server) decorateSessionFromData(found session.Session, data session.DecorationData, steeringControl steeringControlProjection) (session.Session, error) {
	active, latest := data.Active, data.Latest
	snapshot, snapshotErr := decodeRuntimeSnapshot(data.Config)
	found.ActiveContinuation, found.LatestContinuation = active, latest
	provider := ""
	if latest != nil {
		provider = latest.RuntimeProvider
	}
	bound, isBound := server.sessionProviderSessions.get(found.ID)
	harnessActive := server.sessionHarness != nil && server.sessionHarness.IsActive(found.ID)
	activity := session.RuntimeActivity{Liveness: runtimeLivenessOffline}
	if isBound {
		if sessionHealthUnknown(bound) {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessUnknown, Warning: "runtime health cannot currently be determined"}
		} else if sessionOffline(bound) {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessOffline}
		} else {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessLive, TurnActivity: runtimeTurnIdle}
			if sessionBusy(bound) {
				activity.TurnActivity = runtimeTurnBusy
			}
		}
	} else if active != nil {
		if harnessActive {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessLive, TurnActivity: runtimeTurnIdle}
		} else {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessOrphaned, Warning: "Session Runtime ownership is not currently bound"}
		}
	} else if harnessActive {
		activity = session.RuntimeActivity{Liveness: runtimeLivenessLive, TurnActivity: runtimeTurnIdle}
	}
	found.RuntimeActivity = activity

	controls := session.RuntimeControls{
		RuntimeProvider: provider,
		// An open Session without a live Runtime accepts a new message to start
		// its replacement Runtime. A bound Runtime overrides this with its
		// provider-native SendTurn capability below.
		QueueSteerAvailable: found.Lifecycle == session.LifecycleOpen,
	}
	_, runtimeSupported := server.runtimePlugins.Get(provider)
	controls.QueueSteerAvailable = controls.QueueSteerAvailable && runtimeSupported
	if latest != nil {
		controls.NativeSessionCaptured = latest.NativeSessionID != "" || latest.NativeSessionPath != ""
		selectionErr := snapshotErr
		profile := runtimeprofile.Profile{}
		if selectionErr == nil {
			profile, selectionErr = runtimeProfileFromDecodedSnapshot(snapshot)
		}
		selection := runtime.ProviderSessionRequest{}
		if selectionErr == nil {
			selection, selectionErr = sessionSelection(profile, data.Config, sessionRuntimeInput{})
		}
		if selectionErr == nil {
			controls.TurnSelection = &session.RuntimeTurnSelection{
				ModelProviderID: selection.ModelProviderID, Model: selection.Model,
				ReasoningEffort: selection.RequestedReasoningEffort,
			}
		}
		controls.NativeResumeAvailable = runtimeSupported && active == nil && controls.NativeSessionCaptured && provider != string(runtimeprofile.ProviderFake)
	}
	controls.NativeSteerRequestID = steeringControl.requestID
	controls.NativeSteerState = steeringControl.state
	controls.NativeSteerErrorCode = steeringControl.errorCode
	controls.NativeSteerError = steeringControl.error
	if isBound {
		capabilities := bound.Capabilities()
		controls.QueueSteerAvailable = capabilities.SendTurn
		if mode, modeErr := nativeSteerModeForSession(bound, false); modeErr == nil {
			controls.NativeSteerAvailable = true
			controls.NativeSteerMode = string(mode)
			controls.InterruptSteerAvailable = capabilities.InterruptThenReplace || capabilities.InTurnSteer
		}
		if steeringControl.nonTerminal {
			controls.NativeSteerAvailable = false
		}
	}
	if active != nil && !isBound && !harnessActive {
		controls.RecoveryState = string(session.RuntimeStatusInterrupted)
		controls.RecoveryReason = "runtime ownership must be recovered or retried"
	}
	if active == nil && latest != nil && latest.Status == session.RuntimeStatusInterrupted && !isBound {
		controls.RecoveryState = string(session.RuntimeStatusInterrupted)
		controls.RecoveryReason = "previous Runtime ownership ended at daemon restart; resume creates a fresh continuation"
	}
	if active == nil && latest != nil && latest.Status == session.RuntimeStatusFailed && !isBound {
		controls.RecoveryState = string(session.RuntimeStatusFailed)
		controls.RecoveryReason = "previous Runtime launch failed; send a new message to retry with the saved profile"
	}
	controls.ProviderPermissions = sessionProviderPermissionsFromEvents(data.PermissionEvents)
	found.RuntimeControls = controls
	if snapshotErr == nil {
		summary := runtimeconfig.Summarize(snapshot)
		found.RuntimeConfiguration = &summary
	}
	found.BlackboardConclusion.Mode = found.RunControls.BlackboardMode
	found.BlackboardConclusion.State = session.BlackboardConclusionStateClean
	return found, nil
}

func (server *Server) sessionCurrentSelection(continuation session.Continuation) (runtime.ProviderSessionRequest, error) {
	versions, err := server.sessions.RuntimeConfigVersions(continuation.SessionID)
	if err != nil {
		return runtime.ProviderSessionRequest{}, err
	}
	var config map[string]any
	if len(versions) > 0 {
		config = versions[len(versions)-1].Config
	}
	profile, err := runtimeProfileFromSnapshot(config)
	if err != nil {
		return runtime.ProviderSessionRequest{}, err
	}
	return sessionSelection(profile, config, sessionRuntimeInput{})
}

func sessionProviderPermissionsFromEvents(events []session.Event) []session.ProviderPermission {
	pending := map[string]session.ProviderPermission{}
	for _, event := range events {
		permissionID, _ := event.Payload["permission_request_id"].(string)
		if permissionID == "" {
			continue
		}
		phase, _ := event.Payload["phase"].(string)
		switch phase {
		case "provider_permission_requested":
			request := session.ProviderPermission{PermissionRequestID: permissionID, CreatedAt: event.CreatedAt}
			request.RequestID, _ = event.Payload["request_id"].(string)
			request.SessionID, _ = event.Payload["session_id"].(string)
			request.ProviderTurnID, _ = event.Payload["provider_turn_id"].(string)
			request.Provider, _ = event.Payload["provider"].(string)
			pending[permissionID] = request
		case "provider_permission_response_applied":
			delete(pending, permissionID)
		}
	}
	result := make([]session.ProviderPermission, 0, len(pending))
	for _, request := range pending {
		result = append(result, request)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}
