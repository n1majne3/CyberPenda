package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/runtimeplugin"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

const disabledBlackboardStateFileReminder = "You may use a state file to keep a lightweight work trace."

func TestDisabledBlackboardModeInitialOwnerLaunchesOmitBlackboardAuthority(t *testing.T) {
	t.Run("Project Task", func(t *testing.T) {
		server, projectID, profileID, factory := newDisabledBlackboardHTTPFixture(t)
		request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
			"type":"pentest",
			"goal":"inspect example.test",
			"runtime_profile_id":"`+profileID+`",
			"runner":"host",
			"run_controls":{
				"host_activated":true,
				"blackboard_mode":"disabled",
				"policy":{"max_wall_time_seconds":300}
			}
		}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create disabled Task status = %d body %s", response.Code, response.Body.String())
		}
		var created task.Task
		if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
			t.Fatalf("decode disabled Task: %v", err)
		}
		if created.RunControls.BlackboardMode != task.BlackboardModeDisabled ||
			created.RunControls.Policy.MaxWallTimeSeconds != 300 ||
			len(created.ScopeSnapshot.Domains) != 1 || created.ScopeSnapshot.Domains[0] != "example.test" {
			t.Fatalf("disabled Task lost immutable mode or ordinary context: %#v", created)
		}
		assertDisabledTaskLaunch(t, server, factory, created)
	})

	t.Run("Non-Project Session", func(t *testing.T) {
		server, _, profileID, factory := newDisabledBlackboardHTTPFixture(t)
		request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
			"input":"inspect the standalone target",
			"runtime_profile_id":"`+profileID+`",
			"runner":"host",
			"host_activated":true,
			"run_controls":{"blackboard_mode":"disabled"}
		}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create disabled Session status = %d body %s", response.Code, response.Body.String())
		}
		var created session.Session
		if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
			t.Fatalf("decode disabled Session: %v", err)
		}
		if created.RunControls.BlackboardMode != session.BlackboardModeDisabled {
			t.Fatalf("disabled Session mode = %q", created.RunControls.BlackboardMode)
		}
		assertDisabledSessionLaunch(t, server, factory, created)
	})
}

func TestBlackboardModeHTTPDefaultsAndValidationApplyToBothOwnerTypes(t *testing.T) {
	tests := []struct {
		name       string
		target     func(projectID string) string
		body       func(profileID string) string
		decodeMode func(*testing.T, *httptest.ResponseRecorder) string
	}{
		{
			name:   "Project Task defaults to Working Graph",
			target: func(projectID string) string { return "/api/projects/" + projectID + "/tasks" },
			body: func(profileID string) string {
				return `{"type":"pentest","goal":"default Task mode","runtime_profile_id":"` + profileID + `","runner":"host","run_controls":{"host_activated":true}}`
			},
			decodeMode: func(t *testing.T, response *httptest.ResponseRecorder) string {
				var found task.Task
				if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
					t.Fatal(err)
				}
				return string(found.RunControls.BlackboardMode)
			},
		},
		{
			name:   "Non-Project Session defaults to Disabled",
			target: func(string) string { return "/api/sessions" },
			body: func(profileID string) string {
				return `{"input":"default Session mode","runtime_profile_id":"` + profileID + `","runner":"host","host_activated":true}`
			},
			decodeMode: func(t *testing.T, response *httptest.ResponseRecorder) string {
				var found session.Session
				if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
					t.Fatal(err)
				}
				return string(found.RunControls.BlackboardMode)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, projectID, profileID, _ := newDisabledBlackboardHTTPFixture(t)
			request := httptest.NewRequest(http.MethodPost, test.target(projectID), strings.NewReader(test.body(profileID)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("create owner status = %d body %s", response.Code, response.Body.String())
			}
			want := "working_graph"
			if test.name == "Non-Project Session defaults to Disabled" {
				want = "disabled"
			}
			if mode := test.decodeMode(t, response); mode != want {
				t.Fatalf("default Blackboard Mode = %q", mode)
			}
		})
	}

	invalid := []struct {
		name   string
		target func(projectID string) string
		body   func(profileID string) string
	}{
		{
			name:   "Project Task rejects unknown mode",
			target: func(projectID string) string { return "/api/projects/" + projectID + "/tasks" },
			body: func(profileID string) string {
				return `{"type":"pentest","goal":"invalid Task mode","runtime_profile_id":"` + profileID + `","runner":"host","run_controls":{"host_activated":true,"blackboard_mode":"automatic"}}`
			},
		},
		{
			name:   "Non-Project Session rejects unknown mode",
			target: func(string) string { return "/api/sessions" },
			body: func(profileID string) string {
				return `{"input":"invalid Session mode","runtime_profile_id":"` + profileID + `","runner":"host","host_activated":true,"run_controls":{"blackboard_mode":"automatic"}}`
			},
		},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			server, projectID, profileID, _ := newDisabledBlackboardHTTPFixture(t)
			request := httptest.NewRequest(http.MethodPost, test.target(projectID), strings.NewReader(test.body(profileID)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Blackboard Mode") {
				t.Fatalf("invalid owner mode status = %d body %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDisabledTaskHTTPResumePreservesOmittedBlackboardProjection(t *testing.T) {
	server, projectID, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest",
		"goal":"inspect replacement behavior",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"run_controls":{"host_activated":true,"blackboard_mode":"disabled"}
	}`))
	create.Header.Set("Content-Type", "application/json")
	createdResponse := httptest.NewRecorder()
	server.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled Task status = %d body %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode disabled Task: %v", err)
	}

	// The accepted create response precedes durable Runtime startup; Stop must
	// observe the running state so its cancel reaches the registered Harness
	// run instead of settling only durable state.
	waitForDisabledTaskPublicStatus(t, server, projectID, created.ID, task.StatusRunning)

	stoppedResponse := httptest.NewRecorder()
	server.ServeHTTP(stoppedResponse, httptest.NewRequest(
		http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil,
	))
	if stoppedResponse.Code != http.StatusOK {
		t.Fatalf("stop disabled Task status = %d body %s", stoppedResponse.Code, stoppedResponse.Body.String())
	}

	replacementSession := fakeprovider.New(fakeprovider.Config{
		SessionID: "disabled-owner-replacement-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true,
		},
	})
	factory.mu.Lock()
	factory.session = replacementSession
	factory.adapter = &persistentTestAdapter{}
	factory.mu.Unlock()

	resume := httptest.NewRequest(
		http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/resume", bytes.NewBufferString(`{}`),
	)
	resume.Header.Set("Content-Type", "application/json")
	resumedResponse := httptest.NewRecorder()
	server.ServeHTTP(resumedResponse, resume)
	if resumedResponse.Code != http.StatusAccepted {
		t.Fatalf("resume disabled Task status = %d body %s", resumedResponse.Code, resumedResponse.Body.String())
	}

	requests := factory.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider Runtime launches = %d, want initial and replacement", len(requests))
	}
	replacement := requests[1]
	if strings.Count(replacement.LaunchGoal, disabledBlackboardStateFileReminder) != 1 {
		t.Fatalf("replacement launch reminder count = %d goal = %q", strings.Count(replacement.LaunchGoal, disabledBlackboardStateFileReminder), replacement.LaunchGoal)
	}
	assertNoBlackboardLaunchMaterial(t, replacement)
	var resumed task.Task
	if err := json.NewDecoder(resumedResponse.Body).Decode(&resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.RunControls.BlackboardMode != task.BlackboardModeDisabled {
		t.Fatalf("resume response lost Disabled mode: %#v", resumed)
	}
	resumed = waitForDisabledTaskPublicContinuation(t, server, projectID, created.ID, 2)
	assertDisabledTaskPublicContinuation(t, resumed, 2, task.StatusRunning)
}

func TestDisabledSessionHTTPReplacementPreservesOmittedBlackboardProjection(t *testing.T) {
	server, _, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"inspect Session replacement behavior",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"host_activated":true,
		"run_controls":{"blackboard_mode":"disabled"}
	}`))
	create.Header.Set("Content-Type", "application/json")
	createdResponse := httptest.NewRecorder()
	server.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled Session status = %d body %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode disabled Session: %v", err)
	}

	// The accepted create response precedes durable Runtime startup; Stop must
	// observe the running state so its cancel reaches the registered Harness
	// run instead of settling only durable state.
	initial := waitForDisabledSessionPublicContinuation(t, server, created.ID, 1)
	assertDisabledSessionPublicContinuation(t, initial, 1, session.RuntimeStatusRunning)

	stoppedResponse := httptest.NewRecorder()
	server.ServeHTTP(stoppedResponse, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stoppedResponse.Code != http.StatusOK {
		t.Fatalf("stop disabled Session status = %d body %s", stoppedResponse.Code, stoppedResponse.Body.String())
	}

	replacementSession := fakeprovider.New(fakeprovider.Config{
		SessionID: "disabled-session-replacement-runtime",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true,
		},
	})
	factory.mu.Lock()
	factory.session = replacementSession
	factory.adapter = &persistentTestAdapter{}
	factory.mu.Unlock()

	message := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/messages", bytes.NewBufferString(`{
		"input":"continue after Stop",
		"host_activated":true
	}`))
	message.Header.Set("Content-Type", "application/json")
	replacedResponse := httptest.NewRecorder()
	server.ServeHTTP(replacedResponse, message)
	if replacedResponse.Code != http.StatusAccepted {
		t.Fatalf("replace disabled Session status = %d body %s", replacedResponse.Code, replacedResponse.Body.String())
	}

	requests := factory.Requests()
	if len(requests) != 2 {
		t.Fatalf("Session provider Runtime launches = %d, want initial and replacement", len(requests))
	}
	replacement := requests[1]
	if strings.Count(replacement.LaunchGoal, disabledBlackboardStateFileReminder) != 1 {
		t.Fatalf("Session replacement reminder count = %d goal = %q", strings.Count(replacement.LaunchGoal, disabledBlackboardStateFileReminder), replacement.LaunchGoal)
	}
	assertNoBlackboardLaunchMaterial(t, replacement)
	var replaced session.Session
	if err := json.NewDecoder(replacedResponse.Body).Decode(&replaced); err != nil {
		t.Fatal(err)
	}
	if replaced.RunControls.BlackboardMode != session.BlackboardModeDisabled {
		t.Fatalf("replacement response lost Disabled mode: %#v", replaced)
	}
	replaced = waitForDisabledSessionPublicContinuation(t, server, created.ID, 2)
	assertDisabledSessionPublicContinuation(t, replaced, 2, session.RuntimeStatusRunning)
}
