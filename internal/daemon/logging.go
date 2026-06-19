package daemon

import (
	"net/http"
	"strings"
	"time"

	"pentest/internal/task"
)

// noisyPollPaths are GET endpoints the UI polls on a fixed interval while a
// task runs. Logging every one drowns out meaningful activity, so successful
// GETs against them are suppressed; errors (non-2xx) still log.
var noisyPollPaths = []string{
	"/events",
	"/transcript",
}

// isNoisyPoll reports whether a successful GET is a UI poll endpoint whose
// per-request log line is noise rather than signal.
func isNoisyPoll(method, path string, status int) bool {
	if method != http.MethodGet || status < 200 || status >= 300 {
		return false
	}
	for _, suffix := range noisyPollPaths {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	// The task-detail GET /api/projects/{id}/tasks/{taskID} (no further path
	// segment) is polled every few seconds while the task runs.
	if strings.Contains(path, "/tasks/") {
		afterTasks := path[strings.LastIndex(path, "/tasks/")+len("/tasks/"):]
		if afterTasks != "" && !strings.Contains(afterTasks, "/") {
			return true
		}
	}
	return false
}

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
// status, and duration. No-op when no logger is configured. Successful GETs to
// frequently-polled endpoints are suppressed to avoid drowning out signal.
func (server *Server) logRequest(start time.Time, request *http.Request, status int) {
	if server.logger == nil {
		return
	}
	if isNoisyPoll(request.Method, request.URL.Path, status) {
		return
	}
	server.logger.Printf("http %s %s -> %d (%dms)", request.Method, request.URL.Path, status, time.Since(start).Milliseconds())
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
