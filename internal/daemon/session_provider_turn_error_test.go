package daemon

import (
	"testing"

	"pentest/internal/session"
	"pentest/internal/task"
)

func TestSessionProviderEventPayloadPreservesErrorAndErrorCode(t *testing.T) {
	kind, filtered := sessionProviderEventPayload(task.EventKindLifecycle, task.EventPayload{
		"phase":      "provider_turn_failed",
		"request_id": "req-1",
		"outcome":    "failed",
		"error_code": "provider_rejected",
		"error":      "400 request (13032 tokens) exceeds the available context size (8192 tokens)",
	}, "cont-1")

	if kind != session.EventKindLifecycle {
		t.Fatalf("expected lifecycle event kind, got %q", kind)
	}
	if filtered["error_code"] != "provider_rejected" {
		t.Fatalf("expected error_code to be preserved, got %#v", filtered)
	}
	if filtered["error"] != "400 request (13032 tokens) exceeds the available context size (8192 tokens)" {
		t.Fatalf("expected error message to be preserved, got %#v", filtered)
	}
}
