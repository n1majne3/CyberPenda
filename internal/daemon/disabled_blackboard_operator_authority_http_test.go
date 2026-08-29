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

	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

const disabledOutputOperatorToken = "disabled-output-operator-token"

// #252 primary operator seam: an authenticated operator selects one Disabled
// Task file and retains it through an operator-only Project Blackboard route.
func TestOperatorRetainsSelectedDisabledTaskFileWithoutRuntimeBlackboardFeedback(t *testing.T) {
	server, projectID, profileID, factory := newDisabledOutputAuthorityFixture(t)
	created := createDisabledOutputTask(t, server, projectID, profileID)

	base := "/api/v2/projects/" + projectID
	workBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:disabled-review","type":"objective","record":{"status":"open","objective":"Review selected Disabled Task output"}},{"op":"create","key":"attempt:disabled-review","type":"attempt","record":{"status":"open","summary":"Operator reviews selected Disabled Task output"}},{"op":"relate","from":"attempt:disabled-review","relation":"tests","to":"objective:disabled-review"}]}`
	operatorV2Request(t, server, http.MethodPost, base+"/blackboard/changes", "operator-review-work", workBatch, http.StatusOK)

	selectedPath := filepath.Join(server.runtimeRoot, created.ID, "workdir", "selected-output.txt")
	if err := os.WriteFile(selectedPath, []byte("operator-selected proof\n"), 0o600); err != nil {
		t.Fatalf("write selected Disabled Task output: %v", err)
	}
	retainBody := `{"key":"evidence:disabled-selected","attempt":"attempt:disabled-review","source_path":"selected-output.txt","artifact_type":"text","summary":"Operator-retained Disabled Task output","media_type":"text/plain"}`
	retained := operatorV2Request(
		t, server, http.MethodPost,
		base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		"operator-retain-disabled-output", retainBody, http.StatusOK,
	)
	if !bytes.Contains(retained, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("operator Evidence retain result = %s", retained)
	}

	snapshot := operatorV2Request(t, server, http.MethodGet, base+"/blackboard/snapshot", "", "", http.StatusOK)
	if !bytes.Contains(snapshot, []byte(`"evidence:disabled-selected"`)) ||
		!bytes.Contains(snapshot, []byte(`"Operator-retained Disabled Task output"`)) {
		t.Fatalf("Project Blackboard omitted operator-retained Evidence: %s", snapshot)
	}

	var detail task.Task
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID, nil)
	request.Header.Set("Authorization", "Bearer "+disabledOutputOperatorToken)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("read Disabled Task after retain status = %d body %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatalf("decode Disabled Task after retain: %v", err)
	}
	if detail.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeDisabled {
		t.Fatalf("operator retain changed immutable Blackboard Mode to %q", detail.RunControls.BlackboardConclusionMode)
	}
	var evidenceActor, sourceTaskID, sourceContinuationID, sourcePath string
	if err := server.db.QueryRow(`
		SELECT actor_id,source_task_id,source_continuation_id,source_path
		FROM blackboard_v2_operator_evidence_origins
		WHERE project_id=? AND key=? AND version=1`,
		projectID, "evidence:disabled-selected",
	).Scan(&evidenceActor, &sourceTaskID, &sourceContinuationID, &sourcePath); err != nil {
		t.Fatalf("read operator Evidence Trusted Origin: %v", err)
	}
	if evidenceActor != "operator-reviewer" || sourceTaskID != created.ID ||
		detail.LatestContinuation == nil || sourceContinuationID != detail.LatestContinuation.ID ||
		sourcePath != "selected-output.txt" {
		t.Fatalf("operator Evidence Trusted Origin = actor:%q task:%q continuation:%q path:%q", evidenceActor, sourceTaskID, sourceContinuationID, sourcePath)
	}
	var operatorAttemptActor string
	if err := server.db.QueryRow(`
		SELECT actor_id FROM blackboard_v2_operator_attempt_origins
		WHERE project_id=? AND key=?`, projectID, "attempt:disabled-review",
	).Scan(&operatorAttemptActor); err != nil {
		t.Fatalf("read operator Reconciliation Attempt Trusted Origin: %v", err)
	}
	if operatorAttemptActor != "operator-reviewer" {
		t.Fatalf("operator Reconciliation Attempt actor = %q", operatorAttemptActor)
	}
	var runtimeAttemptOrigins int
	if err := server.db.QueryRow(`
		SELECT COUNT(*) FROM blackboard_v2_attempt_origins
		WHERE project_id=? AND key=?`, projectID, "attempt:disabled-review",
	).Scan(&runtimeAttemptOrigins); err != nil {
		t.Fatalf("count Runtime Attempt origins: %v", err)
	}
	if runtimeAttemptOrigins != 0 {
		t.Fatalf("operator Reconciliation Attempt was attributed to a Disabled Runtime")
	}
	if _, err := os.Stat(filepath.Join(server.runtimeRoot, created.ID, "workdir", ".pentest", "blackboard.json")); !os.IsNotExist(err) {
		t.Fatalf("operator retain fed a Working Blackboard Snapshot to the Disabled Runtime: %v", err)
	}
	if launches := factory.Requests(); len(launches) != 1 {
		t.Fatalf("operator retain caused %d Runtime launches, want only the initial launch", len(launches))
	}
}

func TestDisabledRuntimeBlackboardRequestUsesOrdinaryNoGrantAuthorization(t *testing.T) {
	server, projectID, profileID, _ := newDisabledOutputAuthorityFixture(t)
	created := createDisabledOutputTask(t, server, projectID, profileID)
	continuation, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || continuation == nil {
		t.Fatalf("load Disabled Task Continuation: continuation=%#v err=%v", continuation, err)
	}

	var grantCount int
	if err := server.db.QueryRow(
		`SELECT COUNT(*) FROM blackboard_continuation_grants WHERE continuation_id=?`, continuation.ID,
	).Scan(&grantCount); err != nil {
		t.Fatalf("count Disabled Runtime grants: %v", err)
	}
	if grantCount != 0 {
		t.Fatalf("Disabled Runtime retained %d callable Blackboard grants", grantCount)
	}

	body := `{"schema":"semantic-change-batch/v2","changes":[]}`
	request := httptest.NewRequest(
		http.MethodPost, "/api/v2/projects/"+projectID+"/blackboard/changes", strings.NewReader(body),
	)
	request.Header.Set("Idempotency-Key", "disabled-runtime-without-grant")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("ungranted Disabled Runtime status = %d, want %d; body=%s", response.Code, http.StatusUnauthorized, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"code":"authority_denied"`) ||
		strings.Contains(response.Body.String(), "blackboard_disabled") {
		t.Fatalf("ungranted Disabled Runtime did not use ordinary authorization contract: %s", response.Body.String())
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/api/v2/projects/"+projectID+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Idempotency-Key", "ungranted-disabled-retain")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		!strings.Contains(response.Body.String(), `"code":"authority_denied"`) ||
		strings.Contains(response.Body.String(), "blackboard_disabled") {
		t.Fatalf("ungranted Disabled Runtime crossed the operator Evidence boundary: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDisabledOutputEntersReportsAndDashboardOnlyAfterOperatorReconciliation(t *testing.T) {
	server, projectID, profileID, _ := newDisabledOutputAuthorityFixture(t)
	created := createDisabledOutputTask(t, server, projectID, profileID)
	const unreviewed = "UNREVIEWED_DISABLED_RUNTIME_CONCLUSION"
	if err := os.WriteFile(
		filepath.Join(server.runtimeRoot, created.ID, "workdir", "state.txt"), []byte(unreviewed+"\n"), 0o600,
	); err != nil {
		t.Fatalf("write unreviewed Runtime state file: %v", err)
	}
	if _, err := server.tasks.AppendEvent(created.ID, task.EventKindRuntimeOutput, task.EventPayload{"text": unreviewed}); err != nil {
		t.Fatalf("append unreviewed Runtime Transcript output: %v", err)
	}

	base := "/api/v2/projects/" + projectID
	beforeReport := operatorV2Request(t, server, http.MethodGet, base+"/reports/pentest?format=json", "", "", http.StatusOK)
	if bytes.Contains(beforeReport, []byte(unreviewed)) {
		t.Fatalf("Report inferred a conclusion from Disabled Runtime output: %s", beforeReport)
	}
	beforeDashboard := operatorV2Request(t, server, http.MethodGet, "/api/projects/"+projectID+"/dashboard", "", "", http.StatusOK)
	if !bytes.Contains(beforeDashboard, []byte(`"facts":0`)) || !bytes.Contains(beforeDashboard, []byte(`"evidence":0`)) {
		t.Fatalf("Project Dashboard inferred Blackboard records before operator action: %s", beforeDashboard)
	}

	workBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:disabled-reconciliation","type":"objective","record":{"status":"open","objective":"Reconcile selected Disabled Task output"}},{"op":"create","key":"attempt:disabled-reconciliation","type":"attempt","record":{"status":"open","summary":"Operator reviews selected Disabled Task output"}},{"op":"relate","from":"attempt:disabled-reconciliation","relation":"tests","to":"objective:disabled-reconciliation"}]}`
	operatorV2Request(t, server, http.MethodPost, base+"/blackboard/changes", "operator-reconciliation-open", workBatch, http.StatusOK)
	retainBody := `{"key":"evidence:disabled-reconciliation","attempt":"attempt:disabled-reconciliation","source_path":"state.txt","artifact_type":"text","summary":"Operator selected Disabled output for reconciliation","media_type":"text/plain"}`
	operatorV2Request(
		t, server, http.MethodPost, base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		"operator-reconciliation-retain", retainBody, http.StatusOK,
	)
	const acceptedSummary = "Operator-reviewed conclusion from selected Disabled output"
	reconcileBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"fact:disabled-reviewed","type":"fact","record":{"category":"service","summary":"` + acceptedSummary + `","confidence":"tentative","scope_status":"in_scope"}},{"op":"relate","from":"attempt:disabled-reconciliation","relation":"produced","to":"fact:disabled-reviewed"},{"op":"relate","from":"fact:disabled-reviewed","relation":"derived_from","to":"evidence:disabled-reconciliation"},{"op":"transition","key":"attempt:disabled-reconciliation","version":1,"status":"succeeded","summary":"Operator completed reconciliation"}]}`
	operatorV2Request(t, server, http.MethodPost, base+"/blackboard/changes", "operator-reconciliation-accept", reconcileBatch, http.StatusOK)

	afterReport := operatorV2Request(t, server, http.MethodGet, base+"/reports/pentest?format=json", "", "", http.StatusOK)
	if !bytes.Contains(afterReport, []byte(acceptedSummary)) || bytes.Contains(afterReport, []byte(unreviewed)) {
		t.Fatalf("Report did not stay on explicit Blackboard records: %s", afterReport)
	}
	afterDashboard := operatorV2Request(t, server, http.MethodGet, "/api/projects/"+projectID+"/dashboard", "", "", http.StatusOK)
	if !bytes.Contains(afterDashboard, []byte(`"facts":1`)) || !bytes.Contains(afterDashboard, []byte(`"evidence":1`)) {
		t.Fatalf("Project Dashboard did not use actual operator-created Blackboard records: %s", afterDashboard)
	}
	if _, err := os.Stat(filepath.Join(server.runtimeRoot, created.ID, "workdir", ".pentest", "blackboard.json")); !os.IsNotExist(err) {
		t.Fatalf("operator reconciliation fed Blackboard state to the Disabled Runtime: %v", err)
	}
}

func newDisabledOutputAuthorityFixture(t *testing.T) (*Server, string, string, *recordingProviderSessionFactory) {
	t.Helper()
	root := t.TempDir()
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatalf("create Artifact Root: %v", err)
	}
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "disabled-output-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true,
			SendTurn:          true,
		},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		ArtifactRoot: artifactRoot, AuthToken: disabledOutputOperatorToken,
		DisableBuiltinSkills:   true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create(
		"Disabled output authority", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{},
	)
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Disabled Output Provider", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatalf("create Model Provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-disabled-output")
	profile, err := server.profiles.Create("Disabled Output Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "gpt-test", BinaryPath: "/bin/sh", DefaultRunner: "host",
	})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	return server, createdProject.ID, profile.ID, factory
}

func createDisabledOutputTask(t *testing.T, server *Server, projectID, profileID string) task.Task {
	t.Helper()
	body := `{"type":"pentest","goal":"produce selected operator evidence","runtime_profile_id":"` + profileID + `","runner":"host","run_controls":{"host_activated":true,"blackboard_conclusion_mode":"disabled"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+disabledOutputOperatorToken)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Disabled Task status = %d body %s", response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode Disabled Task: %v", err)
	}
	return created
}

func operatorV2Request(t *testing.T, server *Server, method, path, idempotencyKey, body string, wantStatus int) []byte {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+disabledOutputOperatorToken)
	request.Header.Set(projectinterface.OperatorActorHeader, "operator-reviewer")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.Code, wantStatus, response.Body.String())
	}
	return response.Body.Bytes()
}
