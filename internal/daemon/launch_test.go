package daemon_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveLaunchRuntimeProfileRouteIsRemoved(t *testing.T) {
	server := newDaemon(t)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/resolve-launch", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed resolve route status = %d, body=%s", response.Code, response.Body.String())
	}
}
