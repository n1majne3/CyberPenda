package preflight

import "time"

// SetEngineProbeClock lets tests control engine probe cache expiry.
func SetEngineProbeClock(s *Service, now func() time.Time) {
	s.engineProbes.mu.Lock()
	s.engineProbes.now = now
	s.engineProbes.mu.Unlock()
}

const EngineProbeTTL = engineProbeTTL
