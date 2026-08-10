package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"pentest/internal/task"
)

// Attachment upload limits for the launch request. Files spill to temporary
// storage during multipart parsing, so these caps bound both memory pressure
// and the amount of operator-supplied data written into a task workdir.
const (
	maxAttachmentFileBytes = 100 << 20 // 100 MiB per file
	maxAttachmentCount     = 25
	// maxTotalUploadBytes bounds the whole multipart request so a client cannot
	// stream unbounded data before per-file checks run.
	maxTotalUploadBytes = maxAttachmentCount*maxAttachmentFileBytes + (16 << 20)
)

var errInvalidTaskBody = errors.New("invalid request body")

// uploadedAttachment is one operator-supplied file staged for projection into
// the task workdir. open streams the contents on demand so large files never
// need to reside fully in memory.
type uploadedAttachment struct {
	filename string
	size     int64
	open     func() (io.ReadCloser, error)
}

// resolvedAttachment pairs an uploaded file with the sanitized, collision-free
// name it takes inside the task workdir.
type resolvedAttachment struct {
	source    uploadedAttachment
	finalName string
}

// createTaskInput is the launch configuration decoded from either a JSON body
// or the JSON "payload" field of a multipart request.
type createTaskInput struct {
	Goal             string            `json:"goal"`
	Type             task.Type         `json:"type"`
	RuntimeProfileID string            `json:"runtime_profile_id"`
	ModelOverride    string            `json:"model_override,omitempty"`
	ReasoningEffort  string            `json:"reasoning_effort,omitempty"`
	Runner           task.Runner       `json:"runner"`
	RunControls      task.RunControls  `json:"run_controls"`
	Extras           map[string]string `json:"extras"`
}

// parseCreateTaskRequest reads the create-task body from either a JSON payload
// or a multipart/form-data request. Multipart requests carry the JSON launch
// config in the "payload" field and files in "attachments" parts; any other
// content type keeps the historical JSON decode so JSON-only clients are
// unaffected.
func parseCreateTaskRequest(request *http.Request) (createTaskInput, []uploadedAttachment, error) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		return parseMultipartCreateTaskRequest(request)
	}
	var input createTaskInput
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		return createTaskInput{}, nil, errInvalidTaskBody
	}
	return input, nil, nil
}

func parseMultipartCreateTaskRequest(request *http.Request) (createTaskInput, []uploadedAttachment, error) {
	// Keep small parts in memory; larger files spill to temp files Go removes
	// when the request ends.
	if err := request.ParseMultipartForm(32 << 20); err != nil {
		return createTaskInput{}, nil, errInvalidTaskBody
	}
	payload := strings.TrimSpace(request.FormValue("payload"))
	if payload == "" {
		return createTaskInput{}, nil, errInvalidTaskBody
	}
	var input createTaskInput
	if err := json.Unmarshal([]byte(payload), &input); err != nil {
		return createTaskInput{}, nil, errInvalidTaskBody
	}
	var headers []*multipart.FileHeader
	if request.MultipartForm != nil {
		headers = request.MultipartForm.File["attachments"]
	}
	if len(headers) > maxAttachmentCount {
		return createTaskInput{}, nil, fmt.Errorf("too many attachments (max %d)", maxAttachmentCount)
	}
	attachments := make([]uploadedAttachment, 0, len(headers))
	for _, header := range headers {
		if header.Size > maxAttachmentFileBytes {
			return createTaskInput{}, nil, fmt.Errorf("attachment %q exceeds the %d MiB limit", header.Filename, maxAttachmentFileBytes>>20)
		}
		bound := header
		attachments = append(attachments, uploadedAttachment{
			filename: header.Filename,
			size:     header.Size,
			open: func() (io.ReadCloser, error) {
				return bound.Open()
			},
		})
	}
	return input, attachments, nil
}

// sanitizeAttachmentFilename reduces a client-supplied name to a safe basename,
// stripping directory components and traversal so a write can never escape the
// workdir. It returns "" when nothing usable remains.
func sanitizeAttachmentFilename(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	// Clients may send Windows or POSIX separators; collapse both to a base.
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	base := filepath.Base(path.Base(trimmed))
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." {
		return ""
	}
	return base
}

// resolveAttachmentNames sanitizes each uploaded filename and de-duplicates
// collisions with a numeric suffix so every attachment lands under a distinct
// workdir path.
func resolveAttachmentNames(files []uploadedAttachment) ([]resolvedAttachment, error) {
	resolved := make([]resolvedAttachment, 0, len(files))
	used := make(map[string]struct{}, len(files))
	for _, file := range files {
		clean := sanitizeAttachmentFilename(file.filename)
		if clean == "" {
			return nil, fmt.Errorf("attachment %q has no usable filename", file.filename)
		}
		final := clean
		if _, taken := used[final]; taken {
			ext := filepath.Ext(clean)
			stem := strings.TrimSuffix(clean, ext)
			for i := 1; ; i++ {
				candidate := fmt.Sprintf("%s-%d%s", stem, i, ext)
				if _, taken := used[candidate]; !taken {
					final = candidate
					break
				}
			}
		}
		used[final] = struct{}{}
		resolved = append(resolved, resolvedAttachment{source: file, finalName: final})
	}
	return resolved, nil
}

// ownerAttachmentGoalPath returns the path the runtime uses to open an owner
// Workdir attachment: the container-relative /task path for the Sandbox
// runner, or the absolute Workdir path for the Host runner.
func ownerAttachmentGoalPath(runner task.Runner, workdir, finalName string) string {
	if runner == task.RunnerHost {
		return filepath.Join(workdir, finalName)
	}
	return path.Join("/task/workdir", finalName)
}

func attachmentWorkdirGoalPath(runner task.Runner, runtimeRoot, taskID, finalName string) string {
	return ownerAttachmentGoalPath(runner, filepath.Join(runtimeRoot, taskID, "workdir"), finalName)
}

// appendAttachmentPathsToGoal appends a trailing ATTACHED FILES section listing
// each runtime-visible attachment path. It returns the goal unchanged when there
// are no attachments.
func appendAttachmentPathsToGoal(goal string, paths []string) string {
	if len(paths) == 0 {
		return goal
	}
	var builder strings.Builder
	builder.WriteString(goal)
	builder.WriteString("\n\nATTACHED FILES:\n")
	for i, p := range paths {
		if i > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("- ")
		builder.WriteString(p)
	}
	return builder.String()
}

// launchGoalWithAttachments computes the goal handed to the runtime, appending
// the runner-appropriate attachment paths, and the resolved names to write into
// the workdir. The stored task goal is left untouched by callers.
func launchGoalWithAttachments(goal string, runner task.Runner, runtimeRoot, taskID string, attachments []uploadedAttachment) (string, []resolvedAttachment, error) {
	resolved, err := resolveAttachmentNames(attachments)
	if err != nil {
		return "", nil, err
	}
	if len(resolved) == 0 {
		return goal, nil, nil
	}
	paths := make([]string, len(resolved))
	for i, item := range resolved {
		paths[i] = attachmentWorkdirGoalPath(runner, runtimeRoot, taskID, item.finalName)
	}
	return appendAttachmentPathsToGoal(goal, paths), resolved, nil
}

// writeTaskAttachments streams each resolved attachment into the task workdir
// under its final name with owner-only permissions.
func writeTaskAttachments(workdir string, resolved []resolvedAttachment) error {
	for _, item := range resolved {
		if err := writeSingleAttachment(workdir, item); err != nil {
			return err
		}
	}
	return nil
}

func writeSingleAttachment(workdir string, item resolvedAttachment) error {
	reader, err := item.source.open()
	if err != nil {
		return fmt.Errorf("open attachment %q: %w", item.finalName, err)
	}
	defer func() { _ = reader.Close() }()
	dest := filepath.Join(workdir, item.finalName)
	// Defense in depth: finalName is already a sanitized basename, but confirm
	// the resolved path still sits directly inside the workdir before writing.
	if filepath.Dir(dest) != filepath.Clean(workdir) {
		return fmt.Errorf("attachment %q resolves outside the workdir", item.finalName)
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create attachment %q: %w", item.finalName, err)
	}
	if _, err := io.Copy(file, reader); err != nil {
		_ = file.Close()
		return fmt.Errorf("write attachment %q: %w", item.finalName, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("finalize attachment %q: %w", item.finalName, err)
	}
	return nil
}
