package transcript

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSurfacesPiMessageErrorAndProviderTurnFailed(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	entries := Build(Subject{ID: "sess-1", CreatedAt: ts}, []Event{
		{
			ID:   "ev-1",
			Seq:  1,
			Kind: "runtime_output",
			Payload: map[string]any{
				"stream": PiSessionStream,
				"text":   `{"type":"message","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"400 request (13032 tokens) exceeds the available context size (8192 tokens)"}}`,
			},
			CreatedAt: ts,
		},
		{
			ID:   "ev-2",
			Seq:  2,
			Kind: "lifecycle",
			Payload: map[string]any{
				"phase":   "provider_turn_failed",
				"outcome": "failed",
				"error":   "provider rejected the turn",
			},
			CreatedAt: ts.Add(time.Second),
		},
	})

	if len(entries) != 2 {
		t.Fatalf("expected 2 error transcript entries, got %#v", entries)
	}
	if entries[0].Kind != KindError || !strings.Contains(entries[0].Text, "exceeds the available context size") {
		t.Fatalf("unexpected first error entry: %#v", entries[0])
	}
	if entries[1].Kind != KindError || entries[1].Text != "provider rejected the turn" {
		t.Fatalf("unexpected second error entry: %#v", entries[1])
	}
}
