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

	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/store"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

func newDisabledBlackboardHTTPFixture(t *testing.T) (*Server, string, string, *recordingProviderSessionFactory) {
	t.Helper()
	root := t.TempDir()
	factory := newDisabledBlackboardProviderFactory("disabled-owner-session", "disabled-owner-initial-turn")
	server := openDisabledBlackboardHTTPServer(t, root, factory)
	t.Cleanup(func() { _ = server.Close() })
	projectID, profileID := seedDisabledBlackboardHTTPProject(t, server)
	return server, projectID, profileID, factory
}

func newDisabledBlackboardProviderFactory(sessionID, activeTurnID string) *recordingProviderSessionFactory {
	providerSession := fakeprovider.New(fakeprovider.Config{
		SessionID: sessionID, ActiveTurnID: activeTurnID,
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true},
	})
	return &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}, bindContinuation: true}
}

func openDisabledBlackboardHTTPServer(t *testing.T, root string, factory *recordingProviderSessionFactory) *Server {
	t.Helper()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SessionRoot: filepath.Join(root, "sessions"), DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	return server
}

func seedDisabledBlackboardHTTPProject(t *testing.T, server *Server) (string, string) {
	t.Helper()
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
	return createdProject.ID, profile.ID
}

func createDisabledTaskHTTP(t *testing.T, server *Server, projectID, profileID, goal string) task.Task {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest",
		"goal":`+quoteJSON(goal)+`,
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"run_controls":{"host_activated":true,"blackboard_mode":"disabled"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Disabled Task status = %d body %s", response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created
}

// These fixtures represent old inconsistent data only. Behavior assertions
// remain at HTTP and ProviderSession boundaries.
func seedLegacyDisabledFinishDebtFixture(t *testing.T, db *store.DB, taskID, continuationID string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE task_continuations SET blackboard_reconciliation_status=? WHERE id=?`, task.ReconciliationPending, continuationID); err != nil {
		t.Fatalf("seed legacy reconciliation debt: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO pending_blackboard_conclusions (
			id,task_id,source_request_id,source_request_correlation_exact,source_continuation_id,
			source_session_id,source_turn_id,state,source_work_watermark,semantic_persistence_watermark,
			created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		"disabled-finish-debt", taskID, "legacy-disabled-request", 1, continuationID,
		"legacy-disabled-session", "legacy-disabled-turn", "pending", 1, 0, now, now,
	); err != nil {
		t.Fatalf("seed legacy Pending Blackboard Conclusion: %v", err)
	}
}

func seedLegacyDisabledTerminalReconciliationFailureFixture(t *testing.T, db *store.DB, projectID, continuationID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE task_continuations SET blackboard_reconciliation_status=? WHERE id=?`, []any{task.ReconciliationPending, continuationID}},
		{`INSERT OR IGNORE INTO blackboard_v2_project_state(project_id,revision) VALUES (?,0)`, []any{projectID}},
		{`INSERT INTO blackboard_v2_records(project_id,key,type,version,record_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, []any{projectID, "attempt/legacy-disabled-corrupt", "attempt", 1, "{", now, now}},
		{`INSERT INTO blackboard_v2_attempt_origins(project_id,key,continuation_id,created_at) VALUES (?,?,?,?)`, []any{projectID, "attempt/legacy-disabled-corrupt", continuationID, now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed legacy terminal reconciliation failure: %v", err)
		}
	}
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
	if detail.RunControls.BlackboardMode != task.BlackboardModeDisabled ||
		detail.BlackboardConclusion.Mode != task.BlackboardModeDisabled ||
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
	if detail.RunControls.BlackboardMode != session.BlackboardModeDisabled ||
		detail.BlackboardConclusion.Mode != session.BlackboardModeDisabled ||
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

func assertDisabledOwnerTimelineOmitsContent(t *testing.T, server *Server, path, content string) {
	t.Helper()
	var page disabledOwnerTimelinePage
	getDisabledOwnerHTTP(t, server, path, &page)
	for _, item := range page.Items {
		if strings.Contains(item.Content, content) {
			t.Fatalf("GET %s exposed forbidden %q: %#v", path, content, page)
		}
	}
}

func assertNoBlackboardLaunchMaterial(t *testing.T, launch ProviderSessionLaunchRequest) {
	t.Helper()
	// Snapshot settings are inert owner-local source data. Authority leakage is
	// checked on the actual Config Projection, launch goal, adapter env and argv.
	projected := make(map[string]any, len(launch.RuntimeConfig))
	for key, value := range launch.RuntimeConfig {
		if key != "settings" {
			projected[key] = value
		}
	}
	encoded, err := json.Marshal(projected)
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
	if strings.TrimSpace(launch.Facts.Workdir) == "" {
		t.Fatalf("disabled host launch missing workdir: %#v", launch.Facts)
	}
	for _, forbidden := range []string{"PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_SESSION_ID", "PENTEST_MCP_URL", "PENTEST_API_URL", "PENTEST_INTERFACE_TOKEN"} {
		if value := launch.Facts.Env[forbidden]; value != "" {
			t.Fatalf("disabled launch environment retained %s=%q", forbidden, value)
		}
	}
}

func assertDisabledTaskPublicContinuation(t *testing.T, detail task.Task, number int, status task.Status) {
	t.Helper()
	if detail.RunControls.BlackboardMode != task.BlackboardModeDisabled ||
		detail.BlackboardConclusion.Mode != task.BlackboardModeDisabled ||
		detail.BlackboardConclusion.State != task.BlackboardConclusionStateClean ||
		detail.LatestContinuation == nil || detail.LatestContinuation.Number != number ||
		detail.LatestContinuation.Status != status ||
		detail.LatestContinuation.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("Disabled Task public detail = %#v", detail)
	}
}

func assertDisabledSessionPublicContinuation(t *testing.T, detail session.Session, number int, status session.RuntimeStatus) {
	t.Helper()
	if detail.RunControls.BlackboardMode != session.BlackboardModeDisabled ||
		detail.BlackboardConclusion.Mode != session.BlackboardModeDisabled ||
		detail.BlackboardConclusion.State != session.BlackboardConclusionStateClean ||
		detail.LatestContinuation == nil || detail.LatestContinuation.Number != number ||
		detail.LatestContinuation.Status != status {
		t.Fatalf("Disabled Session public detail = %#v", detail)
	}
}

func waitForDisabledTaskPublicContinuation(t *testing.T, server *Server, projectID, taskID string, number int) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var detail task.Task
		getDisabledOwnerHTTP(t, server, "/api/projects/"+projectID+"/tasks/"+taskID, &detail)
		if detail.LatestContinuation != nil && detail.LatestContinuation.Number >= number && detail.LatestContinuation.Status != task.StatusPending {
			return detail
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("public Task detail did not expose Continuation %d", number)
	return task.Task{}
}

func waitForDisabledSessionPublicContinuation(t *testing.T, server *Server, sessionID string, number int) session.Session {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var detail session.Session
		getDisabledOwnerHTTP(t, server, "/api/sessions/"+sessionID, &detail)
		if detail.LatestContinuation != nil && detail.LatestContinuation.Number >= number && detail.LatestContinuation.Status != session.RuntimeStatusPending {
			return detail
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("public Session detail did not expose Continuation %d", number)
	return session.Session{}
}

func waitForDisabledTaskRuntimeIdleHTTP(t *testing.T, server *Server, projectID, taskID string) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var detail task.Task
		getDisabledOwnerHTTP(t, server, "/api/projects/"+projectID+"/tasks/"+taskID, &detail)
		if detail.RuntimeActivity.Liveness == "live" && detail.RuntimeActivity.TurnActivity == "idle" {
			return detail
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("public Task detail did not become live and idle")
	return task.Task{}
}

func waitForDisabledTaskPublicStatus(t *testing.T, server *Server, projectID, taskID string, status task.Status) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var detail task.Task
		getDisabledOwnerHTTP(t, server, "/api/projects/"+projectID+"/tasks/"+taskID, &detail)
		if detail.Status == status {
			return detail
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("public Task detail did not reach status %q", status)
	return task.Task{}
}

func disabledFakeProvider(t *testing.T, factory *recordingProviderSessionFactory) *fakeprovider.Session {
	t.Helper()
	factory.mu.Lock()
	defer factory.mu.Unlock()
	provider, ok := factory.session.(*fakeprovider.Session)
	if !ok || provider == nil {
		t.Fatalf("Disabled ProviderSession boundary = %T", factory.session)
	}
	return provider
}
