package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

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
	disabled := found.RunControls.BlackboardMode == task.BlackboardModeDisabled
	var next task.TaskContinuation
	if disabled {
		next, err = server.tasks.CreateReplacementContinuationWithoutBlackboard(*previous)
	} else {
		next, err = server.tasks.CreateReplacementContinuation(*previous)
	}
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
	if !disabled {
		if server.blackboardV2Continuity == nil {
			return fail(fmt.Errorf("Blackboard continuation rebind is unavailable"))
		}
		if err := server.blackboardV2Continuity.RebindContinuationForNativeSteer(context.Background(), previous.ID, next.ID); err != nil {
			return fail(fmt.Errorf("rebind Blackboard continuation grant: %w", err))
		}
	}
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
	versions, err := server.tasks.RuntimeConfigVersions(found.ID)
	if err != nil || len(versions) == 0 {
		writeError(response, http.StatusInternalServerError, "runtime configuration snapshot is missing")
		return task.RuntimeConfigVersion{}, false
	}
	prior, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return task.RuntimeConfigVersion{}, false
	}
	turn := runtimeconfig.RuntimeTurnSelection{
		ModelProviderID: requestedProfile.Fields.ModelProviderID,
		Model:           firstNonBlank(input.selectedModel(), requestedProfile.Fields.ModelOverride, requestedProfile.Fields.Model),
		ReasoningEffort: firstNonBlank(input.ReasoningEffort, requestedProfile.Fields.ReasoningEffort),
	}
	cloned, err := server.cloneSnapshotForTurn(prior, found.ProjectID, turn)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return task.RuntimeConfigVersion{}, false
	}
	provenance := ""
	if cloned.RuntimeProfile != nil {
		provenance = cloned.RuntimeProfile.ID
	}
	recorded, err := server.tasks.RecordRuntimeConfig(found.ID, provenance, runtimeSnapshotMap(cloned))
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
	latestVersion, err := server.tasks.LatestRuntimeConfig(found.ID)
	if err != nil || latestVersion == nil {
		return nil
	}
	snapshot, err := decodeRuntimeSnapshot(latestVersion.Config)
	if err != nil {
		return nil
	}
	return stringSliceFromConfig(snapshot.ConfigProjection["projected_model_provider_ids"])
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

func (server *Server) selectionFromCapturedRuntimeConfig(found task.Task) (runtime.ProviderSessionRequest, error) {
	selection, _, err := server.selectionFromCapturedRuntimeConfigTimed(found)
	return selection, err
}

func (server *Server) selectionFromCapturedRuntimeConfigTimed(found task.Task) (runtime.ProviderSessionRequest, time.Time, error) {
	latestVersion, err := server.tasks.LatestRuntimeConfig(found.ID)
	if err != nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, err
	}
	if latestVersion == nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, errors.New("runtime configuration snapshot is missing")
	}
	snapshot, err := decodeRuntimeSnapshot(latestVersion.Config)
	if err != nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, err
	}
	requested, err := runtimeprofile.ResolveRequestedReasoningEffort("", snapshot.TurnSelection.ReasoningEffort, "")
	if err != nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, err
	}
	return runtime.ProviderSessionRequest{
		ModelProviderID:          snapshot.TurnSelection.ModelProviderID,
		Model:                    snapshot.TurnSelection.Model,
		RequestedReasoningEffort: string(requested),
	}, latestVersion.CreatedAt, nil
}

func (server *Server) precedingConversationTurnSelectionTimed(taskID string) (runtime.ProviderSessionRequest, time.Time, bool) {
	latest, err := server.tasks.LatestTurnSelectionEvent(taskID)
	if err != nil || latest == nil {
		return runtime.ProviderSessionRequest{}, time.Time{}, false
	}
	for _, event := range []task.Event{*latest} {
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
	return server.resolveSelectedRuntimeProfile(response, currentProfile, input)
}
