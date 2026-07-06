package runtime

import (
	"encoding/json"
	"strings"
)

// NativeSessionMetadataFromRuntimeLine extracts provider-native session metadata
// from JSONL stdout records. Claude Code emits the session id in a system init
// record; Pi emits it in a session header; Codex session files use a
// session_meta payload record.
func NativeSessionMetadataFromRuntimeLine(line string) NativeSessionMetadata {
	var record struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		Payload   struct {
			SessionID string `json:"session_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return NativeSessionMetadata{}
	}
	sessionID := strings.TrimSpace(record.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(record.Payload.SessionID)
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(record.ID)
	}
	if sessionID == "" {
		return NativeSessionMetadata{}
	}
	switch {
	case record.Type == "system" && record.Subtype == "init":
		return NativeSessionMetadata{NativeSessionID: sessionID}
	case record.Type == "session_meta":
		return NativeSessionMetadata{NativeSessionID: sessionID}
	case record.Type == "session":
		return NativeSessionMetadata{NativeSessionID: sessionID}
	default:
		return NativeSessionMetadata{}
	}
}
