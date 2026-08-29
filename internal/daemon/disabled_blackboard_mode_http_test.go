package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/finishreadiness"
	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

const disabledBlackboardStateFileReminder = "You may use a state file to keep a lightweight work trace."

// #250 primary acceptance seam: public Task and Session HTTP owner surfaces,
// projected launch files, and a deterministic fake ProviderSession boundary.
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
				"blackboard_conclusion_mode":"disabled",
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
		if created.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeDisabled ||
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
			"run_controls":{"blackboard_conclusion_mode":"disabled"}
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
		if created.RunControls.BlackboardConclusionMode != session.BlackboardConclusionModeDisabled {
			t.Fatalf("disabled Session mode = %q", created.RunControls.BlackboardConclusionMode)
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
			name:   "Project Task defaults to Interactive",
			target: func(projectID string) string { return "/api/projects/" + projectID + "/tasks" },
			body: func(profileID string) string {
				return `{"type":"pentest","goal":"default Task mode","runtime_profile_id":"` + profileID + `","runner":"host","run_controls":{"host_activated":true}}`
			},
			decodeMode: func(t *testing.T, response *httptest.ResponseRecorder) string {
				var found task.Task
				if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
					t.Fatal(err)
				}
				return string(found.RunControls.BlackboardConclusionMode)
			},
		},
		{
			name:   "Non-Project Session defaults to Interactive",
			target: func(string) string { return "/api/sessions" },
			body: func(profileID string) string {
				return `{"input":"default Session mode","runtime_profile_id":"` + profileID + `","runner":"host","host_activated":true}`
			},
			decodeMode: func(t *testing.T, response *httptest.ResponseRecorder) string {
				var found session.Session
				if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
					t.Fatal(err)
				}
				return string(found.RunControls.BlackboardConclusionMode)
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
			if mode := test.decodeMode(t, response); mode != "interactive" {
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
				return `{"type":"pentest","goal":"invalid Task mode","runtime_profile_id":"` + profileID + `","runner":"host","run_controls":{"host_activated":true,"blackboard_conclusion_mode":"automatic"}}`
			},
		},
		{
			name:   "Non-Project Session rejects unknown mode",
			target: func(string) string { return "/api/sessions" },
			body: func(profileID string) string {
				return `{"input":"invalid Session mode","runtime_profile_id":"` + profileID + `","runner":"host","host_activated":true,"run_controls":{"blackboard_conclusion_mode":"automatic"}}`
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

// #251 primary acceptance seam: a stopped Disabled Task resumes through the
// public HTTP API with a replacement Runtime that keeps Blackboard omitted.
func TestDisabledTaskHTTPResumePreservesOmittedBlackboardProjection(t *testing.T) {
	server, projectID, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest",
		"goal":"inspect replacement behavior",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"run_controls":{"host_activated":true,"blackboard_conclusion_mode":"disabled"}
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

	stoppedResponse := httptest.NewRecorder()
	server.ServeHTTP(stoppedResponse, httptest.NewRequest(
		http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil,
	))
	if stoppedResponse.Code != http.StatusOK {
		t.Fatalf("stop disabled Task status = %d body %s", stoppedResponse.Code, stoppedResponse.Body.String())
	}

	replacementSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
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
	assertDisabledTaskBlackboardRows(t, server, created.ID, 2)
}

func TestDisabledSessionHTTPReplacementPreservesOmittedBlackboardProjection(t *testing.T) {
	server, _, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"inspect Session replacement behavior",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"host_activated":true,
		"run_controls":{"blackboard_conclusion_mode":"disabled"}
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

	stoppedResponse := httptest.NewRecorder()
	server.ServeHTTP(stoppedResponse, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stoppedResponse.Code != http.StatusOK {
		t.Fatalf("stop disabled Session status = %d body %s", stoppedResponse.Code, stoppedResponse.Body.String())
	}

	replacementSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
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
	assertDisabledSessionBlackboardRows(t, server, created.ID, 2)
}

func TestDisabledTaskHTTPSteerCreatesOrdinaryContinuationWithoutBlackboard(t *testing.T) {
	server, projectID, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest",
		"goal":"inspect ordinary continuation behavior",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"run_controls":{"host_activated":true,"blackboard_conclusion_mode":"disabled"}
	}`))
	create.Header.Set("Content-Type", "application/json")
	createdResponse := httptest.NewRecorder()
	server.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled Task status = %d body %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"disabled-task-steer",
		"message":"focus on the alternate path"
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer disabled Task status = %d body %s", steerResponse.Code, steerResponse.Body.String())
	}

	latest := waitForDisabledTaskContinuationNumber(t, server, created.ID, 2)
	if latest.Status != task.StatusRunning || latest.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("disabled Task replacement Continuation = %#v", latest)
	}
	assertDisabledTaskBlackboardRows(t, server, created.ID, 2)
	factory.mu.Lock()
	provider, _ := factory.session.(*runtime.FakeProviderSession)
	factory.mu.Unlock()
	if provider == nil {
		t.Fatal("disabled Task provider session is unavailable")
	}
	requests := provider.LastRequests()
	if len(requests) == 0 || requests[len(requests)-1].Message != "focus on the alternate path" ||
		strings.Contains(requests[len(requests)-1].Message, disabledBlackboardStateFileReminder) {
		t.Fatalf("ordinary Task steer request = %#v", requests)
	}
}

func TestDisabledSessionHTTPSteerCreatesOrdinaryContinuationWithoutBlackboard(t *testing.T) {
	server, _, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"inspect Session continuation behavior",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"host_activated":true,
		"run_controls":{"blackboard_conclusion_mode":"disabled"}
	}`))
	create.Header.Set("Content-Type", "application/json")
	createdResponse := httptest.NewRecorder()
	server.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled Session status = %d body %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	steer := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"disabled-session-steer",
		"message":"focus on the alternate Session path"
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer disabled Session status = %d body %s", steerResponse.Code, steerResponse.Body.String())
	}

	latest := waitForDisabledSessionContinuationNumber(t, server, created.ID, 2)
	if latest.Status != session.RuntimeStatusRunning {
		t.Fatalf("disabled Session replacement Continuation = %#v", latest)
	}
	assertDisabledSessionBlackboardRows(t, server, created.ID, 2)
	factory.mu.Lock()
	provider, _ := factory.session.(*runtime.FakeProviderSession)
	factory.mu.Unlock()
	if provider == nil {
		t.Fatal("disabled Session provider session is unavailable")
	}
	requests := provider.LastRequests()
	if len(requests) == 0 || requests[len(requests)-1].Message != "focus on the alternate Session path" ||
		strings.Contains(requests[len(requests)-1].Message, disabledBlackboardStateFileReminder) {
		t.Fatalf("ordinary Session steer request = %#v", requests)
	}
}

func TestDisabledTaskHTTPFinishIgnoresOnlyBlackboardReconciliationDebt(t *testing.T) {
	server, projectID, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest",
		"goal":"finish disabled work",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"run_controls":{"host_activated":true,"blackboard_conclusion_mode":"disabled"}
	}`))
	create.Header.Set("Content-Type", "application/json")
	createdResponse := httptest.NewRecorder()
	server.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled Task status = %d body %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	busyFinish := httptest.NewRecorder()
	server.ServeHTTP(busyFinish, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/finish", nil))
	if busyFinish.Code != http.StatusConflict || !strings.Contains(busyFinish.Body.String(), "busy") {
		t.Fatalf("busy Disabled Finish status = %d body %s", busyFinish.Code, busyFinish.Body.String())
	}

	factory.mu.Lock()
	provider, _ := factory.session.(*runtime.FakeProviderSession)
	factory.mu.Unlock()
	if provider == nil {
		t.Fatal("disabled Task provider session is unavailable")
	}
	if err := provider.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: "disabled-initial-turn",
		ProviderTurnID: "disabled-owner-initial-turn", Status: "completed",
	}); err != nil {
		t.Fatalf("settle disabled initial Turn: %v", err)
	}
	waitForLiveIdle(t, server, created)
	continuation, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || continuation == nil {
		t.Fatalf("load disabled Task Continuation: %#v %v", continuation, err)
	}
	if _, err := server.db.Exec(`UPDATE task_continuations SET blackboard_reconciliation_status=? WHERE id=?`, task.ReconciliationPending, continuation.ID); err != nil {
		t.Fatalf("seed Blackboard-only reconciliation debt: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := server.db.Exec(`
		INSERT INTO pending_blackboard_conclusions (
			id,task_id,source_request_id,source_request_correlation_exact,source_continuation_id,
			source_session_id,source_turn_id,state,source_work_watermark,semantic_persistence_watermark,
			created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		"disabled-finish-debt", created.ID, "legacy-disabled-request", 1, continuation.ID,
		"legacy-disabled-session", "legacy-disabled-turn", "pending", 1, 0, now, now,
	); err != nil {
		t.Fatalf("seed Pending Blackboard Conclusion: %v", err)
	}

	readinessResponse := httptest.NewRecorder()
	server.ServeHTTP(readinessResponse, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID+"/finish-readiness", nil))
	if readinessResponse.Code != http.StatusOK {
		t.Fatalf("Disabled Finish Readiness status = %d body %s", readinessResponse.Code, readinessResponse.Body.String())
	}
	var readiness finishreadiness.Readiness
	if err := json.NewDecoder(readinessResponse.Body).Decode(&readiness); err != nil {
		t.Fatal(err)
	}
	if !readiness.ReadyToFinish || len(readiness.Blockers) != 0 {
		t.Fatalf("Disabled Finish Readiness retained Blackboard blocker: %#v", readiness)
	}

	finishedResponse := httptest.NewRecorder()
	server.ServeHTTP(finishedResponse, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/finish", nil))
	if finishedResponse.Code != http.StatusOK {
		t.Fatalf("finish Disabled Task status = %d body %s", finishedResponse.Code, finishedResponse.Body.String())
	}
	var finished task.Task
	if err := json.NewDecoder(finishedResponse.Body).Decode(&finished); err != nil {
		t.Fatal(err)
	}
	if finished.Status != task.StatusCompleted || finished.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeDisabled {
		t.Fatalf("finished Disabled Task = %#v", finished)
	}
}

func newDisabledBlackboardHTTPFixture(t *testing.T) (*Server, string, string, *recordingProviderSessionFactory) {
	t.Helper()
	root := t.TempDir()
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "disabled-owner-session", ActiveTurnID: "disabled-owner-initial-turn",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}, bindContinuation: true}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SessionRoot: filepath.Join(root, "sessions"), DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create(
		"Disabled Blackboard", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{},
	)
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Disabled Test Provider", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatalf("create Model Provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-disabled-test")
	profile, err := server.profiles.Create("Disabled Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "gpt-test", BinaryPath: "/bin/sh", DefaultRunner: "host",
		Env: map[string]string{"PENTEST_INTERFACE_TOKEN": "stale-profile-grant"},
		MCPServers: []runtimeprofile.MCPServer{{
			Name: "project-memory", Mode: runtimeprofile.MCPServerTrusted,
			URL: "http://daemon.test/mcp?token=stale-profile-grant",
		}},
	})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	return server, createdProject.ID, profile.ID, factory
}

func assertDisabledTaskLaunch(t *testing.T, server *Server, factory *recordingProviderSessionFactory, created task.Task) {
	t.Helper()
	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("Task provider launch requests = %d, want 1", len(requests))
	}
	launch := requests[0]
	if !strings.Contains(launch.LaunchGoal, created.Goal) ||
		strings.Count(launch.LaunchGoal, disabledBlackboardStateFileReminder) != 1 {
		t.Fatalf("Task initial launch goal = %q", launch.LaunchGoal)
	}
	if launch.Continuation.ID == "" || launch.Owner.ID != created.ID || launch.Facts.Workdir == "" {
		t.Fatalf("Task ordinary continuation or workdir is incomplete: %#v", launch)
	}
	assertNoBlackboardLaunchMaterial(t, launch)
	if _, err := os.Stat(filepath.Join(launch.Facts.Workdir, ".pentest", "scope.json")); err != nil {
		t.Fatalf("Task Scope Snapshot is unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(launch.Facts.Workdir, ".pentest", "blackboard.json")); !os.IsNotExist(err) {
		t.Fatalf("Task Working Blackboard Snapshot exists: %v", err)
	}

	var detail task.Task
	getDisabledOwnerHTTP(t, server, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID, &detail)
	if detail.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeDisabled ||
		detail.BlackboardConclusion.Mode != task.BlackboardConclusionModeDisabled ||
		detail.BlackboardConclusion.State != task.BlackboardConclusionStateClean ||
		detail.Goal != created.Goal || detail.LatestContinuation == nil ||
		detail.LatestContinuation.ID != launch.Continuation.ID ||
		detail.LatestContinuation.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("Task public detail lost Disabled mode or ordinary continuation: %#v", detail)
	}

	timelinePage := waitForDisabledOwnerTimeline(
		t, server, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/timeline", "Lifecycle: started",
	)
	if timelinePage.TaskID != created.ID {
		t.Fatalf("Task public timeline owner = %q, want %q", timelinePage.TaskID, created.ID)
	}
	var transcriptPage disabledOwnerTranscriptPage
	getDisabledOwnerHTTP(t, server, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/transcript", &transcriptPage)
	if transcriptPage.TaskID != created.ID || len(transcriptPage.Entries) == 0 ||
		transcriptPage.Entries[0].Role != transcript.RoleUser || transcriptPage.Entries[0].Text != created.Goal {
		t.Fatalf("Task public transcript lost the operator goal: %#v", transcriptPage)
	}
}

func assertDisabledSessionLaunch(t *testing.T, server *Server, factory *recordingProviderSessionFactory, created session.Session) {
	t.Helper()
	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("Session provider launch requests = %d, want 1", len(requests))
	}
	launch := requests[0]
	if !strings.Contains(launch.LaunchGoal, "inspect the standalone target") ||
		strings.Count(launch.LaunchGoal, disabledBlackboardStateFileReminder) != 1 {
		t.Fatalf("Session initial launch goal = %q", launch.LaunchGoal)
	}
	if launch.Continuation.ID == "" || launch.Owner.ID != created.ID || launch.Facts.Workdir == "" {
		t.Fatalf("Session ordinary continuation or workdir is incomplete: %#v", launch)
	}
	assertNoBlackboardLaunchMaterial(t, launch)
	for _, absent := range []string{"AGENTS.md", "CLAUDE.md", ".mcp.json", filepath.Join(".pentest", "blackboard.json")} {
		if _, err := os.Stat(filepath.Join(launch.Facts.Workdir, absent)); !os.IsNotExist(err) {
			t.Fatalf("Session Blackboard launch file %s exists: %v", absent, err)
		}
	}

	var detail session.Session
	getDisabledOwnerHTTP(t, server, "/api/sessions/"+created.ID, &detail)
	if detail.RunControls.BlackboardConclusionMode != session.BlackboardConclusionModeDisabled ||
		detail.BlackboardConclusion.Mode != session.BlackboardConclusionModeDisabled ||
		detail.BlackboardConclusion.State != session.BlackboardConclusionStateClean ||
		detail.LatestContinuation == nil || detail.LatestContinuation.ID != launch.Continuation.ID ||
		detail.LatestContinuation.RuntimeConfigID == "" {
		t.Fatalf("Session public detail lost Disabled mode or ordinary continuation: %#v", detail)
	}

	timelinePage := waitForDisabledOwnerTimeline(
		t, server, "/api/sessions/"+created.ID+"/timeline", "Lifecycle: launch_requested",
	)
	if timelinePage.SessionID != created.ID {
		t.Fatalf("Session public timeline owner = %q, want %q", timelinePage.SessionID, created.ID)
	}
	var transcriptPage disabledOwnerTranscriptPage
	getDisabledOwnerHTTP(t, server, "/api/sessions/"+created.ID+"/transcript", &transcriptPage)
	if transcriptPage.SessionID != created.ID || len(transcriptPage.Entries) == 0 ||
		transcriptPage.Entries[0].Role != transcript.RoleUser ||
		transcriptPage.Entries[0].Text != "inspect the standalone target" {
		t.Fatalf("Session public transcript lost the operator input: %#v", transcriptPage)
	}
}

type disabledOwnerTimelinePage struct {
	TaskID    string          `json:"task_id"`
	SessionID string          `json:"session_id"`
	Items     []timeline.Item `json:"items"`
}

type disabledOwnerTranscriptPage struct {
	TaskID    string             `json:"task_id"`
	SessionID string             `json:"session_id"`
	Entries   []transcript.Entry `json:"entries"`
}

func getDisabledOwnerHTTP(t *testing.T, server *Server, path string, target any) {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body %s", path, response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode GET %s: %v", path, err)
	}
}

func waitForDisabledOwnerTimeline(t *testing.T, server *Server, path, content string) disabledOwnerTimelinePage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var page disabledOwnerTimelinePage
		getDisabledOwnerHTTP(t, server, path, &page)
		for _, item := range page.Items {
			if item.Content == content {
				return page
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s did not expose %q: %#v", path, content, page)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertNoBlackboardLaunchMaterial(t *testing.T, launch ProviderSessionLaunchRequest) {
	t.Helper()
	encoded, err := json.Marshal(launch.RuntimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"runtime-blackboard", "blackboard.json", "project-memory", "stale-profile-grant", "PENTEST_INTERFACE_TOKEN",
	} {
		if strings.Contains(string(encoded), forbidden) || strings.Contains(launch.LaunchGoal, forbidden) {
			t.Fatalf("disabled launch retained Blackboard material %q: goal=%q config=%s", forbidden, launch.LaunchGoal, encoded)
		}
	}
	adapterLaunch, ok := runtime.CommandAdapterLaunch(launch.LegacyAdapter)
	if !ok {
		t.Fatalf("disabled host adapter = %T", launch.LegacyAdapter)
	}
	for _, forbidden := range []string{"PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_SESSION_ID", "PENTEST_MCP_URL", "PENTEST_API_URL", "PENTEST_INTERFACE_TOKEN"} {
		if value := adapterLaunch.Env[forbidden]; value != "" {
			t.Fatalf("disabled launch environment retained %s=%q", forbidden, value)
		}
	}
}

func assertDisabledTaskBlackboardRows(t *testing.T, server *Server, taskID string, wantContinuations int) {
	t.Helper()
	queries := []struct {
		name  string
		query string
		want  int
	}{
		{name: "ordinary continuations", query: `SELECT COUNT(*) FROM task_continuations WHERE task_id=?`, want: wantContinuations},
		{name: "Blackboard pins", query: `SELECT COUNT(*) FROM blackboard_v2_continuation_pins pins JOIN task_continuations continuations ON continuations.id=pins.continuation_id WHERE continuations.task_id=?`},
		{name: "Project Interface grants", query: `SELECT COUNT(*) FROM blackboard_continuation_grants WHERE task_id=?`},
		{name: "Pending Blackboard Conclusions", query: `SELECT COUNT(*) FROM pending_blackboard_conclusions WHERE task_id=?`},
	}
	for _, item := range queries {
		var count int
		if err := server.db.QueryRow(item.query, taskID).Scan(&count); err != nil {
			t.Fatalf("count disabled Task %s: %v", item.name, err)
		}
		if count != item.want {
			t.Fatalf("disabled Task %s = %d, want %d", item.name, count, item.want)
		}
	}
}

func assertDisabledSessionBlackboardRows(t *testing.T, server *Server, sessionID string, wantContinuations int) {
	t.Helper()
	queries := []struct {
		name  string
		query string
		want  int
	}{
		{name: "ordinary continuations", query: `SELECT COUNT(*) FROM session_continuations WHERE session_id=?`, want: wantContinuations},
		{name: "Blackboard pins", query: `SELECT COUNT(*) FROM blackboard_v2_session_continuation_pins WHERE session_id=?`},
		{name: "Project Interface grants", query: `SELECT COUNT(*) FROM session_continuation_interface_grants WHERE session_id=?`},
		{name: "Pending Blackboard Conclusions", query: `SELECT COUNT(*) FROM session_pending_blackboard_conclusions WHERE session_id=?`},
	}
	for _, item := range queries {
		var count int
		if err := server.db.QueryRow(item.query, sessionID).Scan(&count); err != nil {
			t.Fatalf("count disabled Session %s: %v", item.name, err)
		}
		if count != item.want {
			t.Fatalf("disabled Session %s = %d, want %d", item.name, count, item.want)
		}
	}
}

func waitForDisabledTaskContinuationNumber(t *testing.T, server *Server, taskID string, number int) task.TaskContinuation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		latest, err := server.tasks.LatestContinuation(taskID)
		if err == nil && latest != nil && latest.Number >= number && latest.Status != task.StatusPending {
			return *latest
		}
		time.Sleep(5 * time.Millisecond)
	}
	latest, err := server.tasks.LatestContinuation(taskID)
	t.Fatalf("wait for disabled Task Continuation %d: latest=%#v err=%v", number, latest, err)
	return task.TaskContinuation{}
}

func waitForDisabledSessionContinuationNumber(t *testing.T, server *Server, sessionID string, number int) session.Continuation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		latest, err := server.sessions.LatestContinuation(sessionID)
		if err == nil && latest != nil && latest.Number >= number && latest.Status != session.RuntimeStatusPending {
			return *latest
		}
		time.Sleep(5 * time.Millisecond)
	}
	latest, err := server.sessions.LatestContinuation(sessionID)
	t.Fatalf("wait for disabled Session Continuation %d: latest=%#v err=%v", number, latest, err)
	return session.Continuation{}
}
