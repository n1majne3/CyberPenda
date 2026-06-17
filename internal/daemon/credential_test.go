package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"pentest/internal/daemon"
)

// TestPutGlobalCredentialBindingIsIdempotent proves the tracer bullet for the
// credential binding HTTP surface: PUT at the global scope creates a binding,
// and a second PUT for the same credential_ref replaces rather than duplicates.
func TestPutGlobalCredentialBindingIsIdempotent(t *testing.T) {
	server := newDaemon(t)

	putBinding(t, server, "/api/credential-bindings", `{
		"credential_ref": "codex-api-key",
		"source": {"kind": "env", "value": "OLD_KEY"}
	}`)

	// Second PUT for the same ref replaces the source value.
	putBinding(t, server, "/api/credential-bindings", `{
		"credential_ref": "codex-api-key",
		"source": {"kind": "env", "value": "NEW_KEY"}
	}`)

	// Listing global bindings must show exactly one entry, with the new value.
	listReq := httptest.NewRequest(http.MethodGet, "/api/credential-bindings", nil)
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d with body %s", listResp.Code, listResp.Body.String())
	}

	var body struct {
		Bindings []struct {
			CredentialRef string `json:"credential_ref"`
			Source        struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			} `json:"source"`
		} `json:"bindings"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(body.Bindings) != 1 {
		t.Fatalf("expected 1 global binding after idempotent upsert, got %d", len(body.Bindings))
	}
	if body.Bindings[0].CredentialRef != "codex-api-key" {
		t.Fatalf("expected credential_ref codex-api-key, got %q", body.Bindings[0].CredentialRef)
	}
	if body.Bindings[0].Source.Value != "NEW_KEY" {
		t.Fatalf("expected replaced value NEW_KEY, got %q", body.Bindings[0].Source.Value)
	}
}

func putBinding(t *testing.T, server *daemon.Server, path, body string) {
	t.Helper()
	putBindingRaw(t, server, path, body)
}

func putBindingRaw(t *testing.T, server *daemon.Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK && resp.Code != http.StatusCreated {
		t.Fatalf("put %s expected 2xx, got %d with body %s", path, resp.Code, resp.Body.String())
	}
	return resp
}

// TestProjectNestedCredentialBindingOverrideIsScoped proves a project-scoped
// binding lives under the project route, is returned by the project list, and
// does not leak into the global list.
func TestProjectNestedCredentialBindingOverrideIsScoped(t *testing.T) {
	server := newDaemon(t)

	putBinding(t, server, "/api/projects/p1/credential-bindings", `{
		"credential_ref": "codex-api-key",
		"source": {"kind": "file", "value": "/secrets/p1"}
	}`)

	// Project-scoped list shows the override.
	listReq := httptest.NewRequest(http.MethodGet, "/api/projects/p1/credential-bindings", nil)
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected project list status 200, got %d with body %s", listResp.Code, listResp.Body.String())
	}
	var projectBindings struct {
		Bindings []struct {
			CredentialRef string `json:"credential_ref"`
			Scope         string `json:"scope"`
			Source        struct {
				Value string `json:"value"`
			} `json:"source"`
		} `json:"bindings"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&projectBindings); err != nil {
		t.Fatalf("decode project list: %v", err)
	}
	if len(projectBindings.Bindings) != 1 {
		t.Fatalf("expected 1 project binding, got %d", len(projectBindings.Bindings))
	}
	if projectBindings.Bindings[0].Scope != "project" {
		t.Fatalf("expected scope project, got %q", projectBindings.Bindings[0].Scope)
	}
	if projectBindings.Bindings[0].Source.Value != "/secrets/p1" {
		t.Fatalf("expected project source, got %q", projectBindings.Bindings[0].Source.Value)
	}

	// The global list must NOT include the project override.
	globalReq := httptest.NewRequest(http.MethodGet, "/api/credential-bindings", nil)
	globalResp := httptest.NewRecorder()
	server.ServeHTTP(globalResp, globalReq)
	var globalBindings struct {
		Bindings []struct {
			CredentialRef string `json:"credential_ref"`
		} `json:"bindings"`
	}
	if err := json.NewDecoder(globalResp.Body).Decode(&globalBindings); err != nil {
		t.Fatalf("decode global list: %v", err)
	}
	if len(globalBindings.Bindings) != 0 {
		t.Fatalf("expected project override to stay out of global list, got %#v", globalBindings.Bindings)
	}
}

// TestProjectNestedCredentialBindingCanDisable proves a project can disable a
// credential reference without supplying a source.
func TestProjectNestedCredentialBindingCanDisable(t *testing.T) {
	server := newDaemon(t)

	putBinding(t, server, "/api/projects/p1/credential-bindings", `{
		"credential_ref": "codex-api-key",
		"disabled": true
	}`)

	listReq := httptest.NewRequest(http.MethodGet, "/api/projects/p1/credential-bindings", nil)
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)

	var body struct {
		Bindings []struct {
			Disabled bool `json:"disabled"`
		} `json:"bindings"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Bindings) != 1 || !body.Bindings[0].Disabled {
		t.Fatalf("expected one disabled project binding, got %#v", body.Bindings)
	}
}

// TestDeleteCredentialBindingRemovesIt proves a binding is removed by id, and
// deleting a missing id returns 404.
func TestDeleteCredentialBindingRemovesIt(t *testing.T) {
	server := newDaemon(t)

	putResp := putBindingRaw(t, server, "/api/credential-bindings", `{
		"credential_ref": "codex-api-key",
		"source": {"kind": "env", "value": "CODEX_API_KEY"}
	}`)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(putResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode put response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected binding id")
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/credential-bindings/"+created.ID, nil)
	delResp := httptest.NewRecorder()
	server.ServeHTTP(delResp, delReq)
	if delResp.Code != http.StatusNoContent {
		t.Fatalf("expected delete status 204, got %d with body %s", delResp.Code, delResp.Body.String())
	}

	// Listing must no longer include the deleted binding.
	listReq := httptest.NewRequest(http.MethodGet, "/api/credential-bindings", nil)
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	var body struct {
		Bindings []struct {
			ID string `json:"id"`
		} `json:"bindings"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, b := range body.Bindings {
		if b.ID == created.ID {
			t.Fatalf("expected binding %s removed, still present", created.ID)
		}
	}
}

func TestDeleteCredentialBindingMissingReturnsNotFound(t *testing.T) {
	server := newDaemon(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/credential-bindings/missing", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected delete missing to 404, got %d with body %s", resp.Code, resp.Body.String())
	}
}
