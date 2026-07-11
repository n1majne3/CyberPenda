package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/daemon"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestHealthEndpointReportsVersionAndDatabaseStatus(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}

	var body struct {
		Version  string `json:"version"`
		Database struct {
			Status string `json:"status"`
		} `json:"database"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if body.Version != "test-version" {
		t.Fatalf("expected version test-version, got %q", body.Version)
	}
	if body.Database.Status != "ok" {
		t.Fatalf("expected database status ok, got %q", body.Database.Status)
	}
}

func TestProjectCanBeCreatedAndReadWithScope(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	createBody := []byte(`{
		"name": "Acme External",
		"description": "External perimeter test",
		"scope": {
			"domains": ["example.com"],
			"urls": ["https://example.com"],
			"excluded": ["admin.example.com"],
			"testing_limits": ["no destructive payloads"]
		}
	}`)

	createRequest := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(createBody))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()

	server.ServeHTTP(createResponse, createRequest)

	if createResponse.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d with body %s", createResponse.Code, createResponse.Body.String())
	}

	var created struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       struct {
			Domains       []string `json:"domains"`
			URLs          []string `json:"urls"`
			Excluded      []string `json:"excluded"`
			TestingLimits []string `json:"testing_limits"`
		} `json:"scope"`
	}
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected project id")
	}
	if created.Name != "Acme External" {
		t.Fatalf("expected project name Acme External, got %q", created.Name)
	}
	if got := created.Scope.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope domain example.com, got %#v", got)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+created.ID, nil)
	getResponse := httptest.NewRecorder()

	server.ServeHTTP(getResponse, getRequest)

	if getResponse.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d with body %s", getResponse.Code, getResponse.Body.String())
	}

	var fetched struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       struct {
			Domains       []string `json:"domains"`
			URLs          []string `json:"urls"`
			Excluded      []string `json:"excluded"`
			TestingLimits []string `json:"testing_limits"`
		} `json:"scope"`
	}
	if err := json.NewDecoder(getResponse.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}

	if fetched.ID != created.ID {
		t.Fatalf("expected fetched id %q, got %q", created.ID, fetched.ID)
	}
	if fetched.Description != "External perimeter test" {
		t.Fatalf("expected description External perimeter test, got %q", fetched.Description)
	}
	if got := fetched.Scope.TestingLimits; len(got) != 1 || got[0] != "no destructive payloads" {
		t.Fatalf("expected testing limit, got %#v", got)
	}
}

func TestProjectsCanBeListedForDashboard(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	createProject(t, server, `{"name":"First","scope":{"domains":["first.example"]}}`)
	createProject(t, server, `{"name":"Second","scope":{"domains":["second.example"]}}`)

	request := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d with body %s", response.Code, response.Body.String())
	}

	var body struct {
		Projects []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Scope struct {
				Domains []string `json:"domains"`
			} `json:"scope"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode list response: %v", err)
	}

	if len(body.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(body.Projects))
	}
	if body.Projects[0].Name != "First" {
		t.Fatalf("expected first project First, got %q", body.Projects[0].Name)
	}
	if body.Projects[1].Name != "Second" {
		t.Fatalf("expected second project Second, got %q", body.Projects[1].Name)
	}
}

func TestCreateProjectPersistsDefaults(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	id := createProject(t, server, `{
		"name": "Acme",
		"defaults": {"runner": "sandbox", "runtime_profile": "codex-default"}
	}`)

	var fetched struct {
		Defaults struct {
			Runner         string `json:"runner"`
			RuntimeProfile string `json:"runtime_profile"`
		} `json:"defaults"`
	}
	getProject(t, server, id, &fetched)

	if fetched.Defaults.Runner != "sandbox" {
		t.Fatalf("expected default runner sandbox, got %q", fetched.Defaults.Runner)
	}
	if fetched.Defaults.RuntimeProfile != "codex-default" {
		t.Fatalf("expected default runtime profile codex-default, got %q", fetched.Defaults.RuntimeProfile)
	}
}

func TestPatchProjectUpdatesNameAndScope(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	id := createProject(t, server, `{"name":"Original","scope":{"domains":["example.com"],"notes":"original"}}`)

	patchBody := []byte(`{
		"name": "Renamed",
		"scope": {"domains": ["acme.example"], "testing_limits": ["business hours"]}
	}`)
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/projects/"+id, bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	server.ServeHTTP(patchResponse, patchRequest)

	if patchResponse.Code != http.StatusOK {
		t.Fatalf("expected patch status 200, got %d with body %s", patchResponse.Code, patchResponse.Body.String())
	}

	var patched struct {
		Name  string `json:"name"`
		Scope struct {
			Domains       []string `json:"domains"`
			Notes         string   `json:"notes"`
			TestingLimits []string `json:"testing_limits"`
		} `json:"scope"`
	}
	if err := json.NewDecoder(patchResponse.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if patched.Name != "Renamed" {
		t.Fatalf("expected renamed project, got %q", patched.Name)
	}
	if got := patched.Scope.Domains; len(got) != 1 || got[0] != "acme.example" {
		t.Fatalf("expected scope domains overwritten, got %#v", got)
	}
	if patched.Scope.Notes != "" {
		t.Fatalf("expected scope notes cleared by full-scope overwrite, got %q", patched.Scope.Notes)
	}
	if got := patched.Scope.TestingLimits; len(got) != 1 || got[0] != "business hours" {
		t.Fatalf("expected new testing limits, got %#v", got)
	}

	// The update must persist across reloads.
	var fetched struct {
		Name  string `json:"name"`
		Scope struct {
			Domains []string `json:"domains"`
		} `json:"scope"`
	}
	getProject(t, server, id, &fetched)
	if fetched.Name != "Renamed" {
		t.Fatalf("expected persisted rename, got %q", fetched.Name)
	}
	if got := fetched.Scope.Domains; len(got) != 1 || got[0] != "acme.example" {
		t.Fatalf("expected persisted scope, got %#v", got)
	}
}

func TestPatchProjectPreservesUntouchedScope(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	id := createProject(t, server, `{"name":"Original","scope":{"domains":["example.com"],"notes":"keep me"}}`)

	patchBody := []byte(`{"description":"only description changes"}`)
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/projects/"+id, bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	server.ServeHTTP(patchResponse, patchRequest)

	if patchResponse.Code != http.StatusOK {
		t.Fatalf("expected patch status 200, got %d with body %s", patchResponse.Code, patchResponse.Body.String())
	}

	var fetched struct {
		Scope struct {
			Domains []string `json:"domains"`
			Notes   string   `json:"notes"`
		} `json:"scope"`
	}
	getProject(t, server, id, &fetched)
	if got := fetched.Scope.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected untouched scope preserved, got %#v", got)
	}
	if fetched.Scope.Notes != "keep me" {
		t.Fatalf("expected untouched notes preserved, got %q", fetched.Scope.Notes)
	}
}

func TestPatchProjectRejectsBlankName(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	id := createProject(t, server, `{"name":"Original"}`)

	patchBody := []byte(`{"name":"   "}`)
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/projects/"+id, bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	server.ServeHTTP(patchResponse, patchRequest)

	if patchResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected blank-name patch to fail with 400, got %d with body %s", patchResponse.Code, patchResponse.Body.String())
	}
}

func TestPatchProjectReturnsNotFoundForUnknownId(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	patchBody := []byte(`{"name":"Anything"}`)
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/projects/missing", bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	server.ServeHTTP(patchResponse, patchRequest)

	if patchResponse.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown id, got %d with body %s", patchResponse.Code, patchResponse.Body.String())
	}
}

func TestListProjectsReturnsArrayShape(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	request := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", response.Code)
	}
	var body struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode empty list response: %v", err)
	}
	if body.Projects == nil {
		t.Fatal("expected projects array, got null")
	}
}

func getProject(t *testing.T, server *daemon.Server, id string, target any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+id, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d with body %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
}

// TestNewServerReconcilesGhostTasksOnRestart proves that a task left running
// by a previous daemon instance is marked interrupted when a new daemon opens
// the same database, with a lifecycle event recording why.
func TestNewServerReconcilesGhostTasksOnRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pentest.db")

	// First instance: create a project + profile + task, leave it running.
	first, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath})
	if err != nil {
		t.Fatalf("first NewServer: %v", err)
	}
	projectID := createProject(t, first, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, first, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, first, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	// Second instance reopens the same DB; reconcile should interrupt the task.
	second, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath})
	if err != nil {
		t.Fatalf("second NewServer: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	resp := httptest.NewRecorder()
	second.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.Code, resp.Body.String())
	}
	var got struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if got.Status != "interrupted" {
		t.Fatalf("expected ghost task interrupted, got %q", got.Status)
	}

	events := getTaskEvents(t, second, projectID, taskID)
	found := false
	for _, e := range events {
		if payload, _ := e["payload"].(map[string]any); payload["phase"] == "interrupted" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an interrupted lifecycle event, got %#v", events)
	}
}

func TestNewServerStopsGhostSandboxContainersOnRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pentest.db")
	dockerLog := filepath.Join(dir, "docker.log")
	dockerCLI := filepath.Join(dir, "fake-docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(dockerLog) + "\n" +
		"if [ \"$1\" = \"stop\" ]; then exit 0; fi\n" +
		"if [ \"$1\" = \"kill\" ]; then exit 0; fi\n" +
		"if [ \"$1\" = \"rm\" ]; then exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(dockerCLI, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	projects := project.NewService(db)
	tasks := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	created, err := tasks.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Goal:             "ghost sandbox",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(created.ID, "profile-1", "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationRuntimeMetadata(continuation.ID, "ctr-ghost", "", ""); err != nil {
		t.Fatalf("record container id: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("set continuation running: %v", err)
	}
	if _, err := tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatalf("set task running: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, ContainerCLI: dockerCLI})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	raw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	log := string(raw)
	if !strings.Contains(log, "stop ctr-ghost") {
		t.Fatalf("expected docker stop for ghost container, got:\n%s", log)
	}
	if !strings.Contains(log, "rm -f ctr-ghost") {
		t.Fatalf("expected docker rm for ghost container, got:\n%s", log)
	}
}

func createProject(t *testing.T, server *daemon.Server, body string) string {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d with body %s", response.Code, response.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created.ID
}

func TestGraphEpochStartupRepairsTaskGoalsBeforeServing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph-startup.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	projects := project.NewService(db)
	projectRow, err := projects.Create("Graph project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	created, err := tasks.Create(task.CreateRequest{ProjectID: projectRow.ID, Goal: "Repair at startup", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create unprojected task: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("enable graph epoch: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start graph-epoch server: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close graph-epoch server: %v", err)
	}

	verifyDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen graph store: %v", err)
	}
	defer verifyDB.Close()
	graph := blackboard.NewGraphService(verifyDB, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	goal, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectRow.ID, NodeType: blackboard.NodeTypeGoal, Key: "task:" + created.ID + ":goal"})
	if err != nil {
		t.Fatalf("read startup-repaired Goal: %v", err)
	}
	durable, err := task.NewService(verifyDB).Get(created.ID)
	if err != nil {
		t.Fatalf("read reconciled Task: %v", err)
	}
	if goal.Node.PropertyMap["text"] != created.Goal || goal.Node.PropertyMap["task_status"] != string(durable.Status) {
		t.Fatalf("startup Goal projection: task=%+v goal=%+v", durable, goal.Node)
	}
}

func TestBlackboardRecordsHTTPUsesCanonicalEnvelopeETagAndConditionalRead(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "blackboard-read.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	projects := project.NewService(db)
	projectRow, err := projects.Create("Read project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	graph := blackboard.NewGraphService(db, blackboard.NewSequenceClock("2024-01-02T03:04:05Z"), blackboard.RandomIDSource{})
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "u01:http",
		Context: blackboard.SystemExecutionContext(projectRow.ID, projectRow.Kind, "test-system"),
		Operations: []blackboard.Operation{
			{OpID: "a", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:a"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "A", "scope_status": "in_scope"}}},
			{OpID: "b", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:b"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "B", "scope_status": "in_scope"}}},
		},
	}); err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("enable graph epoch: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer server.Close()

	url := "/api/projects/" + projectRow.ID + "/blackboard/records?node_type=project_fact&sort=stable_key&limit=1"
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodGet, url, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q want strong quoted tag", etag)
	}
	if got := first.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var envelope blackboard.ReadEnvelope
	if err := json.NewDecoder(first.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Projection != string(blackboard.ReadKindRecordCollectionV1) || envelope.ProjectID != projectRow.ID || envelope.ProjectionHash == "" {
		t.Fatalf("envelope = %+v", envelope)
	}

	conditionalRequest := httptest.NewRequest(http.MethodGet, url, nil)
	conditionalRequest.Header.Set("If-None-Match", etag)
	conditional := httptest.NewRecorder()
	server.ServeHTTP(conditional, conditionalRequest)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional status=%d body=%s", conditional.Code, conditional.Body.String())
	}

	differentQuery := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectRow.ID+"/blackboard/records?node_type=project_fact&sort=stable_key&limit=2", nil)
	differentQuery.Header.Set("If-None-Match", etag)
	different := httptest.NewRecorder()
	server.ServeHTTP(different, differentQuery)
	if different.Code != http.StatusOK || different.Header().Get("ETag") == etag {
		t.Fatalf("different query reused ETag: status=%d etag=%q body=%s", different.Code, different.Header().Get("ETag"), different.Body.String())
	}
}
