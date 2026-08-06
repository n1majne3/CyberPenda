package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"pentest/internal/daemon"
)

// #193: the Workspace Sidebar loads one bounded navigation projection instead
// of one Task-list request per Project. These tests pin the
// GET /api/workspace/navigation contract: every Project is returned, each with
// a bounded set of recent/busy Tasks inlined and runtime_activity attached the
// same way as the per-Project Task list, plus Project.last_activity_at.

func getWorkspaceNavigation(t *testing.T, server *daemon.Server) []map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/workspace/navigation", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/workspace/navigation status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode workspace navigation: %v", err)
	}
	return body.Projects
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
	body := `{"goal":"` + goal + `","runtime_profile_id":` + quoteJSON(profileID) + `,"runner":"sandbox"}`
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
	for i := 0; i < 123; i++ {
		projectID := createProject(t, server, `{"name":"P","scope":{"domains":["example.com"]}}`)
		for j := 0; j <= i%3; j++ {
			launchTaskForProject(t, server, profileID, projectID, "task")
		}
	}

	projects := getWorkspaceNavigation(t, server)
	if len(projects) != 123 {
		t.Fatalf("navigation projects = %d, want 123", len(projects))
	}
	for index, summary := range projects {
		tasks, _ := summary["tasks"].([]any)
		if len(tasks) > 5 {
			t.Fatalf("project %d inlined %d tasks, want <= 5", index, len(tasks))
		}
	}
}
