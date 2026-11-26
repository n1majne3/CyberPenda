package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/workinggraph"
)

func (server *Server) startSessionRuntime(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput) (session.Continuation, error) {
	return server.startSessionRuntimeWithConversationInput(ctx, found, goal, input, nil)
}

func (server *Server) startSessionRuntimeWithConversationInput(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput, conversationInput *session.ConversationInput) (session.Continuation, error) {
	previous, err := server.sessions.LatestContinuation(found.ID)
	if err != nil {
		return session.Continuation{}, err
	}
	prepared, err := server.prepareSessionRuntime(ctx, found.RunControls.BlackboardMode, input, previous)
	if err != nil {
		server.recordSessionLaunchDiagnostic(found.ID, "launch_selection_failed", err)
		return session.Continuation{}, err
	}
	return server.startPreparedSessionRuntime(ctx, found, goal, input, previous, prepared, conversationInput)
}

type sessionConversationInputCommitError struct{ cause error }

func (err sessionConversationInputCommitError) Error() string { return err.cause.Error() }
func (err sessionConversationInputCommitError) Unwrap() error { return err.cause }

func (server *Server) startPreparedSessionRuntime(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput, previous *session.Continuation, prepared sessionRuntimePreparation, conversationInput *session.ConversationInput) (session.Continuation, error) {
	launch := resolveOwnerBlackboardRuntimeLaunch(
		goal, found.RunControls.BlackboardMode == session.BlackboardModeDisabled,
	)
	return server.startPreparedSessionRuntimeForBlackboardProjection(
		ctx, found, launch.goal, input, previous, prepared, conversationInput, launch.projection,
	)
}

func (server *Server) startPreparedSessionRuntimeForBlackboardProjection(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput, previous *session.Continuation, prepared sessionRuntimePreparation, conversationInput *session.ConversationInput, blackboardProjection runner.BlackboardProjection) (session.Continuation, error) {
	profile, run, runtimeConfig := prepared.Profile, prepared.Runner, prepared.RuntimeConfig
	var err error
	var continuation session.Continuation
	precreatedInitial := previous == nil && found.LatestContinuation != nil && found.LatestContinuation.Number == 1
	resumeNativeIdentity := previous != nil &&
		previous.RuntimeProvider == string(profile.Provider) && previous.Runner == run &&
		(strings.TrimSpace(previous.NativeSessionID) != "" || strings.TrimSpace(previous.NativeSessionPath) != "")
	if precreatedInitial {
		continuation = *found.LatestContinuation
	} else if resumeNativeIdentity {
		if conversationInput != nil {
			continuation, err = server.sessions.CreateReplacementContinuationWithInput(*previous, runtimeConfig, *conversationInput)
		} else {
			continuation, err = server.sessions.CreateReplacementContinuation(*previous, runtimeConfig)
		}
	} else {
		if conversationInput != nil {
			continuation, err = server.sessions.CreateContinuationWithInput(found.ID, profile.ID, string(profile.Provider), run, runtimeConfig, *conversationInput)
		} else {
			continuation, err = server.sessions.CreateContinuation(found.ID, profile.ID, string(profile.Provider), run, runtimeConfig)
		}
	}
	if err != nil {
		if conversationInput != nil {
			return session.Continuation{}, sessionConversationInputCommitError{cause: err}
		}
		server.recordSessionLaunchDiagnostic(found.ID, "continuation_create_failed", err)
		return session.Continuation{}, err
	}
	interfaceToken := ""
	snapshot, err := decodeRuntimeSnapshot(runtimeConfig)
	if err != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, err)
		return continuation, err
	}
	if err := server.ensureHostBrowser(found.ID, snapshot, filepath.Join(found.Workdir, ".runtime", "host-browser")); err != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, err)
		return continuation, err
	}
	server.hostBrowserMu.Lock()
	browserProcess := server.hostBrowserProcesses[found.ID]
	server.hostBrowserMu.Unlock()
	browserOwnedByHarness := false
	defer func() {
		if !browserOwnedByHarness {
			_ = server.closeHostBrowserProcess(context.Background(), found.ID, browserProcess)
		}
	}()
	if blackboardProjection != runner.BlackboardProjectionOmitted {
		if _, pinErr := server.blackboardV2.BindSessionContinuation(ctx, found.ID, continuation.ID); pinErr != nil {
			server.failSessionProviderLaunch(found.ID, continuation.ID, pinErr)
			return continuation, pinErr
		}
		var grantErr error
		interfaceToken, _, grantErr = server.projectInterfaceGrants.IssueSession(ctx, projectinterface.IssueSessionGrantRequest{
			SessionID: found.ID, ContinuationID: continuation.ID,
			RuntimeConfigVersionID: continuation.RuntimeConfigID,
			RuntimeProfileID:       continuation.RuntimeProfileID,
			RuntimePluginID:        string(profile.Provider), Runner: string(run),
			Access: blackboardGrantAccessForSessionMode(found.RunControls.BlackboardMode),
		})
		if grantErr != nil {
			server.failSessionProviderLaunch(found.ID, continuation.ID, grantErr)
			return continuation, grantErr
		}
	}
	var graphProjection *workinggraph.Projection
	if found.BlackboardProtocol == "fgs" && found.RunControls.BlackboardMode != session.BlackboardModeDisabled {
		preparedGraph, prepareErr := workinggraph.NewService().Prepare(ctx, workinggraph.OwnerContext{
			Owner: found.OwnerContract(), ContinuationID: continuation.ID, Workdir: found.Workdir,
		})
		if prepareErr != nil {
			server.failSessionProviderLaunch(found.ID, continuation.ID, prepareErr)
			return continuation, prepareErr
		}
		graphProjection = &preparedGraph
	}
	plan, err := server.buildSessionRuntimePlanForOwnerContext(found, goal, input, profile, run, interfaceToken, continuation.NativeSessionID, blackboardProjection, continuation.ID, graphProjection)
	if err != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, err)
		return continuation, err
	}
	if supportsPersistentProviderSession(task.Runner(run), profile.Provider) {
		if server.providerSessionFactory == nil {
			factoryErr := errors.New("provider session factory is required")
			server.failSessionProviderLaunch(found.ID, continuation.ID, factoryErr)
			return continuation, factoryErr
		}
		binding, factoryErr := server.providerSessionFactory.Open(ctx, ProviderSessionLaunchRequest{
			Owner:        found.OwnerContract(),
			Continuation: ownerContinuationFromSession(continuation), Provider: profile.Provider,
			Runner: task.Runner(run), LaunchGoal: plan.LaunchGoal, RuntimeConfig: plan.RuntimeConfig,
			Facts: plan.Facts,
		})
		if factoryErr == nil {
			factoryErr = validateProviderSessionBinding(binding)
		}
		if factoryErr != nil {
			server.failSessionProviderLaunch(found.ID, continuation.ID, factoryErr)
			return continuation, factoryErr
		}
		if err := server.BindSessionProviderSession(found.ID, binding.Session); err != nil {
			_ = binding.Session.Close(ctx)
			server.failSessionProviderLaunch(found.ID, continuation.ID, err)
			return continuation, err
		}
		if adapter, ok := binding.Adapter.(*runtime.ProviderSessionRunAdapter); ok {
			adapter.SetInitialTurnSelection(plan.Selection)
		}
		plan.Adapter, factoryErr = server.wrapOwnerProviderAdapter(binding.Adapter, found.OwnerContract(), profile.Provider, plan.ProviderHome)
		if factoryErr != nil {
			_ = server.closeSessionProviderSession(found.ID)
			server.failSessionProviderLaunch(found.ID, continuation.ID, factoryErr)
			return continuation, factoryErr
		}
	}
	if _, err := server.sessions.AppendEvent(found.ID, session.EventKindLifecycle, session.EventPayload{
		"phase": "launch_requested", "continuation_id": continuation.ID,
		"runtime_profile_id": profile.ID, "provider": string(profile.Provider), "runner": string(run),
	}); err != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, err)
		return continuation, err
	}
	launchCtx, err := server.beginRuntimeLaunch()
	if err != nil {
		_ = server.closeSessionProviderSession(found.ID)
		return continuation, err
	}
	browserOwnedByHarness = true
	go func() {
		defer server.runtimeLaunchWG.Done()
		defer server.closeHostBrowserProcess(context.Background(), found.ID, browserProcess)
		runErr := server.sessionHarness.Launch(launchCtx, runtime.SessionLaunchRequest{
			SessionID: found.ID, Goal: plan.LaunchGoal, Adapter: plan.Adapter, ContinuationID: continuation.ID,
			Metadata: plan.Metadata, StopConfirmation: plan.StopConfirmation,
		})
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			server.recordSessionLaunchDiagnostic(found.ID, "runtime_failed", runErr)
			_ = server.closeSessionProviderSession(found.ID)
		}
	}()
	return continuation, nil
}
