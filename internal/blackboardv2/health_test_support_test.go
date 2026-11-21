package blackboardv2_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

type healthFixture struct {
	db             *store.DB
	dbPath         string
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	workdir        string
	runtimeRoot    string
}

func newHealthFixture(t *testing.T, kind string) healthFixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "pentest.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open health store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.CreateWithKind("Health fixture", "", kind, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Semantic health", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-health", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	return healthFixture{
		db: db, dbPath: dbPath,
		service: blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{
			RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot,
		}),
		projectID: createdProject.ID, continuationID: continuation.ID,
		workdir: workdir, runtimeRoot: runtimeRoot,
	}
}

func assertHealthyEmpty(t *testing.T, health blackboardv2.SemanticHealth) {
	t.Helper()
	if health.Schema != "blackboard-health/v2" {
		t.Fatalf("schema = %q", health.Schema)
	}
	if health.Revision != 0 || health.Status != "healthy" {
		t.Fatalf("empty health = %#v", health)
	}
	if health.Attention.State != blackboardv2.AttentionWithinTarget || !health.Attention.Complete || !health.Attention.Launchable {
		t.Fatalf("empty attention = %#v", health.Attention)
	}
	if health.Attention.ConsolidationOffered || health.Attention.ConsolidationRequired {
		t.Fatalf("empty consolidation flags set: %#v", health.Attention)
	}
	if len(health.Anomalies) != 0 {
		t.Fatalf("empty anomalies = %#v", health.Anomalies)
	}
	if len(health.Proposals) != 0 {
		t.Fatalf("empty proposals = %#v", health.Proposals)
	}
}

func assertNoAuditNoise(t *testing.T, health blackboardv2.SemanticHealth) {
	t.Helper()
	raw := mustHealthJSON(t, health)
	for _, forbidden := range []string{
		"provenance", "state_hash", "projection_hash", "semantic_hash",
		"graph_hash", "history_hash", "operation_history", "recent_changes",
		"audit", "checker_version", "health_run", "sqlite_integrity",
		"frontier", "current_truth", "project_id", "trusted_origin",
	} {
		if bytes.Contains(bytes.ToLower(raw), []byte(forbidden)) {
			t.Fatalf("health leaked audit/provenance noise %q: %s", forbidden, raw)
		}
	}
}

func assertHasAnomalyCodes(t *testing.T, health blackboardv2.SemanticHealth, codes []string) {
	t.Helper()
	present := map[string]bool{}
	for _, anomaly := range health.Anomalies {
		present[anomaly.Code] = true
	}
	for _, code := range codes {
		if !present[code] {
			t.Fatalf("missing anomaly code %q in %#v", code, health.Anomalies)
		}
	}
}

func assertNoAnomalyCode(t *testing.T, health blackboardv2.SemanticHealth, code string) {
	t.Helper()
	for _, anomaly := range health.Anomalies {
		if anomaly.Code == code {
			t.Fatalf("unexpected anomaly %q: %#v", code, anomaly)
		}
	}
}

func anomalyByCode(t *testing.T, health blackboardv2.SemanticHealth, code string) blackboardv2.HealthAnomaly {
	t.Helper()
	for _, anomaly := range health.Anomalies {
		if anomaly.Code == code {
			return anomaly
		}
	}
	t.Fatalf("anomaly %q not found in %#v", code, health.Anomalies)
	return blackboardv2.HealthAnomaly{}
}

func containsKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}

func mustHealthJSON(t *testing.T, health blackboardv2.SemanticHealth) []byte {
	t.Helper()
	raw, err := json.Marshal(health)
	if err != nil {
		t.Fatalf("marshal health: %v", err)
	}
	// Anomalies must be sorted deterministically in the marshaled form.
	var decoded blackboardv2.SemanticHealth
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !reflect.DeepEqual(decoded.Anomalies, health.Anomalies) {
		t.Fatalf("anomaly order unstable under marshal")
	}
	return raw
}
