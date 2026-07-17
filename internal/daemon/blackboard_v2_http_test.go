package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func TestBlackboardV2HTTPRoutesServeAllSixCLICommandsWithTrustedContinuation(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "v2-http.db")
	runtimeRoot := filepath.Join(root, "runs")
	const operatorToken = "operator-daemon-secret"
	server, err := NewServer(Config{
		Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot,
		AuthToken: operatorToken, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if server.blackboardV2 == nil || server.blackboardV2Continuity == nil || server.projectInterfaceGrants == nil {
		t.Fatal("blackboard v2 HTTP surface is not wired")
	}

	createdProject, err := server.projects.Create("V2 HTTP", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "exercise /api/v2", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: createdTask.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("launch Continuation: %v", err)
	}
	if strings.TrimSpace(launch.Token) == "" {
		t.Fatal("Continuation launch omitted opaque capability token")
	}

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	base := httpServer.URL + "/api/v2/projects/" + createdProject.ID

	// Operator seed entity (no Continuation required).
	seedBody := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:http","type":"entity","record":{"status":"active","kind":"host","name":"HTTP host","scope_status":"in_scope"}}]}`
	seeded := mustV2HTTP(t, http.MethodPost, base+"/blackboard/changes", operatorToken, "operator", "http-seed", seedBody)
	if !bytes.Contains(seeded, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("operator seed result = %s", seeded)
	}

	// Operator read + history.
	read := mustV2HTTP(t, http.MethodGet, base+"/blackboard/records/entity:http", operatorToken, "operator", "", "")
	if !bytes.Contains(read, []byte(`"schema":"blackboard-record/v2"`)) || !bytes.Contains(read, []byte(`"key":"entity:http"`)) {
		t.Fatalf("operator read = %s", read)
	}
	history := mustV2HTTP(t, http.MethodGet, base+"/blackboard/records/entity:http/history?limit=1", operatorToken, "operator", "", "")
	if !bytes.Contains(history, []byte(`"schema":"semantic-history/v2"`)) {
		t.Fatalf("operator history = %s", history)
	}

	// Trusted Continuation owns open work it later checkpoints/finishes.
	workBody := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:http","type":"objective","record":{"status":"open","objective":"HTTP proof"}},{"op":"create","key":"attempt:http","type":"attempt","record":{"status":"open","summary":"Collect HTTP proof"}},{"op":"relate","from":"attempt:http","relation":"tests","to":"objective:http"}]}`
	worked := mustV2HTTP(t, http.MethodPost, base+"/blackboard/changes", launch.Token, "", "http-work", workBody)
	if !bytes.Contains(worked, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("continuation work result = %s", worked)
	}

	// Trusted Continuation evidence retain, checkpoint, finish.
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("http proof\n"), 0o600); err != nil {
		t.Fatalf("write proof: %v", err)
	}
	retainBody := `{"key":"evidence:http","attempt":"attempt:http","source_path":"proof.txt","artifact_type":"text","summary":"HTTP retained proof","media_type":"text/plain","links":[["about","entity:http"]]}`
	retained := mustV2HTTP(t, http.MethodPost, base+"/blackboard/evidence:retain", launch.Token, "", "http-evidence", retainBody)
	if !bytes.Contains(retained, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("evidence retain = %s", retained)
	}
	checkpointBody := `{"version":1,"summary":"Proof retained"}`
	checkpointed := mustV2HTTP(t, http.MethodPost, base+"/blackboard/attempts/attempt:http:checkpoint", launch.Token, "", "http-checkpoint", checkpointBody)
	if !bytes.Contains(checkpointed, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("checkpoint = %s", checkpointed)
	}
	terminalBody := `{"schema":"semantic-change-batch/v2","changes":[{"op":"transition","key":"attempt:http","version":2,"status":"succeeded","summary":"HTTP proof retained successfully"}]}`
	if body := mustV2HTTP(t, http.MethodPost, base+"/blackboard/changes", launch.Token, "", "http-terminal", terminalBody); !bytes.Contains(body, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("terminal change = %s", body)
	}
	finished := mustV2HTTP(t, http.MethodPost, base+"/continuation:finish", launch.Token, "", "http-finish", `{}`)
	if !bytes.Contains(finished, []byte(`"schema":"continuation-finish/v2"`)) || bytes.Contains(finished, []byte(`"sync"`)) {
		t.Fatalf("finish = %s", finished)
	}
	// Exact Finish replay keeps the same compact result and never attaches sync.
	replayed := mustV2HTTP(t, http.MethodPost, base+"/continuation:finish", launch.Token, "", "http-finish", `{}`)
	if !bytes.Equal(finished, replayed) {
		t.Fatalf("finish replay drifted\nfirst=%s\nreplay=%s", finished, replayed)
	}
	// Closed Continuation loses trusted read/history.
	closedRead := doV2HTTP(t, http.MethodGet, base+"/blackboard/records/entity:http", launch.Token, "", "", "")
	if closedRead.status != http.StatusGone || !bytes.Contains(closedRead.body, []byte(`"code":"closed_continuation"`)) {
		t.Fatalf("closed read = %d %s", closedRead.status, closedRead.body)
	}
	closedHistory := doV2HTTP(t, http.MethodGet, base+"/blackboard/records/entity:http/history", launch.Token, "", "", "")
	if closedHistory.status != http.StatusGone || !bytes.Contains(closedHistory.body, []byte(`"code":"closed_continuation"`)) {
		t.Fatalf("closed history = %d %s", closedHistory.status, closedHistory.body)
	}

	// Foreign Project path with owner capability is denied before service access.
	foreign, err := server.projects.Create("Foreign HTTP", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign Project: %v", err)
	}
	foreignBase := httpServer.URL + "/api/v2/projects/" + foreign.ID
	foreignDenied := doV2HTTP(t, http.MethodGet, foreignBase+"/blackboard/records/entity:http", launch.Token, "", "", "")
	if foreignDenied.status != http.StatusForbidden && foreignDenied.status != http.StatusUnauthorized {
		t.Fatalf("foreign Project capability status = %d %s", foreignDenied.status, foreignDenied.body)
	}
	if !bytes.Contains(foreignDenied.body, []byte(`"code":"authority_denied"`)) {
		t.Fatalf("foreign Project capability body = %s", foreignDenied.body)
	}

	// Snapshot route is registered and operator-reachable.
	snapshot := mustV2HTTP(t, http.MethodGet, base+"/blackboard/snapshot", operatorToken, "operator", "", "")
	if !bytes.Contains(snapshot, []byte(`"schema":"runtime-blackboard/v2"`)) {
		t.Fatalf("snapshot = %s", snapshot)
	}

	// Semantic health is a live operator/UI read under the same v2 surface.
	health := mustV2HTTP(t, http.MethodGet, base+"/blackboard/health", operatorToken, "operator", "", "")
	if !bytes.Contains(health, []byte(`"schema":"blackboard-health/v2"`)) || !bytes.Contains(health, []byte(`"attention"`)) {
		t.Fatalf("health = %s", health)
	}
	if bytes.Contains(bytes.ToLower(health), []byte("provenance")) || bytes.Contains(health, []byte("state_hash")) || bytes.Contains(health, []byte("health_run")) {
		t.Fatalf("health leaked audit noise: %s", health)
	}
}

func TestBlackboardV2HTTPHealthIsProjectIsolatedDeterministicAndActionable(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "v2-health-http.db")
	runtimeRoot := filepath.Join(root, "runs")
	const operatorToken = "operator-health-secret"
	server, err := NewServer(Config{
		Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot,
		AuthToken: operatorToken, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	alpha, err := server.projects.Create("Health Alpha", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	beta, err := server.projects.Create("Health Beta", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	alphaBase := httpServer.URL + "/api/v2/projects/" + alpha.ID
	betaBase := httpServer.URL + "/api/v2/projects/" + beta.ID

	mustV2HTTP(t, http.MethodPost, alphaBase+"/blackboard/changes", operatorToken, "operator", "alpha-stranded",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:alpha-stranded","type":"objective","record":{"status":"open","objective":"Alpha stranded work"}},{"op":"create","key":"attempt:alpha-orphan","type":"attempt","record":{"status":"open","summary":"Alpha attempt without tests"}}]}`)
	mustV2HTTP(t, http.MethodPost, betaBase+"/blackboard/changes", operatorToken, "operator", "beta-entity",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:beta-only","type":"entity","record":{"status":"active","kind":"host","name":"Beta host","scope_status":"in_scope"}}]}`)

	first := mustV2HTTP(t, http.MethodGet, alphaBase+"/blackboard/health", operatorToken, "operator", "", "")
	if !bytes.Contains(first, []byte(`"schema":"blackboard-health/v2"`)) || !bytes.Contains(first, []byte(`"stranded_objective"`)) || !bytes.Contains(first, []byte(`"stranded_attempt"`)) {
		t.Fatalf("alpha health missing stranded work: %s", first)
	}
	if bytes.Contains(first, []byte("entity:beta-only")) || bytes.Contains(first, []byte(beta.ID)) {
		t.Fatalf("alpha health leaked beta state: %s", first)
	}
	betaHealth := mustV2HTTP(t, http.MethodGet, betaBase+"/blackboard/health", operatorToken, "operator", "", "")
	if !bytes.Contains(betaHealth, []byte(`"status":"healthy"`)) || bytes.Contains(betaHealth, []byte("objective:alpha-stranded")) {
		t.Fatalf("beta health not isolated/healthy: %s", betaHealth)
	}
	// Deterministic repeated read + ETag conditional.
	second := mustV2HTTP(t, http.MethodGet, alphaBase+"/blackboard/health", operatorToken, "operator", "", "")
	if !bytes.Equal(first, second) {
		t.Fatalf("health not deterministic\nfirst=%s\nsecond=%s", first, second)
	}
	etagResult := doV2HTTP(t, http.MethodGet, alphaBase+"/blackboard/health", operatorToken, "operator", "", "")
	etag := etagResult.header.Get("ETag")
	if etag == "" {
		t.Fatalf("health response missing ETag: %#v", etagResult.header)
	}
	notModified := doV2HTTP(t, http.MethodGet, alphaBase+"/blackboard/health", operatorToken, "operator", "", "", v2HTTPOptions{ifNoneMatch: etag})
	if notModified.status != http.StatusNotModified {
		t.Fatalf("If-None-Match health = %d %s", notModified.status, notModified.body)
	}
	// Foreign Project path with mismatched capability is denied.
	profile, err := server.profiles.Create("Health Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	taskRow, err := server.tasks.Create(task.CreateRequest{
		ProjectID: alpha.ID, Goal: "health isolation", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: alpha.ID, TaskID: taskRow.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	foreign := doV2HTTP(t, http.MethodGet, betaBase+"/blackboard/health", launch.Token, "", "", "")
	if foreign.status != http.StatusForbidden && foreign.status != http.StatusUnauthorized {
		t.Fatalf("foreign health status = %d %s", foreign.status, foreign.body)
	}
	if !bytes.Contains(foreign.body, []byte(`"code":"authority_denied"`)) {
		t.Fatalf("foreign health body = %s", foreign.body)
	}
}

func TestBlackboardV2HTTPRejectsQueryCredentialAndMissingIdempotency(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "v2-auth.db"), RuntimeRoot: filepath.Join(root, "runs"),
		AuthToken: "operator-secret", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Auth", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	url := httpServer.URL + "/api/v2/projects/" + createdProject.ID + "/blackboard/changes?token=operator-secret"
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"schema":"semantic-change-batch/v2","changes":[]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer operator-secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "query-cred")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("query credential request: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK || !bytes.Contains(body, []byte(`"code":"invalid_schema"`)) {
		t.Fatalf("query credential accepted: %d %s", response.StatusCode, body)
	}

	missingKey := doV2HTTP(t, http.MethodPost, httpServer.URL+"/api/v2/projects/"+createdProject.ID+"/blackboard/changes", "operator-secret", "operator", "", `{"schema":"semantic-change-batch/v2","changes":[]}`)
	if missingKey.status != http.StatusUnprocessableEntity || !bytes.Contains(missingKey.body, []byte(`"code":"semantic_validation"`)) {
		t.Fatalf("missing idempotency = %d %s", missingKey.status, missingKey.body)
	}
}

type v2HTTPResult struct {
	status int
	body   []byte
	header http.Header
}

type v2HTTPOptions struct {
	ifNoneMatch string
	headers     map[string]string
}

func mustV2HTTP(t *testing.T, method, url, token, actor, idempotencyKey, body string) []byte {
	t.Helper()
	result := doV2HTTP(t, method, url, token, actor, idempotencyKey, body)
	if result.status < 200 || result.status >= 300 {
		t.Fatalf("%s %s => %d %s", method, url, result.status, result.body)
	}
	return result.body
}

func doV2HTTP(t *testing.T, method, url, token, actor, idempotencyKey, body string, opts ...v2HTTPOptions) v2HTTPResult {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if actor != "" {
		request.Header.Set("CyberPenda-Actor-ID", actor)
	}
	if len(opts) > 0 {
		if opts[0].ifNoneMatch != "" {
			request.Header.Set("If-None-Match", opts[0].ifNoneMatch)
		}
		for key, value := range opts[0].headers {
			request.Header.Set(key, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return v2HTTPResult{status: response.StatusCode, body: raw, header: response.Header.Clone()}
}

func assertV2ErrorEnvelope(t *testing.T, status int, body []byte, wantCode string, wantStatus int) *blackboardv2.Error {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d body=%s", status, wantStatus, body)
	}
	if status == http.StatusOK {
		t.Fatalf("success status must never carry an error envelope: %s", body)
	}
	var envelope struct {
		Error *blackboardv2.Error                     `json:"error"`
		Sync  *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, body)
	}
	if envelope.Error == nil || envelope.Error.Code != wantCode {
		t.Fatalf("error envelope = %s, want code %q", body, wantCode)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode envelope object: %v", err)
	}
	for key := range fields {
		switch key {
		case "error", "sync":
		default:
			t.Fatalf("error envelope leaked field %q: %s", key, body)
		}
	}
	var errorFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["error"], &errorFields); err != nil {
		t.Fatalf("decode error object: %v", err)
	}
	for key := range errorFields {
		switch key {
		case "code", "message", "path", "retryable", "details":
		default:
			t.Fatalf("error object leaked field %q: %s", key, body)
		}
	}
	return envelope.Error
}

// Ensure typed JSON still round-trips through the daemon success path for Finish.
func TestBlackboardV2HTTPFinishResultIsClosedTypedDTO(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "finish-dto.db"), RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Finish DTO", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "finish dto", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: createdTask.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	base := httpServer.URL + "/api/v2/projects/" + createdProject.ID
	finished := mustV2HTTP(t, http.MethodPost, base+"/continuation:finish", launch.Token, "", "finish-dto", `{}`)
	var result blackboardv2.FinishContinuationResult
	if err := json.Unmarshal(finished, &result); err != nil {
		t.Fatalf("decode Finish DTO: %v %s", err, finished)
	}
	if result.Schema != "continuation-finish/v2" || result.Status != "finished" || result.WorkingSnapshot.Path != ".pentest/blackboard.json" {
		t.Fatalf("Finish DTO = %#v", result)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(finished, &fields); err != nil {
		t.Fatalf("raw fields: %v", err)
	}
	for field := range fields {
		switch field {
		case "schema", "status", "revision", "working_snapshot":
		default:
			t.Fatalf("Finish success DTO leaked field %q: %s", field, finished)
		}
	}
}

type v2HTTPFixture struct {
	server       *Server
	httpServer   *httptest.Server
	project      project.Project
	foreign      project.Project
	task         task.Task
	peerTask     task.Task
	profile      runtimeprofile.Profile
	continuation blackboardv2.ContinuationLaunch
	peer         blackboardv2.ContinuationLaunch
	operator     string
	base         string
	runtimeRoot  string
}

func newV2HTTPFixture(t *testing.T) v2HTTPFixture {
	t.Helper()
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runs")
	const operatorToken = "operator-daemon-secret"
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "v2-http-parity.db"), RuntimeRoot: runtimeRoot,
		AuthToken: operatorToken, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("V2 HTTP parity", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	foreign, err := server.projects.Create("Foreign HTTP parity", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	createTask := func(projectID, goal string) task.Task {
		created, err := server.tasks.Create(task.CreateRequest{
			ProjectID: projectID, Goal: goal, RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		})
		if err != nil {
			t.Fatalf("create Task: %v", err)
		}
		return created
	}
	ownerTask := createTask(createdProject.ID, "HTTP parity")
	peerTask := createTask(createdProject.ID, "HTTP peer")
	launch := func(created task.Task) blackboardv2.ContinuationLaunch {
		result, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
			ProjectID: created.ProjectID, TaskID: created.ID, RuntimeProfileID: profile.ID,
			RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
			RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
		})
		if err != nil {
			t.Fatalf("launch Continuation: %v", err)
		}
		return result
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return v2HTTPFixture{
		server: server, httpServer: httpServer, project: createdProject, foreign: foreign,
		task: ownerTask, peerTask: peerTask, profile: profile,
		continuation: launch(ownerTask), peer: launch(peerTask),
		operator: operatorToken, base: httpServer.URL + "/api/v2/projects/" + createdProject.ID,
		runtimeRoot: runtimeRoot,
	}
}

func TestBlackboardV2HTTPSnapshotAndDetailUseRevisionETags(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	seed := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:etag","type":"entity","record":{"status":"active","kind":"host","name":"ETag host","scope_status":"in_scope"}}]}`
	if body := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "etag-seed", seed); !bytes.Contains(body, []byte(`"revision"`)) {
		t.Fatalf("seed = %s", body)
	}

	snapshot := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", fixture.operator, "operator", "", "")
	if snapshot.status != http.StatusOK {
		t.Fatalf("snapshot status = %d %s", snapshot.status, snapshot.body)
	}
	etag := snapshot.header.Get("ETag")
	if etag == "" || etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Fatalf("snapshot ETag must be a quoted revision, got %q", etag)
	}
	if cache := snapshot.header.Get("Cache-Control"); cache != "private, no-cache" {
		t.Fatalf("snapshot Cache-Control = %q", cache)
	}
	var snap blackboardv2.RuntimeSnapshot
	if err := json.Unmarshal(snapshot.body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	wantETag := `"` + strconv.Itoa(snap.Revision) + `"`
	if etag != wantETag {
		t.Fatalf("snapshot ETag = %q, want revision %q", etag, wantETag)
	}
	notModified := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", fixture.operator, "operator", "", "", v2HTTPOptions{ifNoneMatch: etag})
	if notModified.status != http.StatusNotModified || len(bytes.TrimSpace(notModified.body)) != 0 {
		t.Fatalf("snapshot If-None-Match = %d %q", notModified.status, notModified.body)
	}
	star := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", fixture.operator, "operator", "", "", v2HTTPOptions{ifNoneMatch: "*"})
	if star.status != http.StatusNotModified {
		t.Fatalf("snapshot If-None-Match=* = %d %s", star.status, star.body)
	}
	listMatch := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", fixture.operator, "operator", "", "", v2HTTPOptions{ifNoneMatch: `"0", ` + etag})
	if listMatch.status != http.StatusNotModified {
		t.Fatalf("snapshot multi-value If-None-Match = %d %s", listMatch.status, listMatch.body)
	}

	detail := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:etag", fixture.operator, "operator", "", "")
	if detail.status != http.StatusOK {
		t.Fatalf("detail status = %d %s", detail.status, detail.body)
	}
	detailETag := detail.header.Get("ETag")
	if detailETag != etag {
		t.Fatalf("detail ETag = %q, snapshot ETag = %q", detailETag, etag)
	}
	if detail.header.Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("detail Cache-Control = %q", detail.header.Get("Cache-Control"))
	}
	detail304 := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:etag", fixture.operator, "operator", "", "", v2HTTPOptions{ifNoneMatch: detailETag})
	if detail304.status != http.StatusNotModified || len(bytes.TrimSpace(detail304.body)) != 0 {
		t.Fatalf("detail If-None-Match = %d %q", detail304.status, detail304.body)
	}

	// After a mutation the revision advances and stale ETags must revalidate.
	update := `{"schema":"semantic-change-batch/v2","changes":[{"op":"update","key":"entity:etag","version":1,"type":"entity","record":{"name":"ETag host v2"}}]}`
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "etag-update", update)
	stale := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:etag", fixture.operator, "operator", "", "", v2HTTPOptions{ifNoneMatch: etag})
	if stale.status != http.StatusOK {
		t.Fatalf("stale ETag revalidation = %d %s", stale.status, stale.body)
	}
	if stale.header.Get("ETag") == etag {
		t.Fatalf("mutated detail kept stale ETag %q", etag)
	}
}

func TestBlackboardV2HTTPServiceParityForSuccessReplayConflictValidationAndClosure(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	ctx := context.Background()

	// Success + exact replay for change match the semantic service.
	change := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "http-parity-entity",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:parity", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Parity host", ScopeStatus: "in_scope"},
		}},
	}
	// First apply through HTTP; second request is exact replay.
	first := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", change.IdempotencyKey,
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:parity","type":"entity","record":{"status":"active","kind":"host","name":"Parity host","scope_status":"in_scope"}}]}`)
	serviceReplay, err := fixture.server.blackboardV2.ApplyForContinuation(ctx, fixture.project.ID, fixture.continuation.Continuation.ID, change)
	if err != nil {
		t.Fatalf("service exact replay: %v", err)
	}
	serviceRaw, err := json.Marshal(serviceReplay)
	if err != nil {
		t.Fatalf("marshal service result: %v", err)
	}
	httpReplay := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", change.IdempotencyKey,
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:parity","type":"entity","record":{"status":"active","kind":"host","name":"Parity host","scope_status":"in_scope"}}]}`)
	if !bytes.Equal(first, httpReplay) {
		t.Fatalf("HTTP exact replay drifted\nfirst=%s\nreplay=%s", first, httpReplay)
	}
	var httpResult, serviceResult blackboardv2.ChangeResult
	if err := json.Unmarshal(first, &httpResult); err != nil {
		t.Fatalf("decode HTTP change: %v", err)
	}
	if err := json.Unmarshal(serviceRaw, &serviceResult); err != nil {
		t.Fatalf("decode service change: %v", err)
	}
	if httpResult.Schema != serviceResult.Schema || httpResult.Revision != serviceResult.Revision {
		t.Fatalf("HTTP change=%#v service=%#v", httpResult, serviceResult)
	}

	// Altered idempotent replay is a closed conflict envelope (409).
	altered := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", change.IdempotencyKey,
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:parity","type":"entity","record":{"status":"active","kind":"host","name":"Different","scope_status":"in_scope"}}]}`)
	assertV2ErrorEnvelope(t, altered.status, altered.body, "idempotency_conflict", http.StatusConflict)

	// Detail and history match the service.
	serviceDetail, err := fixture.server.blackboardV2.ReadCurrent(ctx, fixture.project.ID, "entity:parity")
	if err != nil {
		t.Fatalf("service detail: %v", err)
	}
	detailRaw := mustV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:parity", fixture.continuation.Token, "", "", "")
	var httpDetail blackboardv2.CurrentDetail
	if err := json.Unmarshal(detailRaw, &httpDetail); err != nil {
		t.Fatalf("decode HTTP detail: %v", err)
	}
	if httpDetail.Key != serviceDetail.Key || httpDetail.Version != serviceDetail.Version || httpDetail.Record.Name != serviceDetail.Record.Name {
		t.Fatalf("HTTP detail=%#v service=%#v", httpDetail, serviceDetail)
	}
	nameV2, nameV3 := "Parity host v2", "Parity host v3"
	for i, name := range []string{nameV2, nameV3} {
		mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-parity-update-"+strconv.Itoa(i+2),
			`{"schema":"semantic-change-batch/v2","changes":[{"op":"update","key":"entity:parity","version":`+strconv.Itoa(i+1)+`,"type":"entity","record":{"name":"`+name+`"}}]}`)
	}
	serviceHistory, err := fixture.server.blackboardV2.ReadHistory(ctx, fixture.project.ID, "entity:parity", blackboardv2.HistoryOptions{Limit: 1})
	if err != nil {
		t.Fatalf("service history: %v", err)
	}
	historyRaw := mustV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:parity/history?limit=1", fixture.continuation.Token, "", "", "")
	var httpHistory blackboardv2.SemanticHistory
	if err := json.Unmarshal(historyRaw, &httpHistory); err != nil {
		t.Fatalf("decode HTTP history: %v", err)
	}
	if httpHistory.Schema != serviceHistory.Schema || len(httpHistory.Items) != 1 || httpHistory.Items[0].Version != serviceHistory.Items[0].Version || httpHistory.NextCursor == "" {
		t.Fatalf("HTTP history=%#v service=%#v", httpHistory, serviceHistory)
	}
	pageTwo := mustV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:parity/history?limit=1&cursor="+url.QueryEscape(httpHistory.NextCursor), fixture.continuation.Token, "", "", "")
	if !bytes.Contains(pageTwo, []byte(`"schema":"semantic-history/v2"`)) {
		t.Fatalf("history page two = %s", pageTwo)
	}

	// Stale version → 409 version_conflict with closed envelope.
	stale := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-stale",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"update","key":"entity:parity","version":1,"type":"entity","record":{"name":"stale"}}]}`)
	assertV2ErrorEnvelope(t, stale.status, stale.body, "version_conflict", http.StatusConflict)

	// Semantic validation → 422 with closed error envelope (no 200+error).
	invalid := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-invalid-entity",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:invalid","type":"entity","record":{"status":"active","kind":"host","name":"","scope_status":"in_scope"}}]}`)
	assertV2ErrorEnvelope(t, invalid.status, invalid.body, "semantic_validation", http.StatusUnprocessableEntity)

	// Project mismatch → 403 authority_denied without foreign data.
	foreignBase := fixture.httpServer.URL + "/api/v2/projects/" + fixture.foreign.ID
	mismatch := doV2HTTP(t, http.MethodGet, foreignBase+"/blackboard/records/entity:parity", fixture.continuation.Token, "", "", "")
	assertV2ErrorEnvelope(t, mismatch.status, mismatch.body, "authority_denied", http.StatusForbidden)
	if bytes.Contains(mismatch.body, []byte("Parity host")) {
		t.Fatalf("project mismatch leaked record body: %s", mismatch.body)
	}

	// Closed Continuation → 410 for later writes/reads that require a live grant binding.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/continuation:finish", fixture.continuation.Token, "", "http-parity-finish", `{}`)
	closed := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-after-finish",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:after-finish","type":"entity","record":{"status":"active","kind":"host","name":"After","scope_status":"in_scope"}}]}`)
	assertV2ErrorEnvelope(t, closed.status, closed.body, "closed_continuation", http.StatusGone)
	closedRead := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:parity", fixture.continuation.Token, "", "", "")
	assertV2ErrorEnvelope(t, closedRead.status, closedRead.body, "closed_continuation", http.StatusGone)
}

func TestBlackboardV2HTTPHistoryDefaultLimitAndOpaqueCursor(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	// Create more than the default page size (20) of versions.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "history-create",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:history","type":"entity","record":{"status":"active","kind":"host","name":"v1","scope_status":"in_scope"}}]}`)
	for version := 1; version <= 22; version++ {
		name := "v" + strconv.Itoa(version+1)
		mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "history-update-"+strconv.Itoa(version),
			`{"schema":"semantic-change-batch/v2","changes":[{"op":"update","key":"entity:history","version":`+strconv.Itoa(version)+`,"type":"entity","record":{"name":"`+name+`"}}]}`)
	}
	defaultPage := mustV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:history/history", fixture.operator, "operator", "", "")
	var page blackboardv2.SemanticHistory
	if err := json.Unmarshal(defaultPage, &page); err != nil {
		t.Fatalf("decode default history: %v", err)
	}
	if len(page.Items) != 20 || page.NextCursor == "" || !strings.HasPrefix(page.NextCursor, "opaque:") {
		t.Fatalf("default history page = items=%d cursor=%q", len(page.Items), page.NextCursor)
	}
	over := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:history/history?limit=101", fixture.operator, "operator", "", "")
	assertV2ErrorEnvelope(t, over.status, over.body, "semantic_validation", http.StatusUnprocessableEntity)
	// Foreign Project cannot reuse the opaque cursor.
	foreignHistory := doV2HTTP(t, http.MethodGet, fixture.httpServer.URL+"/api/v2/projects/"+fixture.foreign.ID+"/blackboard/records/entity:history/history?cursor="+url.QueryEscape(page.NextCursor), fixture.operator, "operator", "", "")
	if foreignHistory.status == http.StatusOK {
		t.Fatalf("foreign Project accepted foreign cursor: %s", foreignHistory.body)
	}
	if !bytes.Contains(foreignHistory.body, []byte(`"error"`)) {
		t.Fatalf("foreign cursor must use error envelope: %s", foreignHistory.body)
	}
}

func TestBlackboardV2HTTPAuthenticatedErrorCarriesSameProjectSyncOnly(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	// Peer is behind; operator mutation advances Project revision for the owner path.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "sync-external",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:sync-http","type":"entity","record":{"status":"active","kind":"host","name":"Sync","scope_status":"in_scope"}}]}`)
	// Peer's semantic failure attaches same-Project synchronization only.
	missing := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:missing:checkpoint", fixture.peer.Token, "", "sync-missing-checkpoint",
		`{"version":1,"summary":"must fail"}`)
	if missing.status != http.StatusNotFound {
		t.Fatalf("missing checkpoint status = %d %s", missing.status, missing.body)
	}
	var envelope struct {
		Error *blackboardv2.Error                     `json:"error"`
		Sync  *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(missing.body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "not_found" || envelope.Sync == nil {
		t.Fatalf("expected not_found with sync, got %s", missing.body)
	}
	if envelope.Sync.Reason != "another_task_changed_shared_project_knowledge" {
		t.Fatalf("sync reason = %#v", envelope.Sync)
	}
	if envelope.Sync.Snapshot.Schema != "runtime-blackboard/v2" {
		t.Fatalf("sync snapshot = %#v", envelope.Sync.Snapshot)
	}
	// Sync must not advertise a different Project's identity or operator token.
	if bytes.Contains(missing.body, []byte(fixture.foreign.ID)) || bytes.Contains(missing.body, []byte(fixture.operator)) || bytes.Contains(missing.body, []byte(fixture.peer.Token)) {
		t.Fatalf("sync/error leaked foreign identity or secrets: %s", missing.body)
	}
}

// Issue #117 — parallel Task notice delivers once on the next trusted response,
// then later responses stay ordinary delta/detail only.
func TestBlackboardV2HTTPParallelTaskSyncDeliversOnceThenOrdinaryResponses(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	// Owner advances shared Project knowledge; peer Continuation stays behind.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "parallel-owner-entity",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:parallel-http","type":"entity","record":{"status":"active","kind":"host","name":"Parallel","scope_status":"in_scope"}}]}`)
	want, err := fixture.server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project expected Snapshot: %v", err)
	}

	// Next trusted read piggybacks exact current Snapshot + reason, no Task identity.
	first := mustV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:parallel-http", fixture.peer.Token, "", "", "")
	var synchronized struct {
		Schema string                                 `json:"schema"`
		Key    string                                 `json:"key"`
		Sync   *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(first, &synchronized); err != nil {
		t.Fatalf("decode synchronized read: %v", err)
	}
	if synchronized.Schema != "blackboard-record/v2" || synchronized.Key != "entity:parallel-http" || synchronized.Sync == nil {
		t.Fatalf("first peer read = %s", first)
	}
	if synchronized.Sync.Reason != "another_task_changed_shared_project_knowledge" || synchronized.Sync.Revision != want.Snapshot.Revision {
		t.Fatalf("sync attachment = %#v", synchronized.Sync)
	}
	gotSnapshot, err := json.Marshal(synchronized.Sync.Snapshot)
	if err != nil {
		t.Fatalf("marshal sync snapshot: %v", err)
	}
	if !bytes.Equal(gotSnapshot, want.Bytes) {
		t.Fatalf("HTTP sync Snapshot is not exact canonical bytes\ngot=%s\nwant=%s", gotSnapshot, want.Bytes)
	}
	for _, leak := range []string{fixture.task.ID, fixture.peerTask.ID, fixture.continuation.Continuation.ID, fixture.peer.Continuation.ID, fixture.project.ID} {
		if leak != "" && bytes.Contains(first, []byte(leak)) {
			t.Fatalf("synchronized read leaked identity %q: %s", leak, first)
		}
	}
	onDisk, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, fixture.peerTask.ID, "workdir", ".pentest", "blackboard.json"))
	if err != nil {
		t.Fatalf("read peer Working Snapshot after delivery: %v", err)
	}
	if !bytes.Equal(onDisk, want.Bytes) {
		t.Fatalf("peer Working Snapshot not replaced on delivery\ngot=%s\nwant=%s", onDisk, want.Bytes)
	}

	// Later read returns ordinary detail only.
	second := mustV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:parallel-http", fixture.peer.Token, "", "", "")
	if bytes.Contains(second, []byte(`"sync"`)) {
		t.Fatalf("later peer read reattached sync: %s", second)
	}
	if !bytes.Contains(second, []byte(`"schema":"blackboard-record/v2"`)) {
		t.Fatalf("later peer read = %s", second)
	}

	// Peer opens work, then another external advance, then checkpoint carries sync once.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "parallel-peer-open",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:parallel","type":"objective","record":{"status":"open","objective":"Observe parallel sync"}},{"op":"create","key":"attempt:parallel","type":"attempt","record":{"status":"open","summary":"Watching"}},{"op":"relate","from":"attempt:parallel","relation":"tests","to":"objective:parallel"}]}`)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "parallel-external-again",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:parallel-again","type":"entity","record":{"status":"active","kind":"host","name":"Again","scope_status":"in_scope"}}]}`)
	checkpoint := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:parallel:checkpoint", fixture.peer.Token, "", "parallel-checkpoint-sync",
		`{"version":1,"summary":"Checkpoint after external change"}`)
	if !bytes.Contains(checkpoint, []byte(`"schema":"semantic-change-result/v2"`)) || !bytes.Contains(checkpoint, []byte(`"sync"`)) || !bytes.Contains(checkpoint, []byte(`another_task_changed_shared_project_knowledge`)) {
		t.Fatalf("checkpoint while pending must attach sync: %s", checkpoint)
	}
	// Exact checkpoint response-loss replay redelivers the same sync attachment.
	checkpointReplay := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:parallel:checkpoint", fixture.peer.Token, "", "parallel-checkpoint-sync",
		`{"version":1,"summary":"Checkpoint after external change"}`)
	if !bytes.Equal(checkpoint, checkpointReplay) {
		t.Fatalf("checkpoint response-loss replay drifted\nfirst=%s\nreplay=%s", checkpoint, checkpointReplay)
	}
	// A different later trusted mutation stays ordinary (no sticky sync).
	later := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:parallel:checkpoint", fixture.peer.Token, "", "parallel-checkpoint-later",
		`{"version":2,"summary":"Later ordinary checkpoint"}`)
	if bytes.Contains(later, []byte(`"sync"`)) {
		t.Fatalf("later peer checkpoint reattached sync: %s", later)
	}
}

// Issue #117 P1 — lost authenticated HTTP response must redeliver the exact
// synchronization attachment on Idempotency-Key retry; later keys must not.
func TestBlackboardV2HTTPResponseLossRetryRedeliversExactSyncAttachment(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "loss-open-work",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:loss","type":"objective","record":{"status":"open","objective":"Loss retry"}},{"op":"create","key":"attempt:loss","type":"attempt","record":{"status":"open","summary":"Watching"}},{"op":"relate","from":"attempt:loss","relation":"tests","to":"objective:loss"}]}`)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "loss-external",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:loss-http","type":"entity","record":{"status":"active","kind":"host","name":"Loss","scope_status":"in_scope"}}]}`)
	first := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:loss:checkpoint", fixture.peer.Token, "", "loss-checkpoint",
		`{"version":1,"summary":"Checkpoint with pending sync"}`)
	var delivered struct {
		Schema string                                 `json:"schema"`
		Sync   *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(first, &delivered); err != nil || delivered.Schema != "semantic-change-result/v2" || delivered.Sync == nil {
		t.Fatalf("first checkpoint = %s err=%v", first, err)
	}
	// Capture after the checkpoint write so the attachment matches the exact
	// post-action current Snapshot that was delivered and acknowledged.
	want, err := fixture.server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project Snapshot: %v", err)
	}
	if delivered.Sync.Reason != "another_task_changed_shared_project_knowledge" || delivered.Sync.Revision != want.Snapshot.Revision {
		t.Fatalf("sync attachment = %#v want revision %d", delivered.Sync, want.Snapshot.Revision)
	}
	gotSnapshot, err := json.Marshal(delivered.Sync.Snapshot)
	if err != nil || !bytes.Equal(gotSnapshot, want.Bytes) {
		t.Fatalf("sync Snapshot drifted\ngot=%s\nwant=%s err=%v", gotSnapshot, want.Bytes, err)
	}
	for _, leak := range []string{fixture.task.ID, fixture.peerTask.ID, fixture.continuation.Continuation.ID, fixture.peer.Continuation.ID, `"task_id"`, `"project_id"`, `"continuation_id"`} {
		if leak != "" && bytes.Contains(first, []byte(leak)) {
			t.Fatalf("sync attachment leaked %q: %s", leak, first)
		}
	}
	// Simulate transport loss: client never observed first body; exact retry must match.
	retry := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:loss:checkpoint", fixture.peer.Token, "", "loss-checkpoint",
		`{"version":1,"summary":"Checkpoint with pending sync"}`)
	if !bytes.Equal(first, retry) {
		t.Fatalf("response-loss retry drifted\nfirst=%s\nretry=%s", first, retry)
	}
	// Ordinary later response without Pending does not keep receiving sync.
	later := mustV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:loss-http", fixture.peer.Token, "", "", "")
	if bytes.Contains(later, []byte(`"sync"`)) {
		t.Fatalf("later read reattached sync: %s", later)
	}
}

// Issue #117 — conditional GET must not acknowledge-then-discard a pending sync
// via 304 empty body. When sync is delivered, the response must carry a body.
func TestBlackboardV2HTTPConditionalDoesNotDiscardAcknowledgedSyncAttachment(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	// External write creates a pending notice for the peer Continuation.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "cond-sync-external",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:cond-sync","type":"entity","record":{"status":"active","kind":"host","name":"Cond","scope_status":"in_scope"}}]}`)
	want, err := fixture.server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project Snapshot: %v", err)
	}
	// Client presents If-None-Match for the current Project revision (the same
	// value the snapshot action would use for ETag). Capture would acknowledge
	// then a naive 304 would discard the only Pending-only delivery.
	matchingETag := `"` + strconv.Itoa(want.Snapshot.Revision) + `"`
	withSync := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", fixture.peer.Token, "", "", "", v2HTTPOptions{ifNoneMatch: matchingETag})
	if withSync.status != http.StatusOK {
		t.Fatalf("pending sync conditional = %d, want 200 body (not 304 discard)", withSync.status)
	}
	if !bytes.Contains(withSync.body, []byte(`"sync"`)) || !bytes.Contains(withSync.body, []byte(`another_task_changed_shared_project_knowledge`)) {
		t.Fatalf("pending sync conditional missing attachment: %s", withSync.body)
	}
	// After acknowledgement, ordinary matching ETag may 304 with empty body.
	newETag := withSync.header.Get("ETag")
	if newETag == "" {
		t.Fatalf("missing ETag after sync delivery")
	}
	after := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", fixture.peer.Token, "", "", "", v2HTTPOptions{ifNoneMatch: newETag})
	if after.status != http.StatusNotModified || len(bytes.TrimSpace(after.body)) != 0 {
		t.Fatalf("post-sync ordinary 304 = %d %q", after.status, after.body)
	}
}

// Issue #117 — semantic error responses must keep a finalized sync attachment
// in the body (never ack then strip the only delivery).
func TestBlackboardV2HTTPErrorDoesNotDiscardAcknowledgedSyncAttachment(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "err-sync-open",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:err-sync","type":"objective","record":{"status":"open","objective":"Err sync"}},{"op":"create","key":"attempt:err-sync","type":"attempt","record":{"status":"open","summary":"Watch"}},{"op":"relate","from":"attempt:err-sync","relation":"tests","to":"objective:err-sync"}]}`)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "err-sync-external",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:err-sync","type":"entity","record":{"status":"active","kind":"host","name":"Err","scope_status":"in_scope"}}]}`)
	// Version conflict on checkpoint while Pending: error envelope must include sync.
	conflict := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:err-sync:checkpoint", fixture.peer.Token, "", "err-sync-checkpoint",
		`{"version":99,"summary":"Stale version while pending sync"}`)
	if conflict.status == http.StatusOK {
		t.Fatalf("expected semantic error, got 200: %s", conflict.body)
	}
	if !bytes.Contains(conflict.body, []byte(`"error"`)) || !bytes.Contains(conflict.body, []byte(`"sync"`)) {
		t.Fatalf("semantic error discarded sync after delivery: %d %s", conflict.status, conflict.body)
	}
	if !bytes.Contains(conflict.body, []byte(`another_task_changed_shared_project_knowledge`)) {
		t.Fatalf("semantic error sync missing reason: %s", conflict.body)
	}
	// Exact response-loss retry of the same erroring key redelivers identical sync.
	retry := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:err-sync:checkpoint", fixture.peer.Token, "", "err-sync-checkpoint",
		`{"version":99,"summary":"Stale version while pending sync"}`)
	if !bytes.Contains(retry.body, []byte(`"sync"`)) {
		t.Fatalf("error response-loss retry lost sync: %d %s", retry.status, retry.body)
	}
	var first, second struct {
		Sync *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(conflict.body, &first); err != nil || first.Sync == nil {
		t.Fatalf("decode first error sync: %v body=%s", err, conflict.body)
	}
	if err := json.Unmarshal(retry.body, &second); err != nil || second.Sync == nil {
		t.Fatalf("decode retry error sync: %v body=%s", err, retry.body)
	}
	a, _ := json.Marshal(first.Sync)
	b, _ := json.Marshal(second.Sync)
	if !bytes.Equal(a, b) {
		t.Fatalf("error sync replay drifted\nfirst=%s\nretry=%s", a, b)
	}
}

// Issue #117 P1 — initial live Finish carries exact sync when pending; closed
// exact Finish replay stays byte-stable (including the original attachment).
func TestBlackboardV2HTTPFinishCarriesSyncWhenPendingAndExactReplayStable(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "finish-sync-open",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:finish-sync","type":"objective","record":{"status":"open","objective":"Finish sync"}},{"op":"create","key":"attempt:finish-sync","type":"attempt","record":{"status":"open","summary":"Work"}},{"op":"relate","from":"attempt:finish-sync","relation":"tests","to":"objective:finish-sync"}]}`)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "finish-sync-terminal",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"transition","key":"attempt:finish-sync","version":1,"status":"failed","summary":"Done without reusable outcome"}]}`)
	// External advance after the Runtime's own terminal write so Finish itself is
	// the next trusted response that must carry the pending synchronization.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "finish-sync-external",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:finish-sync","type":"entity","record":{"status":"active","kind":"host","name":"Finish sync","scope_status":"in_scope"}}]}`)
	want, err := fixture.server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project Snapshot: %v", err)
	}
	finished := mustV2HTTP(t, http.MethodPost, fixture.base+"/continuation:finish", fixture.peer.Token, "", "finish-with-sync", `{}`)
	var envelope struct {
		Schema          string                                 `json:"schema"`
		Status          string                                 `json:"status"`
		Revision        int                                    `json:"revision"`
		WorkingSnapshot blackboardv2.WorkingSnapshot           `json:"working_snapshot"`
		Sync            *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(finished, &envelope); err != nil {
		t.Fatalf("decode finish: %v body=%s", err, finished)
	}
	if envelope.Schema != "continuation-finish/v2" || envelope.Status != "finished" || envelope.Sync == nil {
		t.Fatalf("finish while pending must carry sync: %s", finished)
	}
	if envelope.Sync.Reason != "another_task_changed_shared_project_knowledge" ||
		envelope.Sync.FromRevision < 0 || envelope.Sync.Revision != want.Snapshot.Revision ||
		envelope.Sync.Revision != envelope.Revision {
		t.Fatalf("finish sync attachment = %#v want revision %d", envelope.Sync, want.Snapshot.Revision)
	}
	gotSnapshot, err := json.Marshal(envelope.Sync.Snapshot)
	if err != nil || !bytes.Equal(gotSnapshot, want.Bytes) {
		t.Fatalf("finish sync Snapshot drifted\ngot=%s\nwant=%s err=%v", gotSnapshot, want.Bytes, err)
	}
	for _, leak := range []string{fixture.task.ID, fixture.peerTask.ID, fixture.continuation.Continuation.ID, fixture.peer.Continuation.ID, `"task_id"`, `"project_id"`, `"continuation_id"`} {
		if leak != "" && bytes.Contains(finished, []byte(leak)) {
			t.Fatalf("finish sync leaked %q: %s", leak, finished)
		}
	}
	replayed := mustV2HTTP(t, http.MethodPost, fixture.base+"/continuation:finish", fixture.peer.Token, "", "finish-with-sync", `{}`)
	if !bytes.Equal(finished, replayed) {
		t.Fatalf("exact Finish replay drifted\nfirst=%s\nreplay=%s", finished, replayed)
	}
}

func TestBlackboardV2HTTPStatusMappingStorageBusyInternalAndAuth(t *testing.T) {
	// Closed mapping table: transport never invents 200 error bodies.
	cases := []struct {
		code   string
		path   string
		status int
	}{
		{"invalid_schema", "body", http.StatusBadRequest},
		{"authority_denied", "authorization", http.StatusUnauthorized},
		{"authority_denied", "path.project_id", http.StatusForbidden},
		{"not_found", "key", http.StatusNotFound},
		{"closed_continuation", "", http.StatusGone},
		{"version_conflict", "version", http.StatusConflict},
		{"semantic_validation", "limit", http.StatusUnprocessableEntity},
		{"storage_busy", "storage", http.StatusServiceUnavailable},
		{"internal", "internal", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		got := blackboardV2HTTPStatus(&blackboardv2.Error{Code: tc.code, Path: tc.path, Retryable: tc.code == "storage_busy"})
		if got != tc.status {
			t.Fatalf("code %s path %s => %d, want %d", tc.code, tc.path, got, tc.status)
		}
	}
	busy := asBlackboardV2Error(errors.New("SQLITE_BUSY: database is locked"))
	if busy.Code != "storage_busy" || !busy.Retryable {
		t.Fatalf("busy mapping = %#v", busy)
	}
	if got := blackboardV2HTTPStatus(busy); got != http.StatusServiceUnavailable {
		t.Fatalf("busy status = %d", got)
	}
	raw := asBlackboardV2Error(errors.New("sql: connection refused at /tmp/secret.db"))
	if raw.Code != "internal" || strings.Contains(raw.Message, "secret.db") || strings.Contains(raw.Message, "connection refused") {
		t.Fatalf("internal error must be sanitized, got %#v", raw)
	}

	fixture := newV2HTTPFixture(t)
	// Missing Authorization → 401.
	unauth := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", "", "", "", "")
	assertV2ErrorEnvelope(t, unauth.status, unauth.body, "authority_denied", http.StatusUnauthorized)
	// Query-string credentials rejected even without Authorization.
	queryOnly, err := http.NewRequest(http.MethodGet, fixture.base+"/blackboard/snapshot?token="+fixture.operator, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := http.DefaultClient.Do(queryOnly)
	if err != nil {
		t.Fatalf("query-only auth: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK || !bytes.Contains(body, []byte(`"invalid_schema"`)) {
		t.Fatalf("query-only credential accepted: %d %s", response.StatusCode, body)
	}
	// Body-carried Idempotency-Key is rejected; transport header is authoritative.
	conflictKey := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "header-key",
		`{"schema":"semantic-change-batch/v2","idempotency_key":"body-key","changes":[]}`)
	assertV2ErrorEnvelope(t, conflictKey.status, conflictKey.body, "invalid_schema", http.StatusBadRequest)
}

func TestBlackboardV2HTTPEvidenceCheckpointFinishParityAndNoAuthorityLeak(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-work-open",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:http-target","type":"entity","record":{"status":"active","kind":"host","name":"Target","scope_status":"in_scope"}},{"op":"create","key":"objective:http-parity","type":"objective","record":{"status":"open","objective":"Retain proof"}},{"op":"create","key":"attempt:http-parity","type":"attempt","record":{"status":"open","summary":"Collecting"}},{"op":"relate","from":"attempt:http-parity","relation":"tests","to":"objective:http-parity"}]}`)
	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("http parity proof\n"), 0o600); err != nil {
		t.Fatalf("write proof: %v", err)
	}
	retain := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/evidence:retain", fixture.continuation.Token, "", "http-retain",
		`{"key":"evidence:http-parity","attempt":"attempt:http-parity","source_path":"proof.txt","artifact_type":"text","summary":"HTTP proof","media_type":"text/plain","links":[["about","entity:http-target"]]}`)
	if !bytes.Contains(retain, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("retain = %s", retain)
	}
	for _, leak := range []string{fixture.project.ID, fixture.task.ID, fixture.continuation.Continuation.ID, fixture.continuation.Token, fixture.operator, `"project_id"`, `"task_id"`, `"continuation_id"`} {
		if bytes.Contains(retain, []byte(leak)) {
			t.Fatalf("retain leaked %q: %s", leak, retain)
		}
	}
	checkpoint := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:http-parity:checkpoint", fixture.continuation.Token, "", "http-checkpoint-parity",
		`{"version":1,"summary":"Proof retained"}`)
	if !bytes.Contains(checkpoint, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("checkpoint = %s", checkpoint)
	}
	// Exact checkpoint replay.
	checkpointReplay := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:http-parity:checkpoint", fixture.continuation.Token, "", "http-checkpoint-parity",
		`{"version":1,"summary":"Proof retained"}`)
	if !bytes.Equal(checkpoint, checkpointReplay) {
		t.Fatalf("checkpoint replay drifted\nfirst=%s\nreplay=%s", checkpoint, checkpointReplay)
	}
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-terminal-parity",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"transition","key":"attempt:http-parity","version":2,"status":"succeeded","summary":"done"}]}`)
	finished := mustV2HTTP(t, http.MethodPost, fixture.base+"/continuation:finish", fixture.continuation.Token, "", "http-finish-parity", `{}`)
	if !bytes.Contains(finished, []byte(`"schema":"continuation-finish/v2"`)) || bytes.Contains(finished, []byte(`"sync"`)) {
		t.Fatalf("finish = %s", finished)
	}
	// Exact Finish replay stays stable and never reattaches live sync.
	finishReplay := mustV2HTTP(t, http.MethodPost, fixture.base+"/continuation:finish", fixture.continuation.Token, "", "http-finish-parity", `{}`)
	if !bytes.Equal(finished, finishReplay) {
		t.Fatalf("finish replay drifted\nfirst=%s\nreplay=%s", finished, finishReplay)
	}
	// Operator cannot call Continuation-only routes.
	operatorCheckpoint := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:http-parity:checkpoint", fixture.operator, "operator", "operator-checkpoint",
		`{"version":2,"summary":"nope"}`)
	assertV2ErrorEnvelope(t, operatorCheckpoint.status, operatorCheckpoint.body, "authority_denied", http.StatusForbidden)
}

func TestBlackboardV2HTTPUnexpectedFailureIsSanitized500(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	// Close the store under the daemon to force an unexpected service fault.
	if err := fixture.server.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	failed := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", fixture.operator, "operator", "", "")
	semantic := assertV2ErrorEnvelope(t, failed.status, failed.body, "internal", http.StatusInternalServerError)
	if strings.Contains(strings.ToLower(semantic.Message), "sql") || strings.Contains(semantic.Message, "closed") {
		t.Fatalf("internal message leaked storage detail: %#v", semantic)
	}
}

// Authenticated service-origin invalid_schema (unsupported schema after grant
// binding) must retain same-Project sync when the Continuation is behind.
// Transport/body-parse invalid_schema must never fabricate sync.
func TestBlackboardV2HTTPInvalidSchemaSyncAttachDistinguishesServiceFromTransport(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	// Advance Project revision so the peer Continuation is behind and pending sync.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "sync-behind-entity",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:behind-schema","type":"entity","record":{"status":"active","kind":"host","name":"Behind","scope_status":"in_scope"}}]}`)

	// Exact reproduction: authenticated peer posts unsupported semantic schema.
	// Dispatch reaches the service after binding; invalid_schema keeps sync.
	unsupported := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "behind-unsupported-schema",
		`{"schema":"semantic-change-batch/v1","changes":[]}`)
	assertV2ErrorEnvelope(t, unsupported.status, unsupported.body, "invalid_schema", http.StatusBadRequest)
	var semanticEnvelope struct {
		Error *blackboardv2.Error                     `json:"error"`
		Sync  *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(unsupported.body, &semanticEnvelope); err != nil {
		t.Fatalf("decode unsupported-schema envelope: %v body=%s", err, unsupported.body)
	}
	if semanticEnvelope.Sync == nil || semanticEnvelope.Sync.Reason != "another_task_changed_shared_project_knowledge" {
		t.Fatalf("authenticated service invalid_schema must attach same-Project sync, got %s", unsupported.body)
	}
	if semanticEnvelope.Sync.Snapshot.Schema != "runtime-blackboard/v2" {
		t.Fatalf("sync snapshot = %#v", semanticEnvelope.Sync.Snapshot)
	}
	if bytes.Contains(unsupported.body, []byte(fixture.foreign.ID)) || bytes.Contains(unsupported.body, []byte(fixture.operator)) || bytes.Contains(unsupported.body, []byte(fixture.peer.Token)) {
		t.Fatalf("invalid_schema sync leaked foreign identity or secrets: %s", unsupported.body)
	}

	// Transport/body parse invalid_schema before authenticated semantic dispatch
	// must not fabricate a synchronization sibling.
	malformedAuth := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "body-parse-malformed",
		`{"schema":"semantic-change-batch/v2","changes":`)
	assertV2ErrorEnvelope(t, malformedAuth.status, malformedAuth.body, "invalid_schema", http.StatusBadRequest)
	if bytes.Contains(malformedAuth.body, []byte(`"sync"`)) {
		t.Fatalf("body-parse invalid_schema fabricated sync: %s", malformedAuth.body)
	}
	// Malicious unauthenticated caller cannot obtain sync via parse failures.
	unauthBody := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", "", "", "unauth-body",
		`{"schema":"semantic-change-batch/v2","changes":`)
	// Auth fails first for missing capability (no Continuation token).
	if unauthBody.status == http.StatusOK || bytes.Contains(unauthBody.body, []byte(`"sync"`)) {
		t.Fatalf("unauthenticated body-parse path must not attach sync: %d %s", unauthBody.status, unauthBody.body)
	}
	// Query-string credentials are transport invalid_schema without sync.
	queryCred, err := http.NewRequest(http.MethodPost, fixture.base+"/blackboard/changes?token="+fixture.peer.Token,
		strings.NewReader(`{"schema":"semantic-change-batch/v2","changes":[]}`))
	if err != nil {
		t.Fatalf("new query-cred request: %v", err)
	}
	queryCred.Header.Set("Authorization", "Bearer "+fixture.peer.Token)
	queryCred.Header.Set("Content-Type", "application/json")
	queryCred.Header.Set("Idempotency-Key", "query-cred-no-sync")
	response, err := http.DefaultClient.Do(queryCred)
	if err != nil {
		t.Fatalf("query-cred request: %v", err)
	}
	defer response.Body.Close()
	queryBody, _ := io.ReadAll(response.Body)
	assertV2ErrorEnvelope(t, response.StatusCode, queryBody, "invalid_schema", http.StatusBadRequest)
	if bytes.Contains(queryBody, []byte(`"sync"`)) {
		t.Fatalf("query-credential invalid_schema fabricated sync: %s", queryBody)
	}
	// Unknown body fields are transport invalid_schema after auth, still no sync.
	unknownField := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "unknown-field-no-sync",
		`{"schema":"semantic-change-batch/v2","changes":[],"project_id":"attacker"}`)
	assertV2ErrorEnvelope(t, unknownField.status, unknownField.body, "invalid_schema", http.StatusBadRequest)
	if bytes.Contains(unknownField.body, []byte(`"sync"`)) {
		t.Fatalf("unknown-field body-parse invalid_schema fabricated sync: %s", unknownField.body)
	}
}

// HTTP change, Evidence retain, and checkpoint exact replay remain available
// after Finish without live sync, matching service/MCP semantics. Changed
// retries and new writes stay closed; cross-project principals stay denied.
func TestBlackboardV2HTTPExactReplayAfterFinishForChangeRetainCheckpoint(t *testing.T) {
	fixture := newV2HTTPFixture(t)
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-replay-open",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:replay-target","type":"entity","record":{"status":"active","kind":"host","name":"Replay target","scope_status":"in_scope"}},{"op":"create","key":"objective:replay","type":"objective","record":{"status":"open","objective":"Replay objective"}},{"op":"create","key":"attempt:replay","type":"attempt","record":{"status":"open","summary":"Replay attempt"}},{"op":"relate","from":"attempt:replay","relation":"tests","to":"objective:replay"}]}`)

	changeBody := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:replay-exact","type":"entity","record":{"status":"active","kind":"host","name":"Exact replay host","scope_status":"in_scope"}}]}`
	changeFirst := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-replay-entity", changeBody)

	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "replay-proof.txt"), []byte("replay proof\n"), 0o600); err != nil {
		t.Fatalf("write proof: %v", err)
	}
	retainBody := `{"key":"evidence:replay","attempt":"attempt:replay","source_path":"replay-proof.txt","artifact_type":"text","summary":"Replay retained proof","media_type":"text/plain","links":[["about","entity:replay-target"]]}`
	retainFirst := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/evidence:retain", fixture.continuation.Token, "", "http-replay-retain", retainBody)

	checkpointBody := `{"version":1,"summary":"Checkpoint before finish"}`
	checkpointFirst := mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:replay:checkpoint", fixture.continuation.Token, "", "http-replay-checkpoint", checkpointBody)

	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-replay-terminal",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"transition","key":"attempt:replay","version":2,"status":"succeeded","summary":"Replay proof retained"}]}`)
	finishFirst := mustV2HTTP(t, http.MethodPost, fixture.base+"/continuation:finish", fixture.continuation.Token, "", "http-replay-finish", `{}`)

	// All-three mutating exact replays (plus Finish) after Finish: same bytes, no live sync.
	for _, tc := range []struct {
		name           string
		method         string
		url            string
		idempotencyKey string
		body           string
		first          []byte
	}{
		{"change", http.MethodPost, fixture.base + "/blackboard/changes", "http-replay-entity", changeBody, changeFirst},
		{"retain", http.MethodPost, fixture.base + "/blackboard/evidence:retain", "http-replay-retain", retainBody, retainFirst},
		{"checkpoint", http.MethodPost, fixture.base + "/blackboard/attempts/attempt:replay:checkpoint", "http-replay-checkpoint", checkpointBody, checkpointFirst},
		{"finish", http.MethodPost, fixture.base + "/continuation:finish", "http-replay-finish", `{}`, finishFirst},
	} {
		replay := mustV2HTTP(t, tc.method, tc.url, fixture.continuation.Token, "", tc.idempotencyKey, tc.body)
		if !bytes.Equal(replay, tc.first) {
			t.Fatalf("post-finish exact %s replay drifted\nfirst=%s\nreplay=%s", tc.name, tc.first, replay)
		}
		if bytes.Contains(replay, []byte(`"sync"`)) {
			t.Fatalf("post-finish exact %s replay attached live sync: %s", tc.name, replay)
		}
	}

	// Changed retries remain rejected (idempotency_conflict), not reopened writes.
	alteredChange := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-replay-entity",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:replay-exact","type":"entity","record":{"status":"active","kind":"host","name":"Altered","scope_status":"in_scope"}}]}`)
	assertV2ErrorEnvelope(t, alteredChange.status, alteredChange.body, "idempotency_conflict", http.StatusConflict)
	alteredRetain := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/evidence:retain", fixture.continuation.Token, "", "http-replay-retain",
		`{"key":"evidence:replay","attempt":"attempt:replay","source_path":"replay-proof.txt","artifact_type":"text","summary":"different summary","media_type":"text/plain","links":[["about","entity:replay-target"]]}`)
	assertV2ErrorEnvelope(t, alteredRetain.status, alteredRetain.body, "idempotency_conflict", http.StatusConflict)
	alteredCheckpoint := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/attempts/attempt:replay:checkpoint", fixture.continuation.Token, "", "http-replay-checkpoint",
		`{"version":1,"summary":"different checkpoint"}`)
	assertV2ErrorEnvelope(t, alteredCheckpoint.status, alteredCheckpoint.body, "idempotency_conflict", http.StatusConflict)

	// New writes remain closed_continuation after Finish.
	newWrite := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.continuation.Token, "", "http-replay-new-after-finish",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:after-finish","type":"entity","record":{"status":"active","kind":"host","name":"After finish","scope_status":"in_scope"}}]}`)
	assertV2ErrorEnvelope(t, newWrite.status, newWrite.body, "closed_continuation", http.StatusGone)
	// Live read/history authority remains closed.
	closedRead := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:replay-exact", fixture.continuation.Token, "", "", "")
	assertV2ErrorEnvelope(t, closedRead.status, closedRead.body, "closed_continuation", http.StatusGone)
	closedHistory := doV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/records/entity:replay-exact/history", fixture.continuation.Token, "", "", "")
	assertV2ErrorEnvelope(t, closedHistory.status, closedHistory.body, "closed_continuation", http.StatusGone)

	// Cross-project / foreign principal cannot obtain owner stored exact replay.
	foreignBase := fixture.httpServer.URL + "/api/v2/projects/" + fixture.foreign.ID
	foreignChange := doV2HTTP(t, http.MethodPost, foreignBase+"/blackboard/changes", fixture.continuation.Token, "", "http-replay-entity", changeBody)
	assertV2ErrorEnvelope(t, foreignChange.status, foreignChange.body, "authority_denied", http.StatusForbidden)
	if bytes.Equal(foreignChange.body, changeFirst) {
		t.Fatalf("foreign principal received owner exact change replay")
	}
	// Peer Continuation on the same Project cannot claim owner idempotency receipts.
	peerReplay := doV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.peer.Token, "", "http-replay-entity", changeBody)
	assertV2ErrorEnvelope(t, peerReplay.status, peerReplay.body, "authority_denied", http.StatusForbidden)
	if bytes.Equal(peerReplay.body, changeFirst) {
		t.Fatalf("peer principal received owner exact change replay")
	}
}

func TestBlackboardV2HTTPReportAndCTFSolutionConsumers(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "v2-report-http.db")
	const operatorToken = "operator-report-secret"
	server, err := NewServer(Config{
		Version: "test", DBPath: dbPath, RuntimeRoot: filepath.Join(root, "runs"),
		AuthToken: operatorToken, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	pentestProject, err := server.projects.Create("Alpha External", "External assessment", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create pentest Project: %v", err)
	}
	ctfProject, err := server.projects.CreateWithKind("Flag CTF", "Challenge", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Project: %v", err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	pentestBase := httpServer.URL + "/api/v2/projects/" + pentestProject.ID
	ctfBase := httpServer.URL + "/api/v2/projects/" + ctfProject.ID

	const criticalCVSS40 = "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"
	seedBody := `{"schema":"semantic-change-batch/v2","changes":[` +
		`{"op":"create","key":"fact:support","type":"fact","record":{"category":"auth","summary":"Bypass confirmed","body":"Reproduced","confidence":"confirmed","scope_status":"in_scope"}},` +
		`{"op":"create","key":"fact:tentative","type":"fact","record":{"category":"recon","summary":"Maybe related endpoint","confidence":"tentative","scope_status":"unknown"}},` +
		`{"op":"create","key":"finding:sqli","type":"finding","record":{"status":"unconfirmed","title":"SQL injection in login","target":"https://alpha.example/login","description":"Attacker-controlled SQL","proof":"Boolean payload worked","impact":"Account access","recommendation":"Parameterize","cvss_version":"4.0","cvss_vector":"` + criticalCVSS40 + `"}},` +
		`{"op":"create","key":"finding:verbose","type":"finding","record":{"status":"unconfirmed","title":"Verbose errors"}},` +
		`{"op":"transition","key":"finding:sqli","version":1,"status":"confirmed"},` +
		`{"op":"relate","from":"fact:support","relation":"supports","to":"finding:sqli","reason":"Independent reproduction"}` +
		`]}`
	mustV2HTTP(t, http.MethodPost, pentestBase+"/blackboard/changes", operatorToken, "operator", "report-seed", seedBody)

	jsonReport := mustV2HTTP(t, http.MethodGet, pentestBase+"/reports/pentest?format=json", operatorToken, "operator", "", "")
	if !bytes.Contains(jsonReport, []byte(`"schema":"pentest-report/v2"`)) ||
		!bytes.Contains(jsonReport, []byte(`"confirmed_findings"`)) ||
		!bytes.Contains(jsonReport, []byte(`"unconfirmed_findings"`)) ||
		!bytes.Contains(jsonReport, []byte(`"tentative_facts"`)) ||
		!bytes.Contains(jsonReport, []byte(`"key":"finding:sqli"`)) {
		t.Fatalf("pentest json report = %s", jsonReport)
	}
	for _, forbidden := range []string{pentestProject.ID, "trusted_origin", "state_hash", "projection_hash", "provenance"} {
		if bytes.Contains(bytes.ToLower(jsonReport), bytes.ToLower([]byte(forbidden))) {
			t.Fatalf("pentest json leaked %q: %s", forbidden, jsonReport)
		}
	}

	mdResult := doV2HTTP(t, http.MethodGet, pentestBase+"/reports/pentest?format=markdown", operatorToken, "operator", "", "")
	if mdResult.status != http.StatusOK {
		t.Fatalf("pentest markdown status = %d %s", mdResult.status, mdResult.body)
	}
	if mdResult.header.Get("ETag") == "" {
		t.Fatalf("pentest report missing ETag: %#v", mdResult.header)
	}
	if !bytes.Contains(mdResult.body, []byte(`"schema":"report-markdown/v2"`)) ||
		!bytes.Contains(mdResult.body, []byte("Confirmed Findings")) ||
		!bytes.Contains(mdResult.body, []byte("Unconfirmed Findings")) ||
		!bytes.Contains(mdResult.body, []byte("Tentative Facts")) {
		t.Fatalf("pentest markdown report = %s", mdResult.body)
	}
	notModified := doV2HTTP(t, http.MethodGet, pentestBase+"/reports/pentest?format=markdown", operatorToken, "operator", "", "", v2HTTPOptions{ifNoneMatch: mdResult.header.Get("ETag")})
	if notModified.status != http.StatusNotModified {
		t.Fatalf("pentest report If-None-Match = %d %s", notModified.status, notModified.body)
	}

	// CTF Project cannot use Pentest report.
	wrongKind := doV2HTTP(t, http.MethodGet, ctfBase+"/reports/pentest?format=json", operatorToken, "operator", "", "")
	if wrongKind.status != http.StatusUnprocessableEntity && wrongKind.status != http.StatusConflict && wrongKind.status != http.StatusBadRequest {
		// project_kind_mismatch maps through blackboardV2HTTPStatus
		if !bytes.Contains(wrongKind.body, []byte("project_kind_mismatch")) {
			t.Fatalf("CTF pentest report error = %d %s", wrongKind.status, wrongKind.body)
		}
	}

	ctfSeed := `{"schema":"semantic-change-batch/v2","changes":[` +
		`{"op":"create","key":"solution:flag","type":"solution","record":{"status":"candidate","kind":"flag","summary":"Recovered flag","value":"FLAG{accepted}"}},` +
		`{"op":"transition","key":"solution:flag","version":1,"status":"verified","verification_summary":"Accepted by the challenge"}` +
		`]}`
	mustV2HTTP(t, http.MethodPost, ctfBase+"/blackboard/changes", operatorToken, "operator", "ctf-seed", ctfSeed)

	ctfJSON := mustV2HTTP(t, http.MethodGet, ctfBase+"/reports/ctf-solution?format=json", operatorToken, "operator", "", "")
	if !bytes.Contains(ctfJSON, []byte(`"schema":"ctf-solution/v2"`)) ||
		!bytes.Contains(ctfJSON, []byte(`"solved":true`)) ||
		!bytes.Contains(ctfJSON, []byte(`FLAG{accepted}`)) ||
		!bytes.Contains(ctfJSON, []byte(`"key":"solution:flag"`)) {
		t.Fatalf("ctf json = %s", ctfJSON)
	}
	for _, forbidden := range []string{ctfProject.ID, "provenance", "state_hash", "source_hash", "goal"} {
		if bytes.Contains(bytes.ToLower(ctfJSON), bytes.ToLower([]byte(forbidden))) {
			t.Fatalf("ctf json leaked %q: %s", forbidden, ctfJSON)
		}
	}
	ctfMD := mustV2HTTP(t, http.MethodGet, ctfBase+"/reports/ctf-solution?format=markdown", operatorToken, "operator", "", "")
	if !bytes.Contains(ctfMD, []byte("Solved: yes")) || !bytes.Contains(ctfMD, []byte("Verified Flags")) {
		t.Fatalf("ctf markdown = %s", ctfMD)
	}

	// Reverse solved state when no verified flags remain.
	unsolve := `{"schema":"semantic-change-batch/v2","changes":[` +
		`{"op":"create","key":"solution:next","type":"solution","record":{"status":"candidate","kind":"flag","summary":"Replacement","value":"FLAG{next}"}},` +
		`{"op":"supersede","replacement":"solution:next","replacement_version":1,"replaced":"solution:flag","replaced_version":2}` +
		`]}`
	mustV2HTTP(t, http.MethodPost, ctfBase+"/blackboard/changes", operatorToken, "operator", "ctf-unsolve", unsolve)
	unsolved := mustV2HTTP(t, http.MethodGet, ctfBase+"/reports/ctf-solution?format=json", operatorToken, "operator", "", "")
	if !bytes.Contains(unsolved, []byte(`"solved":false`)) {
		t.Fatalf("ctf solved did not reverse: %s", unsolved)
	}
}

// Report/CTF HTTP reads must not attach the trusted-continuation sync sibling
// even when another Task advanced the Project and a Snapshot read would sync.
func TestBlackboardV2HTTPReportAndCTFOmitTrustedContinuationSync(t *testing.T) {
	fixture := newV2HTTPFixture(t)

	// External operator advance creates pending synchronization for peers.
	mustV2HTTP(t, http.MethodPost, fixture.base+"/blackboard/changes", fixture.operator, "operator", "report-sync-external",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:report-sync","type":"entity","record":{"status":"active","kind":"host","name":"Sync","scope_status":"in_scope"}}]}`)

	// Control: peer snapshot still attaches sync when pending.
	snapshot := mustV2HTTP(t, http.MethodGet, fixture.base+"/blackboard/snapshot", fixture.peer.Token, "", "", "")
	if !bytes.Contains(snapshot, []byte(`"sync"`)) || !bytes.Contains(snapshot, []byte(`another_task_changed_shared_project_knowledge`)) {
		t.Fatalf("expected pending sync on snapshot control: %s", snapshot)
	}

	// Report-only responses must never reattach the full Runtime Snapshot sync.
	reportBody := mustV2HTTP(t, http.MethodGet, fixture.base+"/reports/pentest?format=json", fixture.peer.Token, "", "", "")
	if !bytes.Contains(reportBody, []byte(`"schema":"pentest-report/v2"`)) {
		t.Fatalf("pentest report = %s", reportBody)
	}
	if bytes.Contains(reportBody, []byte(`"sync"`)) || bytes.Contains(reportBody, []byte(`runtime-blackboard/v2`)) {
		t.Fatalf("pentest report attached sync sibling: %s", reportBody)
	}
	reportMD := mustV2HTTP(t, http.MethodGet, fixture.base+"/reports/pentest?format=markdown", fixture.peer.Token, "", "", "")
	if bytes.Contains(reportMD, []byte(`"sync"`)) || bytes.Contains(reportMD, []byte(`runtime-blackboard/v2`)) {
		t.Fatalf("pentest markdown attached sync sibling: %s", reportMD)
	}

	// CTF Project with a trusted continuation must also omit sync on solution reads.
	ctfProject, err := fixture.server.projects.CreateWithKind("CTF sync omit", "Challenge", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Project: %v", err)
	}
	ctfTask, err := fixture.server.tasks.Create(task.CreateRequest{
		ProjectID: ctfProject.ID, Goal: "Solve", RuntimeProfileID: fixture.profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create CTF Task: %v", err)
	}
	ctfLaunch, err := fixture.server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: ctfProject.ID, TaskID: ctfTask.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("launch CTF Continuation: %v", err)
	}
	ctfBase := fixture.httpServer.URL + "/api/v2/projects/" + ctfProject.ID
	mustV2HTTP(t, http.MethodPost, ctfBase+"/blackboard/changes", fixture.operator, "operator", "ctf-sync-seed",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"solution:flag","type":"solution","record":{"status":"candidate","kind":"flag","summary":"Flag","value":"FLAG{x}"}},{"op":"transition","key":"solution:flag","version":1,"status":"verified","verification_summary":"ok"}]}`)
	// Second external advance so the CTF continuation has pending sync.
	mustV2HTTP(t, http.MethodPost, ctfBase+"/blackboard/changes", fixture.operator, "operator", "ctf-sync-external",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"fact:extra","type":"fact","record":{"category":"challenge","summary":"Extra","confidence":"tentative","scope_status":"unknown"}}]}`)
	ctfSnap := mustV2HTTP(t, http.MethodGet, ctfBase+"/blackboard/snapshot", ctfLaunch.Token, "", "", "")
	if !bytes.Contains(ctfSnap, []byte(`"sync"`)) {
		t.Fatalf("expected pending sync on CTF snapshot control: %s", ctfSnap)
	}
	ctfSolution := mustV2HTTP(t, http.MethodGet, ctfBase+"/reports/ctf-solution?format=json", ctfLaunch.Token, "", "", "")
	if !bytes.Contains(ctfSolution, []byte(`"schema":"ctf-solution/v2"`)) || !bytes.Contains(ctfSolution, []byte(`"solved":true`)) {
		t.Fatalf("ctf solution = %s", ctfSolution)
	}
	if bytes.Contains(ctfSolution, []byte(`"sync"`)) || bytes.Contains(ctfSolution, []byte(`runtime-blackboard/v2`)) {
		t.Fatalf("ctf solution attached sync sibling: %s", ctfSolution)
	}
}
