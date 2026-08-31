package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

const reasonProposalOperatorToken = "configured-reason-operator"

func TestConfiguredReasonProposalCreateAcceptsBoundContinuationGrantHTTP(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, reasonProposalOperatorToken)
	reasonTask, reasonGrant := launchReasonTaskWithGrant(t, fixture)

	proposal := reasonProposalRequest(
		fixture, reasonTask.ID, reasonGrant, "objective:bound-reason-proposal",
	)
	if proposal.Code != http.StatusCreated {
		t.Fatalf("bound Reason grant proposal status=%d body=%s", proposal.Code, proposal.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(proposal.Body).Decode(&created); err != nil || created.ID == "" || created.Status != "proposed" {
		t.Fatalf("decode bound Reason proposal: proposal=%#v error=%v", created, err)
	}

	approve := disabledOutputRequest(
		fixture, http.MethodPost,
		"/api/projects/"+fixture.projectID+"/reason-task-proposals/"+created.ID+"/approve",
		fixture.operatorToken, "", "", "",
	)
	if approve.Code != http.StatusOK || !strings.Contains(approve.Body.String(), `"status":"approved"`) {
		t.Fatalf("configured operator approval status=%d body=%s", approve.Code, approve.Body.String())
	}
	record := disabledOutputRequest(
		fixture, http.MethodGet,
		"/api/v2/projects/"+fixture.projectID+"/blackboard/records/objective:bound-reason-proposal",
		fixture.operatorToken, "", "", "",
	)
	if record.Code != http.StatusOK || !strings.Contains(record.Body.String(), `"key":"objective:bound-reason-proposal"`) {
		t.Fatalf("approved proposal Blackboard record status=%d body=%s", record.Code, record.Body.String())
	}
}

func TestGeneratedOperatorApprovesReasonProposalHTTP(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, "")
	reasonTask, reasonGrant := launchReasonTaskWithGrant(t, fixture)
	proposal := reasonProposalRequest(
		fixture, reasonTask.ID, reasonGrant, "objective:generated-operator-reason",
	)
	if proposal.Code != http.StatusCreated {
		t.Fatalf("generated-operator fixture proposal status=%d body=%s", proposal.Code, proposal.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(proposal.Body).Decode(&created); err != nil || created.ID == "" {
		t.Fatalf("decode generated-operator fixture proposal: proposal=%#v error=%v", created, err)
	}
	approvePath := "/api/projects/" + fixture.projectID + "/reason-task-proposals/" + created.ID + "/approve"
	tokenless := disabledOutputRequest(fixture, http.MethodPost, approvePath, "", "", "", "")
	if tokenless.Code != http.StatusUnauthorized || !strings.Contains(tokenless.Body.String(), "unauthorized") {
		t.Fatalf("tokenless approval status=%d body=%s", tokenless.Code, tokenless.Body.String())
	}
	approve := disabledOutputRequest(
		fixture, http.MethodPost, approvePath, fixture.operatorToken, "", "", "",
	)
	if approve.Code != http.StatusOK || !strings.Contains(approve.Body.String(), `"status":"approved"`) {
		t.Fatalf("generated operator approval status=%d body=%s", approve.Code, approve.Body.String())
	}
}

func TestConfiguredReasonProposalCreateRejectsMissingOrWrongGrantHTTP(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, reasonProposalOperatorToken)
	reasonTask, _ := launchReasonTaskWithGrant(t, fixture)
	otherProject, err := fixture.server.projects.Create(
		"Other Project", "", project.Scope{Domains: []string{"other.example"}}, project.Defaults{},
	)
	if err != nil {
		t.Fatalf("create other Project fixture: %v", err)
	}

	cases := []struct {
		name  string
		token string
	}{
		{name: "tokenless"},
		{name: "operator token", token: fixture.operatorToken},
		{name: "other Interactive Task", token: issueOtherTaskGrant(t, fixture, fixture.projectID, task.BlackboardConclusionModeInteractive)},
		{name: "other Assisted Task", token: issueOtherTaskGrant(t, fixture, fixture.projectID, task.BlackboardConclusionModeAssisted)},
		{name: "other Project", token: issueOtherTaskGrant(t, fixture, otherProject.ID, task.BlackboardConclusionModeInteractive)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := reasonProposalRequest(fixture, reasonTask.ID, test.token, "objective:unauthorized-reason-proposal")
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "unauthorized") {
				t.Fatalf("proposal status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	list := disabledOutputRequest(
		fixture, http.MethodGet,
		"/api/projects/"+fixture.projectID+"/reason-task-proposals",
		fixture.operatorToken, "", "", "",
	)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"proposals":[]`) {
		t.Fatalf("unauthorized requests changed public proposal list: status=%d body=%s", list.Code, list.Body.String())
	}
}

func TestReasonProposalCreateRejectsClosedContinuationGrantHTTP(t *testing.T) {
	for _, state := range []string{"finished", "terminal"} {
		t.Run(state, func(t *testing.T) {
			fixture := newDisabledOutputAuthorityFixture(t, reasonProposalOperatorToken)
			reasonTask, reasonGrant := launchReasonTaskWithGrant(t, fixture)
			grant, err := fixture.server.projectInterfaceGrants.Resolve(context.Background(), reasonGrant)
			if err != nil {
				t.Fatalf("resolve Reason grant fixture: %v", err)
			}
			switch state {
			case "finished":
				if _, err := fixture.server.projectInterfaceGrants.Finish(context.Background(), grant.ID); err != nil {
					t.Fatalf("finish Reason grant fixture: %v", err)
				}
			case "terminal":
				if err := fixture.server.projectInterfaceGrants.MarkContinuationTerminal(context.Background(), grant.ContinuationID); err != nil {
					t.Fatalf("mark Reason grant terminal fixture: %v", err)
				}
			}

			response := reasonProposalRequest(
				fixture, reasonTask.ID, reasonGrant, "objective:closed-reason-proposal",
			)
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "unauthorized") {
				t.Fatalf("closed Reason grant proposal status=%d body=%s", response.Code, response.Body.String())
			}
			list := disabledOutputRequest(
				fixture, http.MethodGet,
				"/api/projects/"+fixture.projectID+"/reason-task-proposals",
				fixture.operatorToken, "", "", "",
			)
			if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"proposals":[]`) {
				t.Fatalf("closed grant changed public proposal list: status=%d body=%s", list.Code, list.Body.String())
			}
		})
	}
}

func TestReasonContinuationGrantDoesNotAuthorizeOrdinaryAPIHTTP(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, reasonProposalOperatorToken)
	_, reasonGrant := launchReasonTaskWithGrant(t, fixture)

	request := httptest.NewRequest(
		http.MethodPost, "/api/projects/"+fixture.projectID+"/scope-expansions", strings.NewReader(`{}`),
	)
	request.Header.Set("Authorization", "Bearer "+reasonGrant)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "unauthorized") {
		t.Fatalf("Reason grant ordinary API status=%d body=%s", response.Code, response.Body.String())
	}
}

func launchReasonTaskWithGrant(t *testing.T, fixture disabledOutputAuthorityFixture) (task.Task, string) {
	t.Helper()
	response := disabledOutputRequest(
		fixture, http.MethodPost, "/api/projects/"+fixture.projectID+"/reason-tasks",
		fixture.operatorToken, "", "",
		`{"runtime_profile_id":"`+fixture.profileID+`","runner":"host","run_controls":{"host_activated":true}}`,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("launch Reason Task status=%d body=%s", response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil || created.ID == "" || created.LatestContinuation == nil {
		t.Fatalf("decode Reason Task launch: task=%#v error=%v", created, err)
	}
	grantToken, _, err := fixture.server.projectInterfaceGrants.Issue(context.Background(), projectinterface.IssueGrantRequest{
		ProjectID: fixture.projectID, TaskID: created.ID, ContinuationID: created.LatestContinuation.ID,
		RuntimeConfigVersionID: created.LatestContinuation.RuntimeConfigVersionID,
		RuntimeProfileID:       fixture.profileID,
		RuntimePluginID:        string(runtimeprofile.ProviderCodex),
		Runner:                 string(created.Runner),
	})
	if err != nil {
		t.Fatalf("issue Reason Task Continuation grant fixture: %v", err)
	}
	return created, grantToken
}

func issueOtherTaskGrant(
	t *testing.T,
	fixture disabledOutputAuthorityFixture,
	projectID string,
	mode task.BlackboardConclusionMode,
) string {
	t.Helper()
	created, err := fixture.server.tasks.Create(task.CreateRequest{
		ProjectID: projectID, Type: task.TypePentest, Goal: "other Task authority fixture",
		RuntimeProfileID: fixture.profileID, Runner: task.RunnerHost,
		RunControls: task.RunControls{HostActivated: true, BlackboardConclusionMode: mode},
	})
	if err != nil {
		t.Fatalf("create other %s Task fixture: %v", mode, err)
	}
	_, continuation, err := fixture.server.tasks.CreateContinuationLaunchWithoutBlackboard(
		context.Background(), task.ContinuationLaunchRequest{
			ProjectID: projectID, TaskID: created.ID, RuntimeProfileID: fixture.profileID,
			RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerHost,
			RuntimeConfig: map[string]any{"fixture": "other-task-grant"},
		},
	)
	if err != nil {
		t.Fatalf("create other %s Continuation fixture: %v", mode, err)
	}
	token, _, err := fixture.server.projectInterfaceGrants.Issue(context.Background(), projectinterface.IssueGrantRequest{
		ProjectID: projectID, TaskID: created.ID, ContinuationID: continuation.ID,
		RuntimeConfigVersionID: continuation.RuntimeConfigVersionID,
		RuntimeProfileID:       fixture.profileID,
		RuntimePluginID:        string(runtimeprofile.ProviderCodex), Runner: string(task.RunnerHost),
	})
	if err != nil {
		t.Fatalf("issue other %s Task grant fixture: %v", mode, err)
	}
	return token
}

func reasonProposalRequest(
	fixture disabledOutputAuthorityFixture,
	reasonTaskID, token, objectiveKey string,
) *httptest.ResponseRecorder {
	body := `{"next_task_goals":["Continue validation"],"exploration_objective_changes":["Review the Reason proposal"],"readiness_judgment":"Ready","changes":[{"op":"create","key":"` + objectiveKey + `","type":"objective","record":{"status":"open","objective":"Review the Reason Task proposal"}}]}`
	return disabledOutputRequest(
		fixture, http.MethodPost,
		"/api/projects/"+fixture.projectID+"/reason-tasks/"+reasonTaskID+"/proposals",
		token, "", "", body,
	)
}
