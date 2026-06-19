package daemon

import (
	"net/http"
	"strings"
	"time"

	"pentest/internal/task"
)

// statusRecorder captures the HTTP status code written by a handler so the
// request log can report it. It delegates every WriteHeader/Write call to the
// underlying ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	status    int
	wroteHead bool
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHead {
		return
	}
	r.status = code
	r.wroteHead = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHead {
		r.WriteHeader(r.status)
	}
	return r.ResponseWriter.Write(b)
}

// logRequest writes one structured line per HTTP request: method, path,
// status, and duration. No-op when no logger is configured.
func (server *Server) logRequest(start time.Time, method, path string, status int) {
	if server.logger == nil {
		return
	}
	server.logger.Printf("http %s %s -> %d (%dms)", method, path, status, time.Since(start).Milliseconds())
}

// logTask writes one task-lifecycle line. The goal is truncated so the runtime
// prompt is not echoed into logs.
func (server *Server) logTask(t task.Task, phase, detail string) {
	if server.logger == nil {
		return
	}
	goal := strings.TrimSpace(t.Goal)
	if len(goal) > 60 {
		goal = goal[:57] + "..."
	}
	if detail == "" {
		server.logger.Printf("task %s runner=%s provider=%s id=%s goal=%q", phase, t.Runner, t.RuntimeProfileID, t.ID, goal)
		return
	}
	server.logger.Printf("task %s runner=%s provider=%s id=%s goal=%q detail=%q", phase, t.Runner, t.RuntimeProfileID, t.ID, goal, detail)
}
