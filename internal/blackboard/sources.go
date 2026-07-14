package blackboard

import (
	"sync"
	"time"
)

// Clock is the injected time source for the graph service. recorded_at is
// always server-generated via this seam; callers cannot supply it (graph
// contract §3.3, TDD slices §2.1).
type Clock interface {
	Now() time.Time
}

// IDSource is the injected immutable-ID source for graph identities. Stable,
// deterministic IDs let reopen produce byte-identical result hashes (TDD
// slices §4.1).
type IDSource interface {
	NextID() string
}

// SystemClock is the production Clock implementation.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// RandomIDSource is the production IDSource implementation using crypto/rand.
type RandomIDSource struct{}

// NextID returns a fresh opaque 16-byte hex ID.
func (RandomIDSource) NextID() string { return newID() }

// SequenceClock is a deterministic Clock for tests: each Now() call advances
// to the next fixed RFC3339Nano value. It panics if exhausted to keep tests
// honest about their fixture size.
type SequenceClock struct {
	mu     sync.Mutex
	values []time.Time
	next   int
}

// NewSequenceClock parses the given RFC3339Nano values into a deterministic
// sequence clock.
func NewSequenceClock(values ...string) *SequenceClock {
	ts := make([]time.Time, 0, len(values))
	for _, v := range values {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			panic("SequenceClock: invalid RFC3339Nano value: " + v)
		}
		ts = append(ts, t)
	}
	return &SequenceClock{values: ts}
}

// Now returns the next fixed timestamp in sequence.
func (c *SequenceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next >= len(c.values) {
		panic("SequenceClock exhausted")
	}
	t := c.values[c.next]
	c.next++
	return t
}

// SequenceIDSource is a deterministic IDSource for tests: each NextID() call
// returns the next fixed value. It panics if exhausted.
type SequenceIDSource struct {
	mu     sync.Mutex
	values []string
	next   int
}

// NewSequenceIDSource returns a deterministic ID source yielding the given
// values in order.
func NewSequenceIDSource(values ...string) *SequenceIDSource {
	return &SequenceIDSource{values: values}
}

// NextID returns the next fixed ID in sequence.
func (s *SequenceIDSource) NextID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.values) {
		panic("SequenceIDSource exhausted")
	}
	v := s.values[s.next]
	s.next++
	return v
}
