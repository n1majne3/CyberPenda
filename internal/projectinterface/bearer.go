package projectinterface

import (
	"net/http"
	"strings"
)

// OperatorActorHeader carries the stable local operator identity on daemon-
// authenticated Blackboard v2 requests.
const OperatorActorHeader = "CyberPenda-Actor-ID"

// BearerToken extracts a Continuation capability from the Authorization header
// or the MCP query fallback used by transports that cannot attach headers.
func BearerToken(request *http.Request) string {
	if header := strings.TrimSpace(request.Header.Get("Authorization")); header != "" {
		if scheme, token, ok := strings.Cut(header, " "); ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(token)
		}
	}
	return request.URL.Query().Get("token")
}
