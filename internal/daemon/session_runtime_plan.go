package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/modeskill"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/workinggraph"
)

func (server *Server) buildSessionRuntimePlan(found session.Session, goal string, input sessionRuntimeInput, profile runtimeprofile.Profile, run session.Runner, interfaceToken, nativeResumeSessionID string) (sessionRuntimePlan, error) {
	return server.buildSessionRuntimePlanForBlackboardProjection(
		found, goal, input, profile, run, interfaceToken, nativeResumeSessionID, runner.BlackboardProjectionRequired,
	)
}

func (server *Server) buildSessionRuntimePlanForBlackboardProjection(found session.Session, goal string, input sessionRuntimeInput, profile runtimeprofile.Profile, run session.Runner, interfaceToken, nativeResumeSessionID string, blackboardProjection runner.BlackboardProjection) (sessionRuntimePlan, error) {
	return server.buildSessionRuntimePlanForOwnerContext(found, goal, input, profile, run, interfaceToken, nativeResumeSessionID, blackboardProjection, "", nil)
}

func (server *Server) buildSessionRuntimePlanForOwnerContext(found session.Session, goal string, input sessionRuntimeInput, profile runtimeprofile.Profile, run session.Runner, interfaceToken, nativeResumeSessionID string, blackboardProjection runner.BlackboardProjection, continuationID string, graph *workinggraph.Projection) (sessionRuntimePlan, error) {
	launchGoal, err := server.sessionLaunchGoal(found, goal, run)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if found.BlackboardProtocol == "fgs" && found.RunControls.BlackboardMode != session.BlackboardModeDisabled {
		launchGoal = runner.FGSLaunchInstruction + "\n\n" + launchGoal
	} else {
		launchGoal, err = modeskill.InjectInvocation(launchGoal, modeskill.Mode(found.RunControls.BlackboardMode))
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	capturedSnapshot, err := server.latestSessionRuntimeSnapshot(found.ID)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	modelSnapshot, err := server.resolveSessionModelSnapshotForTurn(profile, input, capturedSnapshot)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if modelSnapshot != nil {
		profile = runner.BlackboardV2ProfileWithModelSnapshot(profile, *modelSnapshot)
	}
	config := sessionRuntimeConfig(profile, run, input)
	if capturedSnapshot != nil {
		config = runtimeSnapshotMap(*capturedSnapshot)
	}
	if server.hostBrowserURL(found.ID, false) != "" {
		launchGoal += "\n\nHost Browser is explicitly enabled. Use the host_browser MCP tools for the host browser's real session. This MCP service can execute code on the host, including browser_run_code_unsafe. This does not select Host Runner."
	}
	selection, err := sessionSelection(profile, config, input)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		return sessionRuntimePlan{
			Adapter: runtime.NewFakeAdapter(), Profile: profile, Runner: run,
			LaunchGoal: launchGoal, RuntimeConfig: config, Selection: selection, BlackboardProjection: blackboardProjection,
		}, nil
	}

	runtimeRoot := filepath.Join(found.Workdir, ".runtime")
	providerDir := string(profile.Provider)
	if profile.Provider == runtimeprofile.ProviderClaudeCode {
		providerDir = "claude"
	}
	layout := runner.Layout{
		TaskRoot:     runtimeRoot,
		Workdir:      found.Workdir,
		RuntimeHome:  filepath.Join(runtimeRoot, "runtime-home"),
		ProviderHome: filepath.Join(runtimeRoot, "runtime-home", providerDir),
		SkillsRoot:   filepath.Join(runtimeRoot, "skills"),
		Artifacts:    filepath.Join(runtimeRoot, "artifacts"),
		Logs:         filepath.Join(runtimeRoot, "logs"),
	}
	for _, directory := range []string{layout.RuntimeHome, layout.ProviderHome, layout.SkillsRoot, layout.Artifacts, layout.Logs} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return sessionRuntimePlan{}, fmt.Errorf("prepare Session Runtime directory: %w", err)
		}
	}

	if server.skills == nil {
		return sessionRuntimePlan{}, fmt.Errorf("Session Runtime skills service is unavailable")
	}
	var skillIDs []string
	if capturedSnapshot != nil {
		skillIDs = append([]string{}, capturedSnapshot.EnabledSkillIDs...)
	}
	launchProfile := profile
	projectionRequest := runner.ProjectionRequest{
		HostBrowserMCPURL:  server.hostBrowserURL(found.ID, run == session.RunnerSandbox),
		BlackboardProtocol: found.BlackboardProtocol,
		Owner:              found.OwnerContract(), DaemonAddr: server.listenAddr,
		Credentials:   server.creds,
		ModelSnapshot: modelSnapshot, RuntimePlugins: server.runtimePlugins,
		RuntimeExtensions:        server.runtimeExtensions,
		BlackboardMode:           modeskill.Mode(found.RunControls.BlackboardMode),
		LaunchModelOverride:      selection.Model,
		RequestedReasoningEffort: selection.RequestedReasoningEffort,
		Sandbox:                  run == session.RunnerSandbox,
		CapabilityCache:          server.capabilityCache,
		BlackboardProjection:     blackboardProjection,
	}
	if server.modelProviders != nil {
		projectionRequest.ModelProviders = server.modelProviders
	}
	prepared, err := runner.PrepareConfigProjection(launchProfile, runner.ConfigProjectionInput{
		ProjectionRequest: projectionRequest, Skills: server.skills, CapturedSkillIDs: skillIDs,
	})
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	projectionBinding := runner.ProjectionBinding{InterfaceToken: interfaceToken, ContinuationID: continuationID}
	if graph != nil {
		projectionBinding.WorkingGraphRoot = graph.Root
		projectionBinding.WorkingGraphOutbox = graph.Outbox
		projectionBinding.WorkingGraphReceipts = graph.Receipts
	}
	projection, processEnv, err := prepared.Render(layout, projectionBinding)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if projection.ResolvedProfile.Provider != "" {
		launchProfile = projection.ResolvedProfile
	}
	configPath := runner.LaunchConfigPath(layout, launchProfile.Provider, projection.ConfigPath, run == session.RunnerSandbox)
	providerBinary, err := resolveProviderBinary(server.runtimePlugins, launchProfile)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	launchFacts := ProviderSessionLaunchFacts{
		ProviderBinary: providerBinary,
		CustomArgs:     append([]string(nil), launchProfile.Fields.CustomArgs...),
		SettingsPath:   configPath,
		Workdir:        "/task/workdir",
		HostTaskRoot:   layout.TaskRoot,
		Env:            processEnv,
	}
	if run != session.RunnerSandbox {
		launchFacts.Workdir = found.Workdir
	}
	if strings.TrimSpace(selection.Model) != "" {
		launchFacts.Model = strings.TrimSpace(selection.Model)
	} else {
		launchFacts.Model = strings.TrimSpace(launchProfile.Fields.ModelOverride)
	}
	containerIDFile := ""
	if run == session.RunnerSandbox {
		containerIDFile = filepath.Join(layout.Logs, "container.cid")
		if err := os.Remove(containerIDFile); err != nil && !os.IsNotExist(err) {
			return sessionRuntimePlan{}, err
		}
		image := strings.TrimSpace(profile.Fields.SandboxImage)
		if image == "" {
			image = strings.TrimSpace(server.sandboxImage)
		}
		if image == "" {
			image = "kalilinux/kali-rolling"
		}
		containerCLI := task.ResolveContainerCLI(input.ContainerCLI, server.containerCLI)
		networkMode := runner.SandboxNetworkDefault
		if strings.TrimSpace(input.SandboxNetwork) == string(runner.SandboxNetworkHostProxyOnly) {
			networkMode = runner.SandboxNetworkHostProxyOnly
		}
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout: layout, Provider: profile.Provider, Image: image,
			ContainerCLI: containerCLI, ContainerIDFile: containerIDFile,
			RuntimeCommand: []string{providerBinary}, ProcessEnv: processEnv,
			NetworkMode: networkMode,
			VPNTun:      input.SandboxVPNTun,
			TaskVolume:  server.taskVolume, TaskVolumeRoot: server.taskVolumeRoot,
		})
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		launchFacts.SandboxContainerCLI = command.Program
		launchFacts.SandboxCreateArgs = command.Args
		launchFacts.SandboxCreateEnv = command.Env
	}

	metadata := providerNativeSessionMetadata(run == session.RunnerSandbox, profile.Provider, layout, containerIDFile)
	stopConfirmation := dockerStopConfirmation(input.ContainerCLI, server.containerCLI, containerIDFile)
	return sessionRuntimePlan{
		Profile: launchProfile, Runner: run, LaunchGoal: launchGoal, RuntimeConfig: config, Selection: selection,
		BlackboardProjection: blackboardProjection,
		Metadata:             metadata, StopConfirmation: stopConfirmation, ProviderHome: layout.ProviderHome, Facts: launchFacts,
	}, nil
}
