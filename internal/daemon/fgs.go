package daemon

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/owner"
)

func (server *Server) registerFGSRoutes() {
	for _, scope := range []string{"projects", "sessions"} {
		server.mux.HandleFunc("GET /api/v2/"+scope+"/{id}/fgs", server.handleFGSRead)
		server.mux.HandleFunc("GET /api/v2/"+scope+"/{id}/fgs/status", server.handleFGSRead)
		server.mux.HandleFunc("GET /api/v2/"+scope+"/{id}/fgs/report", server.handleFGSRead)
		server.mux.HandleFunc("GET /api/v2/"+scope+"/{id}/fgs/nodes/{key}/history", server.handleFGSRead)
	}
}

func (server *Server) allowLegacyGraphWrite(w http.ResponseWriter, principal blackboardV2Principal) bool {
	protocol := ""
	if principal.sessionID != "" {
		found, err := server.sessions.Get(principal.sessionID)
		if err != nil {
			writeError(w, http.StatusNotFound, "Session not found")
			return false
		}
		protocol = found.BlackboardProtocol
	} else {
		found, err := server.projects.Get(principal.projectID)
		if err != nil {
			writeError(w, http.StatusNotFound, "Project not found")
			return false
		}
		protocol = found.BlackboardProtocol
	}
	if protocol == "fgs" {
		writeError(w, http.StatusConflict, "This Blackboard uses FGS. Publish Goal, Step, and Fact updates through the Runtime Outbox.")
		return false
	}
	return true
}

func (server *Server) handleFGSRead(w http.ResponseWriter, r *http.Request) {
	var principal blackboardV2Principal
	var authErr *blackboardv2.Error
	if strings.HasPrefix(r.URL.Path, "/api/v2/sessions/") {
		principal, authErr = server.authenticateSessionBlackboardV2(r)
	} else {
		principal, authErr = server.authenticateBlackboardV2(r, false)
	}
	if authErr != nil {
		writeBlackboardV2Error(w, authErr, nil)
		return
	}
	var contract owner.Contract
	var title string
	if principal.sessionID != "" {
		found, err := server.sessions.Get(principal.sessionID)
		if err != nil {
			http.Error(w, "Session not found", http.StatusNotFound)
			return
		}
		if found.BlackboardProtocol != "fgs" || found.RunControls.BlackboardMode == "disabled" {
			http.Error(w, "FGS is not enabled", http.StatusConflict)
			return
		}
		contract = found.OwnerContract()
		title = found.Title
	} else {
		found, err := server.projects.Get(principal.projectID)
		if err != nil {
			http.Error(w, "Project not found", http.StatusNotFound)
			return
		}
		if found.BlackboardProtocol != "fgs" {
			http.Error(w, "FGS is not enabled", http.StatusConflict)
			return
		}
		title = found.Name
		taskID := principal.taskID
		if principal.operator {
			taskID = "operator-read"
		}
		contract = owner.NewTaskContract(taskID, found.ID, "")
	}
	var result any
	var err error
	if strings.HasSuffix(r.URL.Path, "/report") {
		result, err = server.fgs.Report(r.Context(), contract, title)
	} else if strings.HasSuffix(r.URL.Path, "/status") {
		result, err = server.fgs.Status(r.Context(), contract)
	} else if key := r.PathValue("key"); key != "" {
		before := 0
		if value := r.URL.Query().Get("cursor"); value != "" {
			before, err = strconv.Atoi(value)
			if err != nil || before < 0 {
				http.Error(w, "invalid history cursor", http.StatusBadRequest)
				return
			}
		}
		result, err = server.fgs.HistoryPage(r.Context(), contract, key, before, 100)
	} else {
		limit := 200
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 200 {
				http.Error(w, "limit must be 1 to 200", http.StatusBadRequest)
				return
			}
		}
		result, err = server.fgs.ReadPage(r.Context(), contract, r.URL.Query().Get("cursor"), limit)
	}
	if err != nil {
		http.Error(w, "FGS read failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// The receiver uses server-owned Continuation bindings, never IDs from files.
// Each pass reads a bounded page and releases SQLite rows before settlement.
func (server *Server) startFGSReceiver() {
	ctx, cancel := context.WithCancel(context.Background())
	server.fgsCancel = cancel
	server.fgsWG.Add(1)
	go func() {
		defer server.fgsWG.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		cursor := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			rows, err := server.db.QueryContext(ctx, `SELECT kind,owner_id,continuation_id FROM (
    SELECT 'session' AS kind,s.id AS owner_id,c.id AS continuation_id FROM sessions s JOIN session_continuations c ON c.session_id=s.id WHERE s.blackboard_protocol='fgs' AND s.blackboard_mode!='disabled'
    UNION ALL
    SELECT 'task',t.id,c.id FROM tasks t JOIN projects p ON p.id=t.project_id JOIN task_continuations c ON c.task_id=t.id WHERE p.blackboard_protocol='fgs' AND json_extract(t.run_controls_json,'$.blackboard_mode')!='disabled'
   ) WHERE kind || ':' || continuation_id > ? ORDER BY kind || ':' || continuation_id LIMIT 64`, cursor)
			if err != nil {
				log.Printf("FGS receiver: %v", err)
				continue
			}
			type binding struct{ kind, id, continuation string }
			bindings := []binding{}
			for rows.Next() {
				var b binding
				if err = rows.Scan(&b.kind, &b.id, &b.continuation); err != nil {
					break
				}
				bindings = append(bindings, b)
			}
			rowErr := rows.Err()
			rows.Close()
			if err != nil || rowErr != nil {
				continue
			}
			for _, b := range bindings {
				var c owner.Contract
				if b.kind == "session" {
					found, e := server.sessions.Get(b.id)
					if e != nil {
						continue
					}
					c = found.OwnerContract()
				} else {
					found, e := server.tasks.Get(b.id)
					if e != nil {
						continue
					}
					workdir, e := filepath.Abs(filepath.Join(server.runtimeRoot, b.id, "workdir"))
					if e != nil {
						continue
					}
					c = found.OwnerContract(workdir)
				}
				if info, e := os.Stat(filepath.Join(c.Workdir, "graph", "outbox", b.continuation)); e == nil && info.IsDir() {
					if _, drainErr := server.fgs.ReceivePage(ctx, c, b.continuation, 64); drainErr != nil {
						log.Printf("FGS receive owner %s: %v", b.id, drainErr)
					}
				}
				cursor = b.kind + ":" + b.continuation
			}
			if len(bindings) < 64 {
				cursor = ""
			}
		}
	}()
}
