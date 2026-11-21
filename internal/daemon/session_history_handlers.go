package daemon

import (
	"net/http"

	"pentest/internal/session"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

func (server *Server) handleSessionConversation(response http.ResponseWriter, request *http.Request) {
	conversation, err := server.sessions.Conversation(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Events []session.Event `json:"events"`
	}{Events: conversation})
}

func (server *Server) handleSessionTimeline(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	page, err := server.sessionOwnerHistory(found).TimelinePage(parseHistoryRequest(request))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		SessionID string          `json:"session_id"`
		Items     []timeline.Item `json:"items"`
		Cursor    int             `json:"cursor"`
		HasOlder  bool            `json:"has_older"`
	}{
		SessionID: found.ID,
		Items:     page.items,
		Cursor:    page.cursor,
		HasOlder:  page.hasOlder,
	})
}

func (server *Server) handleSessionTimelineItem(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	item, foundItem, err := server.sessionOwnerHistory(found).TimelineItem(request.PathValue("seq"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if !foundItem {
		writeError(response, http.StatusNotFound, "timeline item not found")
		return
	}
	writeJSON(response, http.StatusOK, item)
}

func (server *Server) handleSessionTranscript(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	page, err := server.sessionOwnerHistory(found).TranscriptPage(parseHistoryRequest(request))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		SessionID string             `json:"session_id"`
		Entries   []transcript.Entry `json:"entries"`
		Cursor    int                `json:"cursor"`
		Before    int                `json:"before"`
		HasOlder  bool               `json:"has_older"`
	}{
		SessionID: found.ID,
		Entries:   page.items,
		Cursor:    page.cursor,
		HasOlder:  page.hasOlder,
		Before:    page.before,
	})
}

// handleSessionTranscriptEntry returns one complete retained transcript entry
// by ID, including the full payload that the history window preview truncated.
func (server *Server) handleSessionTranscriptEntry(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	entry, foundEntry, err := server.sessionOwnerHistory(found).TranscriptEntry(request.PathValue("entry_id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if !foundEntry {
		writeError(response, http.StatusNotFound, "transcript entry not found")
		return
	}
	writeJSON(response, http.StatusOK, entry)
}
