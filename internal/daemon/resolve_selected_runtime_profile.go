package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/task"
	"pentest/internal/timeline"
)

func (server *Server) resolveSelectedRuntimeProfile(response http.ResponseWriter, currentProfile runtimeprofile.Profile, input taskContinuationSelectionInput) (runtimeprofile.Profile, bool) {
	runtimeProfileID := strings.TrimSpace(input.RuntimeProfileID)
	modelProviderID := strings.TrimSpace(input.ModelProviderID)
	if runtimeProfileID != "" && modelProviderID != "" {
		writeError(response, http.StatusBadRequest, "choose runtime_profile_id or model_provider_id, not both")
		return runtimeprofile.Profile{}, false
	}
	if runtimeProfileID != "" {
		writeError(response, http.StatusBadRequest, "runtime_profile_locked")
		return runtimeprofile.Profile{}, false
	}
	if modelProviderID == "" {
		return currentProfile, true
	}

	if server.modelProviders != nil {
		_, err := server.modelProviders.Get(modelProviderID)
		if err != nil {
			if errors.Is(err, modelprovider.ErrNotFound) {
				writeError(response, http.StatusBadRequest, "model provider not found")
				return runtimeprofile.Profile{}, false
			}
			writeError(response, http.StatusInternalServerError, "load model provider")
			return runtimeprofile.Profile{}, false
		}
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
	currentProfile.ID = ""
	currentProfile.Name = ""
	currentProfile.Fields = fields
	return currentProfile, true
}

func (server *Server) resolveTaskRuntimeProfile(found task.Task) (runtimeprofile.Profile, error) {
	latestVersion, err := server.tasks.LatestRuntimeConfig(found.ID)
	if err != nil {
		return runtimeprofile.Profile{}, err
	}
	if latestVersion == nil {
		return runtimeprofile.Profile{}, errors.New("runtime configuration snapshot is missing")
	}
	return runtimeProfileFromSnapshot(latestVersion.Config)
}

func (server *Server) discoverNativeResumeSession(found task.Task) (runtime.NativeSessionMetadata, error) {
	profile, err := server.resolveTaskRuntimeProfile(found)
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
	case errors.Is(err, task.ErrMissingGoal), errors.Is(err, task.ErrUnsupportedRunner), errors.Is(err, task.ErrInvalidTaskType), errors.Is(err, task.ErrInvalidBlackboardMode), errors.Is(err, task.ErrInvalidTaskPolicy), errors.Is(err, task.ErrSandboxVPNTunHostProxyConflict), errors.Is(err, task.ErrInvalidContainerCLI):
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
	case errors.Is(err, skill.ErrInvalidSkill),
		errors.Is(err, runtimeprofile.ErrUnknownProvider),
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
