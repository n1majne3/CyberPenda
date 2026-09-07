package daemon

import (
	"net/http"
	"pentest/internal/childhistory"
	"strconv"
)

func (server *Server) handleTaskChildHistory(w http.ResponseWriter, r *http.Request) {
	found, ok := server.requireProjectTask(w, r)
	if !ok {
		return
	}
	server.writeChildHistory(w, r, server.taskOwnerHistory(found))
}
func (server *Server) handleSessionChildHistory(w http.ResponseWriter, r *http.Request) {
	found, err := server.sessions.Get(r.PathValue("id"))
	if err != nil {
		writeSessionError(w, err)
		return
	}
	server.writeChildHistory(w, r, server.sessionOwnerHistory(found))
}
func (server *Server) writeChildHistory(w http.ResponseWriter, r *http.Request, h ownerHistory) {
	child := r.PathValue("child_id")
	raw := r.PathValue("cursor")
	detail := childhistory.Detail
	if raw == "" {
		raw = r.PathValue("change_cursor")
		detail = childhistory.RawDetail
	}
	if raw != "" {
		cursor, err := strconv.Atoi(raw)
		if err != nil || cursor <= 0 {
			writeError(w, http.StatusBadRequest, "invalid child item cursor")
			return
		}
		entry, found, err := detail(h.childDB, h.childOwner, child, cursor)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read child entry")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "child entry not found")
			return
		}
		writeJSON(w, http.StatusOK, entry)
		return
	}
	query, err := childhistory.ParseQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, found, err := childhistory.Read(h.childDB, h.childOwner, child, query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read child history")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "child history not found")
		return
	}
	writeJSON(w, http.StatusOK, page)
}
