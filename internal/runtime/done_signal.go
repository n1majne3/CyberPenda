package runtime

import "sync"

// FirstSignal returns a channel that is closed when the first of the given
// signals fires. Nil channels are ignored. The result is one-shot and safe for
// concurrent waiters.
//
// Production bridges expose distinct Closed (explicit cleanup) and Terminated
// (unexpected process/protocol exit) signals; adapters wait on FirstSignal of
// both so either path ends the Task-scoped run.
func FirstSignal(signals ...<-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }
	listening := false
	for _, signal := range signals {
		if signal == nil {
			continue
		}
		listening = true
		go func(ch <-chan struct{}) {
			<-ch
			closeDone()
		}(signal)
	}
	if !listening {
		closeDone()
	}
	return done
}
