package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"pentest/internal/daemon"
)

func newAssetHTTPServer(t *testing.T) *daemon.Server {
	t.Helper()
	server, err := daemon.NewServer(daemon.Config{Version: "test", DBPath: ":memory:", ListenAddr: "127.0.0.1:8787"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func assetRequest(t *testing.T, server *daemon.Server, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var decoded map[string]any
	if response.Body.Len() > 0 {
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode response %d %q: %v", response.Code, response.Body.String(), err)
		}
	}
	return response, decoded
}

func createAssetHTTPProject(t *testing.T, server *daemon.Server, name string) string {
	t.Helper()
	response, decoded := assetRequest(t, server, http.MethodPost, "/api/projects", map[string]any{
		"name":  name,
		"kind":  "pentest",
		"scope": map[string]any{"domains": []string{"example.com"}, "ports": []string{"443"}},
	})
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("create project status %d body %v", response.Code, decoded)
	}
	id, _ := decoded["id"].(string)
	if id == "" {
		t.Fatalf("project response missing id: %#v", decoded)
	}
	return id
}

func TestOperatorAssetInventoryAndCoverageAPI(t *testing.T) {
	server := newAssetHTTPServer(t)
	projectID := createAssetHTTPProject(t, server, "Acme")

	response, decoded := assetRequest(t, server, http.MethodPost, "/api/projects/"+projectID+"/assets/upsert", map[string]any{"assets": []map[string]any{
		{"type": "root_domain", "value": "EXAMPLE.com."},
		{"type": "service", "value": "https://example.com:443"},
		{"type": "endpoint", "value": "https://example.com/login"},
	}})
	if response.Code != http.StatusOK {
		t.Fatalf("upsert status %d body %v", response.Code, decoded)
	}
	if decoded["created"] != float64(3) {
		t.Fatalf("created count: %#v", decoded)
	}

	response, decoded = assetRequest(t, server, http.MethodGet, "/api/projects/"+projectID+"/assets", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status %d body %v", response.Code, decoded)
	}
	assets, _ := decoded["assets"].([]any)
	if len(assets) != 3 {
		t.Fatalf("asset count: %#v", decoded)
	}
	first, _ := assets[0].(map[string]any)
	if first["scope_status"] != "in_scope" {
		t.Fatalf("scope status missing: %#v", first)
	}

	response, decoded = assetRequest(t, server, http.MethodGet, "/api/projects/"+projectID+"/assets/coverage", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("coverage status %d body %v", response.Code, decoded)
	}
	if decoded["total_assets"] != float64(3) || decoded["coverage_percent"] != float64(0) {
		t.Fatalf("coverage response: %#v", decoded)
	}

	response, decoded = assetRequest(t, server, http.MethodGet, "/api/projects/"+projectID+"/dashboard", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard status %d body %v", response.Code, decoded)
	}
	counts, _ := decoded["counts"].(map[string]any)
	if counts["assets"] != float64(3) {
		t.Fatalf("dashboard asset count: %#v", counts)
	}
}

func TestOperatorAssetImportAPI(t *testing.T) {
	server := newAssetHTTPServer(t)
	sourceID := createAssetHTTPProject(t, server, "Source")
	targetID := createAssetHTTPProject(t, server, "Target")
	response, decoded := assetRequest(t, server, http.MethodPost, "/api/projects/"+sourceID+"/assets/upsert", map[string]any{"assets": []map[string]any{
		{"type": "root_domain", "value": "example.com"},
	}})
	if response.Code != http.StatusOK {
		t.Fatalf("upsert status %d body %v", response.Code, decoded)
	}

	response, decoded = assetRequest(t, server, http.MethodPost, "/api/projects/"+targetID+"/assets/import", map[string]any{"source_project_id": sourceID})
	if response.Code != http.StatusOK {
		t.Fatalf("import status %d body %v", response.Code, decoded)
	}
	if decoded["created"] != float64(1) {
		t.Fatalf("import result: %#v", decoded)
	}
	assets, _ := decoded["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("import assets: %#v", decoded)
	}
	imported, _ := assets[0].(map[string]any)
	if imported["imported_from_project_id"] != sourceID {
		t.Fatalf("import provenance: %#v", imported)
	}
}

func TestAssetV2RequiresContinuationCapability(t *testing.T) {
	server := newAssetHTTPServer(t)
	projectID := createAssetHTTPProject(t, server, "Acme")
	response, decoded := assetRequest(t, server, http.MethodGet, "/api/v2/projects/"+projectID+"/assets", nil)
	if response.Code == http.StatusOK {
		t.Fatalf("tokenless v2 read succeeded: %#v", decoded)
	}
	if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
		t.Fatalf("tokenless v2 status = %d body %#v", response.Code, decoded)
	}
}
