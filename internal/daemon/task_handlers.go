package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"pentest/internal/blackboardv2"

	"pentest/internal/modelprovider"
	"pentest/internal/modeskill"
	"pentest/internal/preflight"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/workinggraph"
)

var (
	errNativeResumeUnavailable  = errors.New("native resume unavailable")
	errNativeSessionUnavailable = errors.New("native session unavailable")
)

type taskContinuationSelectionInput struct {
	RuntimeProfileID string         `json:"runtime_profile_id"`
	ModelProviderID  string         `json:"model_provider_id"`
	Model            string         `json:"model"`
	ReasoningEffort  string         `json:"reasoning_effort"`
	SubmittedBy      string         `json:"submitted_by"`
	Config           map[string]any `json:"config"`
}

func (input taskContinuationSelectionInput) hasSelection() bool {
	// Effort-only changes are real Runtime Turn Selection. Resume/queue must
	// record them; otherwise turn_selection stays stuck on the prior default.
	return strings.TrimSpace(input.RuntimeProfileID) != "" ||
		strings.TrimSpace(input.ModelProviderID) != "" ||
		input.selectedModel() != "" ||
		strings.TrimSpace(input.ReasoningEffort) != ""
}

func (input taskContinuationSelectionInput) hasRuntimeProfileSelection() bool {
	return strings.TrimSpace(input.RuntimeProfileID) != ""
}

func (input taskContinuationSelectionInput) selectedModel() string {
	return strings.TrimSpace(input.Model)
}

func (server *Server) handleCreateTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}

	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		request.Body = http.MaxBytesReader(response, request.Body, maxTotalUploadBytes)
	}
	input, attachments, err := parseCreateTaskRequest(request)
	if err != nil {
		writeRequestBodyError(response, err)
		return
	}
	if input.RunControls.Extras == nil && input.Extras != nil {
		input.RunControls.Extras = input.Extras
	}
	if input.RunControls.BlackboardMode == "" || input.RunControls.BlackboardMode == task.BlackboardModeInteractive {
		input.RunControls.BlackboardMode = task.BlackboardModeWorkingGraph
	}
	if input.Type != task.TypePentest && input.Type != task.TypeCTFChallenge {
		writeTaskError(response, task.ErrInvalidTaskType)
		return
	}

	defaulted, err := server.applyTaskLaunchDefaults(projectID, input.RuntimeProfileID, input.Runner)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load project defaults")
		return
	}
	input.RuntimeProfileID = defaulted.runtimeProfileID
	input.Runner = defaulted.runner
	launchModel := strings.TrimSpace(input.Model)
	resolvedConfiguration, err := server.resolveLaunchConfiguration(runtimeconfig.LaunchSelection{
		RuntimeProfileID:   input.RuntimeProfileID,
		RuntimePluginID:    input.RuntimePluginID,
		ModelProviderID:    input.ModelProviderID,
		Model:              launchModel,
		ReasoningEffort:    input.ReasoningEffort,
		Runner:             string(input.Runner),
		HostBrowserEnabled: input.HostBrowserEnabled,
	}, projectID)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	launchModelOverride := launchModel
	launchReasoningEffort, err := normalizeLaunchReasoningEffort(input.ReasoningEffort)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	preflightResult := server.preflight.Run(request.Context(), preflight.Request{
		RuntimeProfileID:    input.RuntimeProfileID,
		Profile:             &resolvedConfiguration.Profile,
		LaunchModelOverride: launchModelOverride,
		ProjectID:           projectID,
		Runner:              string(input.Runner),
		HostActivated:       input.RunControls.HostActivated,
		ProjectKind:         defaulted.project.Kind,
		ScopeCapabilities:   append([]string(nil), defaulted.project.Scope.Capabilities...),
		ContainerCLI:        task.ResolveContainerCLI(input.RunControls.ContainerCLI, server.containerCLI),
		SandboxVPNTun:       input.RunControls.SandboxVPNTun,
		SandboxNetwork:      input.RunControls.SandboxNetwork,
		RuntimeRoot:         server.runtimeRoot,
		BlackboardMode:      modeskill.Mode(input.RunControls.BlackboardMode),
	})
	server.logPreflightCustomArgConflict(input.RuntimeProfileID, preflightResult)
	server.checkHostBrowser(&preflightResult, input.HostBrowserEnabled, resolvedConfiguration.Profile.Provider)
	if !preflightResult.Pass {
		writeJSON(response, http.StatusBadRequest, struct {
			Error     string           `json:"error"`
			Preflight preflight.Result `json:"preflight"`
		}{
			Error:     "preflight failed",
			Preflight: preflightResult,
		})
		return
	}
	if err := runner.ValidateActivation(runner.ActivationRequest{
		Runner:        runner.Runner(input.Runner),
		HostActivated: input.RunControls.HostActivated,
	}); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID:        projectID,
		Type:             input.Type,
		Goal:             input.Goal,
		RuntimeProfileID: input.RuntimeProfileID,
		Runner:           input.Runner,
		RunControls:      input.RunControls,
		RuntimeConfig:    runtimeSnapshotMap(resolvedConfiguration.Snapshot),
	})
	if err != nil {
		writeTaskError(response, err)
		return
	}
	// The stored Task goal stays exactly as typed; only the launch goal handed
	// to the runtime carries the appended attachment paths.
	launchGoal, resolvedAttachments, err := launchGoalWithAttachments(created.Goal, created.Runner, server.runtimeRoot, created.ID, attachments)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	initialLaunch := resolveOwnerBlackboardRuntimeLaunch(
		launchGoal, created.RunControls.BlackboardMode == task.BlackboardModeDisabled,
	)
	plan, err := server.buildTaskLaunchPlanForBlackboardProjection(
		created, initialLaunch.goal, launchModelOverride, "", launchReasoningEffort, initialLaunch.projection,
	)
	if err != nil {
		writeTaskAdapterError(response, err)
		return
	}

	// Attachments project into the workdir created by buildTaskLaunchPlan, before
	// the background launch reads the goal that references them.
	if len(resolvedAttachments) > 0 {
		if plan.ValidatedLayout == nil {
			_ = server.closeHostBrowser(context.Background(), created.ID)
			writeTaskLaunchError(response, fmt.Errorf("task layout unavailable for attachments"))
			return
		}
		if err := writeTaskAttachments(plan.ValidatedLayout.Workdir, resolvedAttachments); err != nil {
			_ = server.closeHostBrowser(context.Background(), created.ID)
			writeTaskLaunchError(response, err)
			return
		}
	}

	server.recordLoopbackRewriteEvent(created)

	if err := server.launchTaskInBackground(created, plan, initialLaunch.goal); err != nil {
		writeTaskLaunchError(response, err)
		return
	}

	launched, err := server.taskDetail(created.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, launched)
}

type taskLaunchPlan struct {
	Adapter                      runtime.Adapter
	RuntimeConfig                map[string]any
	CapturedRuntimeConfig        map[string]any
	MaterializedCredentials      map[string]string
	Metadata                     func() (runtime.NativeSessionMetadata, error)
	StopConfirmation             runtime.StopConfirmation
	LaunchModelOverride          string
	LaunchReasoningEffort        string
	NativeResumeSessionID        string
	NativeResumeSessionPath      string
	ResolvedProfile              runtimeprofile.Profile
	ModelSnapshot                *modelprovider.Snapshot
	PreparedProjection           *runner.PreparedConfigProjection
	LaunchGoal                   string
	BlackboardProjection         runner.BlackboardProjection
	BlackboardV2                 bool
	ValidatedLayout              *runner.Layout
	BlackboardV2SteeringEventIDs []string
	Facts                        ProviderSessionLaunchFacts
}

type continuationLaunchBinding struct {
	V2Header       *blackboardv2.LaunchHeader
	InterfaceToken string
	ContinuationID string
	WorkingGraph   *workinggraph.Projection
}

func (server *Server) launchTaskInBackground(created task.Task, plan taskLaunchPlan, goal string) (err error) {
	defer func() {
		if err != nil {
			_ = server.closeHostBrowser(context.Background(), created.ID)
		}
	}()
	if plan.BlackboardProjection != runner.BlackboardProjectionOmitted && !plan.BlackboardV2 {
		return fmt.Errorf("Blackboard v2 launch projection is required")
	}
	// Replacement launches must not start while a prior Runtime is still owned
	// or not proven absent.
	if err := server.ensureRuntimeAbsentBeforeLaunch(created.ID); err != nil {
		return err
	}
	// Every Continuation pins a clone of the latest owner-local Snapshot. Runner
	// adapter diagnostics never replace the source-neutral configuration.
	versions, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil || len(versions) == 0 {
		return errors.New("runtime configuration snapshot is missing")
	}
	latestSnapshot, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return err
	}
	turn := runtimeconfig.RuntimeTurnSelection{
		ModelProviderID: plan.ResolvedProfile.Fields.ModelProviderID,
		Model:           firstNonBlank(plan.LaunchModelOverride, plan.ResolvedProfile.Fields.ModelOverride),
		ReasoningEffort: firstNonBlank(plan.LaunchReasoningEffort, plan.ResolvedProfile.Fields.ReasoningEffort),
	}
	providerSnapshot := latestSnapshot.ModelProvider
	if plan.ModelSnapshot != nil {
		providerSnapshot = *plan.ModelSnapshot
	}
	clonedSnapshot, err := runtimeconfig.CloneForTurn(latestSnapshot, turn, providerSnapshot)
	if err != nil {
		return err
	}
	if projected, ok := plan.CapturedRuntimeConfig["projected_model_provider_ids"]; ok {
		if clonedSnapshot.ConfigProjection == nil {
			clonedSnapshot.ConfigProjection = map[string]any{}
		}
		clonedSnapshot.ConfigProjection["projected_model_provider_ids"] = projected
	}
	plan.CapturedRuntimeConfig = runtimeSnapshotMap(clonedSnapshot)
	server.logTaskLaunchStage(created, "prepare_continuation")
	continuation, boundPlan, err := server.prepareTaskContinuationLaunch(created, plan, goal)
	if err != nil {
		return err
	}
	plan = boundPlan
	if supportsPersistentProviderSession(created.Runner, plan.ResolvedProfile.Provider) {
		if server.providerSessionFactory == nil {
			redactedErr := newProviderSessionFactoryError(errors.New("provider session factory is required"), plan.MaterializedCredentials)
			server.failProviderSessionLaunch(created.ID, continuation.ID, redactedErr)
			return redactedErr
		}
		binding, factoryErr := server.providerSessionFactory.Open(context.Background(), ProviderSessionLaunchRequest{
			Owner: created.OwnerContract(""), Continuation: ownerContinuationFromTask(continuation), Provider: plan.ResolvedProfile.Provider,
			Runner: created.Runner, LaunchGoal: plan.LaunchGoal, RuntimeConfig: plan.CapturedRuntimeConfig,
			Facts: plan.Facts,
		})
		if factoryErr == nil {
			factoryErr = validateProviderSessionBinding(binding)
		}
		if factoryErr != nil {
			redactedErr := newProviderSessionFactoryError(factoryErr, plan.MaterializedCredentials)
			server.failProviderSessionLaunch(created.ID, continuation.ID, redactedErr)
			return redactedErr
		}
		if bindErr := server.BindProviderSession(created.ID, binding.Session); bindErr != nil {
			_ = binding.Session.Close(context.Background())
			redactedErr := newProviderSessionFactoryError(bindErr, plan.MaterializedCredentials)
			server.failProviderSessionLaunch(created.ID, continuation.ID, redactedErr)
			return redactedErr
		}
		if adapter, ok := binding.Adapter.(*runtime.ProviderSessionRunAdapter); ok {
			selection, selectionErr := initialProviderTurnSelection(plan)
			if selectionErr != nil {
				_ = binding.Session.Close(context.Background())
				server.providerSessions.remove(created.ID)
				redactedErr := newProviderSessionFactoryError(selectionErr, plan.MaterializedCredentials)
				server.failProviderSessionLaunch(created.ID, continuation.ID, redactedErr)
				return redactedErr
			}
			adapter.SetInitialTurnSelection(selection)
		}
		providerHome := ""
		if plan.ValidatedLayout != nil {
			providerHome = plan.ValidatedLayout.ProviderHome
		}
		plan.Adapter = wrapPersistentProviderAdapter(binding.Adapter, plan.ResolvedProfile.Provider, providerHome)
	}
	launchCtx, err := server.beginRuntimeLaunch()
	if err != nil {
		_ = server.closeProviderSession(created.ID)
		return err
	}
	server.logTask(created, "launched", "")
	server.hostBrowserMu.Lock()
	browserProcess := server.hostBrowserProcesses[created.ID]
	server.hostBrowserMu.Unlock()
	go func() {
		defer server.runtimeLaunchWG.Done()
		// A finished launch must not close a replacement Continuation's service.
		defer server.closeHostBrowserProcess(context.Background(), created.ID, browserProcess)
		launchGoal := plan.LaunchGoal
		if launchGoal == "" {
			launchGoal = goal
		}
		err := server.harness.Launch(launchCtx, runtime.LaunchRequest{
			TaskID:           created.ID,
			Goal:             launchGoal,
			Adapter:          plan.Adapter,
			ContinuationID:   continuation.ID,
			Metadata:         plan.Metadata,
			StopConfirmation: plan.StopConfirmation,
		})
		switch {
		case errors.Is(err, runtime.ErrHarnessShutdown):
			server.logTask(created, "shutdown", "")
		case err == nil:
			server.logTask(created, "completed", "")
			_ = server.closeProviderSession(created.ID)
		case errors.Is(err, context.Canceled):
			server.logTask(created, "stopped", "")
		default:
			// Unexpected persistent Runtime exit: ownership must not linger.
			_ = server.closeProviderSession(created.ID)
			// HTTP-facing error text stays redacted; log the unwrap chain for
			// operator diagnostics (bridge RPC failures, selection rejections).
			root := err
			for {
				unwrapped := errors.Unwrap(root)
				if unwrapped == nil {
					break
				}
				root = unwrapped
			}
			server.logTask(created, "failed", fmt.Sprintf("%v root=%v", err, root))
		}
	}()
	return nil
}
