package projectinterface

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRetryableHTTPErrorCarriesRetryAfter(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeProjectInterfaceError(recorder, &Error{
		ProtocolVersion: RuntimeProtocolVersion,
		Code:            ErrCodeStorageBusy,
		Message:         "SQLite writer is busy",
		Path:            "storage",
		Retryable:       true,
	})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got == "" {
		t.Fatal("retryable 503 omitted Retry-After")
	}
}
