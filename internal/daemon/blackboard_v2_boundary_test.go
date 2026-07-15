package daemon_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/daemon"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
	"pentest/internal/testsupport/blackboardfixture"
)

const retiredBlackboardV1Message = "legacy Blackboard v1 interface is unavailable for blackboard_v2; use the Blackboard v2 semantic interface"

// TestBlackboardV2DaemonRefusesEveryLegacyBlackboardSurface proves that a
// fresh v2 daemon cannot fall through any retained HTTP or MCP route into the
// v1 tables while the later v2 transport tickets are still pending.
func TestBlackboardV2DaemonRefusesEveryLegacyBlackboardSurface(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "blackboard-v2.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open setup store: %v", err)
	}
	projects := project.NewService(db)
	createdProject, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	createdTask, err := task.NewService(db, projects).Create(task.CreateRequest{
		ProjectID:        createdProject.ID,
		Goal:             "enumerate example.com",
		RuntimeProfileID: "profile-for-boundary-test",
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	blackboardfixture.SeedLegacyState(t, db, createdProject.ID, createdTask.ID)
	if err := db.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	inspectionDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open inspection store: %v", err)
	}
	t.Cleanup(func() { _ = inspectionDB.Close() })
	before := blackboardfixture.CaptureLegacyState(t, inspectionDB)

	projectBase := "/api/projects/" + createdProject.ID
	writes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "graph mutation", method: http.MethodPost, path: projectBase + "/blackboard/mutations", body: `{"request_kind":"apply","protocol_version":1}`},
		{name: "graph evidence retain", method: http.MethodPost, path: projectBase + "/blackboard/evidence:retain", body: `{}`},
		{name: "graph attempt checkpoint", method: http.MethodPost, path: projectBase + "/blackboard/attempts:checkpoint", body: `{}`},
		{name: "graph continuation finish", method: http.MethodPost, path: projectBase + "/tasks/" + createdTask.ID + "/continuations/not-active:finish", body: `{}`},
		{name: "graph health run", method: http.MethodPost, path: projectBase + "/blackboard/health-runs", body: `{}`},
		{name: "fact upsert", method: http.MethodPut, path: projectBase + "/facts/fact:boundary", body: `{"category":"target","summary":"must not persist","confidence":"confirmed"}`},
		{name: "fact relation", method: http.MethodPut, path: projectBase + "/facts/fact:boundary/relations", body: `{"target_fact_key":"fact:other","relation":"supports"}`},
		{name: "fact merge", method: http.MethodPost, path: projectBase + "/facts/merge", body: `{"source_fact_key":"fact:boundary","canonical_fact_key":"fact:other"}`},
		{name: "finding upsert", method: http.MethodPut, path: projectBase + "/findings/finding:boundary", body: `{"title":"must not persist"}`},
		{name: "finding merge", method: http.MethodPost, path: projectBase + "/findings/merge", body: `{"source_finding_key":"finding:boundary","canonical_finding_key":"finding:other"}`},
		{name: "evidence attach", method: http.MethodPost, path: projectBase + "/evidence", body: `{"evidence_key":"evidence:boundary","attach_to_type":"fact","attach_to_key":"fact:boundary","artifact_type":"text"}`},
		{name: "task summary", method: http.MethodPut, path: projectBase + "/tasks/" + createdTask.ID + "/summary", body: `{"summary":"must not persist","submitted_by":"boundary-test"}`},
		{name: "legacy report generation", method: http.MethodPost, path: projectBase + "/report", body: `{}`},
	}
	for _, tt := range writes {
		t.Run("write/"+tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			assertRetiredBlackboardV1Response(t, response)
		})
	}

	reads := []string{
		projectBase + "/blackboard/runtime-graph",
		projectBase + "/blackboard/work-view",
		projectBase + "/blackboard/current-truth",
		projectBase + "/blackboard/frontier",
		projectBase + "/blackboard/records",
		projectBase + "/blackboard/records:resolve",
		projectBase + "/blackboard/records/node-1",
		projectBase + "/blackboard/records/node-1/history",
		projectBase + "/blackboard/records/node-1/provenance",
		projectBase + "/blackboard/records/node-1/traversal",
		projectBase + "/blackboard/health",
		projectBase + "/blackboard/health-runs/run-1",
		projectBase + "/blackboard/health-runs/run-1/results",
		projectBase + "/blackboard/graph-explorer",
		projectBase + "/blackboard/entities",
		projectBase + "/blackboard/entities/node-1",
		projectBase + "/facts/fact:boundary",
		projectBase + "/facts/fact:boundary/versions",
		projectBase + "/facts/fact:boundary/relations",
		projectBase + "/facts/index",
		projectBase + "/findings",
		projectBase + "/findings/finding:boundary/versions",
		projectBase + "/evidence",
		projectBase + "/tasks/" + createdTask.ID + "/summary",
		projectBase + "/tasks/" + createdTask.ID + "/continuation",
		projectBase + "/reports/pentest",
		projectBase + "/reports/ctf-solution",
	}
	t.Run("dashboard does not read v1 sentinels", func(t *testing.T) {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, projectBase+"/dashboard", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("dashboard status = %d, want 200; body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), blackboardfixture.SentinelSummary) ||
			strings.Contains(response.Body.String(), `"facts":1`) ||
			strings.Contains(response.Body.String(), `"findings":1`) ||
			strings.Contains(response.Body.String(), `"evidence":1`) {
			t.Fatalf("v2 dashboard exposed v1 state: %s", response.Body.String())
		}
	})
	for _, path := range reads {
		t.Run("read/"+path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			assertRetiredBlackboardV1Response(t, response)
		})
	}

	assertV2BootstrapMCPHasNoLegacyTools(t, server)

	after := blackboardfixture.CaptureLegacyState(t, inspectionDB)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("legacy Blackboard state changed through a v2 daemon\nbefore: %#v\nafter:  %#v", before, after)
	}
}

// TestBlackboardV2HandoffResumeIgnoresLegacyRows keeps the Task/Runner resume
// seam alive while proving its prompt is rebuilt without the retired Fact,
// Finding, or Task Summary fallbacks.
func TestBlackboardV2HandoffResumeIgnoresLegacyRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "handoff-v2.db")
	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	projectID := createProject(t, server, `{"name":"Resume","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	inspectionDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open inspection store: %v", err)
	}
	t.Cleanup(func() { _ = inspectionDB.Close() })
	blackboardfixture.SeedLegacyState(t, inspectionDB, projectID, taskID)
	before := blackboardfixture.CaptureLegacyState(t, inspectionDB)
	eventCountBefore := len(getTaskEvents(t, server, projectID, taskID))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume/handoff", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("handoff resume status = %d, want 202; body=%s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	var resumedGoals []string
	var events []map[string]any
	for time.Now().Before(deadline) {
		events = getTaskEvents(t, server, projectID, taskID)
		if eventCountBefore > len(events) {
			t.Fatalf("task event count moved backwards: before=%d after=%d", eventCountBefore, len(events))
		}
		for _, event := range events[eventCountBefore:] {
			if event["kind"] != "runtime_output" {
				continue
			}
			payload, _ := event["payload"].(map[string]any)
			if goal, _ := payload["goal"].(string); goal != "" {
				resumedGoals = append(resumedGoals, goal)
			}
		}
		if len(resumedGoals) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(resumedGoals) == 0 {
		t.Fatalf("resumed fake Runtime emitted no goal after event %d: %#v", eventCountBefore, events)
	}
	for _, goal := range resumedGoals {
		for _, forbidden := range []string{blackboardfixture.SentinelSummary, "fact:v1-sentinel", "finding:v1-sentinel"} {
			if strings.Contains(goal, forbidden) {
				t.Fatalf("handoff resume prompt exposed retired v1 state %q: %s", forbidden, goal)
			}
		}
	}
	waitForTaskStatus(t, server, projectID, taskID, "completed")
	after := blackboardfixture.CaptureLegacyState(t, inspectionDB)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("handoff resume mutated legacy Blackboard state\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func assertV2BootstrapMCPHasNoLegacyTools(t *testing.T, server *daemon.Server) {
	t.Helper()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	ctx := context.Background()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "v2-boundary-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect v2 bootstrap MCP: %v", err)
	}
	defer func() { _ = session.Close() }()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list v2 bootstrap MCP tools: %v", err)
	}
	if len(listed.Tools) != 0 {
		t.Fatalf("v2 bootstrap MCP tools = %#v, want an empty catalog until #114", listed.Tools)
	}
	retiredTools := []string{
		"upsert_project_fact", "deprecate_project_fact", "upsert_fact_relation",
		"record_vulnerability", "attach_evidence", "generate_report", "submit_task_summary",
		"blackboard_apply", "blackboard_retain_evidence", "blackboard_checkpoint_attempt",
		"blackboard_finish_continuation",
	}
	for _, tool := range listed.Tools {
		for _, retired := range retiredTools {
			if tool.Name == retired {
				t.Fatalf("v2 bootstrap MCP exposed retired tool %q", retired)
			}
		}
	}
	for _, retired := range retiredTools {
		_, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: retired, Arguments: map[string]any{}})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf(`unknown tool %q`, retired)) {
			t.Fatalf("retired MCP tool %q call error = %v", retired, err)
		}
	}
}

func assertRetiredBlackboardV1Response(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", response.Code, response.Body.String())
	}
	want := fmt.Sprintf("{\"error\":%q}\n", retiredBlackboardV1Message)
	if response.Body.String() != want {
		t.Fatalf("body = %q, want %q", response.Body.String(), want)
	}
}
