package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/daemon"
)

// TestRetiredV1ProjectInterfaceRouteIsInactiveOnFreshV2 keeps the no-dual-path
// assertion while the v2 HTTP adapter is delivered in its later ticket.
func TestRetiredV1ProjectInterfaceRouteIsInactiveOnFreshV2(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pi-v2.db")
	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/projects/any/blackboard/mutations", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		body := rec.Body.String()
		var probe map[string]any
		if json.Unmarshal(rec.Body.Bytes(), &probe) == nil {
			if kind, _ := probe["request_kind"].(string); kind == "apply" {
				t.Fatalf("retired v1 Project Interface unexpectedly active in blackboard_v2: %s", body)
			}
		}
	}
}
