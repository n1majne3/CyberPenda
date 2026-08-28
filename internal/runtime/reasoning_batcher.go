package runtime

import (
	"strings"
	"sync"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/task"
)

// reasoningBatchWindow coalesces streaming reasoning deltas before they become
// durable Task Events. Live-tail polling cannot render finer granularity, so
// tighter batches only grow the event store without changing what the
// operator sees.
const reasoningBatchWindow = 300 * time.Millisecond

type reasoningRuntimeOutput struct {
	ProviderEvent string
	SessionID     string
	TurnID        string
	ItemID        string
	Stream        string
	Phase         string
	Text          string
}

// emitReasoningRuntimeOutput is the single provider-session boundary for a
// Runtime Reasoning Entry. It applies the normal shape-based redaction before
// forwarding the bounded correlation payload to the Harness or daemon sink.
func (s *providerSessionAdapter) emitReasoningRuntimeOutput(emit ProviderSessionEmit, output reasoningRuntimeOutput) {
	if s == nil || output.Text == "" {
		return
	}
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	if emit == nil {
		return
	}
	payload := task.EventPayload{
		"provider": s.provider, "provider_event": output.ProviderEvent,
		"session_id": output.SessionID, "provider_turn_id": output.TurnID,
		"provider_item_id": output.ItemID, "phase": output.Phase,
		"stream": output.Stream, "text": output.Text,
	}
	emit(task.EventKindRuntimeOutput, task.EventPayload(adapters.Redact(map[string]any(payload))))
}

// reasoningDeltaBatcher accumulates reasoning deltas for one streaming segment
// and emits the full cumulative text after the batch window. Every emission
// carries the whole text so far, so transcript projections replace one stable
// entry in place instead of appending fragments. A flush also fires when any
// other event must pass (Barrier), which keeps durable event order equal to
// wire order.
type reasoningDeltaBatcher struct {
	window   time.Duration
	schedule func(delay time.Duration, fn func())

	mu         sync.Mutex
	key        string
	base       string
	text       strings.Builder
	flush      func(text string)
	timerSet   bool
	generation uint64
}

func newReasoningDeltaBatcher(window time.Duration) *reasoningDeltaBatcher {
	return &reasoningDeltaBatcher{
		window: window,
		schedule: func(delay time.Duration, fn func()) {
			time.AfterFunc(delay, fn)
		},
	}
}

// Add accumulates one reasoning delta for the segment identified by key. A
// pending segment for a different key flushes first so durable order follows
// wire order. The flush callback receives the full cumulative segment text,
// including text already flushed for the same key, so transcript projections
// can replace one stable entry in place.
func (b *reasoningDeltaBatcher) Add(key string, flush func(text string), delta string) {
	if b == nil || key == "" || delta == "" {
		return
	}
	b.mu.Lock()
	if b.key != "" && b.key != key {
		var text string
		var flushFn func(string)
		if b.text.Len() > 0 {
			text, flushFn = b.emitTextLocked()
		}
		b.mu.Unlock()
		if flushFn != nil {
			flushFn(text)
		}
		b.mu.Lock()
		// The old segment ended; the new key starts fresh. A fully flushed old
		// base is not emitted again during the key transition.
		b.key = ""
		b.base = ""
		b.flush = nil
		b.timerSet = false
	}
	if b.key == "" {
		b.key = key
		b.text.Reset()
	}
	// Refresh the callback because a later Add can carry a newer direct emit
	// target even when the segment key stays the same.
	b.flush = flush
	if !b.timerSet {
		b.timerSet = true
		b.generation++
		generation := b.generation
		b.schedule(b.window, func() { b.flushGeneration(generation) })
	}
	b.text.WriteString(delta)
	b.mu.Unlock()
}

// Barrier flushes any pending reasoning segment immediately. Adapters call it
// before forwarding a non-reasoning event so reasoning never lands after the
// work it preceded. The segment stays cumulative for later deltas.
func (b *reasoningDeltaBatcher) Barrier() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.key == "" || b.text.Len() == 0 {
		b.mu.Unlock()
		return
	}
	text, flushFn := b.emitTextLocked()
	b.mu.Unlock()
	flushFn(text)
}

func (b *reasoningDeltaBatcher) flushGeneration(generation uint64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if generation != b.generation || b.key == "" || b.text.Len() == 0 {
		b.mu.Unlock()
		return
	}
	text, flushFn := b.emitTextLocked()
	b.mu.Unlock()
	flushFn(text)
}

// Flush emits any pending segment immediately. Tests and explicit lifecycle
// boundaries use it; scheduled callbacks use flushGeneration so stale timers
// cannot flush a later segment.
func (b *reasoningDeltaBatcher) Flush() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.key == "" || b.text.Len() == 0 {
		b.mu.Unlock()
		return
	}
	text, flushFn := b.emitTextLocked()
	b.mu.Unlock()
	flushFn(text)
}

// Complete returns the full cumulative segment and clears its lifecycle state.
// Adapters use it when a provider turn ends without a separate completed
// reasoning item, so the final projection replaces `streaming` with
// `collapsed` instead of leaving a stale live entry.
func (b *reasoningDeltaBatcher) Complete() (string, string, bool) {
	if b == nil {
		return "", "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.key == "" {
		return "", "", false
	}
	key := b.key
	text := b.base + b.text.String()
	b.resetLocked()
	return key, text, text != ""
}

// Reset clears one completed segment after the provider supplied its own full
// reasoning item.
func (b *reasoningDeltaBatcher) Reset() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.resetLocked()
	b.mu.Unlock()
}

// emitTextLocked joins the committed base with the pending text, remembers
// the combined text as the new base, and clears the pending state.
func (b *reasoningDeltaBatcher) emitTextLocked() (string, func(string)) {
	text := b.base + b.text.String()
	b.base = text
	b.text.Reset()
	b.timerSet = false
	return text, b.flush
}

func (b *reasoningDeltaBatcher) resetLocked() {
	b.key = ""
	b.base = ""
	b.text.Reset()
	b.flush = nil
	b.timerSet = false
}
