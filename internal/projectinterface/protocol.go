package projectinterface

// TrustedToolDefinition is the versioned Runtime Protocol source used by MCP
// adapters and generated task instructions.
type TrustedToolDefinition struct {
	ProtocolVersion int
	Name            string
	Description     string
}

var trustedToolDefinitions = []TrustedToolDefinition{
	{RuntimeProtocolVersion, "blackboard_apply", "Apply one atomic typed graph mutation batch to the Blackboard. Project and provenance are bound from the Continuation Interface Grant; do not supply them."},
	{RuntimeProtocolVersion, "blackboard_resolve_records", "Resolve graph nodes and edges by stable key or immutable ID at one observed graph revision."},
	{RuntimeProtocolVersion, "blackboard_get_current_graph", "Return the exact current CanonicalMainGraphV1 projection and metadata for the bound Project."},
}

// TrustedToolDefinitions returns a copy in canonical protocol order.
func TrustedToolDefinitions() []TrustedToolDefinition {
	return append([]TrustedToolDefinition(nil), trustedToolDefinitions...)
}

// TrustedToolDescription returns the canonical description for name.
func TrustedToolDescription(name string) string {
	for _, definition := range trustedToolDefinitions {
		if definition.Name == name {
			return definition.Description
		}
	}
	return ""
}
