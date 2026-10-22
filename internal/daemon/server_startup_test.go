package daemon_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
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

func TestDaemonStartupRefusesV1WithStableOfflineMigrationGuidance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph-v1.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap disposable store: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE blackboard_store_state
		SET canonical_store='graph_v1', cutover_state='graph',
		    migration_contract_version='legacy_blackboard_to_graph_v1', graph_schema_version=1
		WHERE id=1;
		DELETE FROM schema_migrations WHERE version=20;
	`); err != nil {
		t.Fatalf("construct graph_v1 source fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close graph_v1 source fixture: %v", err)
	}

	beforeSource, err := store.OpenMigrationSource(dbPath)
	if err != nil {
		t.Fatalf("open graph_v1 through offline source seam: %v", err)
	}
	beforeChecksums := migrationChecksumSet(t, beforeSource.DB)
	if err := beforeSource.Close(); err != nil {
		t.Fatalf("close graph_v1 source: %v", err)
	}
	beforeBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read graph_v1 source bytes: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, ListenAddr: "127.0.0.1:0", DisableBuiltinSkills: true})
	if server != nil {
		_ = server.Close()
		t.Fatal("v1 startup returned an active daemon server")
	}
	want := "blackboard v1 store epoch \"graph_v1\" cannot be opened for daemon/runtime use; stop the daemon and run 'blackboard v2 inspect', 'blackboard v2 migrate', then 'blackboard v2 verify'"
	if err == nil || err.Error() != want {
		t.Fatalf("v1 daemon startup error = %v, want %q", err, want)
	}
	afterBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("reread graph_v1 source bytes: %v", err)
	}
	if !bytes.Equal(afterBytes, beforeBytes) {
		t.Fatal("refused daemon startup changed graph_v1 source bytes")
	}

	afterSource, err := store.OpenMigrationSource(dbPath)
	if err != nil {
		t.Fatalf("reopen refused graph_v1 source: %v", err)
	}
	defer afterSource.Close()
	if afterSource.CanonicalStore() != store.CanonicalStoreGraphV1 {
		t.Fatalf("refused source epoch = %q, want graph_v1", afterSource.CanonicalStore())
	}
	afterChecksums := migrationChecksumSet(t, afterSource.DB)
	if strings.Join(afterChecksums, "\n") != strings.Join(beforeChecksums, "\n") {
		t.Fatalf("refused daemon startup changed migration checksums: before=%v after=%v", beforeChecksums, afterChecksums)
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

	beforeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open unknown-epoch fixture for verification: %v", err)
	}
	beforeChecksums := migrationChecksumSet(t, beforeDB)
	if err := beforeDB.Close(); err != nil {
		t.Fatalf("close unknown-epoch verification DB: %v", err)
	}
	beforeBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read unknown-epoch source bytes: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, ListenAddr: "127.0.0.1:0", DisableBuiltinSkills: true})
	if server != nil {
		_ = server.Close()
		t.Fatal("unknown epoch startup returned an active daemon server")
	}
	want := "unknown canonical store epoch \"future_v3\" cannot be opened for daemon/runtime use; restore a supported database or use an explicit offline migration workflow"
	if err == nil || err.Error() != want {
		t.Fatalf("unknown epoch daemon startup error = %v, want %q", err, want)
	}

	afterBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("reread unknown-epoch source bytes: %v", err)
	}
	if !bytes.Equal(afterBytes, beforeBytes) {
		t.Fatal("refused daemon startup changed unknown-epoch source bytes")
	}
	afterDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen refused unknown-epoch source: %v", err)
	}
	defer afterDB.Close()
	var afterEpoch string
	if err := afterDB.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&afterEpoch); err != nil {
		t.Fatalf("read refused unknown epoch: %v", err)
	}
	if afterEpoch != unknownEpoch {
		t.Fatalf("refused source epoch = %q, want %q", afterEpoch, unknownEpoch)
	}
	afterChecksums := migrationChecksumSet(t, afterDB)
	if strings.Join(afterChecksums, "\n") != strings.Join(beforeChecksums, "\n") {
		t.Fatalf("refused daemon startup changed migration checksums: before=%v after=%v", beforeChecksums, afterChecksums)
	}
}

func migrationChecksumSet(t *testing.T, db interface {
	Query(string, ...any) (*sql.Rows, error)
}) []string {
	t.Helper()
	rows, err := db.Query(`SELECT printf('%d|%s|%s',version,name,checksum) FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("read migration checksum set: %v", err)
	}
	defer rows.Close()
	var checksums []string
	for rows.Next() {
		var checksum string
		if err := rows.Scan(&checksum); err != nil {
			t.Fatalf("scan migration checksum: %v", err)
		}
		checksums = append(checksums, checksum)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration checksums: %v", err)
	}
	return checksums
}
