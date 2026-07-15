package daemon

import (
	"net/http"
	"strings"
)

const retiredBlackboardV1Message = "legacy Blackboard v1 interface is unavailable for blackboard_v2; use the Blackboard v2 semantic interface"

// isLegacyBlackboardV1Transport is the bootstrap deny-list for public v1
// transports. Later v2 transport tickets replace these paths and remove their
// entries as each semantic adapter becomes available.
func isLegacyBlackboardV1Transport(request *http.Request) bool {
	path := request.URL.Path
	if !strings.HasPrefix(path, "/api/projects/") {
		return false
	}
	if strings.Contains(path, "/blackboard/") ||
		strings.Contains(path, "/facts/") ||
		strings.HasSuffix(path, "/findings") ||
		strings.Contains(path, "/findings/") ||
		strings.HasSuffix(path, "/evidence") ||
		strings.HasSuffix(path, "/report") ||
		strings.Contains(path, "/reports/") {
		return true
	}
	if strings.Contains(path, "/tasks/") && strings.Contains(path, "/continuations/") && strings.HasSuffix(path, ":finish") {
		return true
	}
	if strings.Contains(path, "/tasks/") && strings.HasSuffix(path, "/continuation") {
		return true
	}
	return strings.Contains(path, "/tasks/") && strings.HasSuffix(path, "/summary")
}
