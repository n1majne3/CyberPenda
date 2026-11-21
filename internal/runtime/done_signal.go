package runtime

import "sync"

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
