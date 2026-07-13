package daemon_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/daemon"
	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestContinuationLaunchAtomicallyPinsConfigGraphGrantAndVerifiesSnapshotBeforeRuntimeStart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "continuation-launch.db")
	runtimeRoot := filepath.Join(t.TempDir(), "runs")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("enable graph epoch: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{
		Version:              "test-version",
		DBPath:               dbPath,
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start graph daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	projectID := createProject(t, server, `{"name":"Atomic launch","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"goal":"enumerate the authorized target",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("launch status = %d body=%s", response.Code, response.Body.String())
	}

	var launched struct {
		ID                 string `json:"id"`
		LatestContinuation struct {
			ID                                  string `json:"id"`
			RuntimeConfigVersionID              string `json:"runtime_config_version_id"`
			BlackboardGraphRevision             int    `json:"blackboard_graph_revision"`
			BlackboardRendererVersion           string `json:"blackboard_renderer_version"`
			BlackboardEstimatorVersion          string `json:"blackboard_estimator_version"`
			BlackboardProjectionHash            string `json:"blackboard_projection_hash"`
			BlackboardProjectionBytes           int    `json:"blackboard_projection_bytes"`
			BlackboardProjectionEstimatedTokens int    `json:"blackboard_projection_estimated_tokens"`
			BlackboardReconciliationStatus      string `json:"blackboard_reconciliation_status"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(response.Body).Decode(&launched); err != nil {
		t.Fatalf("decode launch: %v", err)
	}
	continuation := launched.LatestContinuation
	if continuation.ID == "" || continuation.RuntimeConfigVersionID == "" {
		t.Fatalf("launch did not atomically pin Continuation/config: %+v", continuation)
	}
	if continuation.BlackboardRendererVersion != blackboard.CanonicalMainGraphRendererV1 ||
		continuation.BlackboardEstimatorVersion != blackboard.UTF8BytesDiv4EstimatorV1 ||
		continuation.BlackboardProjectionHash == "" || continuation.BlackboardProjectionBytes <= 0 ||
		continuation.BlackboardProjectionEstimatedTokens <= 0 ||
		continuation.BlackboardReconciliationStatus != string(task.ReconciliationPending) {
		t.Fatalf("invalid graph pin: %+v", continuation)
	}

	workdir := filepath.Join(runtimeRoot, launched.ID, "workdir")
	snapshotPath := filepath.Join(workdir, ".pentest", "blackboard.json")
	pin := blackboard.CanonicalMainGraphPin{
		ProjectID: projectID, GraphRevision: continuation.BlackboardGraphRevision,
		RendererVersion:  continuation.BlackboardRendererVersion,
		EstimatorVersion: continuation.BlackboardEstimatorVersion,
		ProjectionHash:   continuation.BlackboardProjectionHash,
		ProjectionBytes:  continuation.BlackboardProjectionBytes,
		EstimatedTokens:  continuation.BlackboardProjectionEstimatedTokens,
	}
	if err := blackboard.VerifyCanonicalMainGraphSnapshot(pin, snapshotPath); err != nil {
		t.Fatalf("Runtime was launchable without a verified snapshot: %v", err)
	}

	contextBytes, err := os.ReadFile(filepath.Join(workdir, ".pentest", "context.json"))
	if err != nil {
		t.Fatalf("read Runtime Blackboard context: %v", err)
	}
	var runtimeContext map[string]any
	if err := json.Unmarshal(contextBytes, &runtimeContext); err != nil {
		t.Fatalf("decode Runtime Blackboard context: %v", err)
	}
	for key, want := range map[string]any{
		"protocol_version":           float64(projectinterface.RuntimeProtocolVersion),
		"project_id":                 projectID,
		"task_id":                    launched.ID,
		"continuation_id":            continuation.ID,
		"runtime_config_version_id":  continuation.RuntimeConfigVersionID,
		"blackboard_projection_hash": continuation.BlackboardProjectionHash,
	} {
		if got := runtimeContext[key]; got != want {
			t.Fatalf("context %s = %#v want %#v", key, got, want)
		}
	}
	if _, leaked := runtimeContext["interface_token"]; leaked {
		t.Fatal("context.json leaked the Continuation Interface Grant token")
	}

	agents, err := os.ReadFile(filepath.Join(workdir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	claude, err := os.ReadFile(filepath.Join(workdir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	for name, instructions := range map[string][]byte{"AGENTS.md": agents, "CLAUDE.md": claude} {
		text := string(instructions)
		if !strings.Contains(text, "Blackboard Runtime Protocol v1") ||
			!strings.Contains(text, continuation.BlackboardProjectionHash) ||
			!strings.Contains(text, continuation.ID) {
			t.Fatalf("%s does not carry the pinned canonical protocol:\n%s", name, text)
		}
	}

	verificationDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open verification store: %v", err)
	}
	defer verificationDB.Close()
	var grantCount, configCount int
	if err := verificationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM blackboard_continuation_grants WHERE continuation_id=? AND runtime_config_version_id=?`, continuation.ID, continuation.RuntimeConfigVersionID).Scan(&grantCount); err != nil {
		t.Fatalf("read atomic grant: %v", err)
	}
	if err := verificationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM task_runtime_config_versions WHERE id=? AND task_id=?`, continuation.RuntimeConfigVersionID, launched.ID).Scan(&configCount); err != nil {
		t.Fatalf("read atomic runtime config: %v", err)
	}
	if grantCount != 1 || configCount != 1 {
		t.Fatalf("atomic rows: grant=%d config=%d", grantCount, configCount)
	}
	var configJSON string
	if err := verificationDB.QueryRow(`SELECT config_json FROM task_runtime_config_versions WHERE id=?`, continuation.RuntimeConfigVersionID).Scan(&configJSON); err != nil {
		t.Fatalf("read captured Task Runtime Configuration: %v", err)
	}
	var capturedConfig map[string]any
	if err := json.Unmarshal([]byte(configJSON), &capturedConfig); err != nil {
		t.Fatalf("decode captured Task Runtime Configuration: %v", err)
	}
	if capturedConfig["runtime_plugin_id"] != "fake" || capturedConfig["runtime_profile_id"] != profileID {
		t.Fatalf("captured Task Runtime Configuration = %#v", capturedConfig)
	}
	for _, projectionOnly := range []string{"launch_command", "layout", "interface_token"} {
		if _, persisted := capturedConfig[projectionOnly]; persisted {
			t.Fatalf("captured Task Runtime Configuration persisted projection-only %s", projectionOnly)
		}
	}
	snapshotBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read snapshot for adapter assertion: %v", err)
	}
	var payloadJSON string
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = verificationDB.QueryRow(`SELECT payload_json FROM task_events WHERE task_id=? AND kind='runtime_output' AND payload_json LIKE '%"goal"%' ORDER BY seq LIMIT 1`, launched.ID).Scan(&payloadJSON)
		if err == nil || !errors.Is(err, sql.ErrNoRows) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read fake adapter launch context: %v", err)
	}
	var runtimeOutput map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &runtimeOutput); err != nil {
		t.Fatalf("decode fake adapter output: %v", err)
	}
	adapterContext, _ := runtimeOutput["goal"].(string)
	if !strings.Contains(adapterContext, "<<< CURRENT CONTINUATION SNAPSHOT >>>") ||
		!strings.Contains(adapterContext, string(snapshotBytes)) ||
		!strings.Contains(adapterContext, "TASK GOAL:\nenumerate the authorized target") {
		t.Fatalf("fake adapter did not receive the exact current full graph context: %q", adapterContext)
	}
}

func TestGraphEpochStartupRegeneratesCommittedContinuationFilesWithoutRepinning(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "continuation-recovery.db")
	runtimeRoot := filepath.Join(t.TempDir(), "runs")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("enable graph epoch: %v", err)
	}
	_ = db.Close()

	server, err := daemon.NewServer(daemon.Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	projectID := createProject(t, server, `{"name":"Recovery"}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"goal":"recover committed files",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("launch status=%d body=%s", response.Code, response.Body.String())
	}
	var launched struct {
		ID                 string `json:"id"`
		LatestContinuation struct {
			ID                                  string `json:"id"`
			BlackboardGraphRevision             int    `json:"blackboard_graph_revision"`
			BlackboardRendererVersion           string `json:"blackboard_renderer_version"`
			BlackboardEstimatorVersion          string `json:"blackboard_estimator_version"`
			BlackboardProjectionHash            string `json:"blackboard_projection_hash"`
			BlackboardProjectionBytes           int    `json:"blackboard_projection_bytes"`
			BlackboardProjectionEstimatedTokens int    `json:"blackboard_projection_estimated_tokens"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(response.Body).Decode(&launched); err != nil {
		t.Fatalf("decode launch: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close first daemon: %v", err)
	}
	workdir := filepath.Join(runtimeRoot, launched.ID, "workdir")
	if err := os.RemoveAll(filepath.Join(workdir, ".pentest")); err != nil {
		t.Fatalf("remove committed context files: %v", err)
	}
	_ = os.Remove(filepath.Join(workdir, "AGENTS.md"))
	_ = os.Remove(filepath.Join(workdir, "CLAUDE.md"))

	crashDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open crash image: %v", err)
	}
	if _, err := crashDB.Exec(`UPDATE tasks SET status='pending' WHERE id=?`, launched.ID); err != nil {
		t.Fatalf("restore pending Task state: %v", err)
	}
	if _, err := crashDB.Exec(`UPDATE task_continuations SET status='pending',ended_at='' WHERE id=?`, launched.LatestContinuation.ID); err != nil {
		t.Fatalf("restore pending Continuation state: %v", err)
	}
	if _, err := crashDB.Exec(`DELETE FROM runtime_profiles WHERE id=?`, profileID); err != nil {
		t.Fatalf("delete live Runtime Profile before historical recovery: %v", err)
	}
	_ = crashDB.Close()

	restarted, err := daemon.NewServer(daemon.Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("restart daemon: %v", err)
	}
	defer restarted.Close()
	pin := blackboard.CanonicalMainGraphPin{
		ProjectID: projectID, GraphRevision: launched.LatestContinuation.BlackboardGraphRevision,
		RendererVersion:  launched.LatestContinuation.BlackboardRendererVersion,
		EstimatorVersion: launched.LatestContinuation.BlackboardEstimatorVersion,
		ProjectionHash:   launched.LatestContinuation.BlackboardProjectionHash,
		ProjectionBytes:  launched.LatestContinuation.BlackboardProjectionBytes,
		EstimatedTokens:  launched.LatestContinuation.BlackboardProjectionEstimatedTokens,
	}
	if err := blackboard.VerifyCanonicalMainGraphSnapshot(pin, filepath.Join(workdir, ".pentest", "blackboard.json")); err != nil {
		t.Fatalf("restart did not regenerate the committed pin: %v", err)
	}
	for _, name := range []string{".pentest/context.json", "AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(workdir, name)); err != nil {
			t.Fatalf("restart did not restore %s: %v", name, err)
		}
	}
	verifyDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open verification store: %v", err)
	}
	defer verifyDB.Close()
	var count int
	if err := verifyDB.QueryRow(`SELECT COUNT(*) FROM task_continuations WHERE task_id=?`, launched.ID).Scan(&count); err != nil {
		t.Fatalf("count Continuations: %v", err)
	}
	if count != 1 {
		t.Fatalf("restart repinned the Task with %d Continuations", count)
	}
}
