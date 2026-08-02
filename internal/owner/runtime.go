package owner

import "time"

// Continuation is the owner-neutral Runtime Continuation contract exchanged
// between an owner aggregate and the provider-session boundary. Task and
// Session persistence keep their own richer projections; this contract never
// embeds either aggregate type or a nullable cross-owner relationship.
type Continuation struct {
	ID                     string
	OwnerID                string
	Number                 int
	RuntimeProfileID       string
	RuntimeProvider        string
	Runner                 string
	Status                 string
	ContainerID            string
	NativeSessionID        string
	NativeSessionPath      string
	RuntimeConfigVersionID string
	StartedAt              time.Time
	UpdatedAt              time.Time
	EndedAt                *time.Time
}
