package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"pentest/internal/owner"
	"pentest/internal/preflight"
	"pentest/internal/projectinterface"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
)

func blackboardGrantAccessForSessionMode(mode session.BlackboardMode) projectinterface.GrantAccess {
	if mode == session.BlackboardModeWorkingGraph {
		return projectinterface.GrantAccessReadOnly
	}
	return projectinterface.GrantAccessFull
}

func (server *Server) handleSessionPreflight(response http.ResponseWriter, request *http.Request) {
	var launch createSessionInput
	if err := json.NewDecoder(request.Body).Decode(&launch); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input := sessionRuntimeInputFromCreate(launch)
	if _, err := normalizeLaunchReasoningEffort(input.ReasoningEffort); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	profile, err := server.resolveSessionRuntimeProfile(input, nil)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	run, err := resolveSessionRunner(input, profile, nil)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	result := server.preflight.Run(
		request.Context(),
		preflightRequestForSession(server, profile, input, run, input.selectedModel()),
	)
	server.logPreflightCustomArgConflict(profile.ID, result)
	server.checkHostBrowser(&result, input.HostBrowserEnabled, profile.Provider)
	writeJSON(response, http.StatusOK, result)
}

func preflightRequestForSession(server *Server, profile runtimeprofile.Profile, input sessionRuntimeInput, run session.Runner, launchModel string) preflight.Request {
	return preflight.Request{
		RuntimeProfileID:    profile.ID,
		Profile:             &profile,
		ProjectID:           "",
		Runner:              string(run),
		HostActivated:       input.HostActivated,
		LaunchModelOverride: launchModel,
		ContainerCLI:        task.ResolveContainerCLI(input.ContainerCLI, server.containerCLI),
		SandboxVPNTun:       input.SandboxVPNTun,
		SandboxNetwork:      input.SandboxNetwork,
		RuntimeRoot:         server.runtimeRoot,
	}
}

func (server *Server) failSessionProviderLaunch(sessionID, continuationID string, cause error) {
	_, _ = server.sessions.UpdateContinuationStatus(continuationID, session.RuntimeStatusFailed)
	server.recordSessionLaunchDiagnostic(sessionID, "provider_session_setup_failed", cause)
}

func (server *Server) recordSessionLaunchDiagnostic(sessionID, phase string, detail any) {
	payload := session.EventPayload{"phase": phase}
	switch value := detail.(type) {
	case error:
		payload["error"] = value.Error()
	case string:
		payload["error"] = value
	case map[string]any:
		for key, item := range value {
			payload[key] = item
		}
	default:
		if detail != nil {
			payload["detail"] = fmt.Sprint(detail)
		}
	}
	_, _ = server.sessions.AppendEvent(sessionID, session.EventKindLifecycle, payload)
}

func (server *Server) stopSessionRuntime(sessionID string) error {
	active, err := server.sessions.ActiveContinuation(sessionID)
	if err != nil {
		return err
	}
	bound := false
	if server.sessionProviderSessions != nil {
		_, bound = server.sessionProviderSessions.get(sessionID)
	}
	harnessActive := server.sessionHarness != nil && server.sessionHarness.IsActive(sessionID)
	if active == nil && !bound && !harnessActive {
		return nil
	}
	if server.sessionHarness != nil {
		server.sessionHarness.Stop(sessionID)
	}
	server.cancelProviderTaskControls(sessionID)
	deadline := time.Now().Add(server.runtimeStopTimeout)
	stopContext, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := server.closeSessionProviderSessionForStop(stopContext, sessionID); err != nil && !errors.Is(err, runtime.ErrProviderSessionClosed) {
		return err
	}
	if server.sessionHarness != nil && !server.sessionHarness.StopAndWait(sessionID, time.Until(deadline)) {
		return fmt.Errorf("Session Runtime did not stop in time")
	}
	found, err := server.sessions.Get(sessionID)
	if err != nil {
		return err
	}
	if _, err := server.settleSessionWorkingGraph(stopContext, found, true); err != nil {
		return fmt.Errorf("settle Session Working Graph: %w", err)
	}
	active, err = server.sessions.ActiveContinuation(sessionID)
	if err != nil {
		return err
	}
	if active != nil {
		_, err = server.sessions.UpdateContinuationStatus(active.ID, session.RuntimeStatusStopped)
		if err != nil && !errors.Is(err, session.ErrContinuationStatusConflict) {
			return err
		}
	}
	server.settleSessionAcceptedSteering(sessionID, owner.SteeringReasonOwnerStopped, "Session stopped with queued accepted steering")
	_, _ = server.sessions.AppendEvent(sessionID, session.EventKindLifecycle, session.EventPayload{"phase": "stopped"})
	return nil
}
