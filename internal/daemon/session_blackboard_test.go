package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/session"
)

func TestSessionReadOnlyGrantCanReadAndCannotWrite(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "session-read-only.db"),
		RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"),
		AuthToken: "operator-secret", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	found, err := server.sessions.Create(session.CreateRequest{Input: "Read the Session graph", BlackboardMode: session.BlackboardModeWorkingGraph})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.sessions.CreateContinuation(found.ID, "profile-1", "claude_code", session.RunnerSandbox, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.blackboardV2.BindSessionContinuation(context.Background(), found.ID, continuation.ID); err != nil {
		t.Fatal(err)
	}
	token, _, err := server.projectInterfaceGrants.IssueSession(context.Background(), projectinterface.IssueSessionGrantRequest{
		SessionID: found.ID, ContinuationID: continuation.ID,
		RuntimeConfigVersionID: continuation.RuntimeConfigID, RuntimeProfileID: continuation.RuntimeProfileID,
		RuntimePluginID: continuation.RuntimeProvider, Runner: string(continuation.Runner), Access: projectinterface.GrantAccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v2/sessions/" + found.ID + "/blackboard"
	seed := sessionBlackboardRequest(t, server, http.MethodPost, base+"/changes", "operator-secret", "seed-read-only",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"fact:seed","type":"fact","record":{"category":"note","summary":"Readable","confidence":"tentative"}}]}`)
	if seed.status != http.StatusOK {
		t.Fatalf("seed status = %d body=%s", seed.status, seed.body)
	}
	read := sessionBlackboardRequest(t, server, http.MethodGet, base+"/records/fact:seed", token, "", "")
	if read.status != http.StatusOK || !bytes.Contains(read.body, []byte("Readable")) {
		t.Fatalf("read-only Session read = %d %s", read.status, read.body)
	}
	write := sessionBlackboardRequest(t, server, http.MethodPost, base+"/changes", token, "read-only-write",
		`{"schema":"semantic-change-batch/v2","changes":[]}`)
	if write.status != http.StatusForbidden || !bytes.Contains(write.body, []byte(`"code":"authority_denied"`)) {
		t.Fatalf("read-only Session write = %d %s", write.status, write.body)
	}
}

func TestSessionBlackboardHTTPUsesOwnerLocalSharedV2Semantics(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "session-blackboard.db"),
		RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"),
		AuthToken: "operator-secret", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	first, err := server.sessions.Create(session.CreateRequest{Input: "First session"})
	if err != nil {
		t.Fatalf("create first Session: %v", err)
	}
	second, err := server.sessions.Create(session.CreateRequest{Input: "Second session"})
	if err != nil {
		t.Fatalf("create second Session: %v", err)
	}
	projectOwner, err := server.projects.Create("Project owner", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	base := "/api/v2/sessions/" + first.ID + "/blackboard"
	invalid := sessionBlackboardRequest(t, server, http.MethodPost, base+"/changes", "operator-secret", "session-invalid-schema",
		`{"schema":"semantic-change-batch/v2","changes":[],"actor_id":"runtime"}`)
	var invalidEnvelope struct {
		Error *blackboardv2.Error `json:"error"`
	}
	if err := json.Unmarshal(invalid.body, &invalidEnvelope); err != nil || invalidEnvelope.Error == nil {
		t.Fatalf("decode Session Schema error: %v body=%s", err, invalid.body)
	}
	if invalid.status != http.StatusBadRequest || invalidEnvelope.Error.Path != "actor_id" || invalidEnvelope.Error.Details["reason"] != "additional_field" {
		t.Fatalf("Session Schema error = %d %#v", invalid.status, invalidEnvelope.Error)
	}
	invalidCheckpoint := sessionBlackboardRequest(t, server, http.MethodPost, base+"/attempts/attempt:invalid:checkpoint", "operator-secret", "session-invalid-checkpoint",
		`{"version":"one","summary":"Checkpoint"}`)
	var checkpointEnvelope struct {
		Error *blackboardv2.Error `json:"error"`
	}
	if err := json.Unmarshal(invalidCheckpoint.body, &checkpointEnvelope); err != nil || checkpointEnvelope.Error == nil {
		t.Fatalf("decode Session checkpoint Schema error: %v body=%s", err, invalidCheckpoint.body)
	}
	if invalidCheckpoint.status != http.StatusBadRequest || checkpointEnvelope.Error.Path != "version" || checkpointEnvelope.Error.Details["reason"] != "invalid_type" {
		t.Fatalf("Session checkpoint Schema error = %d %#v", invalidCheckpoint.status, checkpointEnvelope.Error)
	}
	seed := `{"schema":"semantic-change-batch/v2","changes":[` +
		`{"op":"create","key":"entity:note","type":"entity","record":{"status":"active","kind":"host","name":"session.example"}},` +
		`{"op":"create","key":"objective:remember","type":"objective","record":{"status":"open","objective":"Remember session state"}},` +
		`{"op":"create","key":"attempt:remember","type":"attempt","record":{"status":"open","summary":"Capture session state"}},` +
		`{"op":"create","key":"fact:note","type":"fact","record":{"category":"note","summary":"This belongs only to the Session","confidence":"tentative"}},` +
		`{"op":"relate","from":"attempt:remember","relation":"tests","to":"objective:remember"},` +
		`{"op":"relate","from":"attempt:remember","relation":"produced","to":"fact:note"}` +
		`]}`
	seedResult := sessionBlackboardRequest(t, server, http.MethodPost, base+"/changes", "operator-secret", "session-seed", seed)
	if seedResult.status != http.StatusOK || !bytes.Contains(seedResult.body, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("Session seed = %d %s", seedResult.status, seedResult.body)
	}

	snapshot := sessionBlackboardRequest(t, server, http.MethodGet, base+"/snapshot", "operator-secret", "", "")
	if snapshot.status != http.StatusOK || !bytes.Contains(snapshot.body, []byte(`"schema":"runtime-blackboard/v2"`)) {
		t.Fatalf("Session snapshot = %d %s", snapshot.status, snapshot.body)
	}
	for _, forbidden := range []string{"project_id", "scope_status", "finding", "solution", "evidence"} {
		if bytes.Contains(snapshot.body, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("Session snapshot leaked Project-only field %q: %s", forbidden, snapshot.body)
		}
	}

	// The same readable Blackboard Key is valid in another Session and Project.
	secondResult := sessionBlackboardRequest(t, server, http.MethodPost,
		"/api/v2/sessions/"+second.ID+"/blackboard/changes", "operator-secret", "second-seed",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:note","type":"entity","record":{"status":"active","kind":"host","name":"other.example"}}]}`)
	if secondResult.status != http.StatusOK {
		t.Fatalf("same key in second Session = %d %s", secondResult.status, secondResult.body)
	}
	if _, err := server.blackboardV2.Apply(context.Background(), projectOwner.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "project-seed",
		Changes: []blackboardv2.Change{{Op: "create", Key: "entity:note", Type: "entity", Record: blackboardv2.EntityRecord{
			Status: "active", Kind: "host", Name: "project.example", ScopeStatus: "unknown",
		}}},
	}); err != nil {
		t.Fatalf("same key in Project: %v", err)
	}

	foreignRead := sessionBlackboardRequest(t, server, http.MethodGet,
		"/api/v2/sessions/"+second.ID+"/blackboard/records/entity:note", "operator-secret", "", "")
	if foreignRead.status != http.StatusOK || !bytes.Contains(foreignRead.body, []byte("other.example")) {
		t.Fatalf("second Session current read = %d %s", foreignRead.status, foreignRead.body)
	}
	missingInSecond := sessionBlackboardRequest(t, server, http.MethodGet,
		"/api/v2/sessions/"+second.ID+"/blackboard/records/fact:note", "operator-secret", "", "")
	if missingInSecond.status != http.StatusNotFound || bytes.Contains(missingInSecond.body, []byte(first.ID)) {
		t.Fatalf("cross-Session read leaked owner = %d %s", missingInSecond.status, missingInSecond.body)
	}

	rejected := sessionBlackboardRequest(t, server, http.MethodPost, base+"/changes", "operator-secret", "session-finding",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"finding:nope","type":"finding","record":{"status":"unconfirmed","title":"Project finding"}}]}`)
	if rejected.status != http.StatusUnprocessableEntity || !bytes.Contains(rejected.body, []byte(`"code":"owner_capability_denied"`)) {
		t.Fatalf("Project-only Session write = %d %s", rejected.status, rejected.body)
	}

	updated := sessionBlackboardRequest(t, server, http.MethodPost, base+"/changes", "operator-secret", "session-update",
		`{"schema":"semantic-change-batch/v2","changes":[{"op":"update","key":"fact:note","version":1,"type":"fact","record":{"summary":"Updated Session fact"}}]}`)
	if updated.status != http.StatusOK {
		t.Fatalf("Session update = %d %s", updated.status, updated.body)
	}
	history := sessionBlackboardRequest(t, server, http.MethodGet, base+"/records/fact:note/history", "operator-secret", "", "")
	if history.status != http.StatusOK || !bytes.Contains(history.body, []byte(`"schema":"semantic-history/v2"`)) || !bytes.Contains(history.body, []byte("This belongs only to the Session")) {
		t.Fatalf("Session history = %d %s", history.status, history.body)
	}

	if _, err := server.sessions.Archive(first.ID); err != nil {
		t.Fatalf("archive Session: %v", err)
	}
	if err := server.sessions.Delete(first.ID); err != nil {
		t.Fatalf("delete archived Session: %v", err)
	}
	deletedSnapshot := sessionBlackboardRequest(t, server, http.MethodGet, base+"/snapshot", "operator-secret", "", "")
	if deletedSnapshot.status != http.StatusNotFound {
		t.Fatalf("deleted Session Blackboard = %d %s", deletedSnapshot.status, deletedSnapshot.body)
	}
}

type sessionBlackboardHTTPResult struct {
	status int
	body   []byte
}

func sessionBlackboardRequest(t *testing.T, server *Server, method, path, token, idempotencyKey, body string) sessionBlackboardHTTPResult {
	t.Helper()
	reader := strings.NewReader(body)
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer "+token)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return sessionBlackboardHTTPResult{status: response.Code, body: response.Body.Bytes()}
}
