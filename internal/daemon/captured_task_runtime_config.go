package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func (server *Server) capturedTaskRuntimeConfig(created task.Task, profile runtimeprofile.Profile, modelSnapshot any, launchModelOverride string, launchReasoningEffort string) (map[string]any, error) {
	versions, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil || len(versions) == 0 {
		return nil, errors.New("runtime configuration snapshot is missing")
	}
	prior, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return nil, err
	}
	provider := prior.ModelProvider
	if modelSnapshot != nil {
		encoded, marshalErr := json.Marshal(modelSnapshot)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if err := json.Unmarshal(encoded, &provider); err != nil {
			return nil, err
		}
	}
	turn := runtimeconfig.RuntimeTurnSelection{
		ModelProviderID: firstNonBlank(provider.ModelProviderID, profile.Fields.ModelProviderID, prior.TurnSelection.ModelProviderID),
		Model:           firstNonBlank(launchModelOverride, provider.Model, profile.Fields.ModelOverride, prior.TurnSelection.Model),
		ReasoningEffort: firstNonBlank(launchReasoningEffort, profile.Fields.ReasoningEffort, prior.TurnSelection.ReasoningEffort),
	}
	requested, err := runtimeprofile.ResolveRequestedReasoningEffort("", turn.ReasoningEffort, "")
	if err != nil {
		return nil, err
	}
	turn.ReasoningEffort = string(requested)
	cloned, err := runtimeconfig.CloneForTurn(prior, turn, provider)
	if err != nil {
		return nil, err
	}
	cloned.Runner = string(created.Runner)
	return runtimeSnapshotMap(cloned), nil
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
	tasks = server.attachRuntimeActivities(tasks)
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

const workspaceNavigationTaskLimit = 5

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
		summaries = append(summaries, workspaceNavigationProject{
			Project:        current,
			LastActivityAt: workspaceProjectActivity(current, inlined),
			Tasks:          inlined,
		})
	}
	var allTasks []task.Task
	for _, summary := range summaries {
		allTasks = append(allTasks, summary.Tasks...)
	}
	allTasks = server.attachRuntimeActivities(allTasks)
	offset := 0
	for index := range summaries {
		count := len(summaries[index].Tasks)
		copy(summaries[index].Tasks, allTasks[offset:offset+count])
		offset += count
		summaries[index].LastActivityAt = workspaceProjectActivity(summaries[index].Project, summaries[index].Tasks)
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
	found.ChallengeHistoryAvailable, err = server.challengeWorkflow.HasHistory(context.Background(), found.ID)
	if err != nil {
		return task.Task{}, err
	}
	if version, versionErr := server.tasks.LatestRuntimeConfig(found.ID); versionErr == nil && version != nil {
		if snapshot, snapshotErr := decodeRuntimeSnapshot(version.Config); snapshotErr == nil {
			summary := runtimeconfig.Summarize(snapshot)
			found.RuntimeConfiguration = &summary
		}
	}
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
