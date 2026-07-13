package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardcompat"
	"pentest/internal/daemon"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/store"
)

func TestLegacyHTTPWriteUsesGraphCompatibilityAndDeprecationHeaders(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "http-compatibility.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	projectRow, err := project.NewService(db).Create("HTTP compatibility", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("activate graph epoch: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	server, err := daemon.NewServer(daemon.Config{Version: "test", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start graph server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := []byte(`{"summary":"Admin panel exposed","category":"service","body":"Observed directly","confidence":"tentative","scope_status":"in_scope"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectRow.ID+"/facts/fact:admin", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", "http:fact:admin")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Fact PUT status = %d body=%s", response.Code, response.Body.String())
	}
	for header, want := range map[string]string{
		"Deprecation":              "true",
		"CyberPenda-Compatibility": "legacy_blackboard_v1",
	} {
		if got := response.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	if response.Header().Get("Link") == "" {
		t.Fatal("missing deprecation Link header")
	}
	var fact blackboard.LegacyFactDetailV1
	if err := json.Unmarshal(response.Body.Bytes(), &fact); err != nil {
		t.Fatalf("decode legacy Fact: %v", err)
	}
	if fact.FactKey != "fact:admin" || fact.Version != 1 || fact.Summary != "Admin panel exposed" {
		t.Fatalf("legacy Fact payload = %+v", fact)
	}

	readRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectRow.ID+"/facts/fact:admin", nil)
	readResponse := httptest.NewRecorder()
	server.ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("Fact GET status = %d body=%s", readResponse.Code, readResponse.Body.String())
	}
	if readResponse.Header().Get("Deprecation") != "true" || readResponse.Header().Get("CyberPenda-Compatibility") != "legacy_blackboard_v1" || readResponse.Header().Get("Link") == "" {
		t.Fatalf("Fact GET compatibility headers = %#v", readResponse.Header())
	}

	replayRequest := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectRow.ID+"/facts/fact:admin", bytes.NewReader(body))
	replayRequest.Header.Set("Idempotency-Key", "http:fact:admin")
	replay := httptest.NewRecorder()
	server.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusOK || replay.Body.String() != response.Body.String() {
		t.Fatalf("replay = status %d body=%s, want status 200 body=%s", replay.Code, replay.Body.String(), response.Body.String())
	}

	verifyDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer verifyDB.Close()
	legacyFacts, err := blackboard.NewService(verifyDB).FactIndex(projectRow.ID, blackboard.FactIndexOptions{IncludeDeprecated: true})
	if err != nil {
		t.Fatalf("read legacy Facts: %v", err)
	}
	if len(legacyFacts) != 0 {
		t.Fatalf("legacy table path was mutated: %+v", legacyFacts)
	}
	var readCount int
	if err := verifyDB.QueryRow(`SELECT use_count FROM blackboard_compatibility_use WHERE project_id=? AND transport='http' AND call_kind='read_fact' AND use_mode='read'`, projectRow.ID).Scan(&readCount); err != nil {
		t.Fatalf("read compatibility counter: %v", err)
	}
	if readCount != 1 {
		t.Fatalf("Fact compatibility read count = %d, want 1", readCount)
	}
	graphFact, err := blackboard.NewGraphService(verifyDB, blackboard.SystemClock{}, blackboard.RandomIDSource{}).ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: projectRow.ID, NodeType: blackboard.NodeTypeProjectFact, Key: "fact:admin",
	})
	if err != nil || graphFact.Node.Version != 1 {
		t.Fatalf("graph Fact = %+v err=%v", graphFact, err)
	}
}

func TestLegacyHTTPRelationReturnsStructured422ForNonRepresentableSemantics(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "http-relation.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	projectRow, err := project.NewService(db).Create("Relation compatibility", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	for _, key := range []string{"fact:a", "fact:b"} {
		if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
			SchemaVersion: 1, IdempotencyKey: "seed:" + key,
			Context: blackboard.SystemExecutionContext(projectRow.ID, projectRow.Kind, "fixture"),
			Operations: []blackboard.Operation{{
				OpID: "fact", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: key},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": key, "confidence": "tentative", "scope_status": "in_scope"}},
			}},
		}); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("activate graph epoch: %v", err)
	}
	_ = db.Close()
	server, err := daemon.NewServer(daemon.Config{Version: "test", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start graph server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	request := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectRow.ID+"/facts/fact:a/relations", bytes.NewBufferString(`{"target_fact_key":"fact:b","relation":"depends_on"}`))
	request.Header.Set("Idempotency-Key", "relation:depends-on")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("relation status = %d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Deprecation") != "true" {
		t.Fatal("structured compatibility error omitted deprecation headers")
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Code != blackboardcompat.ErrCodeLegacyRelationNotGraphRepresentable {
		t.Fatalf("error code = %q", envelope.Error.Code)
	}
}

func TestLegacyHTTPCompatibilityPreflightErrorsStayStructuredAndDeprecated(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "http-preflight.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	projectRow, err := project.NewService(db).Create("HTTP preflight", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("activate graph epoch: %v", err)
	}
	_ = db.Close()
	server, err := daemon.NewServer(daemon.Config{Version: "test", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer server.Close()
	for _, test := range []struct {
		name, path, body, code string
		status                 int
	}{
		{"malformed JSON", "/api/projects/" + projectRow.ID + "/facts/fact:bad", `{`, projectinterface.ErrCodeInvalidRequest, http.StatusBadRequest},
		{"missing Project", "/api/projects/missing/facts/fact:bad", `{"summary":"missing"}`, projectinterface.ErrCodeProjectNotFound, http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodPut, test.path, bytes.NewBufferString(test.body)))
			if response.Code != test.status || response.Header().Get("Deprecation") != "true" {
				t.Fatalf("status=%d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
			}
			var envelope struct {
				Error projectinterface.Error `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != test.code {
				t.Fatalf("error envelope=%+v decode=%v body=%s", envelope, err, response.Body.String())
			}
		})
	}
}
