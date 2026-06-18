package daemon

import "pentest/internal/runtimeprofile"

// CreateLocalRuntimeProfile stores a profile in the pentest database.
// Tests use this to avoid HTTP round-trips when setting up fixtures.
func (server *Server) CreateLocalRuntimeProfile(name string, provider runtimeprofile.Provider, fields runtimeprofile.Fields) (runtimeprofile.Profile, error) {
	return server.profiles.Create(name, provider, fields)
}