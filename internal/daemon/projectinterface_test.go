package daemon_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/daemon"
)

func TestRetiredV1ProjectInterfaceRouteReturnsStableGoneOnFreshV2(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "v", DBPath: filepath.Join(t.TempDir(), "pi-v2.db"), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer server.Close()

	request := httptest.NewRequest(http.MethodPost, "/api/projects/any/blackboard/mutations", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	assertRetiredBlackboardV1Response(t, response)
}
