package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/adapters"
	"pentest/internal/blackboardv2"

	"pentest/internal/modelprovider"
	"pentest/internal/modeskill"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func (server *Server) buildTaskLaunchPlanWithBinding(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, launchReasoningEffort string, binding *continuationLaunchBinding, captured *taskLaunchPlan) (taskLaunchPlan, error) {
	if captured != nil && strings.TrimSpace(launchReasoningEffort) == "" {
		launchReasoningEffort = captured.LaunchReasoningEffort
	}
	v2 := binding != nil && binding.V2Header != nil
	blackboardProjection := runner.BlackboardProjectionRequired
	if captured != nil {
		blackboardProjection = captured.BlackboardProjection
	}
	runtimeConfig := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runner":             created.Runner,
	}

	var profile runtimeprofile.Profile
	var prepared *runner.PreparedConfigProjection
	if captured != nil && captured.PreparedProjection != nil {
		prepared = captured.PreparedProjection
		profile = prepared.Profile()
	} else {
		var err error
		if captured != nil && captured.ResolvedProfile.Provider != "" {
			profile = captured.ResolvedProfile
		} else {
			profile, err = server.resolveTaskRuntimeProfile(created)
			if err != nil {
				return taskLaunchPlan{}, err
			}
		}
	}
	sandbox := created.Runner == task.RunnerSandbox
	goal = runner.RewriteLoopbackTargets(goal, sandbox)
	injectedGoal, injectErr := goal, error(nil)
	if created.BlackboardProtocol == "fgs" && created.RunControls.BlackboardMode != task.BlackboardModeDisabled {
		injectedGoal = runner.FGSLaunchInstruction + "\n\n" + goal
	} else {
		injectedGoal, injectErr = modeskill.InjectInvocation(goal, modeskill.Mode(created.RunControls.BlackboardMode))
	}
	if injectErr != nil {
		return taskLaunchPlan{}, injectErr
	}
	goal = injectedGoal
	launchGoal := goal
	if binding != nil {
		if binding.V2Header != nil && created.BlackboardProtocol != "fgs" {
			launchGoal = blackboardv2.RenderLaunchHeader(*binding.V2Header) + "\n\nTASK GOAL:\n" + goal
		}
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		runtimeConfig["provider"] = string(runtimeprofile.ProviderFake)
		runtimeConfig["generated_config"] = runtimeprofile.GeneratedConfig(profile)
		var capturedRuntimeConfig map[string]any
		if captured != nil {
			capturedRuntimeConfig = captured.CapturedRuntimeConfig
		} else {
			var err error
			capturedRuntimeConfig, err = server.capturedTaskRuntimeConfig(created, profile, nil, launchModelOverride, launchReasoningEffort)
			if err != nil {
				return taskLaunchPlan{}, err
			}
		}
		return taskLaunchPlan{Adapter: runtime.NewFakeAdapter(), RuntimeConfig: runtimeConfig, CapturedRuntimeConfig: capturedRuntimeConfig, LaunchModelOverride: launchModelOverride, LaunchReasoningEffort: launchReasoningEffort, NativeResumeSessionID: nativeResumeSessionID, ResolvedProfile: profile, LaunchGoal: launchGoal, BlackboardProjection: blackboardProjection}, nil
	}
	if blackboardProjection != runner.BlackboardProjectionOmitted && !v2 {
		return taskLaunchPlan{}, fmt.Errorf("Blackboard v2 binding is required for Blackboard projection")
	}
	var layout runner.Layout
	var err error
	if v2 {
		if captured == nil || captured.ValidatedLayout == nil {
			return taskLaunchPlan{}, fmt.Errorf("Blackboard v2 layout was not validated")
		}
		layout, err = runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	} else {
		layout, err = runner.PrepareTaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	}
	if err != nil {
		return taskLaunchPlan{}, err
	}

	projectionBinding := runner.ProjectionBinding{}
	if v2 {
		projectionBinding.InterfaceToken = strings.TrimSpace(binding.InterfaceToken)
		projectionBinding.ContinuationID = binding.ContinuationID
		if binding.WorkingGraph != nil {
			projectionBinding.WorkingGraphRoot = binding.WorkingGraph.Root
			projectionBinding.WorkingGraphOutbox = binding.WorkingGraph.Outbox
			projectionBinding.WorkingGraphReceipts = binding.WorkingGraph.Receipts
		}
	}
	if prepared == nil {
		prepared, err = server.prepareTaskConfigProjection(created, profile, layout, launchModelOverride, launchReasoningEffort, blackboardProjection, v2)
		if err != nil {
			return taskLaunchPlan{}, err
		}
	}
	projection, processEnv, err := prepared.Render(layout, projectionBinding)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if binding != nil && binding.V2Header != nil {
		if !runner.BlackboardV2SupportsProvider(profile.Provider) {
			return taskLaunchPlan{}, fmt.Errorf("Blackboard v2 launch projection is unsupported for provider %q", profile.Provider)
		}
		if created.BlackboardProtocol != "fgs" {
			if err := runner.ProjectBlackboardV2Files(layout, profile.Provider, *binding.V2Header, created.ScopeSnapshot); err != nil {
				return taskLaunchPlan{}, err
			}
		}
	}
	configPath := runner.LaunchConfigPath(layout, profile.Provider, projection.ConfigPath, sandbox)
	if v2 && !sandbox {
		// Host argv must not embed TaskRoot absolute paths (they contain the Task ID).
		configPath = blackboardV2HostRelativePath(layout.Workdir, configPath)
	}
	launchProfile := profile
	if projection.ResolvedProfile.Provider != "" {
		launchProfile = projection.ResolvedProfile
	}
	providerBinary, err := resolveProviderBinary(server.runtimePlugins, launchProfile)
	if err != nil {
		return taskLaunchPlan{}, err
	}

	commandProgram := providerBinary
	var commandArgs []string
	workdir := layout.Workdir
	launchFacts := ProviderSessionLaunchFacts{
		ProviderBinary: providerBinary,
		CustomArgs:     append([]string(nil), launchProfile.Fields.CustomArgs...),
		SettingsPath:   configPath,
		Workdir:        "/task/workdir",
		Env:            processEnv,
	}
	if !sandbox {
		launchFacts.Workdir = workdir
	}
	if launchModelOverride != "" {
		launchFacts.Model = launchModelOverride
	} else {
		launchFacts.Model = strings.TrimSpace(launchProfile.Fields.Model)
	}
	containerIDFile := ""
	sandboxNetwork := runner.SandboxNetworkDefault
	sandboxImage := ""
	if sandbox {
		sandboxNetwork = sandboxNetworkMode(created.RunControls)
		sandboxImage = strings.TrimSpace(profile.Fields.SandboxImage)
		if sandboxImage == "" {
			sandboxImage = server.sandboxImage
		}
		containerIDFile = filepath.Join(layout.Logs, "container.cid")
		if err := os.Remove(containerIDFile); err != nil && !os.IsNotExist(err) {
			return taskLaunchPlan{}, err
		}
		var readOnlyTaskDirs []string
		if v2 {
			readOnlyTaskDirs = []string{"workdir/.pentest"}
		}
		containerCLI := task.ResolveContainerCLI(created.RunControls.ContainerCLI, server.containerCLI)
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout:           layout,
			Provider:         profile.Provider,
			Image:            sandboxImage,
			ContainerCLI:     containerCLI,
			ContainerIDFile:  containerIDFile,
			RuntimeCommand:   []string{providerBinary},
			ProcessEnv:       processEnv,
			NetworkMode:      sandboxNetwork,
			VPNTun:           created.RunControls.SandboxVPNTun,
			TaskVolume:       server.taskVolume,
			TaskVolumeRoot:   server.taskVolumeRoot,
			ReadOnlyTaskDirs: readOnlyTaskDirs,
		})
		if err != nil {
			return taskLaunchPlan{}, err
		}
		commandProgram = command.Program
		commandArgs = command.Args
		launchFacts.SandboxContainerCLI = command.Program
		launchFacts.SandboxCreateArgs = command.Args
		launchFacts.SandboxCreateEnv = command.Env
		workdir = ""
	}

	runtimeConfig["provider"] = string(profile.Provider)
	runtimeConfig["generated_config"] = projection.Config
	if projection.ModelSnapshot != nil {
		runtimeConfig["model_provider_snapshot"] = projection.Config["model_provider_snapshot"]
	}
	if launchModelOverride != "" {
		runtimeConfig["launch_model_override"] = launchModelOverride
	}
	if launchReasoningEffort != "" {
		runtimeConfig["launch_reasoning_effort_override"] = launchReasoningEffort
	}
	runtimeConfig["layout"] = layout
	if containerIDFile != "" {
		runtimeConfig["container_id_file"] = containerIDFile
	}
	runtimeConfig["launch_command"] = adapters.Redact(map[string]any{
		"program": commandProgram,
		"args":    commandArgs,
	})
	if v2 {
		runtimeConfig = map[string]any{}
	}
	var capturedRuntimeConfig map[string]any
	if captured != nil && captured.CapturedRuntimeConfig != nil {
		// This branch runs while the Continuation transaction is open. Reuse the
		// pre-TX Snapshot and never re-enter SQLite from the binding callback.
		merged := make(map[string]any, len(captured.CapturedRuntimeConfig)+1)
		for key, value := range captured.CapturedRuntimeConfig {
			merged[key] = value
		}
		capturedRuntimeConfig = merged
	} else {
		var err error
		capturedRuntimeConfig, err = server.capturedTaskRuntimeConfig(created, launchProfile, projection.Config["model_provider_snapshot"], launchModelOverride, launchReasoningEffort)
		if err != nil {
			return taskLaunchPlan{}, err
		}
	}
	if capturedRuntimeConfig != nil {
		if ids, ok := projection.Config["projected_model_provider_ids"]; ok {
			capturedRuntimeConfig["projected_model_provider_ids"] = ids
		}
	}

	metadata := providerNativeSessionMetadata(sandbox, profile.Provider, layout, containerIDFile)
	stopConfirmation := dockerStopConfirmation(created.RunControls.ContainerCLI, server.containerCLI, containerIDFile)

	return taskLaunchPlan{
		RuntimeConfig:           runtimeConfig,
		CapturedRuntimeConfig:   capturedRuntimeConfig,
		MaterializedCredentials: prepared.Credentials(),
		Metadata:                metadata,
		StopConfirmation:        stopConfirmation,
		LaunchModelOverride:     launchModelOverride,
		LaunchReasoningEffort:   launchReasoningEffort,
		NativeResumeSessionID:   nativeResumeSessionID,
		ResolvedProfile:         launchProfile,
		ModelSnapshot:           projection.ModelSnapshot,
		PreparedProjection:      prepared,
		LaunchGoal:              launchGoal,
		BlackboardProjection:    blackboardProjection,
		BlackboardV2:            v2,
		ValidatedLayout:         &layout,
		Facts:                   launchFacts,
	}, nil
}

func blackboardV2ModelSnapshotPreview(snapshot *modelprovider.Snapshot) any {
	if snapshot == nil || snapshot.ModelProviderID == "" {
		return nil
	}
	return map[string]any{
		"model_provider_id": snapshot.ModelProviderID, "model_provider_name": snapshot.ModelProviderName,
		"endpoint_base_url": snapshot.EndpointBaseURL, "base_url": snapshot.BaseURL,
		"protocol": string(snapshot.Protocol), "model": snapshot.Model, "api_key_env": snapshot.APIKeyEnv,
		"api_key_source": snapshot.APIKeySource, "projection_target": snapshot.ProjectionTarget,
	}
}

// blackboardV2HostRelativePath rewrites task-local host paths to workdir-relative
// form so model-visible argv never embeds the Task ID from TaskRoot. Paths under
// runtime-home become "../runtime-home/..." which is intentional and ID-free.
func blackboardV2HostRelativePath(workdir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	relative, err := filepath.Rel(workdir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}
