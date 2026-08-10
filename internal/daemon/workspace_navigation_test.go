package daemon_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"pentest/internal/daemon"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// #193: the Workspace Sidebar loads one bounded navigation projection instead
// of one Task-list request per Project. These tests pin the
// GET /api/workspace/navigation contract: every Project is returned, each with
// a bounded set of recent/busy Tasks inlined and runtime_activity attached the
// same way as the per-Project Task list, plus Project.last_activity_at.
//
// #201: the projection bounds query work and response size independently of
// total Task history, keeps the selected Task and every live busy Task visible
// outside the recent summary, and answers an unchanged refresh with an empty
// result instead of reserializing the projection.

func getWorkspaceNavigation(t *testing.T, server *daemon.Server) []map[string]any {
	t.Helper()
	_, _, projects, _ := getWorkspaceNavigationResponse(t, server, "")
	return projects
}

// getWorkspaceNavigationResponse decodes the full navigation response body
// including the opaque revision, the changed flag, and the raw serialized
// body so tests can assert refresh transfer size.
func getWorkspaceNavigationResponse(t *testing.T, server *daemon.Server, query string) (revision string, changed bool, projects []map[string]any, rawBody string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/workspace/navigation"+query, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/workspace/navigation status=%d body=%s", response.Code, response.Body.String())
	}
	rawBody = response.Body.String()
	var body struct {
		Revision string           `json:"revision"`
		Changed  bool             `json:"changed"`
		Projects []map[string]any `json:"projects"`
	}
	if err := json.NewDecoder(strings.NewReader(rawBody)).Decode(&body); err != nil {
		t.Fatalf("decode workspace navigation: %v", err)
	}
	return body.Revision, body.Changed, body.Projects, rawBody
}

// navigationTaskIDs returns the inlined Task ids of one navigation summary in
// response order.
func navigationTaskIDs(summary map[string]any) []string {
	tasks, _ := summary["tasks"].([]any)
	ids := make([]string, 0, len(tasks))
	for _, entry := range tasks {
		if taskMap, ok := entry.(map[string]any); ok {
			if id, ok := taskMap["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// bindLiveIdleRuntime attaches a fake provider session that reports live/idle
// to the given Task, so decoration comparisons are deterministic instead of
// racing the in-process fake runner's completion timing.
func bindLiveIdleRuntime(t *testing.T, server *daemon.Server, taskID string) {
	t.Helper()
	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "idle-runtime-" + taskID,
		ActiveTurnID: "idle-turn-" + taskID,
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true},
	})
	if err := server.BindProviderSession(taskID, provider); err != nil {
		t.Fatalf("bind idle runtime to Task %s: %v", taskID, err)
	}
}

// bindBusyRuntime attaches a fake provider session that reports live/busy to
// the given Task and keeps it busy until test cleanup. It mirrors the Session
// navigation busy fixture so the projection's busy-promotion rule is exercised
// through the real registry, not a mock.
func bindBusyRuntime(t *testing.T, server *daemon.Server, taskID string) {
	t.Helper()
	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "busy-runtime-" + taskID,
		ActiveTurnID: "busy-turn-" + taskID,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true,
			InterruptTurn:     true,
		},
		ManualAcknowledge: true,
	})
	if err := server.BindProviderSession(taskID, provider); err != nil {
		t.Fatalf("bind busy runtime to Task %s: %v", taskID, err)
	}
	turnDone := make(chan error, 1)
	go func() {
		_, sendErr := provider.InterruptTurn(context.Background(), runtime.ProviderSessionRequest{
			RequestID: "busy-turn-" + taskID,
			Message:   "keep working",
		}, func(task.EventKind, task.EventPayload) {})
		turnDone <- sendErr
	}()
	deadline := time.Now().Add(time.Second)
	for !provider.ControlBusy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !provider.ControlBusy() {
		t.Fatal("provider Session did not become busy")
	}
	t.Cleanup(func() {
		_ = provider.Acknowledge("busy-turn-" + taskID)
		<-turnDone
	})
}

// navigationProjectWithTasks creates a Project with the given number of Tasks
// and returns the Project id plus the created Task ids.
func navigationProjectWithTasks(t *testing.T, server *daemon.Server, profileID, name string, count int) (string, []string) {
	t.Helper()
	projectID := createProject(t, server, `{"name":"`+name+`","scope":{"domains":["example.com"]}}`)
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		created := launchTaskForProject(t, server, profileID, projectID, "task")
		ids = append(ids, created["id"].(string))
	}
	return projectID, ids
}

// navigationFixture builds a daemon with a fake Runtime Profile and returns it
// plus the profile id Tasks must reference to pass preflight.
func navigationFixture(t *testing.T) (*daemon.Server, string) {
	t.Helper()
	server := newDaemon(t)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	return server, profileID
}

// launchTaskForProject creates a Task through the public API and returns its
// decoded body so callers can read its id and timestamps.
func launchTaskForProject(t *testing.T, server *daemon.Server, profileID, projectID, goal string) map[string]any {
	t.Helper()
	resp := httptest.NewRecorder()
	body := `{"type":"pentest","goal":"` + goal + `","runtime_profile_id":` + quoteJSON(profileID) + `,"runner":"sandbox"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create task status=%d body=%s", resp.Code, resp.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return created
}

// waitForTaskTerminal polls the Task until it reaches a status that allows
// deletion. The fake runner completes asynchronously, so callers that need to
// delete a Task must wait for it to leave the active states first.
func waitForTaskTerminal(t *testing.T, server *daemon.Server, projectID, taskID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("get task status=%d body=%s", resp.Code, resp.Body.String())
		}
		var current struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&current); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		switch current.Status {
		case "pending", "running", "paused":
			time.Sleep(20 * time.Millisecond)
			continue
		default:
			return
		}
	}
	t.Fatalf("task %s did not reach a terminal status", taskID)
}

func TestWorkspaceNavigationReturnsEveryProjectWithBoundedTasks(t *testing.T) {
	server, profileID := navigationFixture(t)
	projectID := createProject(t, server, `{"name":"Bounded","scope":{"domains":["example.com"]}}`)
	// Seven Tasks; the projection must bound the inlined set per Project.
	for i := 0; i < 7; i++ {
		launchTaskForProject(t, server, profileID, projectID, "task")
	}

	projects := getWorkspaceNavigation(t, server)
	if len(projects) != 1 {
		t.Fatalf("navigation projects = %d, want 1", len(projects))
	}
	tasks, _ := projects[0]["tasks"].([]any)
	if len(tasks) != 5 {
		t.Fatalf("inlined tasks = %d, want bounded at 5", len(tasks))
	}
}

func TestWorkspaceNavigationExcludesSoftDeletedTasks(t *testing.T) {
	server, profileID := navigationFixture(t)
	projectID := createProject(t, server, `{"name":"Deleted","scope":{"domains":["example.com"]}}`)
	kept := launchTaskForProject(t, server, profileID, projectID, "kept")
	deleted := launchTaskForProject(t, server, profileID, projectID, "deleted")
	// Deletion requires a terminal Task; the fake runner completes async, so
	// wait for both Tasks to reach a terminal status before deleting one.
	waitForTaskTerminal(t, server, projectID, kept["id"].(string))
	waitForTaskTerminal(t, server, projectID, deleted["id"].(string))

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/projects/"+projectID+"/tasks/"+deleted["id"].(string), nil)
	deleteResp := httptest.NewRecorder()
	server.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusNoContent && deleteResp.Code != http.StatusOK {
		t.Fatalf("delete task status=%d body=%s", deleteResp.Code, deleteResp.Body.String())
	}

	projects := getWorkspaceNavigation(t, server)
	tasks, _ := projects[0]["tasks"].([]any)
	ids := make([]string, 0, len(tasks))
	for _, entry := range tasks {
		if taskMap, ok := entry.(map[string]any); ok {
			if id, ok := taskMap["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	for _, id := range ids {
		if id == deleted["id"] {
			t.Fatalf("soft-deleted task %s appeared in navigation", id)
		}
	}
	foundKept := false
	for _, id := range ids {
		if id == kept["id"] {
			foundKept = true
		}
	}
	if !foundKept {
		t.Fatalf("kept task missing from navigation, ids=%v", ids)
	}
}

func TestWorkspaceNavigationMatchesPerProjectListDecoration(t *testing.T) {
	// Runtime Activity is computed live and must NOT be derived from durable
	// Task status. The projection must decorate each inlined Task through the
	// same path as GET /api/projects/{id}/tasks, so the two responses agree on
	// runtime_activity for the same Task.
	server, profileID := navigationFixture(t)
	projectID := createProject(t, server, `{"name":"Decoration","scope":{"domains":["example.com"]}}`)
	created := launchTaskForProject(t, server, profileID, projectID, "decoration task")
	taskID, _ := created["id"].(string)
	// Let the fake runner reach its terminal state first so its teardown cannot
	// release the binding mid-comparison, then bind a live idle provider
	// session so both responses deterministically compute live/idle instead of
	// racing the in-process runner's completion timing.
	waitForTaskTerminal(t, server, projectID, taskID)
	bindLiveIdleRuntime(t, server, taskID)

	projects := getWorkspaceNavigation(t, server)
	if len(projects) != 1 {
		t.Fatalf("navigation projects = %d, want 1", len(projects))
	}
	tasks, _ := projects[0]["tasks"].([]any)
	var navTask map[string]any
	for _, entry := range tasks {
		if taskMap, ok := entry.(map[string]any); ok && taskMap["id"] == taskID {
			navTask = taskMap
		}
	}
	if navTask == nil {
		t.Fatalf("task %s missing from navigation tasks", taskID)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks", nil)
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list tasks status=%d body=%s", listResp.Code, listResp.Body.String())
	}
	var listBody struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	var listed map[string]any
	for _, entry := range listBody.Tasks {
		if entry["id"] == taskID {
			listed = entry
		}
	}
	if listed == nil {
		t.Fatalf("task %s missing from per-Project list", taskID)
	}

	// runtime_activity must come from the same live decoration, not be derived
	// from status. Equal values prove the projection did not shortcut it.
	if !reflect.DeepEqual(navTask["runtime_activity"], listed["runtime_activity"]) {
		t.Fatalf("runtime_activity diverged: navigation=%#v list=%#v", navTask["runtime_activity"], listed["runtime_activity"])
	}
}

func TestWorkspaceNavigationComputesProjectLastActivityFromTasks(t *testing.T) {
	server, profileID := navigationFixture(t)
	projectID := createProject(t, server, `{"name":"Activity","scope":{"domains":["example.com"]}}`)
	created := launchTaskForProject(t, server, profileID, projectID, "recent task")

	projects := getWorkspaceNavigation(t, server)
	if len(projects) != 1 {
		t.Fatalf("navigation projects = %d, want 1", len(projects))
	}
	lastActivity, ok := projects[0]["last_activity_at"].(string)
	if !ok || lastActivity == "" {
		t.Fatalf("navigation project missing last_activity_at: %#v", projects[0]["last_activity_at"])
	}
	// last_activity_at must be at least as recent as the Task's updated_at so
	// the Sidebar can order Projects by activity with a single call.
	taskUpdated, _ := created["updated_at"].(string)
	if taskUpdated != "" && lastActivity < taskUpdated {
		t.Fatalf("last_activity_at %q predates task updated_at %q", lastActivity, taskUpdated)
	}
}

func TestWorkspaceNavigationScalesToOneRequestForManyProjects(t *testing.T) {
	server, profileID := navigationFixture(t)
	// 123 Projects with mixed Task states must be served by a single call, each
	// carrying a bounded Task set.
	type launchedTask struct {
		projectID string
		taskID    string
	}
	launched := make([]launchedTask, 0, 246)
	for i := 0; i < 123; i++ {
		projectID := createProject(t, server, `{"name":"P","scope":{"domains":["example.com"]}}`)
		for j := 0; j <= i%3; j++ {
			created := launchTaskForProject(t, server, profileID, projectID, "task")
			launched = append(launched, launchedTask{
				projectID: projectID,
				taskID:    created["id"].(string),
			})
		}
	}
	// The fake runner completes asynchronously. Wait for the durable Task
	// states to become stable before testing an unchanged conditional refresh;
	// a running-to-completed transition must correctly change the revision.
	for _, current := range launched {
		waitForTaskTerminal(t, server, current.projectID, current.taskID)
	}

	revision, changed, projects, fullBody := getWorkspaceNavigationResponse(t, server, "")
	if !changed {
		t.Fatalf("initial navigation changed=false")
	}
	if len(projects) != 123 {
		t.Fatalf("navigation projects = %d, want 123", len(projects))
	}
	for index, summary := range projects {
		tasks, _ := summary["tasks"].([]any)
		if len(tasks) > 5 {
			t.Fatalf("project %d inlined %d tasks, want <= 5", index, len(tasks))
		}
	}

	// #201: an unchanged refresh at 123-Project scale must not reserialize the
	// projection. The response carries the same revision, an empty project
	// list, and a transfer far smaller than the full projection.
	_, changed, projects, unchangedBody := getWorkspaceNavigationResponse(t, server, "?revision="+revision)
	if changed {
		t.Fatalf("unchanged refresh reported changed=true")
	}
	if len(projects) != 0 {
		t.Fatalf("unchanged refresh returned %d projects, want 0", len(projects))
	}
	if len(unchangedBody) >= len(fullBody)/10 || len(unchangedBody) > 1024 {
		t.Fatalf("unchanged refresh transfer too large: unchanged=%d bytes full=%d bytes", len(unchangedBody), len(fullBody))
	}
}

func TestWorkspaceNavigationIncludesBusyTaskOutsideRecentSummary(t *testing.T) {
	server, profileID := navigationFixture(t)
	projectID, created := navigationProjectWithTasks(t, server, profileID, "Busy", 7)
	for _, id := range created {
		waitForTaskTerminal(t, server, projectID, id)
	}

	_, _, projects, _ := getWorkspaceNavigationResponse(t, server, "")
	ordinary := navigationTaskIDs(projects[0])
	if len(ordinary) != 5 {
		t.Fatalf("ordinary inlined tasks = %d, want 5", len(ordinary))
	}

	// Pick a Task the recency summary omits: it must stay visible once its
	// Runtime is live and busy.
	var outside string
	for _, id := range created {
		if !slices.Contains(ordinary, id) {
			outside = id
			break
		}
	}
	if outside == "" {
		t.Fatalf("expected a Task outside the recent summary, created=%v ordinary=%v", created, ordinary)
	}

	bindBusyRuntime(t, server, outside)

	_, changed, projects, _ := getWorkspaceNavigationResponse(t, server, "")
	if !changed {
		t.Fatalf("busy flip reported changed=false")
	}
	ids := navigationTaskIDs(projects[0])
	if len(ids) != 6 {
		t.Fatalf("inlined tasks = %d, want 5 ordinary + 1 busy: %v", len(ids), ids)
	}
	if ids[0] != outside {
		t.Fatalf("busy Task %q not promoted first: %v", outside, ids)
	}
	// The ordinary five remain exactly the same set and order (stability).
	if !reflect.DeepEqual(ids[1:], ordinary) {
		t.Fatalf("ordinary summary changed after busy flip: before=%v after=%v", ordinary, ids[1:])
	}

	// A busy Task that is also the selected Task is inlined once, not twice.
	_, _, projects, _ = getWorkspaceNavigationResponse(t, server, "?selected_task="+outside)
	ids = navigationTaskIDs(projects[0])
	if len(ids) != 6 {
		t.Fatalf("busy+selected Task duplicated: %v", ids)
	}
	if ids[0] != outside {
		t.Fatalf("busy selected Task not promoted first: %v", ids)
	}

	// Busy Tasks appear with a live/busy runtime_activity, not a status guess.
	for _, entry := range projects[0]["tasks"].([]any) {
		taskMap := entry.(map[string]any)
		if taskMap["id"] != outside {
			continue
		}
		activity, _ := taskMap["runtime_activity"].(map[string]any)
		if activity == nil || activity["liveness"] != "live" || activity["turn_activity"] != "busy" {
			t.Fatalf("busy Task runtime_activity = %#v, want live/busy", activity)
		}
	}
}

func TestWorkspaceNavigationIncludesSelectedTaskOutsideRecentSummary(t *testing.T) {
	server, profileID := navigationFixture(t)
	projectID, created := navigationProjectWithTasks(t, server, profileID, "Selected", 7)
	for _, id := range created {
		waitForTaskTerminal(t, server, projectID, id)
	}

	_, _, projects, _ := getWorkspaceNavigationResponse(t, server, "")
	ordinary := navigationTaskIDs(projects[0])
	outside := make([]string, 0, 2)
	for _, id := range created {
		if !slices.Contains(ordinary, id) {
			outside = append(outside, id)
		}
	}
	if len(outside) != 2 {
		t.Fatalf("expected two Tasks outside the recent summary, created=%v ordinary=%v", created, ordinary)
	}

	// The selected Task is accepted as bounded request context and appended
	// after the ordinary summary; the ordinary five never reorder.
	revision, changed, projects, _ := getWorkspaceNavigationResponse(t, server, "?selected_task="+outside[0])
	if !changed {
		t.Fatalf("selection reported changed=false")
	}
	ids := navigationTaskIDs(projects[0])
	if len(ids) != 6 {
		t.Fatalf("inlined tasks = %d, want 5 ordinary + 1 selected: %v", len(ids), ids)
	}
	if ids[5] != outside[0] {
		t.Fatalf("selected Task not appended last: %v", ids)
	}
	if !reflect.DeepEqual(ids[:5], ordinary) {
		t.Fatalf("ordinary summary changed by selection: before=%v after=%v", ordinary, ids[:5])
	}

	// A different selection swaps only the appended entry.
	_, changed, projects, _ = getWorkspaceNavigationResponse(t, server, "?selected_task="+outside[1])
	if !changed {
		t.Fatalf("selection change reported changed=false")
	}
	ids = navigationTaskIDs(projects[0])
	if ids[5] != outside[1] {
		t.Fatalf("second selection not appended last: %v", ids)
	}
	if !reflect.DeepEqual(ids[:5], ordinary) {
		t.Fatalf("ordinary summary changed by second selection: %v", ids[:5])
	}

	// The revision includes the selection, so a refresh that was current
	// without the selection is never answered unchanged for the new context.
	_, changed, _, _ = getWorkspaceNavigationResponse(t, server, "?revision="+revision+"&selected_task="+outside[1])
	if !changed {
		t.Fatalf("selection change under the old revision reported changed=false")
	}
}

func TestWorkspaceNavigationSelectedTaskWithinRecentSummary(t *testing.T) {
	server, profileID := navigationFixture(t)
	projectID, created := navigationProjectWithTasks(t, server, profileID, "SelectedRecent", 7)
	for _, id := range created {
		waitForTaskTerminal(t, server, projectID, id)
	}

	_, _, projects, _ := getWorkspaceNavigationResponse(t, server, "")
	ordinary := navigationTaskIDs(projects[0])
	// The most recent Task is inside the ordinary summary.
	recent := ordinary[0]

	// A Task included by recency keeps its recency slot: the ordinary five are
	// unchanged, nothing is appended, and the Task is never duplicated.
	revision, changed, projects, _ := getWorkspaceNavigationResponse(t, server, "?selected_task="+recent)
	if !changed {
		t.Fatalf("selection reported changed=false")
	}
	ids := navigationTaskIDs(projects[0])
	if len(ids) != 5 {
		t.Fatalf("inlined tasks = %d, want the unchanged ordinary five: %v", len(ids), ids)
	}
	if !reflect.DeepEqual(ids, ordinary) {
		t.Fatalf("selecting a recent Task changed the summary: before=%v after=%v", ordinary, ids)
	}

	// Selecting the same Task again keeps the same revision and therefore an
	// unchanged refresh.
	_, changed, _, _ = getWorkspaceNavigationResponse(t, server, "?revision="+revision+"&selected_task="+recent)
	if changed {
		t.Fatalf("same-selection refresh reported changed=true")
	}
	_ = projectID
}

func TestWorkspaceNavigationResponseStableAcrossIdenticalRefreshes(t *testing.T) {
	server, profileID := navigationFixture(t)
	projectID, created := navigationProjectWithTasks(t, server, profileID, "Stable", 6)
	for _, id := range created {
		waitForTaskTerminal(t, server, projectID, id)
	}

	_, _, projects, first := getWorkspaceNavigationResponse(t, server, "")
	_, _, _, second := getWorkspaceNavigationResponse(t, server, "")
	if first != second {
		t.Fatalf("identical refreshes diverged:\nfirst=%s\nsecond=%s", first, second)
	}
	if len(navigationTaskIDs(projects[0])) != 5 {
		t.Fatalf("inlined tasks = %d, want 5", len(navigationTaskIDs(projects[0])))
	}
}

func TestWorkspaceNavigationUnchangedRefreshAndRevisionAdvance(t *testing.T) {
	server, profileID := navigationFixture(t)
	projectID := createProject(t, server, `{"name":"Refresh","scope":{"domains":["example.com"]}}`)
	created := launchTaskForProject(t, server, profileID, projectID, "task")
	// The Task must be terminal before the revision round-trip: the fake runner
	// completes asynchronously and its status write advances the task epoch, so
	// a mid-completion refresh would legitimately report changed=true.
	waitForTaskTerminal(t, server, projectID, created["id"].(string))

	revision, changed, projects, fullBody := getWorkspaceNavigationResponse(t, server, "")
	if !changed || len(projects) != 1 || revision == "" {
		t.Fatalf("initial navigation revision=%q changed=%v projects=%d", revision, changed, len(projects))
	}

	// Refresh with the current revision: empty unchanged result, and the
	// transfer is far smaller than the full projection.
	_, changed, projects, unchangedBody := getWorkspaceNavigationResponse(t, server, "?revision="+revision)
	if changed {
		t.Fatalf("unchanged refresh reported changed=true")
	}
	if len(projects) != 0 {
		t.Fatalf("unchanged refresh returned %d projects, want 0", len(projects))
	}
	if len(unchangedBody) >= len(fullBody) {
		t.Fatalf("unchanged refresh reserialized the projection: unchanged=%d full=%d", len(unchangedBody), len(fullBody))
	}

	// A stale or unknown revision is a full resend.
	_, changed, projects, _ = getWorkspaceNavigationResponse(t, server, "?revision=stale")
	if !changed || len(projects) != 1 {
		t.Fatalf("stale revision changed=%v projects=%d, want full resend", changed, len(projects))
	}

	// A durable change (Task creation) advances the revision, so a refresh with
	// the old revision is served as a full changed projection.
	launchTaskForProject(t, server, profileID, projectID, "task")
	nextRevision, changed, projects, _ := getWorkspaceNavigationResponse(t, server, "?revision="+revision)
	if nextRevision == revision {
		t.Fatalf("revision did not advance after Task creation")
	}
	if !changed || len(projects) != 1 {
		t.Fatalf("post-change refresh changed=%v projects=%d, want full resend", changed, len(projects))
	}
}

func TestWorkspaceNavigationResponseSizeIndependentOfTaskHistory(t *testing.T) {
	// Two identical-shaped workspaces that differ only in total Task history
	// must serialize to the same bounded size, because the projection inlines
	// the fixed summary and never scans the rest.
	smallServer, smallProfile := navigationFixture(t)
	navigationProjectWithTasks(t, smallServer, smallProfile, "SizeProject", 5)

	largeServer, largeProfile := navigationFixture(t)
	_, created := navigationProjectWithTasks(t, largeServer, largeProfile, "SizeProject", 205)

	_, _, smallProjects, smallBody := getWorkspaceNavigationResponse(t, smallServer, "")
	_, _, largeProjects, largeBody := getWorkspaceNavigationResponse(t, largeServer, "")
	if len(navigationTaskIDs(smallProjects[0])) != 5 {
		t.Fatalf("small workspace inlined %d tasks, want 5", len(navigationTaskIDs(smallProjects[0])))
	}
	if len(navigationTaskIDs(largeProjects[0])) != 5 {
		t.Fatalf("large workspace inlined %d tasks, want 5 (history=%d)", len(navigationTaskIDs(largeProjects[0])), len(created))
	}
	if diff := len(largeBody) - len(smallBody); diff > 2000 || diff < -2000 {
		t.Fatalf("navigation response size depends on history: small=%d bytes large=%d bytes (diff=%d)", len(smallBody), len(largeBody), diff)
	}
}
