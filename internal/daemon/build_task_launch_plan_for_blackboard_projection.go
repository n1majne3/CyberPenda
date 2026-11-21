package daemon

import (
	"context"
	"fmt"

	"pentest/internal/modeskill"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func (server *Server) buildTaskLaunchPlanForBlackboardProjection(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, launchReasoningEffort string, blackboardProjection runner.BlackboardProjection) (plan taskLaunchPlan, err error) {
	defer func() {
		if err != nil {
			_ = server.closeHostBrowser(context.Background(), created.ID)
		}
	}()
	server.logTaskLaunchStage(created, "build_plan")
	profile, err := server.resolveTaskRuntimeProfile(created)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if _, supported := server.runtimePlugins.Get(string(profile.Provider)); !supported {
		return taskLaunchPlan{}, fmt.Errorf("%w: %s", runtimeprofile.ErrUnknownProvider, profile.Provider)
	}
	if err := server.ensureTaskHostBrowser(created); err != nil {
		return taskLaunchPlan{}, err
	}
	if server.hostBrowserURL(created.ID, false) != "" {
		goal += "\n\nBrowser selection: Sandbox Browser uses agent-browser with a temporary session and no persistent Browser Profile. Host Browser is explicitly enabled. Use the host_browser MCP tools for the host browser's real session. This MCP service can execute code on the host, including browser_run_code_unsafe. Browser access does not grant Project Scope authorization."
	}
	if blackboardProjection == runner.BlackboardProjectionOmitted {
		seed := &taskLaunchPlan{ResolvedProfile: profile, BlackboardProjection: blackboardProjection}
		return server.buildTaskLaunchPlanWithBinding(created, goal, launchModelOverride, nativeResumeSessionID, launchReasoningEffort, nil, seed)
	}
	if server.blackboardV2Continuity != nil && runner.BlackboardV2SupportsProvider(profile.Provider) {
		return server.prepareBlackboardV2TaskLaunchPlan(created, goal, launchModelOverride, nativeResumeSessionID, launchReasoningEffort, profile)
	}
	seed := &taskLaunchPlan{ResolvedProfile: profile, BlackboardProjection: runner.BlackboardProjectionRequired}
	return server.buildTaskLaunchPlanWithBinding(created, goal, launchModelOverride, nativeResumeSessionID, launchReasoningEffort, nil, seed)
}

func (server *Server) prepareBlackboardV2TaskLaunchPlan(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, launchReasoningEffort string, profile runtimeprofile.Profile) (taskLaunchPlan, error) {
	layout, err := runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	prepared, err := server.prepareTaskConfigProjection(created, profile, layout,
		launchModelOverride, launchReasoningEffort, runner.BlackboardProjectionRequired, true)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	profile = prepared.Profile()
	modelSnapshot := prepared.ModelSnapshot()
	capturedRuntimeConfig, err := server.capturedTaskRuntimeConfig(created, profile, blackboardV2ModelSnapshotPreview(modelSnapshot), launchModelOverride, launchReasoningEffort)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	return taskLaunchPlan{
		CapturedRuntimeConfig:   capturedRuntimeConfig,
		MaterializedCredentials: prepared.Credentials(),
		LaunchModelOverride:     launchModelOverride,
		LaunchReasoningEffort:   launchReasoningEffort,
		NativeResumeSessionID:   nativeResumeSessionID,
		ResolvedProfile:         profile,
		ModelSnapshot:           modelSnapshot,
		PreparedProjection:      prepared,
		LaunchGoal:              goal,
		BlackboardProjection:    runner.BlackboardProjectionRequired,
		BlackboardV2:            true,
		ValidatedLayout:         &layout,
	}, nil
}

func (server *Server) prepareTaskConfigProjection(created task.Task, profile runtimeprofile.Profile, layout runner.Layout, launchModelOverride, launchReasoningEffort string, blackboardProjection runner.BlackboardProjection, v2 bool) (*runner.PreparedConfigProjection, error) {
	skillIDs, err := server.taskSnapshotSkillIDs(created.ID)
	if err != nil {
		return nil, err
	}
	req := runner.ProjectionRequest{
		BlackboardProtocol: created.BlackboardProtocol, Owner: created.OwnerContract(layout.Workdir),
		ScopeSnapshot: created.ScopeSnapshot, Credentials: server.creds,
		DaemonAddr: server.listenAddr, Sandbox: created.Runner == task.RunnerSandbox,
		HostBrowserMCPURL: server.hostBrowserURL(created.ID, created.Runner == task.RunnerSandbox),
		RuntimePlugins:    server.runtimePlugins, RuntimeExtensions: server.runtimeExtensions,
		LaunchModelOverride: launchModelOverride, RequestedReasoningEffort: launchReasoningEffort,
		BlackboardMode:       modeskill.Mode(created.RunControls.BlackboardMode),
		BlackboardProjection: blackboardProjection, CapabilityCache: server.capabilityCache,
	}
	if server.modelProviders != nil {
		req.ModelProviders = server.modelProviders
	}
	return runner.PrepareConfigProjection(profile, runner.ConfigProjectionInput{
		ProjectionRequest: req, Skills: server.skills, CapturedSkillIDs: skillIDs, BlackboardV2: v2,
	})
}

// snapshotGlobalModelProviders lists every global Model Provider outside any
// Store transaction and returns an immutable snapshot for Pi projection.
func (server *Server) snapshotGlobalModelProviders() (*runner.GlobalModelProviderSnapshot, error) {
	if server.modelProviders == nil {
		return runner.CloneGlobalModelProviderSnapshot(nil), nil
	}
	listed, err := server.modelProviders.List()
	if err != nil {
		return nil, fmt.Errorf("list model providers for launch snapshot: %w", err)
	}
	return runner.CloneGlobalModelProviderSnapshot(listed), nil
}
