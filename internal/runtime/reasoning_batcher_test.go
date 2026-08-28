package runtime

import (
	"strings"
	"testing"
	"time"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

func TestReasoningDeltaBatcherAccumulatesWithinWindow(t *testing.T) {
	var fired []func()
	batcher := newReasoningDeltaBatcher(300 * time.Millisecond)
	batcher.schedule = func(delay time.Duration, fn func()) {
		if delay != 300*time.Millisecond {
			t.Fatalf("schedule delay = %v", delay)
		}
		fired = append(fired, fn)
	}

	var flushed []string
	batcher.Add("seg-1", func(text string) { flushed = append(flushed, text) }, "alpha ")
	batcher.Add("seg-1", func(text string) { flushed = append(flushed, text) }, "beta")
	if len(flushed) != 0 {
		t.Fatalf("deltas must not flush inside the window: %#v", flushed)
	}
	if len(fired) != 1 {
		t.Fatalf("expected one scheduled flush, got %d", len(fired))
	}

	// The window callback flushes the cumulative text once.
	fired[0]()
	if len(flushed) != 1 || flushed[0] != "alpha beta" {
		t.Fatalf("window flush = %#v", flushed)
	}
	fired[0]() // A stale timer must not re-emit.
	if len(flushed) != 1 {
		t.Fatalf("stale timer re-flushed: %#v", flushed)
	}

	// Deltas arriving after an earlier flush stay cumulative for the same
	// segment: later flushes carry the whole text, not just the new tail.
	batcher.Add("seg-1", func(text string) { flushed = append(flushed, text) }, "gamma")
	if len(fired) != 2 {
		t.Fatalf("expected a second scheduled flush, got %d", len(fired))
	}
	fired[1]()
	if len(flushed) != 2 || flushed[1] != "alpha betagamma" {
		t.Fatalf("post-flush accumulation = %#v", flushed)
	}
}

func TestReasoningDeltaBatcherIgnoresStaleTimerFromCompletedSegment(t *testing.T) {
	var fired []func()
	batcher := newReasoningDeltaBatcher(300 * time.Millisecond)
	batcher.schedule = func(_ time.Duration, fn func()) { fired = append(fired, fn) }

	var flushed []string
	batcher.Add("seg-a", func(text string) { flushed = append(flushed, "a:"+text) }, "alpha")
	batcher.Barrier()
	if len(flushed) != 1 || flushed[0] != "a:alpha" {
		t.Fatalf("barrier flush = %#v", flushed)
	}
	batcher.Add("seg-b", func(text string) { flushed = append(flushed, "b:"+text) }, "beta")
	if len(fired) != 2 {
		t.Fatalf("scheduled callbacks = %d", len(fired))
	}

	// The old Segment A callback must not flush Segment B before its own window.
	fired[0]()
	if len(flushed) != 1 {
		t.Fatalf("stale timer flushed the new segment: %#v", flushed)
	}
	fired[1]()
	if len(flushed) != 2 || flushed[1] != "b:beta" {
		t.Fatalf("current timer flush = %#v", flushed)
	}
}

func TestReasoningEmitterShapeRedactsRawContent(t *testing.T) {
	adapter := newProviderSessionAdapter("claude_code", &fakeProviderTransport{}, "session-1", "turn-1", runtimeplugin.Capabilities{}, providerWireMethods{})
	var payload task.EventPayload
	adapter.emitReasoningRuntimeOutput(func(_ task.EventKind, got task.EventPayload) { payload = got }, reasoningRuntimeOutput{
		ProviderEvent: "claude/runtime_output",
		SessionID:     "session-1",
		TurnID:        "turn-1",
		ItemID:        "reasoning-1",
		Stream:        "stream_event",
		Phase:         "streaming",
		Text:          `{"delta":{"thinking":"bearer secret-runtime-token-123456"}}`,
	})
	text, _ := payload["text"].(string)
	if strings.Contains(text, "secret-runtime-token-123456") || !strings.Contains(text, "bearer [REDACTED]") {
		t.Fatalf("reasoning emitter did not redact content: %#v", payload)
	}
}

func TestReasoningDeltaBatcherBarrierFlushesBeforeOtherEvents(t *testing.T) {
	batcher := newReasoningDeltaBatcher(300 * time.Millisecond)
	batcher.schedule = func(time.Duration, func()) {}

	var order []string
	batcher.Add("seg-1", func(text string) { order = append(order, "reasoning:"+text) }, "thought one ")
	batcher.Add("seg-1", func(text string) { order = append(order, "reasoning:"+text) }, "thought two")

	// A different segment key flushes the previous segment first.
	batcher.Add("seg-2", func(text string) { order = append(order, "reasoning:"+text) }, "next")
	if len(order) != 1 || order[0] != "reasoning:thought one thought two" {
		t.Fatalf("segment switch flush = %#v", order)
	}

	batcher.Barrier()
	if len(order) != 2 || order[1] != "reasoning:next" {
		t.Fatalf("barrier flush = %#v", order)
	}
	batcher.Barrier() // Empty barrier is a no-op.
	if len(order) != 2 {
		t.Fatalf("empty barrier emitted: %#v", order)
	}
	batcher.Flush() // Flush on an empty batcher is a no-op.
	if len(order) != 2 {
		t.Fatalf("empty flush emitted: %#v", order)
	}

	// Starting a new key after the old key was fully flushed must not re-emit
	// the old cumulative base.
	batcher.Add("seg-3", func(text string) { order = append(order, "reasoning:"+text) }, "fresh")
	if len(order) != 2 {
		t.Fatalf("new key re-emitted old base: %#v", order)
	}
	batcher.Barrier()
	if strings.Join(order, "|") != "reasoning:thought one thought two|reasoning:next|reasoning:fresh" {
		t.Fatalf("order = %#v", order)
	}
}
