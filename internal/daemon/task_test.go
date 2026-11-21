package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/runtimeprofile"
)

func TestLaunchTaskRunsFakeRuntimeAndStreamsEvents(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"],"notes":"in scope"}
	}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	body := []byte(`{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":` + quoteJSON(profileID) + `,
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected create task status 201, got %d with body %s", resp.Code, resp.Body.String())
	}

	var created struct {
		ID                 string `json:"id"`
		ProjectID          string `json:"project_id"`
		Goal               string `json:"goal"`
		Runner             string `json:"runner"`
		RuntimeProfileID   string `json:"runtime_profile_id"`
		LatestContinuation *struct {
			Number           int    `json:"number"`
			RuntimeProfileID string `json:"runtime_profile_id"`
			RuntimeProvider  string `json:"runtime_provider"`
			Runner           string `json:"runner"`
			Status           string `json:"status"`
		} `json:"latest_continuation"`
		ScopeSnapshot struct {
			Domains []string `json:"domains"`
			Notes   string   `json:"notes"`
		} `json:"scope_snapshot"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected task id")
	}
	if created.Goal != "enumerate example.com" {
		t.Fatalf("expected goal, got %q", created.Goal)
	}
	if created.Runner != "sandbox" {
		t.Fatalf("expected sandbox runner, got %q", created.Runner)
	}
	if created.RuntimeProfileID != profileID {
		t.Fatalf("expected runtime profile id, got %q", created.RuntimeProfileID)
	}
	if created.LatestContinuation == nil {
		t.Fatal("expected latest continuation in create response")
	}
	if created.LatestContinuation.Number != 1 {
		t.Fatalf("expected first continuation number 1, got %d", created.LatestContinuation.Number)
	}
	if created.LatestContinuation.RuntimeProvider != "fake" {
		t.Fatalf("expected runtime provider fake, got %q", created.LatestContinuation.RuntimeProvider)
	}
	if created.LatestContinuation.Runner != "sandbox" {
		t.Fatalf("expected continuation runner sandbox, got %q", created.LatestContinuation.Runner)
	}
	if got := created.ScopeSnapshot.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope snapshot domain, got %#v", got)
	}
	if created.ScopeSnapshot.Notes != "in scope" {
		t.Fatalf("expected scope snapshot notes, got %q", created.ScopeSnapshot.Notes)
	}

	// The runtime runs in the background, so poll until its output is visible.
	waitForEventText(t, server, projectID, created.ID, "enumerating in-scope assets")
	events := getTaskEvents(t, server, projectID, created.ID)
	kinds := map[string]bool{}
	for _, e := range events {
		kinds[e["kind"].(string)] = true
	}
	if !kinds["lifecycle"] || !kinds["runtime_output"] {
		t.Fatalf("expected lifecycle and runtime_output events, got %#v", kinds)
	}
}

func TestLaunchTaskRejectsMissingTaskType(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","kind":"pentest","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	body := []byte(`{
		"goal":"enumerate example.com",
		"runtime_profile_id":` + quoteJSON(profileID) + `,
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("missing Task Type status = %d, want 400; body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "Task Type must be pentest or ctf_challenge") {
		t.Fatalf("missing Task Type error = %s", resp.Body.String())
	}
}

func TestGetTaskIncludesLatestContinuation(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected get task status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var found struct {
		ID              string `json:"id"`
		RuntimeControls struct {
			NativeResumeAvailable   bool   `json:"native_resume_available"`
			NativeResumeReason      string `json:"native_resume_reason"`
			ResumeAvailable         bool   `json:"resume_available"`
			QueueSteerAvailable     bool   `json:"queue_steer_available"`
			SameRuntimeProviderOnly bool   `json:"same_runtime_provider_only"`
			RuntimeProvider         string `json:"runtime_provider"`
		} `json:"runtime_controls"`
		LatestContinuation *struct {
			Number           int    `json:"number"`
			RuntimeProfileID string `json:"runtime_profile_id"`
			RuntimeProvider  string `json:"runtime_provider"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&found); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if found.ID != taskID {
		t.Fatalf("expected task id %q, got %q", taskID, found.ID)
	}
	if found.LatestContinuation == nil {
		t.Fatal("expected latest continuation in task detail")
	}
	if found.LatestContinuation.Number != 1 {
		t.Fatalf("expected latest continuation number 1, got %d", found.LatestContinuation.Number)
	}
	if found.LatestContinuation.RuntimeProvider != "fake" {
		t.Fatalf("expected latest continuation provider fake, got %q", found.LatestContinuation.RuntimeProvider)
	}
	if found.RuntimeControls.NativeResumeAvailable {
		t.Fatal("expected fake runtime native resume to be unavailable")
	}
	if !strings.Contains(found.RuntimeControls.NativeResumeReason, "unsupported") {
		t.Fatalf("expected unsupported native resume reason, got %q", found.RuntimeControls.NativeResumeReason)
	}
	if !found.RuntimeControls.ResumeAvailable || !found.RuntimeControls.QueueSteerAvailable {
		t.Fatalf("expected fresh resume and queue steer available, got %#v", found.RuntimeControls)
	}
	if !found.RuntimeControls.SameRuntimeProviderOnly || found.RuntimeControls.RuntimeProvider != "fake" {
		t.Fatalf("expected same-provider fake controls, got %#v", found.RuntimeControls)
	}
}

func TestGetTaskSurvivesDeletedLatestRuntimeProfile(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileA := createRuntimeProfile(t, server, `{"name":"Fake A","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileA)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	del := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/api/runtime-profiles/"+profileA, nil)
	server.ServeHTTP(del, delReq)
	if del.Code != http.StatusNoContent {
		t.Fatalf("expected delete profile status 204, got %d with body %s", del.Code, del.Body.String())
	}

	get := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(get, getReq)
	if get.Code != http.StatusOK {
		t.Fatalf("expected get task status 200 after deleting the latest profile, got %d with body %s", get.Code, get.Body.String())
	}

	var found struct {
		RuntimeControls struct {
			RuntimeProvider string `json:"runtime_provider"`
		} `json:"runtime_controls"`
		LatestContinuation *struct {
			RuntimeProvider string `json:"runtime_provider"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(get.Body).Decode(&found); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if found.RuntimeControls.RuntimeProvider != "fake" {
		t.Fatalf("expected fallback to launch profile provider fake, got %q", found.RuntimeControls.RuntimeProvider)
	}
	if found.LatestContinuation == nil || found.LatestContinuation.RuntimeProvider != "fake" {
		t.Fatalf("expected latest continuation from the fake runtime, got %#v", found.LatestContinuation)
	}
}

// Deleting the provenance Profile never degrades an existing owner. Runtime
// controls and later turns use the immutable owner-local Snapshot.
func TestGetTaskKeepsSnapshotWhenRuntimeProfileIsDeleted(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	del := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/api/runtime-profiles/"+profileID, nil)
	server.ServeHTTP(del, delReq)
	if del.Code != http.StatusNoContent {
		t.Fatalf("expected delete profile status 204, got %d with body %s", del.Code, del.Body.String())
	}

	get := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	server.ServeHTTP(get, getReq)
	if get.Code != http.StatusOK {
		t.Fatalf("expected get task status 200 after deleting every profile, got %d with body %s", get.Code, get.Body.String())
	}

	var found struct {
		RuntimeControls struct {
			RuntimeProvider     string `json:"runtime_provider"`
			NativeResumeReason  string `json:"native_resume_reason"`
			QueueSteerAvailable bool   `json:"queue_steer_available"`
		} `json:"runtime_controls"`
		LatestContinuation *struct {
			RuntimeProvider string `json:"runtime_provider"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(get.Body).Decode(&found); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if found.RuntimeControls.RuntimeProvider != "fake" {
		t.Fatalf("expected captured runtime provider fake, got %q", found.RuntimeControls.RuntimeProvider)
	}
	if !strings.Contains(found.RuntimeControls.NativeResumeReason, "unsupported") {
		t.Fatalf("expected degraded native resume reason, got %q", found.RuntimeControls.NativeResumeReason)
	}
	if !found.RuntimeControls.QueueSteerAvailable {
		t.Fatal("expected queue steering to remain available from the captured Snapshot")
	}
	if found.LatestContinuation == nil || found.LatestContinuation.RuntimeProvider != "fake" {
		t.Fatalf("expected latest continuation from the fake runtime, got %#v", found.LatestContinuation)
	}

	steer := httptest.NewRecorder()
	steerReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"continue",
		"submitted_by":"operator"
	}`)))
	steerReq.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(steer, steerReq)
	if steer.Code != http.StatusOK {
		t.Fatalf("expected steer status 200 from captured Snapshot, got %d with body %s", steer.Code, steer.Body.String())
	}
}

func TestDeleteCompletedTaskRemovesItFromTaskSurfaces(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	deleted := httptest.NewRecorder()
	server.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/projects/"+projectID+"/tasks/"+taskID, nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("expected delete task status 204, got %d with body %s", deleted.Code, deleted.Body.String())
	}

	list := httptest.NewRecorder()
	server.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("expected list tasks status 200, got %d with body %s", list.Code, list.Body.String())
	}
	var listed struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(listed.Tasks) != 0 {
		t.Fatalf("expected deleted task to be absent from list, got %#v", listed.Tasks)
	}

	detail := httptest.NewRecorder()
	server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil))
	if detail.Code != http.StatusNotFound {
		t.Fatalf("expected deleted task detail status 404, got %d with body %s", detail.Code, detail.Body.String())
	}

	dashboard := httptest.NewRecorder()
	server.ServeHTTP(dashboard, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/dashboard", nil))
	if dashboard.Code != http.StatusOK {
		t.Fatalf("expected project dashboard status 200, got %d with body %s", dashboard.Code, dashboard.Body.String())
	}
	var summary struct {
		Counts struct {
			Tasks int `json:"tasks"`
		} `json:"counts"`
		Tasks struct {
			Total int `json:"total"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(dashboard.Body).Decode(&summary); err != nil {
		t.Fatalf("decode project dashboard: %v", err)
	}
	if summary.Counts.Tasks != 0 || summary.Tasks.Total != 0 {
		t.Fatalf("expected deleted task to be absent from dashboard counts, got counts=%d summary=%d", summary.Counts.Tasks, summary.Tasks.Total)
	}
}

func TestDeleteRunningTaskIsRejected(t *testing.T) {
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          t.TempDir(),
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	binary := filepath.Join(t.TempDir(), "running-task-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Running Task Test", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "claude-sonnet-4",
	})
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "running")

	deleted := httptest.NewRecorder()
	server.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/projects/"+projectID+"/tasks/"+taskID, nil))
	if deleted.Code != http.StatusConflict {
		t.Fatalf("expected running task delete status 409, got %d with body %s", deleted.Code, deleted.Body.String())
	}

	detail := httptest.NewRecorder()
	server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("expected running task to remain available, got %d with body %s", detail.Code, detail.Body.String())
	}
}
