package projectinterface_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/projectinterface"
)

// newHTTPMux wires the project-interface HTTP adapter onto a ServeMux at the
// canonical routes (runtime protocol §12.1).
func newHTTPMux(fixture serviceFixture) http.Handler {
	handler := projectinterface.NewHTTPHandler(fixture.service)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{id}/blackboard/mutations", handler.Apply)
	mux.HandleFunc("POST /api/projects/{id}/blackboard/records:resolve", handler.ResolveRecords)
	mux.HandleFunc("GET /api/projects/{id}/blackboard/runtime-graph", handler.CurrentGraph)
	return mux
}

func mustApplyOverHTTP(t *testing.T, mux http.Handler, token, projectID string, req projectinterface.ApplyMutationRequest) projectinterface.ApplyMutationResponse {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/blackboard/mutations", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("apply over HTTP: status %d body %s", recorder.Code, recorder.Body.String())
	}
	var response projectinterface.ApplyMutationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode apply response: %v", err)
	}
	return response
}

// TestProjectInterfaceHTTPAppliesMutationsAndMapsErrors drives one end-to-end
// canonical path through HTTP: a Runtime Apply lands a record that is then
// visible through records:resolve and runtime-graph, grant auth is enforced,
// and structured errors carry the ProjectInterfaceErrorV1 code.
func TestProjectInterfaceHTTPAppliesMutationsAndMapsErrors(t *testing.T) {
	fixture := newServiceFixture(t)
	mux := newHTTPMux(fixture)
	ctx := context.Background()
	_ = ctx

	apply := mustApplyOverHTTP(t, mux, fixture.token, fixture.project.ID, objectiveApplyRequest())
	if apply.ProjectID != fixture.project.ID || apply.ObservedGraphRevision != 1 {
		t.Fatalf("http apply response = %+v", apply)
	}
	if cacheControl := applyResponseHeader(t, mux, fixture, http.MethodPost, "/mutations"); cacheControl != "no-store" {
		t.Fatalf("mutation Cache-Control = %q want no-store", cacheControl)
	}

	// Missing grant token is rejected with 401 grant_not_found before any work.
	noToken := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.project.ID+"/blackboard/mutations", bytes.NewReader(mustJSON(t, objectiveApplyRequest())))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, noToken)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d want 401", recorder.Code)
	}
	if code := decodeErrorCode(t, recorder); code != projectinterface.ErrCodeGrantNotFound {
		t.Fatalf("missing token error code = %q want %q", code, projectinterface.ErrCodeGrantNotFound)
	}

	// A path Project that disagrees with the grant is 403 project_mismatch.
	mismatched := httptest.NewRequest(http.MethodPost, "/api/projects/another-project/blackboard/mutations", bytes.NewReader(mustJSON(t, objectiveApplyRequest())))
	mismatched.Header.Set("Authorization", "Bearer "+fixture.token)
	mismatchedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(mismatchedRecorder, mismatched)
	if mismatchedRecorder.Code != http.StatusForbidden {
		t.Fatalf("path mismatch status = %d want 403 body %s", mismatchedRecorder.Code, mismatchedRecorder.Body.String())
	}
	if code := decodeErrorCode(t, mismatchedRecorder); code != projectinterface.ErrCodeProjectMismatch {
		t.Fatalf("path mismatch error code = %q want %q", code, projectinterface.ErrCodeProjectMismatch)
	}

	// An unknown JSON field is rejected as invalid_request (closed envelope).
	spoofBody := bytes.NewReader([]byte(`{"protocol_version":1,"batch":{"schema_version":1,"idempotency_key":"x","operations":[]},"project_id":"smuggled"}`))
	spoofed := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.project.ID+"/blackboard/mutations", spoofBody)
	spoofed.Header.Set("Authorization", "Bearer "+fixture.token)
	spoofedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(spoofedRecorder, spoofed)
	if spoofedRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d want 400 body %s", spoofedRecorder.Code, spoofedRecorder.Body.String())
	}

	// records:resolve returns the created objective over HTTP.
	resolveBody, _ := json.Marshal(projectinterface.ResolveRecordsRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Nodes: []projectinterface.NodeLookup{{
			NodeType:  "exploration_objective",
			StableKey: "objective:find-admin-surface",
		}},
	})
	resolveRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.project.ID+"/blackboard/records:resolve", bytes.NewReader(resolveBody))
	resolveRequest.Header.Set("Authorization", "Bearer "+fixture.token)
	resolveRecorder := httptest.NewRecorder()
	mux.ServeHTTP(resolveRecorder, resolveRequest)
	if resolveRecorder.Code != http.StatusOK {
		t.Fatalf("resolve status = %d body %s", resolveRecorder.Code, resolveRecorder.Body.String())
	}
	var resolved projectinterface.ResolveRecordsResponse
	if err := json.Unmarshal(resolveRecorder.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if len(resolved.Nodes) != 1 {
		t.Fatalf("resolve nodes = %d want 1", len(resolved.Nodes))
	}

	// runtime-graph returns the projection with ETag + Cache-Control, and a
	// matching If-None-Match yields 304.
	graphRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+fixture.project.ID+"/blackboard/runtime-graph", nil)
	graphRequest.Header.Set("Authorization", "Bearer "+fixture.token)
	graphRecorder := httptest.NewRecorder()
	mux.ServeHTTP(graphRecorder, graphRequest)
	if graphRecorder.Code != http.StatusOK {
		t.Fatalf("runtime graph status = %d body %s", graphRecorder.Code, graphRecorder.Body.String())
	}
	if cc := graphRecorder.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("runtime graph Cache-Control = %q", cc)
	}
	etag := graphRecorder.Header().Get("ETag")
	if etag == "" {
		t.Fatal("runtime graph ETag missing")
	}
	conditional := httptest.NewRequest(http.MethodGet, "/api/projects/"+fixture.project.ID+"/blackboard/runtime-graph", nil)
	conditional.Header.Set("Authorization", "Bearer "+fixture.token)
	conditional.Header.Set("If-None-Match", etag)
	conditionalRecorder := httptest.NewRecorder()
	mux.ServeHTTP(conditionalRecorder, conditional)
	if conditionalRecorder.Code != http.StatusNotModified || conditionalRecorder.Body.Len() != 0 {
		t.Fatalf("conditional runtime graph = %d body %q want 304 empty", conditionalRecorder.Code, conditionalRecorder.Body.String())
	}

	// The plaintext token never appears in any HTTP response body.
	if bytes.Contains(graphRecorder.Body.Bytes(), []byte(fixture.token)) ||
		bytes.Contains(resolveRecorder.Body.Bytes(), []byte(fixture.token)) {
		t.Fatal("plaintext grant token appeared in an HTTP response body")
	}
}

func TestProjectInterfaceHTTPContinuationClosedMapsToConflict(t *testing.T) {
	fixture := newServiceFixture(t)
	mux := newHTTPMux(fixture)
	mustApplyOverHTTP(t, mux, fixture.token, fixture.project.ID, objectiveApplyRequest())

	if _, err := fixture.grants.Finish(context.Background(), fixture.grant.ID); err != nil {
		t.Fatalf("finish grant: %v", err)
	}

	// A new write after finish is 409 continuation_closed.
	newWrite := objectiveApplyRequest()
	newWrite.Batch.IdempotencyKey = "obj:after-finish"
	newWrite.Batch.Operations[0].Node.StableKey = "objective:after-finish"
	body, _ := json.Marshal(newWrite)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.project.ID+"/blackboard/mutations", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+fixture.token)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("closed continuation status = %d want 409 body %s", recorder.Code, recorder.Body.String())
	}
	if code := decodeErrorCode(t, recorder); code != projectinterface.ErrCodeContinuationClosed {
		t.Fatalf("closed continuation code = %q want %q", code, projectinterface.ErrCodeContinuationClosed)
	}

	// Exact replay over HTTP still succeeds after finish.
	replay := mustApplyOverHTTP(t, mux, fixture.token, fixture.project.ID, objectiveApplyRequest())
	if replay.ObservedGraphRevision != 1 {
		t.Fatalf("http replay revision = %d want 1", replay.ObservedGraphRevision)
	}

	// Reads remain available over HTTP after finish.
	resolveBody, _ := json.Marshal(projectinterface.ResolveRecordsRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Nodes: []projectinterface.NodeLookup{{
			NodeType:  string(blackboard.NodeTypeExplorationObjective),
			StableKey: "objective:find-admin-surface",
		}},
	})
	resolve := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.project.ID+"/blackboard/records:resolve", bytes.NewReader(resolveBody))
	resolve.Header.Set("Authorization", "Bearer "+fixture.token)
	resolveRecorder := httptest.NewRecorder()
	mux.ServeHTTP(resolveRecorder, resolve)
	if resolveRecorder.Code != http.StatusOK {
		t.Fatalf("resolve after finish = %d want 200", resolveRecorder.Code)
	}
}

func TestProjectInterfaceHTTPRevokedGrantMapsToForbidden(t *testing.T) {
	fixture := newServiceFixture(t)
	mux := newHTTPMux(fixture)
	mustApplyOverHTTP(t, mux, fixture.token, fixture.project.ID, objectiveApplyRequest())

	if _, err := fixture.grants.Revoke(context.Background(), fixture.grant.ID); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}

	// Revocation rejects every use: a new write is 403 (not 409), distinct from
	// finish/terminal which stay 409 (runtime protocol §4.2, §12.4).
	newWrite := objectiveApplyRequest()
	newWrite.Batch.IdempotencyKey = "obj:after-revoke"
	newWrite.Batch.Operations[0].Node.StableKey = "objective:after-revoke"
	body, _ := json.Marshal(newWrite)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.project.ID+"/blackboard/mutations", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+fixture.token)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("revoked write status = %d want 403 body %s", recorder.Code, recorder.Body.String())
	}
	if code := decodeErrorCode(t, recorder); code != projectinterface.ErrCodeContinuationClosed {
		t.Fatalf("revoked write code = %q want %q", code, projectinterface.ErrCodeContinuationClosed)
	}

	// Even exact replay is rejected after revocation.
	replay, _ := json.Marshal(objectiveApplyRequest())
	replayReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.project.ID+"/blackboard/mutations", bytes.NewReader(replay))
	replayReq.Header.Set("Authorization", "Bearer "+fixture.token)
	replayRec := httptest.NewRecorder()
	mux.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusForbidden {
		t.Fatalf("revoked replay status = %d want 403", replayRec.Code)
	}

	// The error envelope carries protocol_version and a request_id (runtime
	// protocol §3).
	var envelope struct {
		Error struct {
			ProtocolVersion int    `json:"protocol_version"`
			Code            string `json:"code"`
			RequestID       string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Error.ProtocolVersion != projectinterface.RuntimeProtocolVersion {
		t.Fatalf("error protocol_version = %d want %d", envelope.Error.ProtocolVersion, projectinterface.RuntimeProtocolVersion)
	}
	if envelope.Error.RequestID == "" {
		t.Fatal("error envelope missing request_id")
	}
}


// applyResponseHeader issues an apply and returns one response header value,
// proving mutation responses carry Cache-Control: no-store.
func applyResponseHeader(t *testing.T, mux http.Handler, fixture serviceFixture, method, suffix string) string {
	t.Helper()
	body, _ := json.Marshal(objectiveApplyRequest())
	req := httptest.NewRequest(method, "/api/projects/"+fixture.project.ID+"/blackboard"+suffix, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	return recorder.Header().Get("Cache-Control")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func decodeErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope from %q: %v", recorder.Body.String(), err)
	}
	return envelope.Error.Code
}
