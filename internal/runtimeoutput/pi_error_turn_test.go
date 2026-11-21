package runtimeoutput

import (
	"strings"
	"testing"
	"time"
)

func TestParseLineEmitsErrorTurnForPiMessageError(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	for _, line := range []string{
		`{"type":"message","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"400 request (13032 tokens) exceeds the available context size (8192 tokens), try increasing it"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"400 request (13032 tokens) exceeds the available context size (8192 tokens), try increasing it"}}`,
	} {
		turns, fallback := ParseLine(line, ts, ParseOptions{IncludeErrors: true})
		if fallback {
			t.Fatalf("expected structured parse for %s", line)
		}
		if len(turns) != 1 {
			t.Fatalf("expected 1 error turn, got %#v", turns)
		}
		if turns[0].Kind != KindError {
			t.Fatalf("expected KindError, got %q", turns[0].Kind)
		}
		if !strings.Contains(turns[0].Text, "exceeds the available context size") {
			t.Fatalf("expected provider error text, got %q", turns[0].Text)
		}

		_, fallbackWithoutErrors := ParseLine(line, ts, ParseOptions{IncludeErrors: false})
		if !fallbackWithoutErrors {
			t.Fatalf("expected fallback when IncludeErrors is false for %s", line)
		}
	}
}
