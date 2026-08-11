package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/reasontask"
)

func TestReasonTaskLaunchAndProposalApprovalHTTP(t *testing.T) {
	server := newDaemon(t)
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

	approve := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/reason-task-proposals/"+proposal.ID+"/approve", bytes.NewReader(nil))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, approve)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"approved"`) {
		t.Fatalf("approve proposal status %d body %s", response.Code, response.Body.String())
	}
	if !strings.Contains(snapshot(), "objective:targeted-validation") {
		t.Fatal("approved Reason Task proposal did not change Blackboard")
	}
}
