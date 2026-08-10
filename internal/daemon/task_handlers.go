package daemon

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/blackboardv2"
	"pentest/internal/finishreadiness"

	"pentest/internal/modelprovider"
	"pentest/internal/owner"
	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/steering"
	"pentest/internal/store"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

var (
	errNativeResumeUnavailable  = errors.New("native resume unavailable")
	errNativeSessionUnavailable = errors.New("native session unavailable")
)

type taskContinuationSelectionInput struct {
	RuntimeProfileID string `json:"runtime_profile_id"`
	ModelProviderID  string `json:"model_provider_id"`
	// Model is the preferred Runtime Turn Selection field. model_override remains
	// accepted as a backwards-compatible alias used by older clients.
	Model           string         `json:"model"`
	ModelOverride   string         `json:"model_override"`
	ReasoningEffort string         `json:"reasoning_effort"`
	SubmittedBy     string         `json:"submitted_by"`
	Config          map[string]any `json:"config"`
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

// selectedModel returns the explicit model for this turn. Prefer `model`, then
// the legacy `model_override` alias.
func (input taskContinuationSelectionInput) selectedModel() string {
	if model := strings.TrimSpace(input.Model); model != "" {
		return model
	}
	return strings.TrimSpace(input.ModelOverride)
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
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if input.RunControls.Extras == nil && input.Extras != nil {
		input.RunControls.Extras = input.Extras
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
	if input.RunControls.BlackboardConclusionMode == task.BlackboardConclusionModeAssisted {
		profile, profileErr := server.profiles.Get(input.RuntimeProfileID)
		if profileErr != nil {
			writeError(response, http.StatusBadRequest, "runtime profile not found")
			return
		}
		if !server.supportsAssistedConclusion(profile.Provider) {
			writeError(response, http.StatusBadRequest, errAssistedConclusionUnsupported.Error())
			return
		}
	}

	launchModelOverride := strings.TrimSpace(input.ModelOverride)
	launchReasoningEffort, err := normalizeLaunchReasoningEffort(input.ReasoningEffort)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	preflightResult := server.preflight.Run(request.Context(), preflight.Request{
		RuntimeProfileID:    input.RuntimeProfileID,
		LaunchModelOverride: launchModelOverride,
		ProjectID:           projectID,
		Runner:              string(input.Runner),
		HostActivated:       input.RunControls.HostActivated,
		ProjectKind:         defaulted.project.Kind,
		ScopeCapabilities:   append([]string(nil), defaulted.project.Scope.Capabilities...),
		ContainerCLI:        server.containerCLI,
		SandboxVPNTun:       input.RunControls.SandboxVPNTun,
		SandboxNetwork:      input.RunControls.SandboxNetwork,
	})
	server.logPreflightCustomArgConflict(input.RuntimeProfileID, preflightResult)
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

	plan, err := server.buildTaskLaunchPlan(created, launchGoal, launchModelOverride, "", launchReasoningEffort)
	if err != nil {
		writeTaskAdapterError(response, err)
		return
	}

	// Attachments project into the workdir created by buildTaskLaunchPlan, before
	// the background launch reads the goal that references them.
	if len(resolvedAttachments) > 0 {
		if plan.ValidatedLayout == nil {
			writeTaskLaunchError(response, fmt.Errorf("task layout unavailable for attachments"))
			return
		}
		if err := writeTaskAttachments(plan.ValidatedLayout.Workdir, resolvedAttachments); err != nil {
			writeTaskLaunchError(response, err)
			return
		}
	}

	server.recordLoopbackRewriteEvent(created)

	if err := server.launchTaskInBackground(created, plan, launchGoal); err != nil {
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
	Adapter                 runtime.Adapter
	RuntimeConfig           map[string]any
	CapturedRuntimeConfig   map[string]any
	MaterializedCredentials map[string]string
	Metadata                func() (runtime.NativeSessionMetadata, error)
	StopConfirmation        runtime.StopConfirmation
	LaunchModelOverride     string
	LaunchReasoningEffort   string
	NativeResumeSessionID   string
	NativeResumeSessionPath string
	ResolvedProfile         runtimeprofile.Profile
	ModelSnapshot           *modelprovider.Snapshot
	// GlobalModelProviderSnapshot is listed before CreateContinuation so
	// precommit/BindGrant projection never queries modelProviders.Service.
	GlobalModelProviderSnapshot  *runner.GlobalModelProviderSnapshot
	SkillBundles                 []skill.Bundle
	LaunchGoal                   string
	BlackboardV2                 bool
	ValidatedLayout              *runner.Layout
	BlackboardV2SteeringEventIDs []string
}

type continuationLaunchBinding struct {
	V2Header       *blackboardv2.LaunchHeader
	InterfaceToken string
}

func (server *Server) launchTaskInBackground(created task.Task, plan taskLaunchPlan, goal string) error {
	if !plan.BlackboardV2 {
		return fmt.Errorf("Blackboard v2 launch projection is required")
	}
	// Replacement launches must not start while a prior Runtime is still owned
	// or not proven absent.
	if err := server.ensureRuntimeAbsentBeforeLaunch(created.ID); err != nil {
		return err
	}
	server.logTaskLaunchStage(created, "prepare_continuation")
	continuation, boundPlan, err := server.prepareBlackboardV2ContinuationLaunch(created, plan, goal)
	if err != nil {
		return err
	}
	plan = boundPlan
	if server.providerSessionFactory != nil && supportsPersistentProviderSession(created.Runner, plan.ResolvedProfile.Provider) {
		// Persistent selection with a factory is fail-closed: factory/bridge errors
		// never fall back to the legacy one-shot Adapter. Production always installs
		// the factory; unit tests without one still exercise one-shot host paths.
		binding, factoryErr := server.providerSessionFactory.Open(context.Background(), ProviderSessionLaunchRequest{
			Owner: created.OwnerContract(""), Continuation: ownerContinuationFromTask(continuation), Provider: plan.ResolvedProfile.Provider,
			Runner: created.Runner, LaunchGoal: plan.LaunchGoal, RuntimeConfig: plan.CapturedRuntimeConfig,
			LegacyAdapter: plan.Adapter,
		})
		if factoryErr == nil {
			factoryErr = validateProviderSessionBinding(binding)
		}
		if factoryErr == nil && created.RunControls.BlackboardConclusionMode == task.BlackboardConclusionModeAssisted {
			factoryErr = validateAssistedConclusionBinding(binding)
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
		// Pi persistent sessions write their turn output to a session jsonl
		// rather than the bridge RPC channel, so wrap the session adapter with
		// the tailer that re-emits those lines as runtime_output events. The
		// legacy one-shot path does the same for sandboxed Pi.
		providerHome := ""
		if plan.ValidatedLayout != nil {
			providerHome = plan.ValidatedLayout.ProviderHome
		}
		plan.Adapter = wrapPersistentProviderAdapter(binding.Adapter, plan.ResolvedProfile.Provider, providerHome)
	}
	server.logTask(created, "launched", "")
	go func() {
		launchGoal := plan.LaunchGoal
		if launchGoal == "" {
			launchGoal = goal
		}
		err := server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:           created.ID,
			Goal:             launchGoal,
			Adapter:          plan.Adapter,
			ContinuationID:   continuation.ID,
			Metadata:         plan.Metadata,
			StopConfirmation: plan.StopConfirmation,
		})
		switch {
		case err == nil:
			server.logTask(created, "completed", "")
			// Persistent sessions that end cleanly still release ownership.
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

func (server *Server) failProviderSessionLaunch(taskID, continuationID string, cause error) {
	// The durable Continuation already exists at this point. Marking both
	// records terminal prevents an unbound pending Continuation from looking
	// resumable after a factory crash or setup rejection.
	// Backend log carries the full unredacted cause chain (server-only
	// diagnostics) so operators see the real docker/bridge failure; the
	// persisted lifecycle event and HTTP error carry the redacted detail.
	if server.logger != nil && cause != nil {
		underlying := errors.Unwrap(cause)
		if underlying == nil {
			underlying = cause
		}
		server.logger.Printf("provider session setup failed task=%s continuation=%s cause=%v", taskID, continuationID, underlying)
	}
	lifecycleError := "provider session setup failed"
	if cause != nil {
		lifecycleError = cause.Error()
	}
	_, _ = server.tasks.AppendContinuationEvent(taskID, continuationID, task.EventKindLifecycle, task.EventPayload{
		"phase": "provider_session_setup_failed", "error": lifecycleError,
	})
	_, _ = server.tasks.UpdateContinuationStatus(continuationID, task.StatusFailed)
	_, _ = server.tasks.UpdateStatus(taskID, task.StatusFailed)
}

// newProviderSessionFactoryError wraps a setup failure with the stable phase
// label and a redacted, human-readable detail for the frontend. Credential
// values from the launch snapshot are masked in addition to shape-based
// redaction so the cause can be shown without leaking secrets.
func newProviderSessionFactoryError(cause error, credentials map[string]string) *providerSessionFactoryError {
	return &providerSessionFactoryError{cause: cause, detail: redactProviderSessionCause(cause, credentials)}
}

func redactProviderSessionCause(cause error, credentials map[string]string) string {
	if cause == nil {
		return ""
	}
	secrets := make([]string, 0, len(credentials))
	for _, value := range credentials {
		secrets = append(secrets, value)
	}
	redacted := adapters.NewRedactor(secrets).Redact(map[string]any{"detail": cause.Error()})
	detail, _ := redacted["detail"].(string)
	return strings.TrimSpace(detail)
}

func (server *Server) prepareBlackboardV2ContinuationLaunch(created task.Task, plan taskLaunchPlan, goal string) (task.TaskContinuation, taskLaunchPlan, error) {
	if plan.ValidatedLayout == nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, fmt.Errorf("Blackboard v2 layout was not validated")
	}
	provider := plan.ResolvedProfile.Provider
	if provider == "" {
		profile, err := server.profiles.Get(created.RuntimeProfileID)
		if err != nil {
			return task.TaskContinuation{}, taskLaunchPlan{}, err
		}
		provider = profile.Provider
		plan.ResolvedProfile = profile
	}
	if !runner.BlackboardV2SupportsProvider(provider) {
		return task.TaskContinuation{}, taskLaunchPlan{}, fmt.Errorf("Blackboard v2 launch projection is unsupported for provider %q", provider)
	}
	// Always resolve the global provider snapshot before CreateContinuation so
	// Precommit/BindGrant projection never re-enters modelProviders.Service
	// while the continuity transaction holds SQLite locks.
	if plan.GlobalModelProviderSnapshot == nil {
		snapshot, err := server.snapshotGlobalModelProviders()
		if err != nil {
			return task.TaskContinuation{}, taskLaunchPlan{}, err
		}
		plan.GlobalModelProviderSnapshot = snapshot
	}
	layout, err := runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, created.ID, provider)
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	plan.ValidatedLayout = &layout
	var boundPlan taskLaunchPlan
	var launchHeader blackboardv2.LaunchHeader
	usesTrustedMCP := runner.BlackboardV2UsesTrustedMCP(provider)
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: created.ProjectID, TaskID: created.ID, RuntimeProfileID: created.RuntimeProfileID,
		RuntimeProvider: string(provider), Runner: created.Runner, RuntimeConfig: plan.CapturedRuntimeConfig,
		SteeringEventIDs: plan.BlackboardV2SteeringEventIDs,
		NativeSessionID:  plan.NativeResumeSessionID, NativeSessionPath: plan.NativeResumeSessionPath,
		Precommit: func(projection blackboardv2.ContinuationLaunchProjection) error {
			launchHeader = blackboardv2.LaunchHeader{
				Runner: string(created.Runner), ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				Schema: projection.Schema, Revision: projection.Revision,
			}
			// Codex completes projection here (networkless). Claude/Pi project
			// grant-less layout/config first; BindGrant re-projects with the
			// Continuation grant before the launch transaction commits.
			binding := &continuationLaunchBinding{V2Header: &launchHeader}
			var err error
			boundPlan, err = server.buildTaskLaunchPlanWithBinding(created, goal, plan.LaunchModelOverride, plan.NativeResumeSessionID, plan.LaunchReasoningEffort, binding, &plan)
			if err != nil {
				return err
			}
			// Precommit runs before CreateContinuationLaunchTx stores RuntimeConfig.
			// Copy only non-secret fixed-at-launch fields (Pi projected set) into
			// the map CreateContinuation will persist so native turn selection
			// can fail closed against the real projected set. Never copy auth,
			// env, or credential material from projection previews.
			mergeFixedAtLaunchRuntimeConfig(plan.CapturedRuntimeConfig, boundPlan.CapturedRuntimeConfig)
			return nil
		},
		BindGrant: func(plaintextGrant string) error {
			if !usesTrustedMCP || strings.TrimSpace(plaintextGrant) == "" {
				return nil
			}
			binding := &continuationLaunchBinding{V2Header: &launchHeader, InterfaceToken: plaintextGrant}
			var err error
			boundPlan, err = server.buildTaskLaunchPlanWithBinding(created, goal, plan.LaunchModelOverride, plan.NativeResumeSessionID, plan.LaunchReasoningEffort, binding, &plan)
			if err != nil {
				scrubBlackboardV2GrantBearingProjection(layout, provider)
				return err
			}
			// BindGrant runs after the config version is stored; still keep the
			// in-memory plan consistent for the returned boundPlan path.
			mergeFixedAtLaunchRuntimeConfig(plan.CapturedRuntimeConfig, boundPlan.CapturedRuntimeConfig)
			return nil
		},
		UnbindGrant: func() {
			if usesTrustedMCP {
				scrubBlackboardV2GrantBearingProjection(layout, provider)
			}
		},
	})
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	return launch.Continuation, boundPlan, nil
}

// mergeFixedAtLaunchRuntimeConfig copies only non-secret fixed-at-launch fields
// from source into dest so CreateContinuationLaunchTx can persist Precommit
// projection results without a second config version. Credential values, auth
// previews, and env maps must never enter stored Task Runtime Configuration.
func mergeFixedAtLaunchRuntimeConfig(dest, source map[string]any) {
	if dest == nil || source == nil {
		return
	}
	if ids, ok := source["projected_model_provider_ids"]; ok {
		dest["projected_model_provider_ids"] = ids
	}
}

// scrubBlackboardV2GrantBearingProjection removes trusted MCP config that may
// embed a Continuation grant token after a failed atomic launch.
func scrubBlackboardV2GrantBearingProjection(layout runner.Layout, provider runtimeprofile.Provider) {
	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		_ = os.Remove(filepath.Join(layout.Workdir, ".mcp.json"))
	case runtimeprofile.ProviderPi:
		_ = os.Remove(filepath.Join(layout.ProviderHome, "agent", "mcp.json"))
	}
}

func (server *Server) recoverBlackboardV2ContinuationFiles(ctx context.Context) error {
	active, err := server.blackboardV2Continuity.ActiveSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, snapshot := range active {
		provider := runtimeprofile.Provider(snapshot.RuntimeProvider)
		if !runner.BlackboardV2SupportsProvider(provider) {
			continue
		}
		created, err := server.tasks.Get(snapshot.TaskID)
		if err != nil {
			return fmt.Errorf("recover Blackboard v2 Continuation Task: %w", err)
		}
		layout, err := runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, snapshot.TaskID, provider)
		if err != nil {
			return fmt.Errorf("recover Blackboard v2 layout: %w", err)
		}
		// Restart recovery must rematerialize persisted Working Snapshot bytes
		// (last acknowledged revision), never overwrite a synchronized working
		// file with immutable Launch Pin bytes.
		if err := server.blackboardV2Continuity.MaterializeWorkingSnapshot(ctx, snapshot.ContinuationID); err != nil {
			return fmt.Errorf("recover Blackboard v2 Working Snapshot: %w", err)
		}
		header := blackboardv2.LaunchHeader{
			Runner: string(snapshot.Runner), ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
			Schema: snapshot.Schema, Revision: snapshot.Revision,
		}
		if err := runner.ProjectBlackboardV2Files(layout, provider, header, created.ScopeSnapshot); err != nil {
			return fmt.Errorf("recover Blackboard v2 context: %w", err)
		}
	}
	return nil
}

type taskLaunchDefaults struct {
	runtimeProfileID string
	runner           task.Runner
	project          project.Project
}

func (server *Server) applyTaskLaunchDefaults(projectID, requestedProfileID string, requestedRunner task.Runner) (taskLaunchDefaults, error) {
	found, err := server.projects.Get(projectID)
	if err != nil {
		return taskLaunchDefaults{}, err
	}

	resolved := taskLaunchDefaults{
		runtimeProfileID: requestedProfileID,
		runner:           requestedRunner,
		project:          found,
	}
	if resolved.runtimeProfileID == "" {
		resolved.runtimeProfileID = found.Defaults.RuntimeProfile
	}
	if resolved.runner == "" {
		resolved.runner = task.Runner(found.Defaults.Runner)
	}
	if resolved.runner == "" {
		resolved.runner = task.RunnerSandbox
	}
	return resolved, nil
}

// recordLoopbackRewriteEvent records a task event when a sandbox task's goal
// loopback targets were rewritten to host.docker.internal. It is a best-effort
// record: failures are ignored so they cannot block task launch. Host-runner
// tasks and goals without loopback targets produce no event.
func (server *Server) recordLoopbackRewriteEvent(created task.Task) {
	sandbox := created.Runner == task.RunnerSandbox
	if !sandbox {
		return
	}
	rewritten := runner.RewriteLoopbackTargets(created.Goal, sandbox)
	if rewritten == created.Goal {
		return
	}
	_, _ = server.tasks.AppendEvent(created.ID, task.EventKindLifecycle, task.EventPayload{
		"phase": "target_rewrite",
		"from":  created.Goal,
		"to":    rewritten,
		"note":  "loopback targets rewritten to host.docker.internal for sandbox runtime",
	})
}

func (server *Server) buildTaskAdapter(created task.Task, launchModelOverride string) (runtime.Adapter, map[string]any, error) {
	plan, err := server.buildTaskLaunchPlanWithBinding(created, created.Goal, launchModelOverride, "", "", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return plan.Adapter, plan.RuntimeConfig, nil
}

func (server *Server) buildTaskAdapterForGoal(created task.Task, goal string, launchModelOverride string) (runtime.Adapter, map[string]any, error) {
	plan, err := server.buildTaskLaunchPlanWithBinding(created, goal, launchModelOverride, "", "", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return plan.Adapter, plan.RuntimeConfig, nil
}

func (server *Server) buildTaskLaunchPlan(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, launchReasoningEffort string) (taskLaunchPlan, error) {
	server.logTaskLaunchStage(created, "build_plan")
	profile, err := server.profiles.Get(created.RuntimeProfileID)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if server.blackboardV2Continuity != nil && runner.BlackboardV2SupportsProvider(profile.Provider) {
		return server.prepareBlackboardV2TaskLaunchPlan(created, goal, launchModelOverride, nativeResumeSessionID, launchReasoningEffort, profile)
	}
	return server.buildTaskLaunchPlanWithBinding(created, goal, launchModelOverride, nativeResumeSessionID, launchReasoningEffort, nil, nil)
}

func (server *Server) prepareBlackboardV2TaskLaunchPlan(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, launchReasoningEffort string, profile runtimeprofile.Profile) (taskLaunchPlan, error) {
	layout, err := runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	skillBundles, err := server.skills.EnabledSkillBundles(profile.ID)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	var modelSnapshot *modelprovider.Snapshot
	if strings.TrimSpace(profile.Fields.ModelProviderID) != "" {
		resolved, err := modelprovider.Resolve(modelprovider.ResolveRequest{
			Profile: profile, Providers: server.modelProviders, Plugins: server.runtimePlugins,
			Credentials: server.creds, ProjectID: created.ProjectID, CheckEnv: true,
			LaunchModelOverride: launchModelOverride,
		})
		if err != nil {
			return taskLaunchPlan{}, err
		}
		if resolved.ModelProviderID != "" {
			modelSnapshot = &resolved
			profile = runner.BlackboardV2ProfileWithModelSnapshot(profile, resolved)
		}
	}
	// List globals before CreateContinuation; precommit projection must use
	// this immutable snapshot only (ADR 0015 fixed set, no SQLite re-entry).
	globalSnapshot, err := server.snapshotGlobalModelProviders()
	if err != nil {
		return taskLaunchPlan{}, err
	}
	materializedCredentials, err := runner.MaterializeLaunchCredentials(profile, runner.ProjectionRequest{
		Owner:                       created.OwnerContract(layout.Workdir),
		Credentials:                 server.creds,
		ModelProviders:              server.modelProviders,
		GlobalModelProviderSnapshot: globalSnapshot,
		ModelSnapshot:               modelSnapshot,
	})
	if err != nil {
		return taskLaunchPlan{}, err
	}
	capturedRuntimeConfig, err := capturedTaskRuntimeConfig(created, profile, runtimeprofile.GeneratedConfig(profile), blackboardV2ModelSnapshotPreview(modelSnapshot), launchModelOverride, launchReasoningEffort)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	return taskLaunchPlan{
		CapturedRuntimeConfig:       capturedRuntimeConfig,
		MaterializedCredentials:     materializedCredentials,
		LaunchModelOverride:         launchModelOverride,
		LaunchReasoningEffort:       launchReasoningEffort,
		NativeResumeSessionID:       nativeResumeSessionID,
		ResolvedProfile:             profile,
		ModelSnapshot:               modelSnapshot,
		GlobalModelProviderSnapshot: globalSnapshot,
		SkillBundles:                append([]skill.Bundle(nil), skillBundles...),
		LaunchGoal:                  goal,
		BlackboardV2:                true,
		ValidatedLayout:             &layout,
	}, nil
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

func (server *Server) buildTaskLaunchPlanWithBinding(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, launchReasoningEffort string, binding *continuationLaunchBinding, captured *taskLaunchPlan) (taskLaunchPlan, error) {
	if captured != nil && strings.TrimSpace(launchReasoningEffort) == "" {
		launchReasoningEffort = captured.LaunchReasoningEffort
	}
	v2 := binding != nil && binding.V2Header != nil
	runtimeConfig := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runner":             created.Runner,
	}

	var profile runtimeprofile.Profile
	var skillBundles []skill.Bundle
	var capturedModelSnapshot *modelprovider.Snapshot
	var materializedCredentials map[string]string
	var globalSnapshot *runner.GlobalModelProviderSnapshot
	if captured != nil {
		profile = captured.ResolvedProfile
		skillBundles = append([]skill.Bundle(nil), captured.SkillBundles...)
		capturedModelSnapshot = captured.ModelSnapshot
		materializedCredentials = captured.MaterializedCredentials
		globalSnapshot = captured.GlobalModelProviderSnapshot
	} else {
		var err error
		profile, err = server.profiles.Get(created.RuntimeProfileID)
		if err != nil {
			return taskLaunchPlan{}, err
		}
		// Non-v2 / first-pass path: list before any projection so Pi never
		// re-enters modelProviders.Service mid-transaction.
		globalSnapshot, err = server.snapshotGlobalModelProviders()
		if err != nil {
			return taskLaunchPlan{}, err
		}
	}
	sandbox := created.Runner == task.RunnerSandbox
	goal = runner.RewriteLoopbackTargets(goal, sandbox)
	launchGoal := goal
	if binding != nil {
		if binding.V2Header != nil {
			launchGoal = blackboardv2.RenderLaunchHeader(*binding.V2Header) + "\n\nTASK GOAL:\n" + goal
		}
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		runtimeConfig["provider"] = string(runtimeprofile.ProviderFake)
		runtimeConfig["generated_config"] = runtimeprofile.GeneratedConfig(profile)
		capturedRuntimeConfig, err := capturedTaskRuntimeConfig(created, profile, runtimeConfig["generated_config"], nil, launchModelOverride, launchReasoningEffort)
		if err != nil {
			return taskLaunchPlan{}, err
		}
		if captured != nil {
			capturedRuntimeConfig = captured.CapturedRuntimeConfig
		}
		return taskLaunchPlan{Adapter: runtime.NewFakeAdapter(), RuntimeConfig: runtimeConfig, CapturedRuntimeConfig: capturedRuntimeConfig, LaunchModelOverride: launchModelOverride, LaunchReasoningEffort: launchReasoningEffort, NativeResumeSessionID: nativeResumeSessionID, ResolvedProfile: profile, LaunchGoal: launchGoal}, nil
	}
	if captured == nil {
		var err error
		skillBundles, err = server.skills.EnabledSkillBundles(profile.ID)
		if err != nil {
			return taskLaunchPlan{}, err
		}
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

	authToken := server.authToken
	projectionProfile := profile
	if v2 {
		if profile.Provider == runtimeprofile.ProviderCodex {
			// Codex v2 stays networkless for Project Interface writes.
			projectionProfile = codexV2ProjectionProfile(profile)
			authToken = ""
		} else if runner.BlackboardV2UsesTrustedMCP(profile.Provider) {
			// Claude/Pi use the Continuation grant, never the operator token.
			authToken = ""
			if binding != nil {
				authToken = binding.InterfaceToken
			}
		} else {
			return taskLaunchPlan{}, fmt.Errorf("Blackboard v2 launch projection is unsupported for provider %q", profile.Provider)
		}
	}
	projectionRequest := runner.ProjectionRequest{
		Owner:                       created.OwnerContract(layout.Workdir),
		ScopeSnapshot:               created.ScopeSnapshot,
		Credentials:                 server.creds,
		MaterializedCredentials:     materializedCredentials,
		DaemonAddr:                  server.listenAddr,
		AuthToken:                   authToken,
		Sandbox:                     sandbox,
		RuntimePlugins:              server.runtimePlugins,
		RuntimeExtensions:           server.runtimeExtensions,
		ModelProviders:              server.modelProviders,
		GlobalModelProviderSnapshot: globalSnapshot,
		ModelSnapshot:               capturedModelSnapshot,
		LaunchModelOverride:         launchModelOverride,
		SkillBundles:                skillBundles,
	}
	var projection runner.ConfigProjection
	if v2 {
		projection, err = runner.ProjectBlackboardV2RuntimeConfig(layout, projectionProfile, projectionRequest)
	} else {
		projection, err = runner.ProjectRuntimeConfig(layout, projectionProfile, projectionRequest)
	}
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if binding != nil && binding.V2Header != nil {
		if !runner.BlackboardV2SupportsProvider(profile.Provider) {
			return taskLaunchPlan{}, fmt.Errorf("Blackboard v2 launch projection is unsupported for provider %q", profile.Provider)
		}
		if err := runner.ProjectBlackboardV2Files(layout, profile.Provider, *binding.V2Header, created.ScopeSnapshot); err != nil {
			return taskLaunchPlan{}, err
		}
	}
	configPath := runner.LaunchConfigPath(layout, profile.Provider, projection.ConfigPath, sandbox)
	mcpConfigPath := runner.LaunchMCPConfigPath(layout, profile.Provider, sandbox, projection)
	if v2 && !sandbox {
		// Host argv must not embed TaskRoot absolute paths (they contain the Task ID).
		configPath = blackboardV2HostRelativePath(layout.Workdir, configPath)
		mcpConfigPath = blackboardV2HostRelativePath(layout.Workdir, mcpConfigPath)
	}
	launchProfile := profile
	if projection.ResolvedProfile.Provider != "" {
		launchProfile = projection.ResolvedProfile
	}
	providerCommand, err := adapters.BuildLaunchOrResumeArgs(adapters.LaunchArgsRequest{
		Provider:      profile.Provider,
		Profile:       launchProfile,
		Goal:          launchGoal,
		ConfigPath:    configPath,
		MCPConfigPath: mcpConfigPath,
		Sandbox:       sandbox,
	}, nativeResumeSessionID)
	if err != nil {
		return taskLaunchPlan{}, err
	}

	runtimeCommand := append([]string{}, providerCommand...)
	commandProgram := runtimeCommand[0]
	commandArgs := runtimeCommand[1:]
	workdir := layout.Workdir
	containerIDFile := ""
	sandboxNetwork := runner.SandboxNetworkDefault
	sandboxImage := ""
	launchCtx := runner.RuntimeOwnerContext{Owner: created.OwnerContract(layout.Workdir), Sandbox: sandbox}
	processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, launchProfile, sandbox, launchCtx, runner.ProjectionRequest{
		Owner:                       created.OwnerContract(layout.Workdir),
		ScopeSnapshot:               created.ScopeSnapshot,
		Credentials:                 server.creds,
		MaterializedCredentials:     materializedCredentials,
		DaemonAddr:                  server.listenAddr,
		AuthToken:                   authToken,
		Sandbox:                     sandbox,
		RuntimePlugins:              server.runtimePlugins,
		RuntimeExtensions:           server.runtimeExtensions,
		ModelProviders:              server.modelProviders,
		GlobalModelProviderSnapshot: globalSnapshot,
		ModelSnapshot:               projection.ModelSnapshot,
		SkillBundles:                skillBundles,
	})
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if v2 {
		processEnv = runner.BlackboardV2ProcessEnv(processEnv, layout, sandbox)
	}
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
		sandboxRuntime := runtimeCommand
		// One-shot Pi sandbox installs pi via an sh -c bootstrap wrapper. The
		// persistent provider-session path rewrites the image command to the
		// bridge and therefore needs a bare "pi" token in docker create argv.
		// Skip the wrapper whenever this launch will open a provider session.
		if profile.Provider == runtimeprofile.ProviderPi {
			usePersistentSession := server.providerSessionFactory != nil &&
				supportsPersistentProviderSession(created.Runner, profile.Provider)
			if !usePersistentSession {
				wrapped, err := runner.WrapSandboxPiCommand(runtimeCommand, launchProfile.Fields.Env)
				if err != nil {
					return taskLaunchPlan{}, err
				}
				sandboxRuntime = wrapped
			}
		}
		var readOnlyTaskFiles, readOnlyTaskDirs []string
		if binding != nil {
			if v2 {
				readOnlyTaskDirs = []string{"workdir/.pentest"}
			} else {
				readOnlyTaskFiles = []string{"workdir/.pentest/blackboard.json", "workdir/.pentest/scope.json"}
			}
		}
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout:            layout,
			Provider:          profile.Provider,
			Image:             sandboxImage,
			ContainerCLI:      server.containerCLI,
			ContainerIDFile:   containerIDFile,
			RuntimeCommand:    sandboxRuntime,
			ProcessEnv:        processEnv,
			NetworkMode:       sandboxNetwork,
			VPNTun:            created.RunControls.SandboxVPNTun,
			TaskVolume:        server.taskVolume,
			TaskVolumeRoot:    server.taskVolumeRoot,
			ReadOnlyTaskFiles: readOnlyTaskFiles,
			ReadOnlyTaskDirs:  readOnlyTaskDirs,
		})
		if err != nil {
			return taskLaunchPlan{}, err
		}
		commandProgram = command.Program
		commandArgs = command.Args
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
	capturedRuntimeConfig, err := capturedTaskRuntimeConfig(created, launchProfile, runtimeprofile.GeneratedConfig(launchProfile), projection.Config["model_provider_snapshot"], launchModelOverride, launchReasoningEffort)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if captured != nil && captured.CapturedRuntimeConfig != nil {
		// Preserve the pre-TX captured baseline, then overlay fixed-at-launch
		// fields from the actual Config Projection (projected provider set).
		merged := make(map[string]any, len(captured.CapturedRuntimeConfig)+2)
		for key, value := range captured.CapturedRuntimeConfig {
			merged[key] = value
		}
		capturedRuntimeConfig = merged
	}
	if capturedRuntimeConfig != nil {
		// Pi multi-provider projection set is fixed for this runtime lifetime.
		if ids, ok := projection.Config["projected_model_provider_ids"]; ok {
			capturedRuntimeConfig["projected_model_provider_ids"] = ids
		}
	}

	var adapter runtime.Adapter
	if sandbox {
		sandboxConfig := runtime.DockerSandboxConfig{
			Name:         string(profile.Provider),
			ContainerCLI: commandProgram,
			Image:        sandboxImage,
			CreateArgs:   commandArgs,
			SecretValues: runtime.EnvSecretValues(processEnv),
			Log: func(event runtime.DockerSandboxLogEvent) {
				server.logDockerSandboxEvent(created, event)
			},
		}
		if sandboxNetwork == runner.SandboxNetworkHostProxyOnly {
			sandboxConfig.RequiredNetwork = &runtime.DockerNetworkRequirement{
				Name:     runner.HostProxyOnlySandboxNetworkName,
				Driver:   "bridge",
				Internal: false,
			}
		}
		adapter = runtime.NewDockerSandboxAdapter(sandboxConfig)
	} else {
		adapter = runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    string(profile.Provider),
			Program: commandProgram,
			Args:    commandArgs,
			Workdir: workdir,
			Env:     processEnv,
		})
	}

	// Pi writes its real-time progress to a session jsonl file instead of
	// stdout, so a sandboxed Pi task's timeline is empty until it exits. Wrap
	// the adapter with a session-file tailer that re-emits appended lines as
	// runtime_output events the transcript parser already understands.
	if sandbox && profile.Provider == runtimeprofile.ProviderPi {
		sessionDir := filepath.Join(layout.ProviderHome, "agent", "sessions")
		adapter = runtime.NewPiSessionTailAdapter(adapter, sessionDir)
	}

	var metadata func() (runtime.NativeSessionMetadata, error)
	if sandbox || profile.Provider == runtimeprofile.ProviderCodex || profile.Provider == runtimeprofile.ProviderPi {
		metadata = func() (runtime.NativeSessionMetadata, error) {
			var collected runtime.NativeSessionMetadata
			if containerIDFile != "" {
				containerID, err := runtime.ReadContainerIDFile(containerIDFile)
				if err != nil && !os.IsNotExist(err) {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.ContainerID = containerID
			}
			switch profile.Provider {
			case runtimeprofile.ProviderCodex:
				session, err := runtime.DiscoverCodexSession(layout.ProviderHome)
				if err != nil {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.NativeSessionID = session.NativeSessionID
				collected.NativeSessionPath = session.NativeSessionPath
			case runtimeprofile.ProviderPi:
				session, err := runtime.DiscoverPiSession(layout.ProviderHome)
				if err != nil {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.NativeSessionID = session.NativeSessionID
				collected.NativeSessionPath = session.NativeSessionPath
			}
			return collected, nil
		}
	}
	var stopConfirmation runtime.StopConfirmation
	if containerIDFile != "" {
		stopConfirmation = runtime.DockerContainerStopConfirmation(server.containerCLI, containerIDFile)
	}

	return taskLaunchPlan{
		Adapter:                     adapter,
		RuntimeConfig:               runtimeConfig,
		CapturedRuntimeConfig:       capturedRuntimeConfig,
		MaterializedCredentials:     materializedCredentials,
		Metadata:                    metadata,
		StopConfirmation:            stopConfirmation,
		LaunchModelOverride:         launchModelOverride,
		LaunchReasoningEffort:       launchReasoningEffort,
		NativeResumeSessionID:       nativeResumeSessionID,
		ResolvedProfile:             launchProfile,
		ModelSnapshot:               projection.ModelSnapshot,
		GlobalModelProviderSnapshot: globalSnapshot,
		SkillBundles:                append([]skill.Bundle(nil), skillBundles...),
		LaunchGoal:                  launchGoal,
		BlackboardV2:                v2,
		ValidatedLayout:             &layout,
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

func codexV2ProjectionProfile(profile runtimeprofile.Profile) runtimeprofile.Profile {
	projected := profile
	projected.Fields.MCPServers = nil
	projected.Fields.Env = make(map[string]string, len(profile.Fields.Env)+1)
	for key, value := range profile.Fields.Env {
		projected.Fields.Env[key] = value
	}
	projected.Fields.Env["PENTEST_DISABLE_TRUSTED_MCP"] = "true"
	return projected
}

func capturedTaskRuntimeConfig(created task.Task, profile runtimeprofile.Profile, generatedConfig any, modelSnapshot any, launchModelOverride string, launchReasoningEffort string) (map[string]any, error) {
	captured := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runtime_plugin_id":  string(profile.Provider),
		"runner":             created.Runner,
		"generated_config":   generatedConfig,
	}
	if modelSnapshot != nil {
		captured["model_provider_snapshot"] = modelSnapshot
	}
	if launchModelOverride != "" {
		captured["launch_model_override"] = launchModelOverride
	}
	if launchReasoningEffort != "" {
		captured["launch_reasoning_effort_override"] = launchReasoningEffort
	}
	// Always capture the resolved Requested Reasoning Effort for the initial
	// turn so historical views show the explicit request without inferring
	// Effective Reasoning Effort.
	requested, err := runtimeprofile.ResolveRequestedReasoningEffort(
		"",
		launchReasoningEffort,
		profile.Fields.ReasoningEffort,
	)
	if err != nil {
		return nil, err
	}
	captured["requested_reasoning_effort"] = string(requested)
	return captured, nil
}

func normalizeLaunchReasoningEffort(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	effort, err := runtimeprofile.NormalizeReasoningEffort(trimmed)
	if err != nil {
		return "", err
	}
	return string(effort), nil
}

func initialProviderTurnSelection(plan taskLaunchPlan) (runtime.ProviderSessionRequest, error) {
	model := ""
	modelProviderID := strings.TrimSpace(plan.ResolvedProfile.Fields.ModelProviderID)
	if plan.ModelSnapshot != nil {
		model = strings.TrimSpace(plan.ModelSnapshot.Model)
		if modelProviderID == "" {
			modelProviderID = strings.TrimSpace(plan.ModelSnapshot.ModelProviderID)
		}
	}
	if model == "" {
		model = strings.TrimSpace(plan.LaunchModelOverride)
	}
	if model == "" {
		model = strings.TrimSpace(plan.ResolvedProfile.Fields.ModelOverride)
	}
	if model == "" {
		model = strings.TrimSpace(plan.ResolvedProfile.Fields.Model)
	}
	requested, err := runtimeprofile.ResolveRequestedReasoningEffort(
		"",
		plan.LaunchReasoningEffort,
		plan.ResolvedProfile.Fields.ReasoningEffort,
	)
	if err != nil {
		return runtime.ProviderSessionRequest{}, err
	}
	return runtime.ProviderSessionRequest{
		ModelProviderID:          modelProviderID,
		Model:                    model,
		RequestedReasoningEffort: string(requested),
	}, nil
}

func sandboxNetworkMode(runControls task.RunControls) runner.SandboxNetworkMode {
	switch strings.TrimSpace(runControls.SandboxNetwork) {
	case string(runner.SandboxNetworkHostProxyOnly):
		return runner.SandboxNetworkHostProxyOnly
	}
	if runControls.Extras == nil {
		return runner.SandboxNetworkDefault
	}
	switch strings.TrimSpace(runControls.Extras["sandbox_network"]) {
	case string(runner.SandboxNetworkHostProxyOnly):
		return runner.SandboxNetworkHostProxyOnly
	default:
		return runner.SandboxNetworkDefault
	}
}

func (server *Server) handleListTasks(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}

	tasks, err := server.tasks.ListForProject(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list tasks")
		return
	}
	if tasks == nil {
		tasks = []task.Task{}
	}
	// Decorate list entries with current Runtime Activity (not Task status).
	for index := range tasks {
		tasks[index] = server.attachRuntimeActivity(tasks[index])
		tasks[index], err = server.attachBlackboardConclusion(tasks[index])
		if err != nil {
			writeError(response, http.StatusInternalServerError, "load Blackboard conclusion")
			return
		}
	}
	writeJSON(response, http.StatusOK, struct {
		Tasks []task.Task `json:"tasks"`
	}{
		Tasks: tasks,
	})
}

func (server *Server) handleGetTask(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	detailed, err := server.decorateTask(found)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load task continuation")
		return
	}
	writeJSON(response, http.StatusOK, detailed)
}

// workspaceNavigationTaskLimit bounds the number of ordinary Tasks inlined per
// Project in the Sidebar navigation projection. Busy-runtime Tasks and the
// selected Task are inlined on top of this limit, so the per-Project row never
// needs a full Task history (#201).
const workspaceNavigationTaskLimit = 5

// workspaceNavigationResponse is the single-call Workspace Sidebar projection.
// A refresh that supplies the current opaque revision receives changed=false
// and an empty project list instead of a reserialized projection (#201).
type workspaceNavigationResponse struct {
	Revision string                       `json:"revision"`
	Changed  bool                         `json:"changed"`
	Projects []workspaceNavigationProject `json:"projects"`
}

type workspaceNavigationProject struct {
	project.Project
	LastActivityAt time.Time   `json:"last_activity_at"`
	Tasks          []task.Task `json:"tasks"`
}

// handleWorkspaceNavigation returns one bounded navigation projection for the
// Workspace Sidebar: every Project, each with the five most recent ordinary
// Tasks inlined (runtime_activity and Blackboard conclusion already attached),
// every Task with a live busy Runtime promoted ahead of the ordinary summary,
// and the selected Task appended when neither rule already included it
// (#201). Runtime liveness is computed live via attachRuntimeActivity; it is
// never derived from durable Task status.
//
// The query work and response size are bounded by Project count × the fixed
// summary size plus active Runtime count plus the selected Task; total
// historical Task count never enters either bound. A request that supplies the
// current opaque revision is answered with an empty unchanged result, so
// polling never reserializes the projection.
func (server *Server) handleWorkspaceNavigation(response http.ResponseWriter, request *http.Request) {
	requestRevision := strings.TrimSpace(request.URL.Query().Get("revision"))
	selectedTaskID := strings.TrimSpace(request.URL.Query().Get("selected_task"))

	projects, err := server.projects.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list projects")
		return
	}

	// The busy Task set is live and in-memory: a bound provider session that is
	// currently busy. It is bounded by the number of active Runtimes, so
	// scanning it never grows with Task history.
	busyIDs := server.providerSessions.busyOwnerIDs()

	var selected *task.Task
	if selectedTaskID != "" {
		found, err := server.tasks.Get(selectedTaskID)
		if err == nil {
			selected = &found
		}
	}

	revision, err := server.navigationRevision(busyIDs, selected)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "compute navigation revision")
		return
	}
	if requestRevision != "" && requestRevision == revision {
		writeJSON(response, http.StatusOK, workspaceNavigationResponse{
			Revision: revision,
			Changed:  false,
			Projects: []workspaceNavigationProject{},
		})
		return
	}

	// Load the busy Tasks once and group them by Project. Each lookup is a
	// single primary-key read, bounded by the active Runtime count.
	busyByProject := make(map[string][]task.Task)
	for _, taskID := range busyIDs {
		found, err := server.tasks.Get(taskID)
		if err != nil {
			continue
		}
		busyByProject[found.ProjectID] = append(busyByProject[found.ProjectID], found)
	}

	projectIDs := make([]string, 0, len(projects))
	for _, current := range projects {
		projectIDs = append(projectIDs, current.ID)
	}
	// Busy Tasks are excluded from the ordinary query so the ordinary five are
	// the most recent non-busy Tasks; the selected Task stays in the query and
	// is appended only when recency omitted it, so selecting a recent Task
	// never shifts or duplicates the ordinary summary.
	recentByProject, err := server.tasks.ListRecentPerProject(projectIDs, workspaceNavigationTaskLimit, busyIDs...)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load recent tasks")
		return
	}

	summaries := make([]workspaceNavigationProject, 0, len(projects))
	for _, current := range projects {
		recent := recentByProject[current.ID]
		if recent == nil {
			recent = []task.Task{}
		}
		busy := busyByProject[current.ID]
		// Busy Tasks sort by recency ahead of the ordinary summary; the
		// selected Task appends last when recency or activity already omitted
		// it, and is never duplicated.
		sort.SliceStable(busy, func(i, j int) bool {
			return workspaceTaskRecency(busy[i], busy[j])
		})
		inlined := append([]task.Task{}, busy...)
		inlined = append(inlined, recent...)
		if selected != nil && selected.ProjectID == current.ID && !taskIDInList(selected.ID, inlined) {
			inlined = append(inlined, *selected)
		}
		if inlined == nil {
			inlined = []task.Task{}
		}
		// Decorate each inlined Task through the same path as the per-Project
		// Task list so runtime_activity is live and consistent.
		for index := range inlined {
			inlined[index] = server.attachRuntimeActivity(inlined[index])
			inlined[index], err = server.attachBlackboardConclusion(inlined[index])
			if err != nil {
				writeError(response, http.StatusInternalServerError, "load Blackboard conclusion")
				return
			}
		}
		summaries = append(summaries, workspaceNavigationProject{
			Project:        current,
			LastActivityAt: workspaceProjectActivity(current, inlined),
			Tasks:          inlined,
		})
	}
	writeJSON(response, http.StatusOK, workspaceNavigationResponse{
		Revision: revision,
		Changed:  true,
		Projects: summaries,
	})
}

// workspaceTaskRecency orders two Tasks by the navigation recency rule:
// updated_at DESC then created_at DESC.
func workspaceTaskRecency(a, b task.Task) bool {
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	return a.CreatedAt.After(b.CreatedAt)
}

func taskIDInList(id string, tasks []task.Task) bool {
	for _, found := range tasks {
		if found.ID == id {
			return true
		}
	}
	return false
}

// navigationRevision returns the opaque conditional-refresh token for the
// Project Navigation Projection (#201). It folds the durable Project and Task
// summary epochs, the live busy-Runtime Task set, and the selected Task
// identity, so every change that can alter the navigation rows yields a new
// revision while an unchanged refresh can be answered without serializing the
// projection. All inputs are bounded: the epochs are single indexed reads and
// the busy set is bounded by the number of active Runtimes.
//
// The revision covers what the Sidebar renders: Project/Task summary fields,
// busy membership, and the selected Task. The attached Blackboard conclusion
// view is not rendered by navigation, so its transitions do not advance the
// revision; a conclusion change alone is served from the cached projection
// until the next revision-relevant change.
func (server *Server) navigationRevision(busyIDs []string, selected *task.Task) (string, error) {
	projectEpoch, err := server.projects.LatestUpdate()
	if err != nil {
		return "", err
	}
	taskEpoch, err := server.tasks.LatestUpdate()
	if err != nil {
		return "", err
	}
	selectedID := ""
	if selected != nil {
		selectedID = selected.ID
	}
	sum := sha256.Sum256([]byte("navigation:v1|" + projectEpoch.Format(time.RFC3339Nano) + "|" +
		taskEpoch.Format(time.RFC3339Nano) + "|" + strings.Join(busyIDs, ",") + "|" + selectedID))
	return hex.EncodeToString(sum[:]), nil
}

// workspaceProjectActivity returns the most recent activity timestamp across a
// Project and its Tasks, mirroring the Sidebar's projectActivity helper so the
// projection's last_activity_at drives the same ordering with one call.
func workspaceProjectActivity(projectRecord project.Project, tasks []task.Task) time.Time {
	latest := projectRecord.UpdatedAt
	if before := projectRecord.CreatedAt; latest.Before(before) {
		latest = before
	}
	for _, current := range tasks {
		if candidate := current.UpdatedAt; candidate.After(latest) {
			latest = candidate
		}
		if candidate := current.CreatedAt; candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

func (server *Server) handleDeleteTask(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	// Task Deletion is legal only for terminal Tasks, but every queued Accepted
	// Steering request must still settle with a truthful outcome instead of
	// being orphaned with the deleted owner.
	server.settleTaskAcceptedSteering(found.ID, owner.SteeringReasonOwnerStateChanged, "Task deleted with queued accepted steering")
	if err := server.tasks.Delete(found.ID); err != nil {
		writeTaskError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) requireProjectTask(response http.ResponseWriter, request *http.Request) (task.Task, bool) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return task.Task{}, false
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return task.Task{}, false
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return task.Task{}, false
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return task.Task{}, false
	}
	return found, true
}

func (server *Server) taskDetail(taskID string) (task.Task, error) {
	found, err := server.tasks.Get(taskID)
	if err != nil {
		return task.Task{}, err
	}
	return server.decorateTask(found)
}

func (server *Server) decorateTask(found task.Task) (task.Task, error) {
	// Runtime Activity is attached first so unexpected offline/orphaned health
	// can reconcile durable lifecycle before controls are computed.
	found = server.attachRuntimeActivity(found)
	found, err := server.attachBlackboardConclusion(found)
	if err != nil {
		return task.Task{}, err
	}
	active, err := server.tasks.ActiveContinuation(found.ID)
	if err != nil {
		return task.Task{}, err
	}
	latest, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return task.Task{}, err
	}
	latest, err = server.captureDiscoverableNativeSession(found, latest)
	if err != nil {
		return task.Task{}, err
	}
	if active != nil && latest != nil && active.ID == latest.ID {
		active = latest
	}
	controls, err := server.runtimeControlsForTask(found, latest)
	if err != nil {
		return task.Task{}, err
	}
	found.RuntimeControls = controls
	found.ActiveContinuation = active
	found.LatestContinuation = latest
	return found, nil
}

func (server *Server) captureDiscoverableNativeSession(found task.Task, latest *task.TaskContinuation) (*task.TaskContinuation, error) {
	if latest == nil || strings.TrimSpace(latest.NativeSessionID) != "" {
		return latest, nil
	}
	profile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return nil, err
	}
	if profile.Provider != runtimeprofile.ProviderCodex && profile.Provider != runtimeprofile.ProviderPi {
		return latest, nil
	}
	metadata, err := server.discoverProviderNativeSession(found.ID, profile.Provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(metadata.NativeSessionID) == "" {
		return latest, nil
	}
	updated, err := server.tasks.UpdateContinuationRuntimeMetadata(latest.ID, "", metadata.NativeSessionID, metadata.NativeSessionPath)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (server *Server) runtimeControlsForTask(found task.Task, latest *task.TaskContinuation) (task.RuntimeControls, error) {
	profile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.RuntimeControls{}, err
	}
	plugin, ok := server.runtimePlugins.Get(string(profile.Provider))
	nativeResumeSupported := ok && plugin.NativeResume.Supported
	active := found.Status == task.StatusRunning || found.Status == task.StatusPaused
	sessionCaptured := latest != nil && strings.TrimSpace(latest.NativeSessionID) != ""
	_, providerSessionBound := server.providerSessions.get(found.ID)

	activity := server.computeRuntimeActivity(found)
	controls := task.RuntimeControls{
		ResumeAvailable:         !active,
		FinishAvailable:         activity.Liveness == runtimeLivenessLive && activity.TurnActivity == runtimeTurnIdle,
		QueueSteerAvailable:     true,
		NativeSessionCaptured:   sessionCaptured,
		SameRuntimeProviderOnly: true,
		RuntimeProvider:         string(profile.Provider),
	}
	if strings.TrimSpace(profile.ID) == "" {
		// Every Runtime Profile reference is gone: queue steering would record
		// an empty profile, so the composer must not offer it.
		controls.QueueSteerAvailable = false
	}
	if profile.Provider == runtimeprofile.ProviderPi {
		// Expose the fixed projected set so Task Conversation UI can fail closed
		// when metadata is missing (legacy) or the target is outside the set.
		if ids := server.projectedPiModelProviderIDs(found); len(ids) > 0 {
			controls.ProjectedModelProviderIDs = ids
		}
	}
	if selection, selectionErr := server.currentTurnSelection(found); selectionErr == nil {
		controls.TurnSelection = &task.TurnSelection{
			ModelProviderID: selection.ModelProviderID,
			Model:           selection.Model,
			ReasoningEffort: selection.RequestedReasoningEffort,
		}
	}
	if events, eventsErr := server.tasks.Events(found.ID); eventsErr == nil {
		if providerSessionBound {
			controls.ProviderPermissions = providerPermissionRequestsForTask(events)
		}
		for index := len(events) - 1; index >= 0; index-- {
			phase, _ := events[index].Payload["phase"].(string)
			if phase == "started" {
				break
			}
			if phase != "provider_session_recovery_required" {
				continue
			}
			controls.RecoveryState, _ = events[index].Payload["recovery_state"].(string)
			controls.RecoveryReason, _ = events[index].Payload["reason"].(string)
			break
		}
	}
	if session, bound := server.providerSessions.get(found.ID); bound {
		_, outcome, _ := nativeSteerStateForTask(found.ID, server.tasks)
		if selectedMode, modeErr := nativeSteerMode(session.Capabilities()); modeErr == nil {
			controls.NativeSteerAvailable = active
			controls.NativeSteerMode = string(selectedMode)
			controls.NativeSteerState = outcome
			if outcome == "requested" || outcome == "acknowledged" || outcome == "settled" || outcome == "started" {
				controls.NativeSteerAvailable = false
			}
			controls.InterruptSteerAvailable = controls.NativeSteerAvailable
			if !controls.NativeSteerAvailable && active {
				controls.InterruptSteerReason = "native steer request is already in progress"
			}
		} else {
			controls.NativeSteerReason = modeErr.Error()
			controls.InterruptSteerReason = controls.NativeSteerReason
		}
		if events, eventsErr := server.tasks.Events(found.ID); eventsErr == nil {
			for index := len(events) - 1; index >= 0; index-- {
				if requestID, ok := events[index].Payload["request_id"].(string); ok && requestID != "" && events[index].Kind == task.EventKindConversation && events[index].Payload["delivery"] == "native_steer" {
					controls.NativeSteerRequestID = requestID
					break
				}
			}
		}
		if controls.NativeSteerState == "" && active {
			controls.NativeSteerState = "idle"
		}
	}
	if nativeResumeSupported {
		controls.NativeResumeAvailable = !active && sessionCaptured
		if !providerSessionBound {
			controls.InterruptSteerAvailable = active && sessionCaptured
		}
	} else {
		controls.NativeResumeReason = fmt.Sprintf("native resume unsupported for provider %s", profile.Provider)
		controls.InterruptSteerReason = controls.NativeResumeReason
	}
	if nativeResumeSupported && !sessionCaptured {
		controls.NativeResumeReason = "native session unavailable"
		controls.InterruptSteerReason = controls.NativeResumeReason
	}
	return controls, nil
}

func providerPermissionRequestsForTask(events []task.Event) []task.ProviderPermissionRequest {
	requests := make(map[string]task.ProviderPermissionRequest)
	for _, event := range events {
		permissionID, _ := event.Payload["permission_request_id"].(string)
		if permissionID == "" {
			continue
		}
		phase, _ := event.Payload["phase"].(string)
		switch phase {
		case "provider_permission_requested":
			request := task.ProviderPermissionRequest{PermissionRequestID: permissionID, CreatedAt: event.CreatedAt}
			request.RequestID, _ = event.Payload["request_id"].(string)
			request.SessionID, _ = event.Payload["session_id"].(string)
			request.ProviderTurnID, _ = event.Payload["provider_turn_id"].(string)
			request.Provider, _ = event.Payload["provider"].(string)
			requests[permissionID] = request
		case "provider_permission_response_applied":
			delete(requests, permissionID)
		}
	}
	result := make([]task.ProviderPermissionRequest, 0, len(requests))
	for _, request := range requests {
		result = append(result, request)
	}
	slices.SortFunc(result, func(left, right task.ProviderPermissionRequest) int {
		return left.CreatedAt.Compare(right.CreatedAt)
	})
	return result
}

func nativeSteerStateForTask(taskID string, tasks *task.Service) (runtime.ProviderSessionMode, string, string) {
	events, err := tasks.Events(taskID)
	if err != nil {
		return "", "", ""
	}
	var requestID string
	for _, event := range events {
		if event.Kind != task.EventKindConversation || event.Payload["delivery"] != "native_steer" {
			continue
		}
		requestID, _ = event.Payload["request_id"].(string)
	}
	if requestID == "" {
		return "", "", ""
	}
	mode, outcome, sessionID := nativeSteerState(events, requestID)
	return mode, outcome, sessionID
}

func (server *Server) handleTaskEvents(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	if events == nil {
		events = []task.Event{}
	}
	writeJSON(response, http.StatusOK, struct {
		Events []task.Event `json:"events"`
	}{
		Events: events,
	})
}

func (server *Server) handleTaskTimeline(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	req := parseHistoryRequest(request)
	items, window, err := collectTimelineItems(req, func(scan historyRequest) (timelineEventChunk, error) {
		stored, err := server.tasks.HistoryEventWindow(found.ID, task.EventWindowQuery{
			Projection: task.EventProjectionTimeline, BeforeSet: scan.beforeSet, Before: scan.before,
			AfterSet: scan.afterSet, After: scan.after, Limit: historyEventQueryLimit,
		})
		if err != nil {
			return timelineEventChunk{}, err
		}
		return timelineEventChunk{
			events:     eventsToTimelineEvents(stored.Events),
			cursor:     stored.Cursor,
			hasOlder:   stored.HasOlder,
			hasNewer:   stored.HasNewer,
			scanCursor: stored.ScanCursor,
		}, nil
	})
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	if items == nil {
		items = []timeline.Item{}
	}
	detailBase := fmt.Sprintf("/api/projects/%s/tasks/%s/timeline/items", found.ProjectID, found.ID)
	page := historyResponseFor(items, req, func(item timeline.Item) int {
		return item.Seq
	}, func(item timeline.Item) (timeline.Item, int) {
		return boundedTimelineItem(item, detailBase)
	})
	page.hasOlder = page.hasOlder || window.hasOlder
	if req.afterSet && window.hasNewer {
		page.cursor = window.scanCursor
	} else {
		page.cursor = window.cursor
	}
	writeJSON(response, http.StatusOK, struct {
		TaskID   string          `json:"task_id"`
		Items    []timeline.Item `json:"items"`
		Cursor   int             `json:"cursor"`
		HasOlder bool            `json:"has_older"`
	}{
		TaskID:   found.ID,
		Items:    page.items,
		Cursor:   page.cursor,
		HasOlder: page.hasOlder,
	})
}

// handleTaskTimelineItem returns one complete retained timeline item by stable
// item ID. A numeric Seq remains valid for retained legacy detail links.
// including the full payload that the history window preview truncated.
func (server *Server) handleTaskTimelineItem(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	itemRef := request.PathValue("seq")
	seq, _ := strconv.Atoi(itemRef)
	if itemRef == "" {
		writeError(response, http.StatusNotFound, "timeline item not found")
		return
	}
	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	for _, item := range timeline.Build(eventsToTimelineEvents(events)) {
		if item.ID == itemRef || (seq > 0 && item.Seq == seq) {
			writeJSON(response, http.StatusOK, item)
			return
		}
	}
	writeError(response, http.StatusNotFound, "timeline item not found")
}

func (server *Server) handleTaskTranscript(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	req := parseHistoryRequest(request)
	window, err := server.tasks.HistoryEventWindow(found.ID, task.EventWindowQuery{
		Projection: task.EventProjectionTranscript, BeforeSet: req.beforeSet, Before: req.before,
		AfterSet: req.afterSet, After: req.after, Limit: historyEventQueryLimit,
	})
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	entries := transcript.BuildWindow(transcript.Subject{
		ID:        found.ID,
		Title:     found.Goal,
		CreatedAt: found.CreatedAt,
	}, eventsToTranscriptEvents(window.Events), transcript.WindowContext{
		Continuation: window.PriorContinuation,
		Adapter:      window.PriorTranscriptAdapter,
	})
	if entries == nil {
		entries = []transcript.Entry{}
	}
	detailBase := fmt.Sprintf("/api/projects/%s/tasks/%s/transcript/entries", found.ProjectID, found.ID)
	page := historyResponseFor(entries, req, func(entry transcript.Entry) int {
		return entry.Seq
	}, func(entry transcript.Entry) (transcript.Entry, int) {
		return boundedTranscriptEntry(entry, detailBase)
	})
	page.hasOlder = page.hasOlder || window.HasOlder
	if req.afterSet && window.HasNewer {
		page.cursor = window.ScanCursor
	} else {
		page.cursor = window.Cursor
	}
	writeJSON(response, http.StatusOK, struct {
		TaskID   string             `json:"task_id"`
		Entries  []transcript.Entry `json:"entries"`
		Cursor   int                `json:"cursor"`
		HasOlder bool               `json:"has_older"`
	}{
		TaskID:   found.ID,
		Entries:  page.items,
		Cursor:   page.cursor,
		HasOlder: page.hasOlder,
	})
}

// handleTaskTranscriptEntry returns one complete retained transcript entry by
// ID, including the full payload that the history window preview truncated.
func (server *Server) handleTaskTranscriptEntry(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	entryID := request.PathValue("entry_id")
	if entryID == "" {
		writeError(response, http.StatusNotFound, "transcript entry not found")
		return
	}
	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	for _, entry := range transcript.Build(transcript.Subject{
		ID:        found.ID,
		Title:     found.Goal,
		CreatedAt: found.CreatedAt,
	}, eventsToTranscriptEvents(events)) {
		if entry.ID == entryID {
			writeJSON(response, http.StatusOK, entry)
			return
		}
	}
	writeError(response, http.StatusNotFound, "transcript entry not found")
}

func (server *Server) handleStopTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	deadline := time.Now().Add(server.runtimeStopTimeout)
	stopContext, cancelStop := context.WithDeadline(context.Background(), deadline)
	defer cancelStop()
	if !server.acquireTaskControl(taskID) {
		// Stop is the preemptive control. Cancel every queued or active provider
		// operation for this Task, then take ownership after it unwinds.
		if !server.cancelProviderTaskControls(taskID) {
			writeError(response, http.StatusConflict, "task control operation already active")
			return
		}
		if server.harness != nil {
			server.harness.Stop(taskID)
		}
		if !server.waitAcquireTaskControl(stopContext, taskID) {
			writeError(response, http.StatusConflict, "task control operation did not stop in time")
			return
		}
	} else {
		// A provider operation may be queued with no active owner yet. Cancel it
		// after Stop acquires the Task boundary so it cannot dispatch once Stop
		// releases the boundary.
		server.cancelProviderTaskControls(taskID)
	}
	defer server.releaseTaskControl(taskID)

	// Re-read under control lock so concurrent Finish/resume cannot race settle.
	found, err = server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}

	if found.Status == task.StatusRunning || found.Status == task.StatusPaused {
		if server.harness != nil {
			server.harness.Stop(taskID)
		}
		if err := server.closeProviderSessionForStop(stopContext, taskID); err != nil {
			writeError(response, http.StatusConflict, "provider session did not close")
			return
		}
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		if server.harness != nil && server.harness.IsActive(taskID) {
			if ok := server.harness.StopAndWait(taskID, remaining); !ok {
				writeError(response, http.StatusConflict, "runtime did not stop in time")
				return
			}
		}
		if err := server.markStoppedBlackboardConclusionsRecoveryRequired(taskID); err != nil {
			writeError(response, http.StatusInternalServerError, "record stopped Blackboard conclusion recovery")
			return
		}
		// Durable Task may still be running when harness/session already gone
		// (finish abort, orphan cleanup). Always settle stopped after cleanup.
		if err := server.settleTaskStopped(taskID); err != nil {
			writeTaskError(response, err)
			return
		}
		server.settleTaskAcceptedSteering(taskID, owner.SteeringReasonOwnerStopped, "Task stopped with queued accepted steering")
		stopped, err := server.taskDetail(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, stopped)
		return
	}
	if err := server.closeProviderSession(taskID); err != nil && !errors.Is(err, runtime.ErrProviderSessionClosed) {
		writeError(response, http.StatusConflict, "provider session did not close")
		return
	}
	if err := server.markStoppedBlackboardConclusionsRecoveryRequired(taskID); err != nil {
		writeError(response, http.StatusInternalServerError, "record stopped Blackboard conclusion recovery")
		return
	}
	if err := server.settleTaskStopped(taskID); err != nil {
		writeTaskError(response, err)
		return
	}
	server.settleTaskAcceptedSteering(taskID, owner.SteeringReasonOwnerStopped, "Task stopped with queued accepted steering")
	stopped, err := server.taskDetail(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, stopped)
}

// markStoppedBlackboardConclusionsRecoveryRequired closes the durable semantic
// coordinator after Stop has canceled every provider control. A later Resume
// therefore sees explicit operator-recoverable debt instead of waiting forever
// on a Conclude Turn whose provider session was intentionally closed.
func (server *Server) markStoppedBlackboardConclusionsRecoveryRequired(taskID string) error {
	receipts, err := server.tasks.BlackboardConclusionRecoveryCandidates()
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if receipt.TaskID != taskID {
			continue
		}
		if _, _, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
			receipt.ID, task.ConclusionRecoveryRuntimeOwnershipNotProven, time.Now().UTC(), blackboardConclusionRetryCooldown,
		); err != nil && !errors.Is(err, task.ErrInvalidBlackboardConclusionReceipt) {
			return err
		}
	}
	return nil
}

// settleTaskStopped marks Task and any non-terminal Continuation as stopped.
// Does not overwrite an existing different terminal Continuation status.
func (server *Server) settleTaskStopped(taskID string) error {
	if cont, err := server.tasks.LatestContinuation(taskID); err == nil && cont != nil {
		if cont.Status == task.StatusRunning || cont.Status == task.StatusPaused || cont.Status == task.StatusPending {
			if _, err := server.tasks.UpdateContinuationStatus(cont.ID, task.StatusStopped); err != nil && !errors.Is(err, task.ErrContinuationStatusConflict) {
				return err
			}
		}
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		return err
	}
	if found.Status == task.StatusRunning || found.Status == task.StatusPaused || found.Status == task.StatusPending {
		if _, err := server.tasks.UpdateStatus(taskID, task.StatusStopped); err != nil {
			return err
		}
	}
	return nil
}

// handleFinishTask is operator-controlled Task completion. It requires current
// Runtime Activity live+idle, closes Runtime resources before harness exit so
// shutdown happens-before Continuation reconciliation, verifies the durable
// reconciliation marker, then marks the Task completed. Busy Runtimes use Stop.
// There is no MCP/Runtime path that invokes this handler.
func (server *Server) handleFinishTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if found.RunControls.BlackboardConclusionMode == task.BlackboardConclusionModeAssisted {
		if err := server.acquireTaskControlAfterAssistedSettlement(request.Context(), found, false, false); err != nil {
			if errors.Is(err, errSemanticConclusionActionRequired) {
				writeError(response, http.StatusConflict, "semantic_conclusion_action_required")
				return
			}
			if errors.Is(err, errTaskControlOperationActive) {
				writeError(response, http.StatusConflict, "task control operation already active")
				return
			}
			writeError(response, http.StatusConflict, err.Error())
			return
		}
	} else if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)

	// Re-read under the control lock so concurrent Stop/steer cannot race the gate.
	found, err = server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	readiness, err := server.finishReadiness.Require(request.Context(), projectID, taskID)
	if err != nil {
		var notReady *finishreadiness.NotReadyError
		if errors.As(err, &notReady) {
			writeFinishReadinessConflict(response, readiness)
			return
		}
		writeError(response, http.StatusInternalServerError, "evaluate Finish Readiness")
		return
	}
	activity := server.computeRuntimeActivity(found)
	if activity.Liveness != runtimeLivenessLive || activity.TurnActivity != runtimeTurnIdle {
		writeError(response, http.StatusConflict, finishRejectedMessage(activity))
		return
	}

	deadline := time.Now().Add(server.runtimeStopTimeout)
	stopContext, cancelStop := context.WithDeadline(context.Background(), deadline)
	defer cancelStop()

	// 1) Finish intent: Launch will exit without writing terminal status.
	// handleFinishTask is the sole owner of Continuation/Task completed.
	if server.harness != nil {
		server.harness.MarkFinishRequested(taskID)
	}
	// 2) Close provider session/bridge/process (production Closed() on Close).
	if err := server.closeProviderSessionForStop(stopContext, taskID); err != nil {
		if server.harness != nil {
			server.harness.ClearFinishIntent(taskID)
		}
		server.finishDiagnostic(taskID, "provider_session_close", "provider session did not close")
		writeError(response, http.StatusConflict, "finish failed at provider_session_close: provider session did not close")
		return
	}
	// 3) Wait for harness exit (cancel residual wait is safe: finish path does
	// not write terminal status). On timeout, clear intent then force-stop so a
	// late exit uses stop/fail rather than hang with residual intent.
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	if server.harness != nil {
		if ok := server.harness.StopAndWait(taskID, remaining); !ok {
			server.harness.ClearFinishIntent(taskID)
			_ = server.harness.StopAndWait(taskID, 0)
			server.finishDiagnostic(taskID, "runtime_shutdown", "runtime did not stop in time")
			writeError(response, http.StatusConflict, "finish failed at runtime_shutdown: runtime did not stop in time")
			return
		}
	}

	// 4) Sole owner: mark Continuation completed (triggers recon), verify
	// durable marker, then Task completed. Fail-closed — no silent fallbacks.
	// After runtime is already closed, any failure here must settle Task to a
	// recoverable terminal (failed) so it does not remain durable running.
	cont, contErr := server.tasks.LatestContinuation(taskID)
	if contErr != nil {
		server.finishFailClosed(response, taskID, "continuation_lookup", contErr.Error(), http.StatusInternalServerError)
		return
	}
	if cont == nil {
		server.finishFailClosed(response, taskID, "continuation_missing", "no Continuation for Task", http.StatusConflict)
		return
	}
	// Complete Continuation and/or retry terminal reconciliation when the
	// durable marker is not yet completed (fail-closed, no silent skip).
	if cont.Status != task.StatusCompleted || cont.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		if _, err := server.tasks.UpdateContinuationStatus(cont.ID, task.StatusCompleted); err != nil {
			// Classify from durable re-read, never string-matching.
			refreshed, refErr := server.tasks.Continuation(cont.ID)
			if refErr == nil && refreshed.Status == task.StatusCompleted && refreshed.BlackboardReconciliationStatus != task.ReconciliationCompleted {
				server.finishFailClosed(response, taskID, "continuation_reconciliation", "marker="+string(refreshed.BlackboardReconciliationStatus), http.StatusConflict)
				return
			}
			if errors.Is(err, task.ErrContinuationStatusConflict) {
				server.finishFailClosed(response, taskID, "continuation_complete", "continuation status conflict", http.StatusConflict)
				return
			}
			server.finishFailClosed(response, taskID, "continuation_complete", err.Error(), http.StatusInternalServerError)
			return
		}
	}
	refreshed, refErr := server.tasks.Continuation(cont.ID)
	if refErr != nil {
		server.finishFailClosed(response, taskID, "continuation_reread", refErr.Error(), http.StatusInternalServerError)
		return
	}
	if refreshed.Status != task.StatusCompleted {
		server.finishFailClosed(response, taskID, "continuation_status", "status="+string(refreshed.Status), http.StatusConflict)
		return
	}
	if refreshed.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		server.finishFailClosed(response, taskID, "continuation_reconciliation", "marker="+string(refreshed.BlackboardReconciliationStatus), http.StatusConflict)
		return
	}

	// 5) Only after durable recon marker is completed may the Task be completed.
	if _, err := server.tasks.UpdateStatus(taskID, task.StatusCompleted); err != nil {
		server.finishFailClosed(response, taskID, "task_complete", err.Error(), http.StatusInternalServerError)
		return
	}
	// Lifecycle only — never a Runtime Activity audit/history record.
	_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
		"phase": "completed", "reason": "operator_finish",
	})
	server.settleTaskAcceptedSteering(taskID, owner.SteeringReasonOwnerFinished, "Task finished with queued accepted steering")

	finished, err := server.taskDetail(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, finished)
}

var errSemanticConclusionActionRequired = errors.New("semantic_conclusion_action_required")
var errTaskControlOperationActive = errors.New("task control operation already active")

func (server *Server) waitForAssistedConclusionDrain(ctx context.Context, found task.Task) error {
	return server.waitForAssistedConclusionSettlement(ctx, found, false)
}

func (server *Server) waitForAssistedConclusionSettlement(ctx context.Context, found task.Task, allowActionRequired bool) error {
	for {
		receipt, err := server.tasks.LatestBlackboardConclusion(found.ID)
		if err != nil {
			return err
		}
		if receipt == nil {
			return nil
		}
		switch receipt.View(found.RunControls.BlackboardConclusionMode).State {
		case task.BlackboardConclusionStateClean:
			return nil
		case task.BlackboardConclusionStateActionRequired:
			if allowActionRequired {
				return nil
			}
			return errSemanticConclusionActionRequired
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// acquireTaskControlAfterAssistedSettlement closes the drain/acquire race:
// after taking Task control it rechecks the durable receipt. If a conclusion
// became pending between the optimistic drain and acquisition, it releases
// control so the Harness coordinator can run, drains again, and retries.
func (server *Server) acquireTaskControlAfterAssistedSettlement(ctx context.Context, found task.Task, allowActionRequired, providerControl bool) error {
	waitedForConclusion := false
	for {
		before, err := server.tasks.LatestBlackboardConclusion(found.ID)
		if err != nil {
			return err
		}
		if before != nil {
			switch before.View(found.RunControls.BlackboardConclusionMode).State {
			case task.BlackboardConclusionStatePending, task.BlackboardConclusionStateConcluding:
				waitedForConclusion = true
			}
		}
		if err := server.waitForAssistedConclusionSettlement(ctx, found, allowActionRequired); err != nil {
			return err
		}
		acquired := false
		if providerControl {
			acquired = server.acquireProviderTaskControl(found.ID)
		} else {
			acquired = server.acquireTaskControl(found.ID)
		}
		if !acquired {
			receipt, err := server.tasks.LatestBlackboardConclusion(found.ID)
			if err != nil {
				return err
			}
			if receipt != nil {
				state := receipt.View(found.RunControls.BlackboardConclusionMode).State
				if state == task.BlackboardConclusionStatePending || state == task.BlackboardConclusionStateConcluding {
					waitedForConclusion = true
					continue
				}
			}
			if waitedForConclusion {
				timer := time.NewTimer(5 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
					continue
				}
			}
			return errTaskControlOperationActive
		}

		receipt, err := server.tasks.LatestBlackboardConclusion(found.ID)
		if err != nil {
			if providerControl {
				server.releaseProviderTaskControl(found.ID)
			} else {
				server.releaseTaskControl(found.ID)
			}
			return err
		}
		if receipt == nil {
			return nil
		}
		switch receipt.View(found.RunControls.BlackboardConclusionMode).State {
		case task.BlackboardConclusionStateClean:
			return nil
		case task.BlackboardConclusionStateActionRequired:
			if allowActionRequired {
				return nil
			}
			if providerControl {
				server.releaseProviderTaskControl(found.ID)
			} else {
				server.releaseTaskControl(found.ID)
			}
			return errSemanticConclusionActionRequired
		default:
			if providerControl {
				server.releaseProviderTaskControl(found.ID)
			} else {
				server.releaseTaskControl(found.ID)
			}
		}
	}
}

// finishFailClosed settles a post-runtime Finish failure: Task must not stay
// durable running. Prefer failed (resumable). Public HTTP uses stable stage
// messages; raw detail stays in logs/events only.
func (server *Server) finishFailClosed(response http.ResponseWriter, taskID, stage, detail string, status int) {
	server.finishDiagnostic(taskID, stage, detail)
	server.settleTaskFailedAfterFinishAbort(taskID)
	server.settleTaskAcceptedSteering(taskID, owner.SteeringReasonOwnerStateChanged, "Task finish aborted before the accepted steering dispatched")
	writeError(response, status, "finish failed at "+stage)
}

// settleTaskFailedAfterFinishAbort marks Task failed when still active after
// runtime close. Non-terminal Continuations become failed; existing different
// terminal Continuation statuses are left unchanged.
func (server *Server) settleTaskFailedAfterFinishAbort(taskID string) {
	if cont, err := server.tasks.LatestContinuation(taskID); err == nil && cont != nil {
		switch cont.Status {
		case task.StatusRunning, task.StatusPaused, task.StatusPending:
			if _, err := server.tasks.UpdateContinuationStatus(cont.ID, task.StatusFailed); err != nil {
				// Status may already be terminal from a partial write; re-read.
				if refreshed, refErr := server.tasks.Continuation(cont.ID); refErr == nil {
					if refreshed.Status == task.StatusRunning || refreshed.Status == task.StatusPaused || refreshed.Status == task.StatusPending {
						server.finishDiagnostic(taskID, "settle_continuation", err.Error())
					}
				}
			}
		}
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		return
	}
	if found.Status == task.StatusRunning || found.Status == task.StatusPaused || found.Status == task.StatusPending {
		if _, err := server.tasks.UpdateStatus(taskID, task.StatusFailed); err != nil {
			server.finishDiagnostic(taskID, "settle_task", err.Error())
		}
	}
}

// finishDiagnostic records a fail-closed Finish diagnostic without inventing
// Runtime Activity audit history. detail may include raw errors for operators.
func (server *Server) finishDiagnostic(taskID, stage, detail string) {
	if found, err := server.tasks.Get(taskID); err == nil {
		server.logTask(found, "finish_failed", stage+": "+detail)
	} else if server.logger != nil {
		server.logger.Printf("task finish_failed id=%s detail=%q", taskID, stage+": "+detail)
	}
	_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
		"phase": "finish_failed", "stage": stage, "detail": detail,
	})
}

func finishRejectedMessage(activity task.RuntimeActivity) string {
	switch activity.Liveness {
	case runtimeLivenessLive:
		if activity.TurnActivity == runtimeTurnBusy {
			return "finish requires a live idle Runtime; Stop interrupts a busy Runtime"
		}
		return "finish requires a live idle Runtime"
	case runtimeLivenessOffline:
		return "finish requires a live idle Runtime; runtime is offline"
	case runtimeLivenessOrphaned:
		return "finish requires a live idle Runtime; runtime ownership is orphaned"
	case runtimeLivenessUnknown:
		return "finish requires a live idle Runtime; runtime health is unknown"
	default:
		return "finish requires a live idle Runtime"
	}
}

func (server *Server) acquireTaskControl(taskID string) bool {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	if server.closing || server.activeControls[taskID] {
		return false
	}
	server.activeControls[taskID] = true
	return true
}

func (server *Server) acquireProviderTaskControl(taskID string) bool {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	if server.closing || server.activeControls[taskID] {
		return false
	}
	server.activeControls[taskID] = true
	server.activeProviderControls[taskID] = true
	server.providerTaskContextLocked(taskID)
	server.providerControlWG.Add(1)
	return true
}

func (server *Server) providerTaskContextLocked(taskID string) context.Context {
	if existing := server.providerTaskContexts[taskID]; existing != nil && existing.Err() == nil {
		return existing
	}
	ctx, cancel := context.WithCancel(server.providerControlCtx)
	server.providerTaskContexts[taskID] = ctx
	server.providerTaskCancels[taskID] = cancel
	return ctx
}

func (server *Server) providerTaskContext(taskID string) context.Context {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	return server.providerTaskContextLocked(taskID)
}

func (server *Server) hasLiveProviderTaskContext(taskID string) bool {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	ctx := server.providerTaskContexts[taskID]
	return ctx != nil && ctx.Err() == nil
}

func (server *Server) cancelProviderTaskControls(taskID string) bool {
	server.controlMu.Lock()
	if !server.activeProviderControls[taskID] && server.queuedProviderControls[taskID] == 0 {
		server.controlMu.Unlock()
		return false
	}
	cancel := server.providerTaskCancels[taskID]
	delete(server.providerTaskContexts, taskID)
	delete(server.providerTaskCancels, taskID)
	server.controlMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (server *Server) decrementQueuedProviderControlLocked(taskID string) {
	queued := server.queuedProviderControls[taskID]
	if queued <= 1 {
		delete(server.queuedProviderControls, taskID)
		return
	}
	server.queuedProviderControls[taskID] = queued - 1
}

func (server *Server) waitAcquireTaskControl(ctx context.Context, taskID string) bool {
	for {
		if server.acquireTaskControl(taskID) {
			return true
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

// enqueueProviderTaskControl waits behind the existing per-Task owner without
// losing durable Harness work. Registration is synchronized with Close so the
// shutdown wait group cannot race a newly started coordinator goroutine.
func (server *Server) enqueueProviderTaskControl(taskID string, operation func(context.Context)) bool {
	return server.enqueueProviderTaskControlWithContext(taskID, false, operation)
}

// enqueueExistingProviderTaskControl queues provider callbacks only while the
// originating provider operation still owns a live Task context. Stop removes
// and cancels that context before provider interruption can be observed.
func (server *Server) enqueueExistingProviderTaskControl(taskID string, operation func(context.Context)) bool {
	return server.enqueueProviderTaskControlWithContext(taskID, true, operation)
}

func (server *Server) enqueueProviderTaskControlWithContext(taskID string, requireExisting bool, operation func(context.Context)) bool {
	return server.enqueueProviderTaskControlWithSettlement(taskID, requireExisting, nil, operation)
}

// providerControlSettlement is evaluated before and after a provider-control
// owner is acquired. The first pass may wait while a durable assisted
// conclusion settles; the second pass must be non-blocking so a pending
// conclusion can yield the control boundary to its own coordinator.
type providerControlSettlement func(context.Context, bool) (bool, error)

func (server *Server) enqueueProviderTaskControlAfterSettlement(ownerID string, settlement providerControlSettlement, operation func(context.Context)) bool {
	return server.enqueueProviderTaskControlWithSettlement(ownerID, false, settlement, operation)
}

func (server *Server) enqueueProviderTaskControlWithSettlement(ownerID string, requireExisting bool, settlement providerControlSettlement, operation func(context.Context)) bool {
	server.controlMu.Lock()
	if server.closing {
		server.controlMu.Unlock()
		return false
	}
	taskCtx := server.providerTaskContexts[ownerID]
	if requireExisting && (taskCtx == nil || taskCtx.Err() != nil) {
		server.controlMu.Unlock()
		return false
	}
	if taskCtx == nil || taskCtx.Err() != nil {
		taskCtx = server.providerTaskContextLocked(ownerID)
	}
	server.queuedProviderControls[ownerID]++
	server.providerControlWG.Add(1)
	server.controlMu.Unlock()

	go func() {
		ownsControl := false
		queued := true
		defer func() {
			server.controlMu.Lock()
			if queued {
				server.decrementQueuedProviderControlLocked(ownerID)
			}
			if ownsControl {
				delete(server.activeControls, ownerID)
				delete(server.activeProviderControls, ownerID)
			}
			server.controlMu.Unlock()
			server.providerControlWG.Done()
		}()
		for {
			if settlement != nil {
				settled, err := settlement(taskCtx, true)
				if err != nil || !settled {
					return
				}
			}
			server.controlMu.Lock()
			if server.closing || taskCtx.Err() != nil {
				server.controlMu.Unlock()
				return
			}
			if !server.activeControls[ownerID] {
				server.activeControls[ownerID] = true
				server.activeProviderControls[ownerID] = true
				server.decrementQueuedProviderControlLocked(ownerID)
				queued = false
				ownsControl = true
				server.controlMu.Unlock()
				if settlement != nil {
					settled, err := settlement(taskCtx, false)
					if err != nil || !settled {
						server.controlMu.Lock()
						delete(server.activeControls, ownerID)
						delete(server.activeProviderControls, ownerID)
						server.queuedProviderControls[ownerID]++
						queued = true
						ownsControl = false
						server.controlMu.Unlock()
						if err != nil {
							return
						}
						continue
					}
				}
				break
			}
			server.controlMu.Unlock()

			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-taskCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		operation(taskCtx)
	}()
	return true
}

func (server *Server) releaseProviderTaskControl(taskID string) {
	server.controlMu.Lock()
	delete(server.activeControls, taskID)
	delete(server.activeProviderControls, taskID)
	server.controlMu.Unlock()
	server.providerControlWG.Done()
}

func (server *Server) releaseTaskControl(taskID string) {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	delete(server.activeControls, taskID)
}

func (server *Server) handleResumeTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if durableTaskActive(found.Status) {
		writeError(response, http.StatusConflict, "task is already running")
		return
	}
	// Terminal Tasks may still show a brief harness window while Launch releases
	// ownership. Wait for absence rather than rejecting a legitimate resume.
	// Budget is server.runtimeStopTimeout (not a hardcoded constant).
	if server.harness != nil && server.harness.IsActive(taskID) {
		if !server.waitRuntimeHarnessInactive(taskID) {
			writeError(response, http.StatusConflict, "runtime harness is still active")
			return
		}
	}
	var input taskContinuationSelectionInput
	if err := decodeOptionalJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)
	// Re-validate under the control lock: a concurrent resume may have already
	// launched. Using stale terminal Task state here would call
	// ensureRuntimeAbsentBeforeLaunch and stop the first Runtime.
	found, err = server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if durableTaskActive(found.Status) {
		writeError(response, http.StatusConflict, "task is already running")
		return
	}
	if server.harness != nil && server.harness.IsActive(taskID) {
		if !server.waitRuntimeHarnessInactive(taskID) {
			writeError(response, http.StatusConflict, "runtime harness is still active")
			return
		}
	}
	if _, bound := server.providerSessions.get(taskID); bound {
		writeError(response, http.StatusConflict, "task already has a live Runtime")
		return
	}
	if input.hasSelection() {
		if _, ok := server.recordSelectedRuntimeConfig(response, found, "", input); !ok {
			return
		}
		refreshed, err := server.tasks.Get(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		found = refreshed
	}
	found, resumeGoal, plan, err := server.prepareResumeContinuation(found, "")
	if err != nil {
		server.writeResumePreparationError(response, err)
		return
	}
	if err := server.launchTaskInBackground(found, plan, resumeGoal); err != nil {
		writeTaskLaunchError(response, err)
		return
	}

	updated, err := server.taskDetail(found.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, updated)
}

func (server *Server) prepareNativeResumeContinuation(found task.Task, resumedMessage string) (task.Task, string, taskLaunchPlan, error) {
	found, resumedMessage, nativeResume, err := server.prepareNativeResumeRequest(found, resumedMessage)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	var steeringEventIDs []string
	if server.canonicalStore == store.CanonicalStoreBlackboardV2 && server.isBlackboardV2Task(found) {
		resumedMessage, steeringEventIDs, err = server.blackboardV2ResumeContext(found)
		if err != nil {
			return task.Task{}, "", taskLaunchPlan{}, err
		}
	}
	modelOverride, reasoningEffort := server.resumeTurnSelectionOverrides(found)
	plan, err := server.buildTaskLaunchPlan(found, resumedMessage, modelOverride, nativeResume.NativeSessionID, reasoningEffort)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan.NativeResumeSessionPath = nativeResume.NativeSessionPath
	plan.BlackboardV2SteeringEventIDs = steeringEventIDs
	return found, resumedMessage, plan, nil
}

// prepareResumeContinuation prefers a provider-native session when one is
// available. Otherwise it creates a fresh continuation from Task-owned Goal,
// interrupted Attempt checkpoints, unconsumed Harness Steering, and the new
// Working Snapshot. No summary or synthetic handoff packet is consulted.
func (server *Server) prepareResumeContinuation(found task.Task, resumedMessage string) (task.Task, string, taskLaunchPlan, error) {
	prepared, goal, plan, err := server.prepareNativeResumeContinuation(found, resumedMessage)
	if err == nil {
		return prepared, goal, plan, nil
	}
	if !errors.Is(err, errNativeSessionUnavailable) && !errors.Is(err, errNativeResumeUnavailable) {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	return server.prepareFreshResumeContinuation(found)
}

func (server *Server) prepareNativeResumeRequest(found task.Task, resumedMessage string) (task.Task, string, runtime.NativeSessionMetadata, error) {
	effectiveProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.Task{}, "", runtime.NativeSessionMetadata{}, err
	}
	found.RuntimeProfileID = effectiveProfile.ID
	nativeResume, err := server.discoverNativeResumeSession(found)
	if err != nil {
		return task.Task{}, "", runtime.NativeSessionMetadata{}, err
	}
	return found, resumedMessage, nativeResume, nil
}

func (server *Server) prepareFreshResumeContinuation(found task.Task) (task.Task, string, taskLaunchPlan, error) {
	effectiveProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	found.RuntimeProfileID = effectiveProfile.ID

	resumeGoal := ""
	var steeringEventIDs []string
	if server.canonicalStore == store.CanonicalStoreBlackboardV2 && runner.BlackboardV2SupportsProvider(effectiveProfile.Provider) {
		resumeGoal, steeringEventIDs, err = server.blackboardV2ResumeContext(found)
	} else {
		resumeGoal, err = server.buildResumeGoal(found)
	}
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	modelOverride, reasoningEffort := server.resumeTurnSelectionOverrides(found)
	plan, err := server.buildTaskLaunchPlan(found, resumeGoal, modelOverride, "", reasoningEffort)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan.BlackboardV2SteeringEventIDs = steeringEventIDs
	return found, resumeGoal, plan, nil
}

// resumeTurnSelectionOverrides carries the preceding Runtime Turn Selection into
// a restart/resume launch so provider-switch paths do not drop model or effort.
func (server *Server) resumeTurnSelectionOverrides(found task.Task) (modelOverride, reasoningEffort string) {
	selection, err := server.currentTurnSelection(found)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(selection.Model), strings.TrimSpace(selection.RequestedReasoningEffort)
}

func (server *Server) buildResumeGoal(found task.Task) (string, error) {
	events, err := server.tasks.Events(found.ID)
	if err != nil {
		return "", err
	}
	if server.canonicalStore == store.CanonicalStoreBlackboardV2 {
		if server.isBlackboardV2Task(found) {
			goal, _, err := server.blackboardV2ResumeContext(found)
			return goal, err
		}
		directives := unconsumedHarnessSteering(events)
		return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{TaskGoal: found.Goal, Steering: directives}), nil
	}

	return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{
		TaskGoal: found.Goal, Steering: unconsumedHarnessSteering(events),
	}), nil
}

func (server *Server) isCodexTask(found task.Task) bool {
	profile, err := server.profiles.Get(found.RuntimeProfileID)
	return err == nil && profile.Provider == runtimeprofile.ProviderCodex
}

func (server *Server) isBlackboardV2Task(found task.Task) bool {
	profile, err := server.profiles.Get(found.RuntimeProfileID)
	return err == nil && runner.BlackboardV2SupportsProvider(profile.Provider)
}

func (server *Server) blackboardV2ResumeContext(found task.Task) (string, []string, error) {
	steering, err := server.tasks.UnconsumedHarnessSteering(context.Background(), found.ID)
	if err != nil {
		return "", nil, err
	}
	var checkpoints []blackboardv2.InterruptedAttemptCheckpoint
	latest, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return "", nil, err
	}
	if latest != nil {
		checkpoints, err = server.blackboardV2.InterruptedAttemptCheckpoints(context.Background(), found.ProjectID, latest.ID)
		if err != nil {
			return "", nil, err
		}
	}
	directives := make([]string, len(steering))
	eventIDs := make([]string, len(steering))
	for index, directive := range steering {
		directives[index] = directive.Directive
		eventIDs[index] = directive.EventID
	}
	return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{
		TaskGoal: found.Goal, Steering: directives, InterruptedAttempts: checkpoints,
	}), eventIDs, nil
}

func unconsumedHarnessSteering(events []task.Event) []string {
	consumed := make(map[string]bool)
	for _, event := range events {
		if event.Kind != task.EventKindSteering || event.Payload["phase"] != "steering_applied" {
			continue
		}
		if requestedID, ok := event.Payload["requested_event_id"].(string); ok && requestedID != "" {
			consumed[requestedID] = true
		}
	}
	directives := make([]string, 0)
	for _, event := range events {
		if event.Kind != task.EventKindSteering || event.Payload["phase"] != "steering_requested" || consumed[event.ID] {
			continue
		}
		if directive, ok := event.Payload["directive"].(string); ok && strings.TrimSpace(directive) != "" {
			directives = append(directives, directive)
		}
	}
	return directives
}

func (server *Server) writeResumePreparationError(response http.ResponseWriter, err error) {
	var boardErr *blackboardv2.Error
	if errors.As(err, &boardErr) && boardErr.Code == "reconciliation_incomplete" {
		writeError(response, http.StatusConflict, boardErr.Message)
		return
	}
	switch {
	case errors.Is(err, runtimeprofile.ErrNotFound):
		writeError(response, http.StatusBadRequest, "runtime profile not found")
	case errors.Is(err, errNativeResumeUnavailable):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, errNativeSessionUnavailable):
		writeError(response, http.StatusConflict, err.Error())
	case errors.Is(err, task.ErrContinuationReconciliationIncomplete), errors.Is(err, task.ErrSteeringSelectionConflict):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeTaskAdapterError(response, err)
	}
}

func (server *Server) handleQueueSteerTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	var input struct {
		Directive string `json:"directive"`
		taskContinuationSelectionInput
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(input.Directive) == "" {
		writeError(response, http.StatusBadRequest, "steering directive is required")
		return
	}
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)
	payload := task.EventPayload{
		"directive": input.Directive,
		"phase":     "steering_requested",
		"mode":      "queue",
	}
	if input.SubmittedBy != "" {
		payload["submitted_by"] = input.SubmittedBy
	}
	if input.RuntimeProfileID != "" {
		payload["runtime_profile_id"] = input.RuntimeProfileID
	}
	if input.ModelProviderID != "" {
		payload["model_provider_id"] = input.ModelProviderID
	}
	if model := input.selectedModel(); model != "" {
		payload["model"] = model
		payload["model_override"] = model
	}
	if input.ReasoningEffort != "" {
		payload["reasoning_effort"] = input.ReasoningEffort
	}
	event, err := server.tasks.AppendEvent(taskID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	// Config Projection / Runtime Config Version is only for profile or Model
	// Provider changes. Same-provider model/effort stay on the event payload
	// (and on native turns) without minting a new version.
	var configVersion *task.RuntimeConfigVersion
	if input.hasSelection() {
		recorded, ok := server.recordSelectedRuntimeConfig(response, found, event.ID, input.taskContinuationSelectionInput)
		if !ok {
			return
		}
		configVersion = &recorded
	}
	writeJSON(response, http.StatusOK, struct {
		Event                task.Event                 `json:"event"`
		RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
	}{Event: event, RuntimeConfigVersion: configVersion})
}

func (server *Server) handleSteerTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if session, ok := server.providerSessions.get(taskID); ok {
		server.handleProviderSessionSteer(response, request, found, session)
		return
	}

	var input struct {
		Directive string `json:"directive"`
		taskContinuationSelectionInput
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.Directive == "" {
		writeError(response, http.StatusBadRequest, "steering directive is required")
		return
	}
	if profile, profileErr := server.resolveTaskRuntimeProfile(found); profileErr != nil {
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return
	} else if strings.TrimSpace(profile.ID) == "" {
		// The Task's launch and Task Runtime Configuration Version profiles are
		// both deleted: no future continuation could deliver this directive, so
		// fail closed instead of recording a pending steering event.
		writeError(response, http.StatusBadRequest, "runtime profile not found")
		return
	}

	activeSteer := found.Status == task.StatusRunning || found.Status == task.StatusPaused
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)

	payload := task.EventPayload{
		"directive": input.Directive,
		"phase":     "steering_requested",
	}
	if input.SubmittedBy != "" {
		payload["submitted_by"] = input.SubmittedBy
	}
	if input.RuntimeProfileID != "" {
		payload["runtime_profile_id"] = input.RuntimeProfileID
	}
	if input.ModelProviderID != "" {
		payload["model_provider_id"] = input.ModelProviderID
	}
	if model := input.selectedModel(); model != "" {
		payload["model"] = model
		payload["model_override"] = model
	}
	if input.ReasoningEffort != "" {
		payload["reasoning_effort"] = input.ReasoningEffort
	}

	event, err := server.tasks.AppendEvent(taskID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return
	}

	var configVersion *task.RuntimeConfigVersion
	if input.hasSelection() {
		recorded, ok := server.recordSelectedRuntimeConfig(response, found, event.ID, input.taskContinuationSelectionInput)
		if !ok {
			return
		}
		configVersion = &recorded
	}

	if activeSteer {
		if _, _, _, err := server.prepareNativeResumeRequest(found, input.Directive); err != nil {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "resume_failed",
				"error": err.Error(),
			})
			server.writeResumePreparationError(response, err)
			return
		}
		_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": "interrupting",
		})
		if ok := server.harness.StopAndWait(taskID, 10*time.Second); !ok {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "stop_failed",
			})
			writeError(response, http.StatusConflict, "runtime did not stop in time")
			return
		}

		resumedTask, resumeGoal, plan, err := server.prepareNativeResumeContinuation(found, input.Directive)
		if err != nil {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "resume_failed",
				"error": err.Error(),
			})
			server.writeResumePreparationError(response, err)
			return
		}
		_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": "resuming_native",
		})
		if err := server.launchTaskInBackground(resumedTask, plan, resumeGoal); err != nil {
			writeTaskLaunchError(response, err)
			return
		}
		if !plan.BlackboardV2 {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindSteering, task.EventPayload{
				"phase":              "steering_applied",
				"directive":          input.Directive,
				"requested_event_id": event.ID,
			})
		}
		detailed, err := server.taskDetail(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		writeJSON(response, http.StatusAccepted, struct {
			Event                task.Event                 `json:"event"`
			RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
			Task                 task.Task                  `json:"task"`
		}{
			Event:                event,
			RuntimeConfigVersion: configVersion,
			Task:                 detailed,
		})
		return
	}

	writeJSON(response, http.StatusOK, struct {
		Event                task.Event                 `json:"event"`
		RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
	}{
		Event:                event,
		RuntimeConfigVersion: configVersion,
	})
}

func (server *Server) handleProviderSessionSteer(response http.ResponseWriter, request *http.Request, found task.Task, session runtime.ProviderSession) {
	var input nativeSteerRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		input.RequestID = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if input.RequestID == "" {
		input.RequestID = newNativeSteerRequestID()
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" {
		input.Message = strings.TrimSpace(input.Directive)
	}
	if input.Message == "" {
		writeError(response, http.StatusBadRequest, "steer message is required")
		return
	}
	// Runtime Plugin / Runtime Profile switches need Config Projection and a
	// restart. Model Provider changes stay native for Pi when the provider was
	// projected at startup (ADR 0015); Codex/Claude still restart.
	if input.hasRuntimeProfileSelection() {
		writeError(response, http.StatusConflict, "native steer cannot change runtime profile; restart the continuation")
		return
	}
	selection, ok := server.resolveNativeTurnSelection(response, found, input.taskContinuationSelectionInput)
	if !ok {
		return
	}
	if found.Status != task.StatusRunning && found.Status != task.StatusPaused {
		writeError(response, http.StatusConflict, "native steer requires an active Task")
		return
	}
	mode, err := nativeSteerMode(session.Capabilities())
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	if nativeSteerOperation(session, mode) == nil {
		writeError(response, http.StatusConflict, "provider session does not support native steer")
		return
	}

	// Repeated requests with the same request identity return the durable
	// current outcome and never create a second queue item. Conflicting content
	// under the same identity returns a conflict.
	if record, err := server.steering.ByRequestID(owner.KindTask, found.ID, input.RequestID); err == nil {
		if conflict := steeringConflictMessage(record, input.Message, selection, owner.SteeringMode(mode)); conflict != "" {
			writeError(response, http.StatusConflict, conflict)
			return
		}
		sessionID := record.SessionID
		if sessionID == "" {
			sessionID = session.SessionID()
		}
		writeJSON(response, http.StatusAccepted, struct {
			RequestID string                      `json:"request_id"`
			SessionID string                      `json:"session_id"`
			Mode      runtime.ProviderSessionMode `json:"mode"`
			Outcome   string                      `json:"outcome"`
		}{RequestID: input.RequestID, SessionID: sessionID, Mode: runtime.ProviderSessionMode(record.Mode), Outcome: steeringOutcomeFromRecord(record)})
		return
	} else if !errors.Is(err, steering.ErrNotFound) {
		writeError(response, http.StatusInternalServerError, "load accepted steering")
		return
	}

	// Durable acceptance (#194, #200): the operator message and the dispatch
	// record commit atomically before the request returns 202. The accepted
	// steering is dispatched by the owner-neutral FIFO queue at the next valid
	// work boundary; the Conversation Event is a projection, not the dispatch
	// source of truth.
	conversationPayload := task.EventPayload{
		"role": "user", "text": input.Message, "request_id": input.RequestID,
		"delivery": "native_steer", "outcome": "pending", "mode": string(mode),
		"session_id":        session.SessionID(),
		"model_provider_id": selection.ModelProviderID, "model": selection.Model,
		"requested_reasoning_effort": selection.RequestedReasoningEffort,
	}
	adapter := taskSteeringAdapter(server, found.ID, server.taskConclusionSettlementForID(found.ID))
	_, err = server.acceptSteeringDurably(request.Context(), adapter, steering.AcceptRequest{
		RequestID:                input.RequestID,
		Message:                  input.Message,
		Mode:                     owner.SteeringMode(mode),
		ModelProviderID:          selection.ModelProviderID,
		Model:                    selection.Model,
		RequestedReasoningEffort: selection.RequestedReasoningEffort,
		SessionID:                session.SessionID(),
	}, func(tx *sql.Tx) (string, error) {
		event, eventErr := server.tasks.AppendEventTx(tx, found.ID, task.EventKindConversation, conversationPayload)
		if eventErr != nil {
			return "", eventErr
		}
		return event.ID, nil
	})
	if errors.Is(err, steering.ErrDuplicateRequest) {
		// A concurrent duplicate committed first. The same identity rules
		// apply: matching content replays the durable outcome, conflicting
		// content is a conflict.
		replayed, replayErr := server.steering.ByRequestID(owner.KindTask, found.ID, input.RequestID)
		if replayErr != nil {
			writeError(response, http.StatusInternalServerError, "load accepted steering")
			return
		}
		if conflict := steeringConflictMessage(replayed, input.Message, selection, owner.SteeringMode(mode)); conflict != "" {
			writeError(response, http.StatusConflict, conflict)
			return
		}
		writeJSON(response, http.StatusAccepted, struct {
			RequestID string                      `json:"request_id"`
			SessionID string                      `json:"session_id"`
			Mode      runtime.ProviderSessionMode `json:"mode"`
			Outcome   string                      `json:"outcome"`
		}{RequestID: input.RequestID, SessionID: replayed.SessionID, Mode: runtime.ProviderSessionMode(replayed.Mode), Outcome: steeringOutcomeFromRecord(replayed)})
		return
	}
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, struct {
		RequestID string                      `json:"request_id"`
		SessionID string                      `json:"session_id"`
		Mode      runtime.ProviderSessionMode `json:"mode"`
		Outcome   string                      `json:"outcome"`
	}{RequestID: input.RequestID, SessionID: session.SessionID(), Mode: mode, Outcome: "accepted"})
}

// nativeSteerOperationFunc is one provider session steer operation selected by
// the session mode (in_turn_steer or interrupt_then_replace).
type nativeSteerOperationFunc func(context.Context, runtime.ProviderSessionRequest, runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error)

// taskConclusionSettlement waits for the durable assisted conclusion to settle
// before an accepted native steer may take the provider control boundary. The
// second (post-acquisition) pass is non-blocking so a conclusion that becomes
// pending can yield control to its own coordinator.
func (server *Server) taskConclusionSettlement(found task.Task) providerControlSettlement {
	return func(ctx context.Context, wait bool) (bool, error) {
		for {
			receipt, err := server.tasks.LatestBlackboardConclusion(found.ID)
			if err != nil {
				return false, err
			}
			if receipt == nil {
				return true, nil
			}
			switch receipt.View(found.RunControls.BlackboardConclusionMode).State {
			case task.BlackboardConclusionStateClean:
				return true, nil
			case task.BlackboardConclusionStateActionRequired:
				// The conclusion needs operator action, not Harness work; the
				// provider control boundary is free for the accepted steering.
				return true, nil
			}
			if !wait {
				return false, nil
			}
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, ctx.Err()
			case <-timer.C:
			}
		}
	}
}

// executeNativeSteerOperation sends one accepted native steer Turn under Task
// control and projects applied/failed/action_required outcome events on the
// correct Runtime Continuation. The caller owns Task control for the whole
// operation. The returned execution is the durable terminal outcome.
func (server *Server) executeNativeSteerOperation(ctx context.Context, found task.Task, session runtime.ProviderSession, mode runtime.ProviderSessionMode, operation nativeSteerOperationFunc, providerRequest runtime.ProviderSessionRequest, conversation task.Event, continuationID string, freshContinuation bool) steeringExecution {
	var continuationMu sync.Mutex
	var continuationTransitionErr error
	emit := func(kind task.EventKind, payload task.EventPayload) {
		payload["conversation_event_id"] = conversation.ID
		continuationMu.Lock()
		currentContinuationID := continuationID
		if currentContinuationID != "" {
			_, _ = server.tasks.AppendContinuationEvent(found.ID, currentContinuationID, kind, payload)
		}
		if mode == runtime.ProviderSessionModeInterruptThenReplace && kind == task.EventKindSteering && payload["outcome"] == "settled" && currentContinuationID != "" && !freshContinuation {
			if transitionErr := server.advanceNativeSteerContinuation(currentContinuationID, session, &continuationID); transitionErr != nil {
				continuationTransitionErr = transitionErr
				failure := task.EventPayload{
					"request_id": payload["request_id"], "session_id": payload["session_id"],
					"mode": string(mode), "outcome": "failed", "phase": "replacement_continuation_failed",
					"error_code": "continuation_transition_failed",
				}
				_, _ = server.tasks.AppendContinuationEvent(found.ID, currentContinuationID, task.EventKindSteering, failure)
			}
		}
		continuationMu.Unlock()
		if currentContinuationID == "" {
			_, _ = server.tasks.AppendEvent(found.ID, kind, payload)
		}
	}
	result, operationErr := operation(ctx, providerRequest, emit)
	if operationErr != nil {
		errorCode, errorMessage := nativeSteerFailurePresentation(operationErr)
		if errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
			// Post-fence with no provider outcome: delivery is ambiguous. The
			// request is never replayed automatically; it settles
			// action_required with a reason-specific recovery path.
			ambiguous := task.EventPayload{
				"request_id": providerRequest.RequestID, "session_id": session.SessionID(), "mode": string(mode),
				"outcome": "action_required", "phase": "steering_action_required",
				"error_code": string(owner.SteeringReasonDeliveryAmbiguous), "error": errorMessage,
				"model_provider_id": providerRequest.ModelProviderID, "model": providerRequest.Model,
				"requested_reasoning_effort": providerRequest.RequestedReasoningEffort,
			}
			emit(task.EventKindSteering, ambiguous)
			return steeringExecution{state: owner.SteeringActionRequired, reason: owner.SteeringReasonDeliveryAmbiguous, message: errorMessage}
		}
		// Public Task Events carry only redacted, stable failure fields.
		// Raw provider text stays out of the conversation surface.
		failure := task.EventPayload{
			"request_id": providerRequest.RequestID, "session_id": session.SessionID(), "mode": string(mode),
			"outcome": "failed", "phase": "steering_failed", "error_code": errorCode,
			"error":             errorMessage,
			"model_provider_id": providerRequest.ModelProviderID, "model": providerRequest.Model,
			"requested_reasoning_effort": providerRequest.RequestedReasoningEffort,
		}
		emit(task.EventKindSteering, failure)
		return steeringExecution{state: owner.SteeringFailed, reason: steerReasonFromFailureCode(errorCode), message: errorMessage}
	}
	continuationMu.Lock()
	transitionErr := continuationTransitionErr
	continuationMu.Unlock()
	if transitionErr != nil {
		_ = server.closeProviderSession(found.ID)
		if current, _ := server.tasks.ActiveContinuation(found.ID); current != nil {
			_, _ = server.tasks.UpdateContinuationStatus(current.ID, task.StatusFailed)
		}
		_, _ = server.tasks.UpdateStatus(found.ID, task.StatusFailed)
		return steeringExecution{state: owner.SteeringFailed, reason: owner.SteeringReasonContinuationUnavailable, message: transitionErr.Error(), failOwner: true}
	}
	payload := result.Payload()
	payload["outcome"] = "applied"
	payload["phase"] = "steering_applied"
	emit(task.EventKindSteering, payload)
	return steeringExecution{state: owner.SteeringApplied, result: result.Payload()}
}

// steerReasonFromFailureCode maps the redacted provider failure presentation
// onto the closed Accepted Steering reason vocabulary. The projected event
// error_code keeps the presentation code; the durable record stores the closed
// reason.
func steerReasonFromFailureCode(code string) owner.SteeringFailureReason {
	switch code {
	case "session_closed":
		return owner.SteeringReasonSessionClosed
	case "control_conflict":
		return owner.SteeringReasonControlConflict
	case "unsupported_reasoning_effort":
		return owner.SteeringReasonUnsupportedReasoningEffort
	case "provider_rejected":
		return owner.SteeringReasonProviderRejected
	case "provider_control_unavailable":
		return owner.SteeringReasonProviderControlUnavailable
	default:
		return owner.SteeringReasonProviderRejected
	}
}

type providerPermissionResponseRequest struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
}

// handleProviderPermissionResponse answers one provider permission request on
// the same Task-owned session. It is authenticated by ServeHTTP's daemon
// middleware and never exposes provider wire payloads.
func (server *Server) handleProviderPermissionResponse(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	session, bound := server.providerSessions.get(found.ID)
	if !bound || session == nil {
		writeError(response, http.StatusConflict, "provider session is unavailable")
		return
	}
	permissionID := strings.TrimSpace(request.PathValue("permission_id"))
	if permissionID == "" {
		writeError(response, http.StatusBadRequest, "permission request id is required")
		return
	}
	var input providerPermissionResponseRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.Decision = normalizePermissionDecision(input.Decision)
	if input.Decision == "" {
		writeError(response, http.StatusBadRequest, "permission decision must be allow or deny")
		return
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		input.RequestID = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if input.RequestID == "" {
		input.RequestID = "permission-" + permissionID + "-" + input.Decision
	}

	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	pending, priorOutcome, priorDecision := providerPermissionStatus(eventsToTimelineEvents(events), permissionID, input.RequestID)
	if priorDecision != "" && priorDecision != input.Decision {
		writeError(response, http.StatusConflict, "permission request id already belongs to a different decision")
		return
	}
	if priorOutcome != "" {
		writeJSON(response, http.StatusAccepted, map[string]any{
			"request_id": input.RequestID, "permission_request_id": permissionID,
			"session_id": session.SessionID(), "decision": input.Decision, "outcome": priorOutcome,
		})
		return
	}
	if !pending {
		writeError(response, http.StatusNotFound, "provider permission request is no longer pending")
		return
	}
	if found.Status != task.StatusRunning && found.Status != task.StatusPaused {
		writeError(response, http.StatusConflict, "provider permission requires an active Task")
		return
	}
	if !session.Capabilities().PermissionResponse {
		writeError(response, http.StatusConflict, "provider session does not support permission responses")
		return
	}
	if !server.acquireProviderTaskControl(found.ID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	active, err := server.tasks.ActiveContinuation(found.ID)
	if err != nil {
		server.releaseProviderTaskControl(found.ID)
		writeTaskError(response, err)
		return
	}
	continuationID := ""
	if active != nil {
		continuationID = active.ID
	}
	requestedPayload := task.EventPayload{
		"phase": "provider_permission_response_requested", "mode": string(runtime.ProviderSessionModePermissionResponse),
		"outcome": "pending", "request_id": input.RequestID, "permission_request_id": permissionID,
		"permission_decision": input.Decision, "session_id": session.SessionID(),
	}
	if continuationID != "" {
		_, err = server.tasks.AppendContinuationEvent(found.ID, continuationID, task.EventKindLifecycle, requestedPayload)
	} else {
		_, err = server.tasks.AppendEvent(found.ID, task.EventKindLifecycle, requestedPayload)
	}
	if err != nil {
		server.releaseProviderTaskControl(found.ID)
		writeTaskError(response, err)
		return
	}
	taskCtx := server.providerTaskContext(found.ID)
	emit := func(kind task.EventKind, payload task.EventPayload) {
		redacted := task.EventPayload{}
		// Fixed correlation allowlist shared with the Session provider event
		// projection (provider_session_control.go): raw protocol payload never
		// reaches the Task Conversation.
		for _, key := range []string{"provider", "request_id", "session_id", "provider_turn_id", "mode", "outcome", "permission_request_id", "permission_decision", "error_code", "phase"} {
			if value, ok := payload[key]; ok {
				redacted[key] = value
			}
		}
		if redacted["request_id"] == nil {
			redacted["request_id"] = input.RequestID
		}
		redacted["permission_request_id"] = permissionID
		if redacted["mode"] == nil {
			redacted["mode"] = string(runtime.ProviderSessionModePermissionResponse)
		}
		switch redacted["outcome"] {
		case "requested":
			redacted["phase"] = "provider_permission_response_requested"
		case "acknowledged":
			redacted["phase"] = "provider_permission_response_acknowledged"
		case "failed":
			redacted["phase"] = "provider_permission_response_failed"
		}
		if continuationID != "" {
			_, _ = server.tasks.AppendContinuationEvent(found.ID, continuationID, kind, redacted)
		} else {
			_, _ = server.tasks.AppendEvent(found.ID, kind, redacted)
		}
	}
	go func() {
		defer server.releaseProviderTaskControl(found.ID)
		ctx, cancel := context.WithTimeout(taskCtx, 30*time.Second)
		defer cancel()
		result, operationErr := session.RespondPermission(ctx, runtime.ProviderSessionRequest{
			RequestID: input.RequestID, PermissionRequestID: permissionID, PermissionDecision: input.Decision,
		}, emit)
		if operationErr != nil {
			emit(task.EventKindLifecycle, task.EventPayload{"outcome": "failed", "phase": "provider_permission_response_failed", "error_code": permissionResponseErrorCode(operationErr)})
			return
		}
		payload := result.Payload()
		payload["phase"] = "provider_permission_response_applied"
		payload["outcome"] = "applied"
		payload["permission_request_id"] = permissionID
		if continuationID != "" {
			_, _ = server.tasks.AppendContinuationEvent(found.ID, continuationID, task.EventKindLifecycle, payload)
		} else {
			_, _ = server.tasks.AppendEvent(found.ID, task.EventKindLifecycle, payload)
		}
	}()
	writeJSON(response, http.StatusAccepted, map[string]any{
		"request_id": input.RequestID, "permission_request_id": permissionID,
		"session_id": session.SessionID(), "decision": input.Decision, "outcome": "accepted",
	})
}

func normalizePermissionDecision(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "allow", "approve", "approved", "yes":
		return "allow"
	case "deny", "reject", "rejected", "no":
		return "deny"
	default:
		return ""
	}
}

// permissionResponseErrorCode maps a provider permission-response failure to
// the stable error_code both owner kinds persist, so task and session
// permission handling share one classification.
func permissionResponseErrorCode(operationErr error) string {
	switch {
	case errors.Is(operationErr, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(operationErr, context.Canceled):
		return "server_closing"
	case errors.Is(operationErr, runtime.ErrProviderSessionClosed):
		return "session_closed"
	case errors.Is(operationErr, runtime.ErrProviderSessionControlConflict):
		return "control_conflict"
	default:
		return "provider_rejected"
	}
}

// providerPermissionStatus scans either owner's event stream for the state of
// one provider permission request. It runs over the neutral event shape shared
// with timeline.Build so task and session permission handling cannot drift.
func providerPermissionStatus(events []timeline.Event, permissionID, requestID string) (pending bool, outcome, decision string) {
	for _, event := range events {
		if event.Payload["permission_request_id"] != permissionID {
			continue
		}
		rid, _ := event.Payload["request_id"].(string)
		if rid == requestID {
			if value, ok := event.Payload["permission_decision"].(string); ok && value != "" {
				decision = normalizePermissionDecision(value)
			}
		}
		phase, _ := event.Payload["phase"].(string)
		switch phase {
		case "provider_permission_requested":
			pending = true
		case "provider_permission_response_requested", "provider_permission_response_acknowledged":
			if rid == requestID {
				outcome = "pending"
			}
		case "provider_permission_response_applied":
			pending = false
			if rid == requestID {
				outcome = "applied"
			}
		case "provider_permission_response_failed":
			pending = true
			if rid == requestID {
				outcome = "failed"
			}
		}
	}
	return pending, outcome, decision
}

func decodeOptionalJSON(request *http.Request, target any) error {
	if request.Body == nil {
		return nil
	}
	err := json.NewDecoder(request.Body).Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// advanceNativeSteerContinuation performs the old-turn settlement boundary
// while the provider control operation is still serialized. The adapter emits
// settled before sending the replacement turn, so the next provider event is
// guaranteed to land on the fresh Continuation.
func (server *Server) advanceNativeSteerContinuation(currentID string, session runtime.ProviderSession, continuationID *string) error {
	old, err := server.tasks.Continuation(currentID)
	if err != nil {
		return fmt.Errorf("load old continuation: %w", err)
	}
	next, err := server.tasks.CreateReplacementContinuation(old)
	if err != nil {
		return fmt.Errorf("create replacement continuation: %w", err)
	}
	if binder, ok := session.(runtime.ProviderSessionContinuationBinder); ok {
		if err := binder.BindContinuation(next.ID); err != nil {
			_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
			return fmt.Errorf("bind provider continuation: %w", err)
		}
	}
	if _, err := server.tasks.UpdateContinuationStatus(next.ID, task.StatusRunning); err != nil {
		_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
		return fmt.Errorf("start replacement continuation: %w", err)
	}
	if server.harness.IsActive(old.TaskID) {
		if err := server.harness.RebindContinuation(old.TaskID, next.ID); err != nil {
			_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
			return fmt.Errorf("rebind runtime continuation: %w", err)
		}
	}
	// Carry the Blackboard grant and Working Snapshot ownership to the
	// replacement before the old Continuation is completed. The in-container MCP
	// token is immutable, so this rebind keeps the still-running agent's
	// Blackboard writes alive on the replacement instead of resolving to the
	// completed old Continuation (closed_continuation).
	if server.blackboardV2Continuity != nil {
		if err := server.blackboardV2Continuity.RebindContinuationForNativeSteer(context.Background(), old.ID, next.ID); err != nil {
			_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
			return fmt.Errorf("rebind Blackboard continuation grant: %w", err)
		}
	}
	// Recover any in-flight assisted-conclusion obligation with a NEW Conclusion
	// Dispatch bound to the replacement Continuation + live session so a later
	// retry delivers its control turn against the live replacement session
	// instead of looping on the pre-steer (now dead) session. Historical
	// dispatch identity is never rewritten (ADR 0021).
	server.recoverConclusionObligationsForReplacedContinuation(old.TaskID, old.ID, next.ID, session.SessionID())
	if _, err := server.tasks.UpdateContinuationStatus(old.ID, task.StatusCompleted); err != nil {
		_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
		return fmt.Errorf("settle old continuation: %w", err)
	}
	*continuationID = next.ID
	return nil
}

// errNoContinuationToContinue reports a Task that never had a Runtime
// Continuation. The legacy one-shot path keeps task-level events in that
// degenerate state instead of inventing continuation authority.
var errNoContinuationToContinue = errors.New("no prior continuation to continue after terminal Blackboard state")

// createWritableContinuationForLiveSession detects a terminal Runtime
// Continuation (for example closed by Blackboard Finish) before new operator
// work is sent and creates a fresh writable Continuation on the same
// Task-scoped persistent Runtime. The immutable in-container MCP grant and the
// Working Blackboard Snapshot are rebound to the replacement so later
// Blackboard writes use the new authority; the prior terminal Continuation
// keeps its closed state. Failure is explicit: the turn is never sent against
// the closed authority.
func (server *Server) createWritableContinuationForLiveSession(found task.Task, session runtime.ProviderSession) (*task.TaskContinuation, error) {
	previous, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return nil, fmt.Errorf("load terminal continuation: %w", err)
	}
	if previous == nil {
		return nil, errNoContinuationToContinue
	}
	next, err := server.tasks.CreateReplacementContinuation(*previous)
	if err != nil {
		return nil, fmt.Errorf("create writable continuation: %w", err)
	}
	fail := func(cause error) (*task.TaskContinuation, error) {
		_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
		return nil, cause
	}
	binder, ok := session.(runtime.ProviderSessionContinuationBinder)
	if !ok {
		return fail(fmt.Errorf("provider session cannot bind the fresh continuation"))
	}
	if err := binder.BindContinuation(next.ID); err != nil {
		return fail(fmt.Errorf("bind provider continuation: %w", err))
	}
	// Carry the Blackboard grant and Working Snapshot ownership to the
	// replacement before any new Turn writes. The in-container MCP token is
	// immutable, so this rebind keeps Blackboard writes alive on the
	// replacement instead of resolving to the terminal Continuation
	// (closed_continuation).
	if server.blackboardV2Continuity == nil {
		return fail(fmt.Errorf("Blackboard continuation rebind is unavailable"))
	}
	if err := server.blackboardV2Continuity.RebindContinuationForNativeSteer(context.Background(), previous.ID, next.ID); err != nil {
		return fail(fmt.Errorf("rebind Blackboard continuation grant: %w", err))
	}
	// Recover any in-flight assisted-conclusion obligation with a NEW Conclusion
	// Dispatch bound to the replacement Continuation + live session so a later
	// retry delivers its control turn against the live replacement session
	// instead of looping on the pre-steer (now dead) session. Historical
	// dispatch identity is never rewritten (ADR 0021).
	server.recoverConclusionObligationsForReplacedContinuation(found.ID, previous.ID, next.ID, session.SessionID())
	if _, err := server.tasks.UpdateContinuationStatus(next.ID, task.StatusRunning); err != nil {
		return fail(fmt.Errorf("start writable continuation: %w", err))
	}
	// Timeline boundary: the prior terminal Continuation stays visible next to
	// the fresh writable one in the Runtime Owner Workspace.
	_, _ = server.tasks.AppendContinuationEvent(found.ID, previous.ID, task.EventKindLifecycle, task.EventPayload{
		"phase": "completed", "reason": "superseded_by_writable_continuation",
	})
	_, _ = server.tasks.AppendContinuationEvent(found.ID, next.ID, task.EventKindLifecycle, task.EventPayload{
		"phase": "started", "adapter": "provider-session", "reason": "writable_after_terminal_continuation",
	})
	if server.harness != nil && server.harness.IsActive(found.ID) {
		if err := server.harness.RebindContinuation(found.ID, next.ID); err != nil {
			return fail(fmt.Errorf("rebind runtime continuation: %w", err))
		}
	}
	return &next, nil
}

func (server *Server) recordSelectedRuntimeConfig(response http.ResponseWriter, found task.Task, steeringEventID string, input taskContinuationSelectionInput) (task.RuntimeConfigVersion, bool) {
	requestedProfile, ok := server.resolveTaskContinuationRuntimeProfile(response, found, input)
	if !ok {
		return task.RuntimeConfigVersion{}, false
	}

	config := input.Config
	if config == nil {
		config = map[string]any{}
	}
	config["runtime_profile_id"] = requestedProfile.ID
	config["runner"] = found.Runner
	if steeringEventID != "" {
		config["steering_event_id"] = steeringEventID
	}
	if requestedProfile.Fields.ModelProviderID != "" {
		config["model_provider_id"] = requestedProfile.Fields.ModelProviderID
	}
	if model := input.selectedModel(); model != "" {
		config["model"] = model
		config["model_override"] = model
	} else if requestedProfile.Fields.ModelOverride != "" {
		config["model"] = requestedProfile.Fields.ModelOverride
		config["model_override"] = requestedProfile.Fields.ModelOverride
	}
	// Capture the explicit turn/queue Requested Reasoning Effort so resume
	// reuses the operator's selection without re-inferring Effective Effort.
	requestedEffort, err := runtimeprofile.ResolveRequestedReasoningEffort(
		input.ReasoningEffort,
		configString(config, "launch_reasoning_effort_override"),
		requestedProfile.Fields.ReasoningEffort,
	)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return task.RuntimeConfigVersion{}, false
	}
	config["requested_reasoning_effort"] = string(requestedEffort)
	if strings.TrimSpace(input.ReasoningEffort) != "" {
		config["reasoning_effort"] = string(requestedEffort)
	}
	recorded, err := server.tasks.RecordRuntimeConfig(found.ID, requestedProfile.ID, config)
	if err != nil {
		writeTaskError(response, err)
		return task.RuntimeConfigVersion{}, false
	}
	return recorded, true
}

// resolveNativeTurnSelection resolves the complete Runtime Turn Selection for a
// native steer. Same-provider model/effort changes are always allowed. A Model
// Provider change is native for Pi when the target was projected at Config
// Projection time; Codex/Claude reject it so the client restarts.
func (server *Server) resolveNativeTurnSelection(response http.ResponseWriter, found task.Task, input taskContinuationSelectionInput) (runtime.ProviderSessionRequest, bool) {
	current, err := server.currentTurnSelection(found)
	if err != nil {
		if errors.Is(err, runtimeprofile.ErrNotFound) {
			writeError(response, http.StatusBadRequest, "runtime profile not found")
			return runtime.ProviderSessionRequest{}, false
		}
		writeError(response, http.StatusInternalServerError, "resolve turn selection")
		return runtime.ProviderSessionRequest{}, false
	}
	requestedProvider := strings.TrimSpace(input.ModelProviderID)
	if requestedProvider != "" && requestedProvider != current.ModelProviderID {
		profile, profileErr := server.resolveTaskRuntimeProfile(found)
		if profileErr != nil {
			if errors.Is(profileErr, runtimeprofile.ErrNotFound) {
				writeError(response, http.StatusBadRequest, "runtime profile not found")
				return runtime.ProviderSessionRequest{}, false
			}
			writeError(response, http.StatusInternalServerError, "resolve turn selection")
			return runtime.ProviderSessionRequest{}, false
		}
		if profile.Provider != runtimeprofile.ProviderPi || !server.piProjectedProviderAllowed(found, requestedProvider) {
			// Codex/Claude (and Pi targets outside the projected set) require
			// credential re-resolution and Config Projection via restart.
			writeError(response, http.StatusConflict, "native steer cannot change model provider; restart the continuation")
			return runtime.ProviderSessionRequest{}, false
		}
	}

	selection := current
	if requestedProvider != "" {
		selection.ModelProviderID = requestedProvider
	}
	if model := input.selectedModel(); model != "" {
		selection.Model = model
	}
	requested, err := runtimeprofile.ResolveRequestedReasoningEffort(input.ReasoningEffort, current.RequestedReasoningEffort, "")
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return runtime.ProviderSessionRequest{}, false
	}
	selection.RequestedReasoningEffort = string(requested)
	return selection, true
}

// piProjectedProviderAllowed reports whether providerID was included in the Pi
// runtime's Config Projection set. ADR 0015 fixes that set until the next
// projection/restart: missing or empty projected_model_provider_ids fails
// closed so legacy tasks cannot perform arbitrary native cross-provider turns
// without a fresh Config Projection that records the set.
func (server *Server) piProjectedProviderAllowed(found task.Task, providerID string) bool {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return false
	}
	ids := server.projectedPiModelProviderIDs(found)
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if id == providerID {
			return true
		}
	}
	return false
}

func (server *Server) projectedPiModelProviderIDs(found task.Task) []string {
	versions, err := server.tasks.RuntimeConfigVersions(found.ID)
	if err != nil || len(versions) == 0 {
		return nil
	}
	latest := versions[len(versions)-1].Config
	return stringSliceFromConfig(latest["projected_model_provider_ids"])
}

func stringSliceFromConfig(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if id := strings.TrimSpace(item); id != "" {
				out = append(out, id)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if id := strings.TrimSpace(fmt.Sprint(item)); id != "" && id != "<nil>" {
				out = append(out, id)
			}
		}
		return out
	default:
		return nil
	}
}

func (server *Server) currentTurnSelection(found task.Task) (runtime.ProviderSessionRequest, error) {
	// Prefer the most recent applied selection:
	// - Conversation / steering events capture native same-session turns
	//   (no Configuration Version).
	// - Runtime Config Versions capture launch / resume / queue / restart
	//   selections. After resume records xhigh, config must win over an older
	//   conversation that still says high.
	convSelection, convAt, convOK := server.precedingConversationTurnSelectionTimed(found.ID)
	configSelection, configAt, configErr := server.selectionFromCapturedRuntimeConfigTimed(found)
	if configErr != nil {
		if convOK {
			return convSelection, nil
		}
		return runtime.ProviderSessionRequest{}, configErr
	}
	if convOK && !convAt.IsZero() && (configAt.IsZero() || convAt.After(configAt)) {
		return convSelection, nil
	}
	return configSelection, nil
}

// selectionFromCapturedRuntimeConfig resolves Model Provider / model / effort
// from the latest Task Runtime Configuration Version and model_provider_snapshot.
// Profile fields are only a fallback — launch-resolved and legacy profiles may
// leave provider/model empty on the Profile while the captured snapshot is true.
func (server *Server) selectionFromCapturedRuntimeConfig(found task.Task) (runtime.ProviderSessionRequest, error) {
	selection, _, err := server.selectionFromCapturedRuntimeConfigTimed(found)
	return selection, err
}

func (server *Server) selectionFromCapturedRuntimeConfigTimed(found task.Task) (runtime.ProviderSessionRequest, time.Time, error) {
	profile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, err
	}
	modelProviderID := strings.TrimSpace(profile.Fields.ModelProviderID)
	model := strings.TrimSpace(profile.Fields.ModelOverride)
	if model == "" {
		model = strings.TrimSpace(profile.Fields.Model)
	}
	launchEffort := ""
	var configAt time.Time
	versions, err := server.tasks.RuntimeConfigVersions(found.ID)
	if err != nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, err
	}
	if len(versions) > 0 {
		latestVersion := versions[len(versions)-1]
		configAt = latestVersion.CreatedAt
		latest := latestVersion.Config
		// Snapshot first (actual projected provider/model), then explicit
		// captured overrides from launch or later continuation selection.
		if snapshotProvider, snapshotModel := modelProviderSnapshotFields(latest["model_provider_snapshot"]); true {
			if snapshotProvider != "" {
				modelProviderID = snapshotProvider
			}
			if snapshotModel != "" {
				model = snapshotModel
			}
		}
		if value := configString(latest, "model_provider_id"); value != "" {
			modelProviderID = value
		}
		if value := configString(latest, "launch_model_override"); value != "" {
			model = value
		}
		if value := configString(latest, "model_override"); value != "" {
			model = value
		}
		if value := configString(latest, "model"); value != "" {
			model = value
		}
		if value := configString(latest, "launch_reasoning_effort_override"); value != "" {
			launchEffort = value
		}
		if value := configString(latest, "requested_reasoning_effort"); value != "" {
			launchEffort = value
		}
	}
	if model == "" && server.modelProviders != nil && modelProviderID != "" {
		if provider, getErr := server.modelProviders.Get(modelProviderID); getErr == nil {
			model = strings.TrimSpace(provider.Catalog.DefaultModel)
		}
	}
	requested, err := runtimeprofile.ResolveRequestedReasoningEffort("", launchEffort, profile.Fields.ReasoningEffort)
	if err != nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, err
	}
	return runtime.ProviderSessionRequest{
		ModelProviderID:          modelProviderID,
		Model:                    model,
		RequestedReasoningEffort: string(requested),
	}, configAt, nil
}

func modelProviderSnapshotFields(raw any) (providerID, model string) {
	switch snapshot := raw.(type) {
	case map[string]any:
		providerID = configString(snapshot, "model_provider_id")
		model = configString(snapshot, "model")
	case modelprovider.Snapshot:
		providerID = strings.TrimSpace(snapshot.ModelProviderID)
		model = strings.TrimSpace(snapshot.Model)
	case *modelprovider.Snapshot:
		if snapshot != nil {
			providerID = strings.TrimSpace(snapshot.ModelProviderID)
			model = strings.TrimSpace(snapshot.Model)
		}
	}
	return providerID, model
}

func (server *Server) precedingConversationTurnSelection(taskID string) (runtime.ProviderSessionRequest, bool) {
	selection, _, ok := server.precedingConversationTurnSelectionTimed(taskID)
	return selection, ok
}

func (server *Server) precedingConversationTurnSelectionTimed(taskID string) (runtime.ProviderSessionRequest, time.Time, bool) {
	events, err := server.tasks.Events(taskID)
	if err != nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, false
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		// Selection is only read from existing conversation / steering turn
		// records — never from a separate selection Task Event.
		if event.Kind != task.EventKindConversation && event.Kind != task.EventKindSteering {
			continue
		}
		providerID, _ := event.Payload["model_provider_id"].(string)
		model, _ := event.Payload["model"].(string)
		if model == "" {
			model, _ = event.Payload["model_override"].(string)
		}
		effort, _ := event.Payload["requested_reasoning_effort"].(string)
		if effort == "" {
			effort, _ = event.Payload["reasoning_effort"].(string)
		}
		if strings.TrimSpace(providerID) == "" && strings.TrimSpace(model) == "" && strings.TrimSpace(effort) == "" {
			continue
		}
		// Prefer a complete conversation turn; skip incomplete steering noise.
		if event.Kind == task.EventKindSteering && strings.TrimSpace(providerID) == "" && strings.TrimSpace(model) == "" {
			continue
		}
		requested, err := runtimeprofile.NormalizeReasoningEffort(effort)
		if err != nil {
			return runtime.ProviderSessionRequest{}, time.Time{}, false
		}
		return runtime.ProviderSessionRequest{
			ModelProviderID:          strings.TrimSpace(providerID),
			Model:                    strings.TrimSpace(model),
			RequestedReasoningEffort: string(requested),
		}, event.CreatedAt, true
	}
	return runtime.ProviderSessionRequest{}, time.Time{}, false
}

func nativeSteerIdempotencyConflict(prior task.Event, message string, selection runtime.ProviderSessionRequest) string {
	if priorText, _ := prior.Payload["text"].(string); priorText != message {
		return "steer request id already belongs to a different message"
	}
	priorProvider, _ := prior.Payload["model_provider_id"].(string)
	priorModel, _ := prior.Payload["model"].(string)
	if priorModel == "" {
		priorModel, _ = prior.Payload["model_override"].(string)
	}
	priorEffort, _ := prior.Payload["requested_reasoning_effort"].(string)
	if priorEffort == "" {
		priorEffort, _ = prior.Payload["reasoning_effort"].(string)
	}
	// Legacy conversation events may omit effort; treat missing as high so a
	// retry that resolves to the default does not false-conflict.
	priorEffortNormalized, _ := runtimeprofile.NormalizeReasoningEffort(priorEffort)
	requestedEffortNormalized, _ := runtimeprofile.NormalizeReasoningEffort(selection.RequestedReasoningEffort)
	if strings.TrimSpace(priorProvider) != strings.TrimSpace(selection.ModelProviderID) ||
		strings.TrimSpace(priorModel) != strings.TrimSpace(selection.Model) ||
		priorEffortNormalized != requestedEffortNormalized {
		return "steer request id already belongs to a different turn selection"
	}
	return ""
}

// nativeSteerFailurePresentation maps provider operation failures to stable,
// redacted public codes/messages. Raw provider text never crosses this seam.
func nativeSteerFailurePresentation(operationErr error) (code, message string) {
	switch {
	case errors.Is(operationErr, context.DeadlineExceeded):
		return "timeout", "native steer timed out"
	case errors.Is(operationErr, context.Canceled):
		return "server_closing", "native steer canceled"
	case errors.Is(operationErr, runtime.ErrProviderSessionClosed):
		return "session_closed", "provider session is closed"
	case errors.Is(operationErr, runtime.ErrProviderSessionControlConflict):
		return "control_conflict", "provider session control conflict"
	}
	raw := ""
	if cause := errors.Unwrap(operationErr); cause != nil {
		raw = cause.Error()
	} else if operationErr != nil {
		raw = operationErr.Error()
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "effort") || strings.Contains(lower, "reasoning") {
		return "unsupported_reasoning_effort", "requested reasoning effort is not supported"
	}
	return "provider_rejected", "provider rejected the turn"
}

func configString(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func (server *Server) resolveTaskContinuationRuntimeProfile(response http.ResponseWriter, found task.Task, input taskContinuationSelectionInput) (runtimeprofile.Profile, bool) {
	currentProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return runtimeprofile.Profile{}, false
	}
	if strings.TrimSpace(currentProfile.ID) == "" {
		// The Task's launch and config Runtime Profiles are both deleted; no
		// continuation can be steered onto a resolvable profile.
		writeError(response, http.StatusBadRequest, "runtime profile not found")
		return runtimeprofile.Profile{}, false
	}
	return server.resolveSelectedRuntimeProfile(response, currentProfile, input)
}

func (server *Server) resolveSelectedRuntimeProfile(response http.ResponseWriter, currentProfile runtimeprofile.Profile, input taskContinuationSelectionInput) (runtimeprofile.Profile, bool) {
	runtimeProfileID := strings.TrimSpace(input.RuntimeProfileID)
	modelProviderID := strings.TrimSpace(input.ModelProviderID)
	if runtimeProfileID != "" && modelProviderID != "" {
		writeError(response, http.StatusBadRequest, "choose runtime_profile_id or model_provider_id, not both")
		return runtimeprofile.Profile{}, false
	}
	if runtimeProfileID != "" {
		requestedProfile, err := server.profiles.Get(runtimeProfileID)
		if err != nil {
			if errors.Is(err, runtimeprofile.ErrNotFound) {
				writeError(response, http.StatusBadRequest, "runtime profile not found")
				return runtimeprofile.Profile{}, false
			}
			writeError(response, http.StatusInternalServerError, "load runtime profile")
			return runtimeprofile.Profile{}, false
		}
		if requestedProfile.Provider != currentProfile.Provider {
			writeError(response, http.StatusBadRequest, "steering runtime profile must keep the same runtime provider")
			return runtimeprofile.Profile{}, false
		}
		return requestedProfile, true
	}
	if modelProviderID == "" {
		return currentProfile, true
	}

	providerName := modelProviderID
	if server.modelProviders != nil {
		provider, err := server.modelProviders.Get(modelProviderID)
		if err != nil {
			if errors.Is(err, modelprovider.ErrNotFound) {
				writeError(response, http.StatusBadRequest, "model provider not found")
				return runtimeprofile.Profile{}, false
			}
			writeError(response, http.StatusInternalServerError, "load model provider")
			return runtimeprofile.Profile{}, false
		}
		providerName = provider.Name
	}

	modelOverride := input.selectedModel()
	requestedEffort := ""
	if effort := strings.TrimSpace(input.ReasoningEffort); effort != "" {
		normalized, err := runtimeprofile.NormalizeReasoningEffort(effort)
		if err != nil {
			writeError(response, http.StatusBadRequest, err.Error())
			return runtimeprofile.Profile{}, false
		}
		requestedEffort = string(normalized)
	}
	currentEffort, _ := runtimeprofile.NormalizeReasoningEffort(currentProfile.Fields.ReasoningEffort)
	sameSelection := strings.TrimSpace(currentProfile.Fields.ModelProviderID) == modelProviderID &&
		strings.TrimSpace(currentProfile.Fields.ModelOverride) == modelOverride &&
		(requestedEffort == "" || string(currentEffort) == requestedEffort)
	if sameSelection {
		return currentProfile, true
	}
	fields := currentProfile.Fields
	fields.ModelProviderID = modelProviderID
	fields.ModelOverride = modelOverride
	if requestedEffort != "" {
		fields.ReasoningEffort = requestedEffort
	}
	fields.ModelProviderProtocol = ""
	fields.Model = ""
	fields.Endpoint = ""
	fields.APIKeys = nil
	name := runtimeprofile.LaunchProfileName(runtimeprofile.LaunchSelection{
		Provider:        currentProfile.Provider,
		ModelProviderID: modelProviderID,
		ModelOverride:   modelOverride,
	}, providerName)
	created, err := server.profiles.CreateLaunchResolved(name, currentProfile.Provider, fields)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return runtimeprofile.Profile{}, false
	}
	return created, true
}

func (server *Server) resolveTaskRuntimeProfile(found task.Task) (runtimeprofile.Profile, error) {
	profileID := found.RuntimeProfileID
	versions, err := server.tasks.RuntimeConfigVersions(found.ID)
	if err != nil {
		return runtimeprofile.Profile{}, err
	}
	if len(versions) > 0 {
		latest := versions[len(versions)-1]
		if strings.TrimSpace(latest.RuntimeProfileID) != "" {
			profileID = latest.RuntimeProfileID
		}
	}
	profile, err := server.profiles.Get(profileID)
	if err == nil {
		return profile, nil
	}
	// A Task's captured Task Runtime Configuration is self-contained, so a
	// deleted Runtime Profile must not make the Task unreadable. Fall back to
	// the Task's own launch Runtime Profile; when that is also gone, resolve
	// to a zero profile so render paths degrade instead of failing the page.
	return server.profileOrFallback(profileID, found.RuntimeProfileID)
}

// profileOrFallback resolves id, then fallbackID when id's Runtime Profile was
// deleted, and finally a zero profile when both are gone. Control paths that
// require a real profile reject the zero profile explicitly.
func (server *Server) profileOrFallback(id, fallbackID string) (runtimeprofile.Profile, error) {
	profile, err := server.profiles.Get(id)
	if err == nil {
		return profile, nil
	}
	if !errors.Is(err, runtimeprofile.ErrNotFound) {
		return runtimeprofile.Profile{}, err
	}
	if id != fallbackID {
		profile, fallbackErr := server.profiles.Get(fallbackID)
		if fallbackErr == nil {
			return profile, nil
		}
		if !errors.Is(fallbackErr, runtimeprofile.ErrNotFound) {
			return runtimeprofile.Profile{}, fallbackErr
		}
	}
	return runtimeprofile.Profile{}, nil
}

func (server *Server) discoverNativeResumeSession(found task.Task) (runtime.NativeSessionMetadata, error) {
	profile, err := server.profiles.Get(found.RuntimeProfileID)
	if err != nil {
		return runtime.NativeSessionMetadata{}, err
	}
	plugin, ok := server.runtimePlugins.Get(string(profile.Provider))
	if !ok || !plugin.NativeResume.Supported {
		return runtime.NativeSessionMetadata{}, fmt.Errorf("%w for provider %s", errNativeResumeUnavailable, profile.Provider)
	}
	latest, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return runtime.NativeSessionMetadata{}, err
	}
	if latest != nil && strings.TrimSpace(latest.NativeSessionID) != "" {
		return runtime.NativeSessionMetadata{
			NativeSessionID: latest.NativeSessionID, NativeSessionPath: latest.NativeSessionPath,
		}, nil
	}
	metadata, err := server.discoverProviderNativeSession(found.ID, profile.Provider)
	if err != nil {
		return runtime.NativeSessionMetadata{}, err
	}
	if strings.TrimSpace(metadata.NativeSessionID) == "" {
		return runtime.NativeSessionMetadata{}, errNativeSessionUnavailable
	}
	return metadata, nil
}

func (server *Server) discoverProviderNativeSession(taskID string, provider runtimeprofile.Provider) (runtime.NativeSessionMetadata, error) {
	if provider != runtimeprofile.ProviderCodex && provider != runtimeprofile.ProviderPi {
		return runtime.NativeSessionMetadata{}, nil
	}
	var layout runner.Layout
	var err error
	if server.blackboardV2Continuity != nil && runner.BlackboardV2SupportsProvider(provider) {
		layout, err = runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, taskID, provider)
	} else {
		layout, err = runner.PrepareTaskLayout(server.runtimeRoot, taskID, provider)
	}
	if err != nil {
		return runtime.NativeSessionMetadata{}, err
	}
	switch provider {
	case runtimeprofile.ProviderCodex:
		return runtime.DiscoverCodexSession(layout.ProviderHome)
	case runtimeprofile.ProviderPi:
		return runtime.DiscoverPiSession(layout.ProviderHome)
	default:
		return runtime.NativeSessionMetadata{}, nil
	}
}

func (server *Server) handleTaskContinuation(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	configVersions, err := server.tasks.RuntimeConfigVersions(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	events, err := server.tasks.Events(taskID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}

	type handoffPayload struct {
		TaskID           string           `json:"task_id"`
		ProjectID        string           `json:"project_id"`
		Goal             string           `json:"goal"`
		RuntimeProfileID string           `json:"runtime_profile_id"`
		Runner           task.Runner      `json:"runner"`
		ScopeDomains     []string         `json:"scope_domains"`
		ScopeNotes       string           `json:"scope_notes"`
		RunControls      task.RunControls `json:"run_controls"`
		EventCount       int              `json:"event_count"`
		ConfigVersions   int              `json:"config_versions"`
	}

	writeJSON(response, http.StatusOK, struct {
		Mode    string         `json:"mode"`
		Handoff handoffPayload `json:"handoff"`
	}{
		Mode: "mechanical_handoff",
		Handoff: handoffPayload{
			TaskID:           found.ID,
			ProjectID:        found.ProjectID,
			Goal:             found.Goal,
			RuntimeProfileID: found.RuntimeProfileID,
			Runner:           found.Runner,
			ScopeDomains:     found.ScopeSnapshot.Domains,
			ScopeNotes:       found.ScopeSnapshot.Notes,
			RunControls:      found.RunControls,
			EventCount:       len(events),
			ConfigVersions:   len(configVersions),
		},
	})
}

// requireProject centralizes the project-exists check that every project-scoped
// task route must perform before doing any work: it returns false (and writes the
// response) when the project is unknown or unreadable, matching the check the
// blackboard / credential / dashboard routes already apply.
func (server *Server) requireProject(response http.ResponseWriter, projectID string) bool {
	if _, err := server.projects.Get(projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
		} else {
			writeError(response, http.StatusInternalServerError, "load project")
		}
		return false
	}
	return true
}

func writeTaskError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrMissingGoal), errors.Is(err, task.ErrUnsupportedRunner), errors.Is(err, task.ErrInvalidTaskType), errors.Is(err, task.ErrInvalidBlackboardConclusionMode), errors.Is(err, task.ErrInvalidTaskPolicy), errors.Is(err, task.ErrSandboxVPNTunHostProxyConflict):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, task.ErrTaskTypeProjectKindMismatch):
		writeError(response, http.StatusConflict, err.Error())
	case errors.Is(err, task.ErrProjectNotFound), errors.Is(err, task.ErrNotFound), errors.Is(err, project.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, task.ErrActiveTask):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "task operation failed")
	}
}

func writeTaskAdapterError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAssistedConclusionUnsupported):
		writeError(response, http.StatusBadRequest, errAssistedConclusionUnsupported.Error())
	case errors.Is(err, skill.ErrInvalidSkill),
		errors.Is(err, modelprovider.ErrMissingAPIKeyEnv),
		errors.Is(err, modelprovider.ErrMissingProvider),
		errors.Is(err, modelprovider.ErrMissingModel),
		errors.Is(err, modelprovider.ErrIncompatibleProtocol):
		writeError(response, http.StatusBadRequest, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, err.Error())
	}
}

func writeTaskLaunchError(response http.ResponseWriter, err error) {
	if errors.Is(err, task.ErrActiveContinuation) || errors.Is(err, task.ErrContinuationReconciliationIncomplete) || errors.Is(err, task.ErrSteeringSelectionConflict) {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	var interfaceErr *projectinterface.Error
	if errors.As(err, &interfaceErr) {
		status := http.StatusInternalServerError
		if interfaceErr.Code == projectinterface.ErrCodeSnapshotUnavailable {
			status = http.StatusServiceUnavailable
		}
		writeJSON(response, status, struct {
			Error *projectinterface.Error `json:"error"`
		}{Error: interfaceErr})
		return
	}
	// Skill/model-provider adapter failures can surface during v2 Continuation
	// Precommit after plan capture; map them like resume-prepare errors.
	writeTaskAdapterError(response, err)
}

// eventsToTimelineEvents projects retained owner events into the shared
// timeline input shape so task and session timelines run the same builder.
func eventsToTimelineEvents(events []task.Event) []timeline.Event {
	converted := make([]timeline.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, timeline.Event{
			ID:        event.ID,
			Seq:       event.Seq,
			Kind:      string(event.Kind),
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	return converted
}

// eventsToTranscriptEvents projects retained task events into the shared
// transcript input shape so task and session transcripts run the same builder.
func eventsToTranscriptEvents(events []task.Event) []transcript.Event {
	converted := make([]transcript.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, transcript.Event{
			ID:        event.ID,
			Seq:       event.Seq,
			Kind:      string(event.Kind),
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	return converted
}
