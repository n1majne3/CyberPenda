package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

const disabledOutputOperatorToken = "disabled-output-operator-token"

func TestDefaultLoopbackGeneratedOperatorRetainsSelectedDisabledAttachment(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, "")
	contents := []byte("operator-selected proof\n")
	created := createDisabledOutputTask(t, fixture, task.BlackboardModeDisabled, "selected-output.txt", contents)
	before := readDisabledOutputTask(t, fixture, created.ID)
	base := "/api/v2/projects/" + fixture.projectID
	workBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:disabled-review","type":"objective","record":{"status":"open","objective":"Review selected Disabled Task output"}},{"op":"create","key":"attempt:disabled-review","type":"attempt","record":{"status":"open","summary":"Operator reviews selected Disabled Task output"}},{"op":"relate","from":"attempt:disabled-review","relation":"tests","to":"objective:disabled-review"}]}`
	mustOperatorV2Request(t, fixture, "operator-reviewer", http.MethodPost, base+"/blackboard/changes", "operator-review-work", workBatch, http.StatusOK)
	retainBody := `{"key":"evidence:disabled-selected","attempt":"attempt:disabled-review","source_path":"selected-output.txt","artifact_type":"text","summary":"Operator-retained Disabled Task output","media_type":"text/plain"}`
	retained := mustOperatorV2Request(
		t, fixture, "operator-reviewer", http.MethodPost,
		base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		"operator-retain-disabled-output", retainBody, http.StatusOK,
	)
	var retainResult blackboardv2.ChangeResult
	if err := json.Unmarshal(retained, &retainResult); err != nil || retainResult.Schema != "semantic-change-result/v2" {
		t.Fatalf("decode operator Evidence retain result: result=%#v err=%v body=%s", retainResult, err, retained)
	}

	var evidence blackboardv2.CurrentDetail
	getDisabledOutputJSON(t, fixture, base+"/blackboard/records/evidence:disabled-selected", fixture.operatorToken, &evidence)
	digest := sha256.Sum256(contents)
	if evidence.Key != "evidence:disabled-selected" || evidence.Type != "evidence" ||
		evidence.Record.SourcePath != "selected-output.txt" || evidence.Record.ManagedPath == "" ||
		evidence.Record.SHA256 != hex.EncodeToString(digest[:]) || evidence.Record.Size != int64(len(contents)) {
		t.Fatalf("public retained Evidence detail = %#v", evidence)
	}

	after := readDisabledOutputTask(t, fixture, created.ID)
	if after.RunControls.BlackboardMode != task.BlackboardModeDisabled ||
		before.LatestContinuation == nil || after.LatestContinuation == nil ||
		before.LatestContinuation.ID != after.LatestContinuation.ID {
		t.Fatalf("operator retain changed Disabled Task mode or Continuation: before=%#v after=%#v", before, after)
	}
	for _, path := range []string{
		"/api/projects/" + fixture.projectID + "/tasks/" + created.ID + "/timeline",
		"/api/projects/" + fixture.projectID + "/tasks/" + created.ID + "/transcript",
	} {
		body := getDisabledOutputBody(t, fixture, path, fixture.operatorToken, http.StatusOK)
		if bytes.Contains(body, []byte("evidence:disabled-selected")) || bytes.Contains(body, []byte("Operator-retained Disabled Task output")) {
			t.Fatalf("operator Evidence was fed back through public Task state %s: %s", path, body)
		}
	}
}

func TestDisabledRuntimeBlackboardRequestUsesOrdinaryNoGrantAuthorization(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, disabledOutputOperatorToken)
	created := createDisabledOutputTask(t, fixture, task.BlackboardModeDisabled, "", nil)
	assertDisabledNoGrantBoundaries(t, fixture, created.ID, "configured")
}

func TestDefaultLoopbackRejectsTokenlessDisabledRuntimeBlackboardAuthority(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, "")
	created := createDisabledOutputTask(t, fixture, task.BlackboardModeDisabled, "", nil)
	assertDisabledNoGrantBoundaries(t, fixture, created.ID, "default-loopback")

	response := disabledOutputRequest(
		fixture, http.MethodGet, "/api/v2/sessions/host-runtime/blackboard/snapshot",
		"", "forged-host-runtime", "", "",
	)
	assertOrdinaryNoGrantResponse(t, response, "tokenless default-loopback Session Blackboard read")
}

func TestConfiguredOperatorTokenIsNotExposedAsGeneratedAccessURL(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, disabledOutputOperatorToken)
	if accessURL := fixture.server.GeneratedOperatorAccessURL(); accessURL != "" {
		t.Fatalf("configured operator credential was exposed through generated access seam: %q", accessURL)
	}
}

func TestInteractiveGrantIsFullAndWorkingGraphGrantIsReadOnly(t *testing.T) {
	for _, mode := range []task.BlackboardMode{
		task.BlackboardModeInteractive,
		task.BlackboardModeWorkingGraph,
	} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newDisabledOutputAuthorityFixture(t, "")
			created := createDisabledOutputTask(t, fixture, mode, "", nil)
			found := readDisabledOutputTask(t, fixture, created.ID)
			if found.LatestContinuation == nil {
				t.Fatalf("%s Task public detail omitted its Continuation", mode)
			}
			access := projectinterface.GrantAccessFull
			wantWriteStatus := http.StatusOK
			if mode == task.BlackboardModeWorkingGraph {
				access = projectinterface.GrantAccessReadOnly
				wantWriteStatus = http.StatusForbidden
			}
			grantToken, _, err := fixture.server.projectInterfaceGrants.Issue(context.Background(), projectinterface.IssueGrantRequest{
				ProjectID: fixture.projectID, TaskID: created.ID, ContinuationID: found.LatestContinuation.ID,
				RuntimeConfigVersionID: found.LatestContinuation.RuntimeConfigVersionID,
				RuntimeProfileID:       fixture.profileID,
				RuntimePluginID:        string(runtimeprofile.ProviderCodex),
				Runner:                 string(created.Runner),
				Access:                 access,
			})
			if err != nil {
				t.Fatalf("issue %s setup Continuation grant: %v", mode, err)
			}
			key := "fact:" + string(mode) + "-auth-regression"
			body := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"` + key + `","type":"fact","record":{"category":"service","summary":"Ordinary ` + string(mode) + ` Runtime grant remains valid","confidence":"tentative","scope_status":"in_scope"}}]}`
			response := disabledOutputRequest(
				fixture, http.MethodPost, "/api/v2/projects/"+fixture.projectID+"/blackboard/changes",
				grantToken, "", "runtime-"+string(mode)+"-change", body,
			)
			if response.Code != wantWriteStatus {
				t.Fatalf("%s Runtime grant status = %d body=%s", mode, response.Code, response.Body.String())
			}
			if mode == task.BlackboardModeWorkingGraph {
				if !strings.Contains(response.Body.String(), "authority_denied") {
					t.Fatalf("Working Graph write denial = %s", response.Body.String())
				}
				read := disabledOutputRequest(
					fixture, http.MethodGet, "/api/v2/projects/"+fixture.projectID+"/blackboard/snapshot",
					grantToken, "", "", "",
				)
				if read.Code != http.StatusOK {
					t.Fatalf("Working Graph read status = %d body=%s", read.Code, read.Body.String())
				}
				return
			}

			var detail blackboardv2.CurrentDetail
			getDisabledOutputJSON(
				t, fixture, "/api/v2/projects/"+fixture.projectID+"/blackboard/records/"+key,
				fixture.operatorToken, &detail,
			)
			if detail.Key != key || detail.Record.Summary == "" {
				t.Fatalf("%s Runtime grant did not create a public Blackboard record: %#v", mode, detail)
			}
		})
	}
}

func assertDisabledNoGrantBoundaries(t *testing.T, fixture disabledOutputAuthorityFixture, taskID, label string) {
	t.Helper()
	response := disabledOutputRequest(
		fixture, http.MethodPost,
		"/api/v2/projects/"+fixture.projectID+"/blackboard/changes",
		"", "forged-host-runtime", label+"-tokenless-change",
		`{"schema":"semantic-change-batch/v2","changes":[]}`,
	)
	assertOrdinaryNoGrantResponse(t, response, label+" Blackboard change")

	response = disabledOutputRequest(
		fixture, http.MethodPost,
		"/api/v2/projects/"+fixture.projectID+"/tasks/"+taskID+"/blackboard/evidence:retain",
		"", "forged-host-runtime", label+"-tokenless-retain", `{}`,
	)
	assertOrdinaryNoGrantResponse(t, response, label+" Evidence retain")
}

func assertOrdinaryNoGrantResponse(t *testing.T, response *httptest.ResponseRecorder, action string) {
	t.Helper()
	if response.Code != http.StatusUnauthorized ||
		!strings.Contains(response.Body.String(), `"code":"authority_denied"`) ||
		strings.Contains(response.Body.String(), "blackboard_disabled") {
		t.Fatalf("%s crossed operator authority: status=%d body=%s", action, response.Code, response.Body.String())
	}
}

func TestDisabledOutputEntersReportsAndDashboardOnlyAfterOperatorReconciliation(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, disabledOutputOperatorToken)
	const unreviewed = "UNREVIEWED_DISABLED_RUNTIME_CONCLUSION"
	created := createDisabledOutputTask(
		t, fixture, task.BlackboardModeDisabled, "state.txt", []byte(unreviewed+"\n"),
	)
	// Setup one legacy/raw Runtime output entry. Report and Dashboard assertions
	// below use only public responses.
	if _, err := fixture.server.tasks.AppendEvent(created.ID, task.EventKindRuntimeOutput, task.EventPayload{"text": unreviewed}); err != nil {
		t.Fatalf("append unreviewed Runtime Transcript output: %v", err)
	}

	base := "/api/v2/projects/" + fixture.projectID
	beforeReport := mustOperatorV2Request(t, fixture, "operator-reviewer", http.MethodGet, base+"/reports/pentest?format=json", "", "", http.StatusOK)
	var beforeProjection blackboardv2.PentestReportProjection
	if err := json.Unmarshal(beforeReport, &beforeProjection); err != nil {
		t.Fatalf("decode pre-Reconciliation Report: %v body=%s", err, beforeReport)
	}
	if bytes.Contains(beforeReport, []byte(unreviewed)) || len(beforeProjection.TentativeFacts) != 0 {
		t.Fatalf("Report inferred a conclusion from Disabled Runtime output: %s", beforeReport)
	}
	beforeDashboard := readDisabledOutputDashboard(t, fixture)
	if beforeDashboard.Counts.Facts != 0 || beforeDashboard.Counts.Evidence != 0 {
		t.Fatalf("Project Dashboard inferred Blackboard records before operator action: %#v", beforeDashboard)
	}

	workBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:disabled-reconciliation","type":"objective","record":{"status":"open","objective":"Reconcile selected Disabled Task output"}},{"op":"create","key":"attempt:disabled-reconciliation","type":"attempt","record":{"status":"open","summary":"Operator reviews selected Disabled Task output"}},{"op":"relate","from":"attempt:disabled-reconciliation","relation":"tests","to":"objective:disabled-reconciliation"}]}`
	mustOperatorV2Request(t, fixture, "operator-reviewer", http.MethodPost, base+"/blackboard/changes", "operator-reconciliation-open", workBatch, http.StatusOK)
	retainBody := `{"key":"evidence:disabled-reconciliation","attempt":"attempt:disabled-reconciliation","source_path":"state.txt","artifact_type":"text","summary":"Operator selected Disabled output for reconciliation","media_type":"text/plain"}`
	mustOperatorV2Request(
		t, fixture, "operator-reviewer", http.MethodPost, base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		"operator-reconciliation-retain", retainBody, http.StatusOK,
	)
	const acceptedSummary = "Operator-reviewed conclusion from selected Disabled output"
	reconcileBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"fact:disabled-reviewed","type":"fact","record":{"category":"service","summary":"` + acceptedSummary + `","confidence":"tentative","scope_status":"in_scope"}},{"op":"relate","from":"attempt:disabled-reconciliation","relation":"produced","to":"fact:disabled-reviewed"},{"op":"relate","from":"fact:disabled-reviewed","relation":"derived_from","to":"evidence:disabled-reconciliation"},{"op":"transition","key":"attempt:disabled-reconciliation","version":1,"status":"succeeded","summary":"Operator completed reconciliation"}]}`
	reconciled := mustOperatorV2Request(t, fixture, "operator-reviewer", http.MethodPost, base+"/blackboard/changes", "operator-reconciliation-accept", reconcileBatch, http.StatusOK)
	var reconcileResult blackboardv2.ChangeResult
	if err := json.Unmarshal(reconciled, &reconcileResult); err != nil || reconcileResult.Schema != "semantic-change-result/v2" {
		t.Fatalf("decode public Reconciliation response: result=%#v err=%v body=%s", reconcileResult, err, reconciled)
	}

	afterReport := mustOperatorV2Request(t, fixture, "operator-reviewer", http.MethodGet, base+"/reports/pentest?format=json", "", "", http.StatusOK)
	var afterProjection blackboardv2.PentestReportProjection
	if err := json.Unmarshal(afterReport, &afterProjection); err != nil {
		t.Fatalf("decode post-Reconciliation Report: %v body=%s", err, afterReport)
	}
	if len(afterProjection.TentativeFacts) != 1 || afterProjection.TentativeFacts[0].Key != "fact:disabled-reviewed" ||
		afterProjection.TentativeFacts[0].Summary != acceptedSummary || bytes.Contains(afterReport, []byte(unreviewed)) {
		t.Fatalf("Report did not stay on explicit Blackboard records: %s", afterReport)
	}
	afterDashboard := readDisabledOutputDashboard(t, fixture)
	if afterDashboard.Counts.Facts != 1 || afterDashboard.Counts.Evidence != 1 {
		t.Fatalf("Project Dashboard did not use actual operator-created Blackboard records: %#v", afterDashboard)
	}
}

func TestDisabledEvidenceRetentionEnforcesOperatorProvenanceReplayAndConfinement(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, "")
	created := createDisabledOutputTask(
		t, fixture, task.BlackboardModeDisabled, "proof.txt", []byte("confined proof\n"),
	)
	base := "/api/v2/projects/" + fixture.projectID
	workBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:retention-controls","type":"objective","record":{"status":"open","objective":"Review retained output controls"}},{"op":"create","key":"attempt:retention-controls","type":"attempt","record":{"status":"open","summary":"Operator A reviews selected output"}},{"op":"relate","from":"attempt:retention-controls","relation":"tests","to":"objective:retention-controls"}]}`
	mustOperatorV2Request(t, fixture, "operator-a", http.MethodPost, base+"/blackboard/changes", "retention-controls-open", workBatch, http.StatusOK)

	retainBody := `{"key":"evidence:retention-controls","attempt":"attempt:retention-controls","source_path":"proof.txt","artifact_type":"text","summary":"Retained with operator provenance","media_type":"text/plain"}`
	foreign := disabledOutputRequest(
		fixture, http.MethodPost, base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		fixture.operatorToken, "operator-b", "foreign-operator-retain", retainBody,
	)
	assertBlackboardV2Error(t, foreign, http.StatusForbidden, "authority_denied")

	first := mustOperatorV2Request(
		t, fixture, "operator-a", http.MethodPost, base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		"retention-controls-retain", retainBody, http.StatusOK,
	)
	replay := mustOperatorV2Request(
		t, fixture, "operator-a", http.MethodPost, base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		"retention-controls-retain", retainBody, http.StatusOK,
	)
	if !bytes.Equal(first, replay) {
		t.Fatalf("exact operator Evidence replay changed response: first=%s replay=%s", first, replay)
	}
	conflictBody := strings.Replace(retainBody, "Retained with operator provenance", "Changed replay semantics", 1)
	conflict := disabledOutputRequest(
		fixture, http.MethodPost, base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		fixture.operatorToken, "operator-a", "retention-controls-retain", conflictBody,
	)
	assertBlackboardV2Error(t, conflict, http.StatusConflict, "idempotency_conflict")

	escapeBody := `{"key":"evidence:escaped-output","attempt":"attempt:retention-controls","source_path":"../outside.txt","artifact_type":"text","summary":"Escaped output must fail"}`
	escape := disabledOutputRequest(
		fixture, http.MethodPost, base+"/tasks/"+created.ID+"/blackboard/evidence:retain",
		fixture.operatorToken, "operator-a", "retention-controls-escape", escapeBody,
	)
	assertBlackboardV2Error(t, escape, http.StatusUnprocessableEntity, "evidence_source_forbidden")
	missing := disabledOutputRequest(
		fixture, http.MethodGet, base+"/blackboard/records/evidence:escaped-output",
		fixture.operatorToken, "operator-a", "", "",
	)
	assertBlackboardV2Error(t, missing, http.StatusNotFound, "not_found")
}

func TestDisabledEvidenceReplayAfterReplacementKeepsOriginalSourceContinuation(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, "")
	const sourcePath = "replacement-proof.txt"
	originalContents := []byte("proof from original Continuation\n")
	created := createDisabledOutputTask(
		t, fixture, task.BlackboardModeDisabled, sourcePath, originalContents,
	)
	original := readDisabledOutputTask(t, fixture, created.ID)
	if original.LatestContinuation == nil {
		t.Fatal("Disabled Task public detail omitted original Continuation")
	}

	base := "/api/v2/projects/" + fixture.projectID
	workBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:replacement-replay","type":"objective","record":{"status":"open","objective":"Review replacement-safe retention"}},{"op":"create","key":"attempt:replacement-replay","type":"attempt","record":{"status":"open","summary":"Operator reviews original Disabled output"}},{"op":"relate","from":"attempt:replacement-replay","relation":"tests","to":"objective:replacement-replay"}]}`
	mustOperatorV2Request(t, fixture, "operator-replay", http.MethodPost, base+"/blackboard/changes", "replacement-replay-open", workBatch, http.StatusOK)
	retainPath := base + "/tasks/" + created.ID + "/blackboard/evidence:retain"
	retainBody := `{"key":"evidence:replacement-replay","attempt":"attempt:replacement-replay","source_path":"replacement-proof.txt","artifact_type":"text","summary":"Original Continuation proof","media_type":"text/plain"}`
	first := mustOperatorV2Request(
		t, fixture, "operator-replay", http.MethodPost, retainPath,
		"replacement-replay-retain", retainBody, http.StatusOK,
	)
	var before blackboardv2.CurrentDetail
	getDisabledOutputJSON(t, fixture, base+"/blackboard/records/evidence:replacement-replay", fixture.operatorToken, &before)

	// Setup a replacement Continuation and different source bytes. Final
	// behavior assertions below use only public HTTP responses and DTOs.
	replacement, err := fixture.server.tasks.CreateContinuation(
		created.ID, fixture.profileID, string(runtimeprofile.ProviderCodex), task.RunnerHost,
	)
	if err != nil {
		t.Fatalf("create replacement Continuation fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.runtimeRoot, created.ID, "workdir", sourcePath), []byte("proof from replacement Continuation\n"), 0o600); err != nil {
		t.Fatalf("replace source fixture: %v", err)
	}
	afterReplacement := readDisabledOutputTask(t, fixture, created.ID)
	if afterReplacement.LatestContinuation == nil || afterReplacement.LatestContinuation.ID != replacement.ID ||
		afterReplacement.LatestContinuation.ID == original.LatestContinuation.ID {
		t.Fatalf("public Task detail did not expose replacement Continuation: original=%#v replacement=%#v task=%#v", original.LatestContinuation, replacement, afterReplacement)
	}

	replay := mustOperatorV2Request(
		t, fixture, "operator-replay", http.MethodPost, retainPath,
		"replacement-replay-retain", retainBody, http.StatusOK,
	)
	if !bytes.Equal(first, replay) {
		t.Fatalf("replacement retry did not replay original public response: first=%s replay=%s", first, replay)
	}
	var after blackboardv2.CurrentDetail
	getDisabledOutputJSON(t, fixture, base+"/blackboard/records/evidence:replacement-replay", fixture.operatorToken, &after)
	originalDigest := sha256.Sum256(originalContents)
	if before.Record.SHA256 != hex.EncodeToString(originalDigest[:]) ||
		after.Record.SHA256 != before.Record.SHA256 || after.Record.ManagedPath != before.Record.ManagedPath {
		t.Fatalf("replacement retry changed public Evidence artifact: before=%#v after=%#v", before.Record, after.Record)
	}
}

func TestDisabledEvidenceRetryAfterPreReservationFailureDoesNotReadReplacementWorkdir(t *testing.T) {
	fixture := newDisabledOutputAuthorityFixture(t, "")
	const sourcePath = "failed-before-reservation.txt"
	created := createDisabledOutputTask(
		t, fixture, task.BlackboardModeDisabled, sourcePath, []byte("original bytes\n"),
	)
	base := "/api/v2/projects/" + fixture.projectID
	workBatch := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:failed-retain","type":"objective","record":{"status":"open","objective":"Review safely bound output"}},{"op":"create","key":"attempt:failed-retain","type":"attempt","record":{"status":"open","summary":"Operator reviews bound Disabled output"}},{"op":"relate","from":"attempt:failed-retain","relation":"tests","to":"objective:failed-retain"}]}`
	mustOperatorV2Request(t, fixture, "operator-retry", http.MethodPost, base+"/blackboard/changes", "failed-retain-open", workBatch, http.StatusOK)
	retainPath := base + "/tasks/" + created.ID + "/blackboard/evidence:retain"
	retainBody := `{"key":"evidence:failed-retain","attempt":"attempt:failed-retain","source_path":"failed-before-reservation.txt","artifact_type":"text","summary":"Bound source must not drift","media_type":"text/plain"}`

	// Setup injects a failure after the Task-scoped source binding but before the
	// continuation-scoped Evidence request reserves the source bytes.
	fixture.server.blackboardV2 = blackboardv2.NewServiceWithEvidence(fixture.server.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.artifactRoot,
		Failures: failBeforeEvidenceReservation{},
	})
	failed := disabledOutputRequest(
		fixture, http.MethodPost, retainPath, fixture.operatorToken, "operator-retry",
		"failed-retain-request", retainBody,
	)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected retain status=%d body=%s, want internal server error", failed.Code, failed.Body.String())
	}
	fixture.server.blackboardV2 = blackboardv2.NewServiceWithEvidence(fixture.server.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.artifactRoot,
	})
	if _, err := fixture.server.tasks.CreateContinuation(
		created.ID, fixture.profileID, string(runtimeprofile.ProviderCodex), task.RunnerHost,
	); err != nil {
		t.Fatalf("create replacement Continuation fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.runtimeRoot, created.ID, "workdir", sourcePath), []byte("replacement bytes must not be retained\n"), 0o600); err != nil {
		t.Fatalf("replace source fixture: %v", err)
	}

	retry := disabledOutputRequest(
		fixture, http.MethodPost, retainPath, fixture.operatorToken, "operator-retry",
		"failed-retain-request", retainBody,
	)
	assertBlackboardV2Error(t, retry, http.StatusUnprocessableEntity, "evidence_source_forbidden")
	missing := disabledOutputRequest(
		fixture, http.MethodGet, base+"/blackboard/records/evidence:failed-retain",
		fixture.operatorToken, "operator-retry", "", "",
	)
	assertBlackboardV2Error(t, missing, http.StatusNotFound, "not_found")
}

type failBeforeEvidenceReservation struct{}

func (failBeforeEvidenceReservation) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point == blackboardv2.EvidenceFailureBeforeReservation {
		return errors.New("injected failure before Evidence reservation")
	}
	return nil
}

type disabledOutputAuthorityFixture struct {
	server        *Server
	projectID     string
	profileID     string
	runtimeRoot   string
	artifactRoot  string
	operatorToken string
}

type authorityProviderSessionFactory struct {
	*recordingProviderSessionFactory
}

func (factory *authorityProviderSessionFactory) SupportsAssistedConclusion(provider runtimeprofile.Provider) bool {
	return provider == runtimeprofile.ProviderCodex
}

func newDisabledOutputAuthorityFixture(t *testing.T, authToken string) disabledOutputAuthorityFixture {
	t.Helper()
	root := t.TempDir()
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatalf("create Artifact Root: %v", err)
	}
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "disabled-output-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession:  true,
			SendTurn:           true,
			AssistedConclusion: true,
		},
	})
	recordingFactory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	factory := &authorityProviderSessionFactory{recordingProviderSessionFactory: recordingFactory}
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: runtimeRoot,
		ArtifactRoot: artifactRoot, AuthToken: authToken,
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
	operatorToken := strings.TrimSpace(authToken)
	if operatorToken == "" {
		accessURL := server.GeneratedOperatorAccessURL()
		parsed, parseErr := url.Parse(accessURL)
		if parseErr != nil {
			t.Fatalf("parse generated operator access URL: %v", parseErr)
		}
		operatorToken = parsed.Query().Get("token")
		if operatorToken == "" {
			t.Fatalf("generated operator access URL omitted its bearer capability: %q", accessURL)
		}
	}
	return disabledOutputAuthorityFixture{
		server: server, projectID: createdProject.ID, profileID: profile.ID, runtimeRoot: runtimeRoot, artifactRoot: artifactRoot,
		operatorToken: operatorToken,
	}
}

func createDisabledOutputTask(
	t *testing.T,
	fixture disabledOutputAuthorityFixture,
	mode task.BlackboardMode,
	attachmentName string,
	attachmentContents []byte,
) task.Task {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type": "pentest", "goal": "produce selected operator evidence",
		"runtime_profile_id": fixture.profileID, "runner": "host",
		"run_controls": map[string]any{
			"host_activated": true, "blackboard_mode": mode,
		},
	})
	if err != nil {
		t.Fatalf("encode Task launch payload: %v", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("payload", string(payload)); err != nil {
		t.Fatalf("write Task launch payload: %v", err)
	}
	if attachmentName != "" {
		part, createErr := writer.CreateFormFile("attachments", attachmentName)
		if createErr != nil {
			t.Fatalf("create Task attachment: %v", createErr)
		}
		if _, writeErr := part.Write(attachmentContents); writeErr != nil {
			t.Fatalf("write Task attachment: %v", writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close Task launch body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.projectID+"/tasks", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+fixture.operatorToken)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create %s Task status = %d body %s", mode, response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode %s Task: %v", mode, err)
	}
	return created
}

func mustOperatorV2Request(
	t *testing.T,
	fixture disabledOutputAuthorityFixture,
	actor, method, path, idempotencyKey, body string,
	wantStatus int,
) []byte {
	t.Helper()
	response := disabledOutputRequest(fixture, method, path, fixture.operatorToken, actor, idempotencyKey, body)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.Code, wantStatus, response.Body.String())
	}
	return append([]byte(nil), response.Body.Bytes()...)
}

func disabledOutputRequest(
	fixture disabledOutputAuthorityFixture,
	method, path, token, actor, idempotencyKey, body string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if actor != "" {
		request.Header.Set(projectinterface.OperatorActorHeader, actor)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	return response
}

func getDisabledOutputBody(
	t *testing.T,
	fixture disabledOutputAuthorityFixture,
	path, token string,
	wantStatus int,
) []byte {
	t.Helper()
	response := disabledOutputRequest(fixture, http.MethodGet, path, token, "", "", "")
	if response.Code != wantStatus {
		t.Fatalf("GET %s status = %d, want %d; body=%s", path, response.Code, wantStatus, response.Body.String())
	}
	return append([]byte(nil), response.Body.Bytes()...)
}

func getDisabledOutputJSON(
	t *testing.T,
	fixture disabledOutputAuthorityFixture,
	path, token string,
	target any,
) {
	t.Helper()
	body := getDisabledOutputBody(t, fixture, path, token, http.StatusOK)
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode GET %s: %v body=%s", path, err, body)
	}
}

func readDisabledOutputTask(t *testing.T, fixture disabledOutputAuthorityFixture, taskID string) task.Task {
	t.Helper()
	var detail task.Task
	getDisabledOutputJSON(
		t, fixture, "/api/projects/"+fixture.projectID+"/tasks/"+taskID,
		fixture.operatorToken, &detail,
	)
	return detail
}

type disabledOutputDashboard struct {
	Counts struct {
		Tasks    int `json:"tasks"`
		Facts    int `json:"facts"`
		Findings int `json:"findings"`
		Evidence int `json:"evidence"`
	} `json:"counts"`
}

func readDisabledOutputDashboard(t *testing.T, fixture disabledOutputAuthorityFixture) disabledOutputDashboard {
	t.Helper()
	var dashboard disabledOutputDashboard
	getDisabledOutputJSON(
		t, fixture, "/api/projects/"+fixture.projectID+"/dashboard",
		fixture.operatorToken, &dashboard,
	)
	return dashboard
}

func assertBlackboardV2Error(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("Blackboard error status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var envelope struct {
		Error *blackboardv2.Error `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error == nil {
		t.Fatalf("decode Blackboard error: error=%#v decode=%v", envelope.Error, err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("Blackboard error code = %q, want %q; error=%#v", envelope.Error.Code, code, envelope.Error)
	}
}
