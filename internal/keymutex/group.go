package keymutex

import "sync"

type entry struct {
	mu    sync.Mutex
	users int
}

// Group is ready for use without initialization. A Group must not be copied.
type Group struct {
	mu      sync.Mutex
	entries map[string]*entry
}

// Lock acquires the key and returns its release function. Entries remain in the
// group only while a caller holds or waits for their lock.
func (g *Group) Lock(key string) func() {
	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[string]*entry)
	}
	e := g.entries[key]
	if e == nil {
		e = &entry{}
		g.entries[key] = e
	}
	e.users++
	g.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		g.mu.Lock()
		e.users--
		if e.users == 0 {
			delete(g.entries, key)
		}
		g.mu.Unlock()
	}
}
