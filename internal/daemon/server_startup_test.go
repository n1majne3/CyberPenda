package daemon_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
	"pentest/internal/task"
)

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
	binary := filepath.Join(t.TempDir(), "ghost-runtime")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho ghost-runtime-started\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatalf("write ghost Runtime: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, first, "Ghost Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})
	taskID := createTask(t, first, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForEventText(t, first, projectID, taskID, "ghost-runtime-started")
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
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "ghost sandbox",
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

func TestDaemonStartupRefusesUnsupportedSchemaWithStableGuidance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open unsupported schema fixture: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY)`); err != nil {
		_ = legacy.Close()
		t.Fatalf("create unsupported schema fixture: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close unsupported schema fixture: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, ListenAddr: "127.0.0.1:0", DisableBuiltinSkills: true})
	if server != nil {
		_ = server.Close()
		t.Fatal("unsupported schema startup returned an active daemon server")
	}
	if !errors.Is(err, store.ErrUnsupportedStoreSchema) {
		t.Fatalf("unsupported schema startup error = %v, want ErrUnsupportedStoreSchema", err)
	}

	after, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen refused source: %v", err)
	}
	defer after.Close()
	var tables int
	if err := after.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&tables); err != nil {
		t.Fatalf("inspect refused source: %v", err)
	}
	if tables != 1 {
		t.Fatalf("refused daemon startup changed the source schema, projects table count = %d", tables)
	}
}

func TestDaemonStartupRefusesUnknownEpochWithoutActiveServer(t *testing.T) {
	const unknownEpoch = "future_v3"
	dbPath := filepath.Join(t.TempDir(), "unknown-epoch.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap disposable v2 store: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=? WHERE id=1`, unknownEpoch); err != nil {
		_ = db.Close()
		t.Fatalf("set unknown canonical store epoch: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close unknown-epoch fixture: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, ListenAddr: "127.0.0.1:0", DisableBuiltinSkills: true})
	if server != nil {
		_ = server.Close()
		t.Fatal("unknown epoch startup returned an active daemon server")
	}
	if !errors.Is(err, store.ErrUnsupportedStoreSchema) {
		t.Fatalf("unknown epoch startup error = %v, want ErrUnsupportedStoreSchema", err)
	}

	after, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen refused unknown-epoch source: %v", err)
	}
	defer after.Close()
	var afterEpoch string
	if err := after.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&afterEpoch); err != nil {
		t.Fatalf("read refused unknown epoch: %v", err)
	}
	if afterEpoch != unknownEpoch {
		t.Fatalf("refused source epoch = %q, want %q", afterEpoch, unknownEpoch)
	}
}
