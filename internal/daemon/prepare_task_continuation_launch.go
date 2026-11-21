package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/adapters"
	"pentest/internal/blackboardv2"

	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/workinggraph"
)

func (server *Server) prepareTaskContinuationLaunch(created task.Task, plan taskLaunchPlan, goal string) (task.TaskContinuation, taskLaunchPlan, error) {
	if plan.BlackboardProjection != runner.BlackboardProjectionOmitted {
		return server.prepareBlackboardV2ContinuationLaunch(created, plan, goal)
	}
	_, continuation, err := server.tasks.CreateContinuationLaunchWithoutBlackboard(context.Background(), task.ContinuationLaunchRequest{
		ProjectID: created.ProjectID, TaskID: created.ID, RuntimeProfileID: created.RuntimeProfileID,
		RuntimeProvider: string(plan.ResolvedProfile.Provider), Runner: created.Runner,
		RuntimeConfig: plan.CapturedRuntimeConfig, SteeringEventIDs: plan.BlackboardV2SteeringEventIDs,
		NativeSessionID: plan.NativeResumeSessionID, NativeSessionPath: plan.NativeResumeSessionPath,
	})
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	return continuation, plan, nil
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
		profile, err := server.resolveTaskRuntimeProfile(created)
		if err != nil {
			return task.TaskContinuation{}, taskLaunchPlan{}, err
		}
		provider = profile.Provider
		plan.ResolvedProfile = profile
	}
	if !runner.BlackboardV2SupportsProvider(provider) {
		return task.TaskContinuation{}, taskLaunchPlan{}, fmt.Errorf("Blackboard v2 launch projection is unsupported for provider %q", provider)
	}
	if plan.PreparedProjection == nil {
		prepared, err := server.prepareTaskConfigProjection(created, plan.ResolvedProfile, *plan.ValidatedLayout,
			plan.LaunchModelOverride, plan.LaunchReasoningEffort, plan.BlackboardProjection, true)
		if err != nil {
			return task.TaskContinuation{}, taskLaunchPlan{}, err
		}
		plan.PreparedProjection = prepared
	}
	layout, err := runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, created.ID, provider)
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	plan.ValidatedLayout = &layout
	var boundPlan taskLaunchPlan
	var launchHeader blackboardv2.LaunchHeader
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: created.ProjectID, TaskID: created.ID, RuntimeProfileID: created.RuntimeProfileID,
		RuntimeProvider: string(provider), Runner: created.Runner, RuntimeConfig: plan.CapturedRuntimeConfig,
		GrantAccess:      blackboardGrantAccessForMode(created.RunControls.BlackboardMode),
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
		BindGrant: func(plaintextGrant string, continuation task.TaskContinuation) error {
			if strings.TrimSpace(plaintextGrant) == "" {
				return nil
			}
			binding := &continuationLaunchBinding{V2Header: &launchHeader, InterfaceToken: plaintextGrant, ContinuationID: continuation.ID}
			if created.BlackboardProtocol == "fgs" && created.RunControls.BlackboardMode != task.BlackboardModeDisabled {
				projection, prepareErr := workinggraph.NewService().Prepare(context.Background(), workinggraph.OwnerContext{
					Owner: created.OwnerContract(layout.Workdir), ContinuationID: continuation.ID, Workdir: layout.Workdir,
				})
				if prepareErr != nil {
					return prepareErr
				}
				binding.WorkingGraph = &projection
			}
			var err error
			boundPlan, err = server.buildTaskLaunchPlanWithBinding(created, goal, plan.LaunchModelOverride, plan.NativeResumeSessionID, plan.LaunchReasoningEffort, binding, &plan)
			if err != nil {
				return err
			}
			// BindGrant runs after the config version is stored; still keep the
			// in-memory plan consistent for the returned boundPlan path.
			mergeFixedAtLaunchRuntimeConfig(plan.CapturedRuntimeConfig, boundPlan.CapturedRuntimeConfig)
			return nil
		},
	})
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	return launch.Continuation, boundPlan, nil
}

func blackboardGrantAccessForMode(mode task.BlackboardMode) projectinterface.GrantAccess {
	if mode == task.BlackboardModeWorkingGraph {
		return projectinterface.GrantAccessReadOnly
	}
	return projectinterface.GrantAccessFull
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
		projection, _ := dest["config_projection"].(map[string]any)
		if projection == nil {
			projection = map[string]any{}
			dest["config_projection"] = projection
		}
		projection["projected_model_provider_ids"] = ids
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
		if created.BlackboardProtocol == "fgs" {
			if err := runner.ProjectFGSFiles(layout, runner.RuntimeOwnerContext{
				Owner: created.OwnerContract(layout.Workdir), BlackboardProtocol: created.BlackboardProtocol,
				ScopeSnapshot: created.ScopeSnapshot, Provider: provider, Sandbox: snapshot.Runner == task.RunnerSandbox,
			}); err != nil {
				return fmt.Errorf("recover FGS context: %w", err)
			}
			continue
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
	if resolved.runner == "" {
		resolved.runner = task.Runner(found.Defaults.Runner)
	}
	if resolved.runner == "" {
		resolved.runner = task.RunnerSandbox
	}
	return resolved, nil
}

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

func (server *Server) buildTaskLaunchPlan(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, launchReasoningEffort string) (taskLaunchPlan, error) {
	launch := resolveOwnerBlackboardRuntimeLaunch(
		goal, created.RunControls.BlackboardMode == task.BlackboardModeDisabled,
	)
	return server.buildTaskLaunchPlanForBlackboardProjection(
		created, launch.goal, launchModelOverride, nativeResumeSessionID, launchReasoningEffort, launch.projection,
	)
}
