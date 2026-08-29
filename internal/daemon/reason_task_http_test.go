package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/reasontask"
)

func TestReasonTaskLaunchAndProposalApprovalHTTP(t *testing.T) {
	server := newDaemon(t)
	accessURL, err := url.Parse(server.GeneratedOperatorAccessURL())
	if err != nil || accessURL.Query().Get("token") == "" {
		t.Fatalf("generated operator access URL = %q, error=%v", server.GeneratedOperatorAccessURL(), err)
	}
	operatorToken := accessURL.Query().Get("token")
	projectID := createProject(t, server, `{"name":"Engagement","kind":"pentest","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	launch := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/reason-tasks", strings.NewReader(`{
		"runtime_profile_id":"`+profileID+`","runner":"host","run_controls":{"host_activated":true}
	}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, launch)
	if response.Code != http.StatusCreated {
		t.Fatalf("launch Reason Task status %d body %s", response.Code, response.Body.String())
	}
	var launched struct {
		ID   string `json:"id"`
		Goal string `json:"goal"`
	}
	if err := json.NewDecoder(response.Body).Decode(&launched); err != nil || launched.ID == "" || launched.Goal != reasontask.LaunchGoal {
		t.Fatalf("launched Reason Task = %#v, error=%v", launched, err)
	}

	proposalBody := `{
		"next_task_goals":["Confirm the administration surface"],
		"exploration_objective_changes":["Use targeted validation"],
		"readiness_judgment":"Ready after consolidation",
		"changes":[{"op":"create","key":"objective:targeted-validation","type":"objective","record":{"status":"open","objective":"Validate the administration surface"}}]
	}`
	propose := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/reason-tasks/"+launched.ID+"/proposals", strings.NewReader(proposalBody))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, propose)
	if response.Code != http.StatusCreated {
		t.Fatalf("Reason Task proposal status %d body %s", response.Code, response.Body.String())
	}
	var proposal struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&proposal); err != nil || proposal.Status != "proposed" {
		t.Fatalf("proposal = %#v, error=%v", proposal, err)
	}

	snapshot := func() string {
		request := httptest.NewRequest(http.MethodGet, "/api/v2/projects/"+projectID+"/blackboard/snapshot", nil)
		request.Header.Set("Authorization", "Bearer "+operatorToken)
		result := httptest.NewRecorder()
		server.ServeHTTP(result, request)
		if result.Code != http.StatusOK {
			t.Fatalf("snapshot status %d body %s", result.Code, result.Body.String())
		}
		return result.Body.String()
	}
	if strings.Contains(snapshot(), "objective:targeted-validation") {
		t.Fatal("Reason Task proposal changed Blackboard before approval")
	}

	approvePath := "/api/projects/" + projectID + "/reason-task-proposals/" + proposal.ID + "/approve"
	approve := httptest.NewRequest(http.MethodPost, approvePath, bytes.NewReader(nil))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, approve)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "unauthorized") {
		t.Fatalf("tokenless approve proposal status %d body %s", response.Code, response.Body.String())
	}
	if strings.Contains(snapshot(), "objective:targeted-validation") {
		t.Fatal("tokenless Reason proposal approval changed Blackboard")
	}

	approve = httptest.NewRequest(http.MethodPost, approvePath, bytes.NewReader(nil))
	approve.Header.Set("Authorization", "Bearer "+operatorToken)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, approve)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"approved"`) {
		t.Fatalf("generated operator approve proposal status %d body %s", response.Code, response.Body.String())
	}
	if !strings.Contains(snapshot(), "objective:targeted-validation") {
		t.Fatal("approved Reason Task proposal did not change Blackboard")
	}
}

func TestReasonTaskRejectsDisabledBlackboardModeHTTP(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Engagement","kind":"pentest","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/reason-tasks", strings.NewReader(`{
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"run_controls":{"host_activated":true,"blackboard_conclusion_mode":"disabled"}
	}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Reason Task Blackboard Mode cannot be disabled") {
		t.Fatalf("disabled Reason Task status %d body %s", response.Code, response.Body.String())
	}
}

func TestConfiguredOperatorApprovesReasonProposalHTTP(t *testing.T) {
	const operatorToken = "configured-reason-operator"
	server := newDaemonWithConfig(t, daemon.Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), AuthToken: operatorToken,
		DisableBuiltinSkills: true,
	})
	createProjectResponse := reasonTaskRequest(t, server, http.MethodPost, "/api/projects", operatorToken,
		`{"name":"Configured Reason","kind":"pentest","scope":{"domains":["example.test"]}}`)
	if createProjectResponse.Code != http.StatusCreated {
		t.Fatalf("create configured Project status %d body %s", createProjectResponse.Code, createProjectResponse.Body.String())
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createProjectResponse.Body).Decode(&project); err != nil || project.ID == "" {
		t.Fatalf("decode configured Project: project=%#v error=%v", project, err)
	}
	profileResponse := reasonTaskRequest(t, server, http.MethodPost, "/api/runtime-profiles", operatorToken,
		`{"name":"Configured Fake","provider":"fake"}`)
	if profileResponse.Code != http.StatusCreated {
		t.Fatalf("create configured Runtime Profile status %d body %s", profileResponse.Code, profileResponse.Body.String())
	}
	var profile struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(profileResponse.Body).Decode(&profile); err != nil || profile.ID == "" {
		t.Fatalf("decode configured Runtime Profile: profile=%#v error=%v", profile, err)
	}
	launchResponse := reasonTaskRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/reason-tasks", operatorToken,
		`{"runtime_profile_id":"`+profile.ID+`","runner":"host","run_controls":{"host_activated":true}}`)
	if launchResponse.Code != http.StatusCreated {
		t.Fatalf("launch configured Reason Task status %d body %s", launchResponse.Code, launchResponse.Body.String())
	}
	var reasonTask struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(launchResponse.Body).Decode(&reasonTask); err != nil || reasonTask.ID == "" {
		t.Fatalf("decode configured Reason Task: task=%#v error=%v", reasonTask, err)
	}
	proposalResponse := reasonTaskRequest(t, server, http.MethodPost,
		"/api/projects/"+project.ID+"/reason-tasks/"+reasonTask.ID+"/proposals", operatorToken,
		`{"next_task_goals":["Continue validation"],"exploration_objective_changes":["Add configured review"],"readiness_judgment":"Ready","changes":[{"op":"create","key":"objective:configured-reason","type":"objective","record":{"status":"open","objective":"Configured operator review"}}]}`)
	if proposalResponse.Code != http.StatusCreated {
		t.Fatalf("create configured Reason proposal status %d body %s", proposalResponse.Code, proposalResponse.Body.String())
	}
	var proposal struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(proposalResponse.Body).Decode(&proposal); err != nil || proposal.ID == "" {
		t.Fatalf("decode configured Reason proposal: proposal=%#v error=%v", proposal, err)
	}
	approveResponse := reasonTaskRequest(t, server, http.MethodPost,
		"/api/projects/"+project.ID+"/reason-task-proposals/"+proposal.ID+"/approve", operatorToken, "")
	if approveResponse.Code != http.StatusOK || !strings.Contains(approveResponse.Body.String(), `"status":"approved"`) {
		t.Fatalf("configured operator approval status %d body %s", approveResponse.Code, approveResponse.Body.String())
	}
	recordResponse := reasonTaskRequest(t, server, http.MethodGet,
		"/api/v2/projects/"+project.ID+"/blackboard/records/objective:configured-reason", operatorToken, "")
	if recordResponse.Code != http.StatusOK || !strings.Contains(recordResponse.Body.String(), `"key":"objective:configured-reason"`) {
		t.Fatalf("configured approval public Blackboard record status %d body %s", recordResponse.Code, recordResponse.Body.String())
	}
}

func reasonTaskRequest(t *testing.T, server *daemon.Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}
