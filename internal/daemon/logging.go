package daemon

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/preflight"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// noisyPollPaths are GET endpoints the UI polls on a fixed interval while a
// task runs. Logging every one drowns out meaningful activity, so successful
// GETs against them are suppressed; errors (non-2xx) still log.
var noisyPollPaths = []string{
	"/events",
	"/transcript",
	"/timeline",
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

// logTaskLaunchStage records the synchronous preparation boundary entered by a
// task launch. If one of these operations stalls, the last stage remains
// visible even though the enclosing HTTP request has not returned yet.
func (server *Server) logTaskLaunchStage(t task.Task, stage string) {
	if server.logger == nil {
		return
	}
	server.logger.Printf("task launch stage=%s runner=%s profile=%s id=%s", stage, t.Runner, t.RuntimeProfileID, t.ID)
}

func (server *Server) logDockerSandboxEvent(t task.Task, event runtime.DockerSandboxLogEvent) {
	if server.logger == nil {
		return
	}
	safe := adapters.Redact(map[string]any{
		"phase":  event.Phase,
		"image":  event.Image,
		"stream": event.Stream,
		"text":   event.Text,
	})
	phase, _ := safe["phase"].(string)
	image, _ := safe["image"].(string)
	stream, _ := safe["stream"].(string)
	detail, _ := safe["text"].(string)
	if detail == "" {
		server.logger.Printf("task sandbox phase=%s runner=%s profile=%s id=%s image=%q", phase, t.Runner, t.RuntimeProfileID, t.ID, image)
		return
	}
	server.logger.Printf("task sandbox phase=%s runner=%s profile=%s id=%s image=%q stream=%s detail=%q", phase, t.Runner, t.RuntimeProfileID, t.ID, image, stream, detail)
}

// logCustomArgConflict writes a diagnostic line naming the complete offending
// Custom Arg (flag/key, value redacted when secret-shaped) and structured field.
// Raw secrets from the original Custom Args list never appear in the log.
func (server *Server) logCustomArgConflict(provider runtimeprofile.Provider, customArgs []string, err error) {
	if server.logger == nil || err == nil {
		return
	}
	argument := ""
	flag := ""
	field := ""
	var conflict *runtimeprofile.CustomArgConflictError
	if errors.As(err, &conflict) {
		// Argument is already secret-safe (values redacted at construction).
		argument = conflict.Argument
		flag = conflict.Flag
		field = conflict.Field
		if len(customArgs) == 0 {
			customArgs = conflict.CustomArgs
		}
		if provider == "" {
			provider = conflict.Provider
		}
	}
	redactedArgs := adapters.Redact(map[string]any{"custom_args": customArgs})
	server.logger.Printf(
		"runtime profile custom_args conflict provider=%s argument=%q flag=%q structured_field=%s custom_args=%v detail=%q",
		provider,
		argument,
		flag,
		field,
		redactedArgs["custom_args"],
		err.Error(),
	)
}

// logPreflightCustomArgConflict emits the same diagnostic when Preflight rejects
// conflicting Custom Args on a stored Runtime Profile.
func (server *Server) logPreflightCustomArgConflict(profileID string, result preflight.Result) {
	if server.logger == nil {
		return
	}
	var detail string
	for _, check := range result.Checks {
		if check.Name == "custom_args" && check.Status == preflight.CheckFail {
			detail = check.Detail
			break
		}
	}
	if detail == "" {
		return
	}
	provider := runtimeprofile.Provider("")
	var customArgs []string
	if profile, err := server.profiles.Get(profileID); err == nil {
		provider = profile.Provider
		customArgs = profile.Fields.CustomArgs
	}
	// Rebuild a typed error so logCustomArgConflict can name argument/field.
	if err := runtimeprofile.ValidateCustomArgs(provider, customArgs); err != nil {
		server.logCustomArgConflict(provider, customArgs, err)
		return
	}
	redactedArgs := adapters.Redact(map[string]any{"custom_args": customArgs})
	server.logger.Printf(
		"runtime profile custom_args conflict provider=%s profile=%s custom_args=%v detail=%q",
		provider,
		profileID,
		redactedArgs["custom_args"],
		detail,
	)
}
