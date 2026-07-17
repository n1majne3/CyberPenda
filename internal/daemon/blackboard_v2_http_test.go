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
