package daemon

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/task"
)

func inMemoryAttachment(name, contents string) uploadedAttachment {
	return uploadedAttachment{
		filename: name,
		size:     int64(len(contents)),
		open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(contents))), nil
		},
	}
}

func TestSanitizeAttachmentFilenameStripsTraversal(t *testing.T) {
	cases := map[string]string{
		"report.pdf":              "report.pdf",
		"../../etc/passwd":        "passwd",
		"/abs/scope.txt":          "scope.txt",
		`windows\path\creds.json`: "creds.json",
		"   ":                     "",
		"..":                      "",
		".":                       "",
		"":                        "",
	}
	for input, want := range cases {
		if got := sanitizeAttachmentFilename(input); got != want {
			t.Errorf("sanitizeAttachmentFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveAttachmentNamesDeduplicates(t *testing.T) {
	resolved, err := resolveAttachmentNames([]uploadedAttachment{
		inMemoryAttachment("a.txt", "1"),
		inMemoryAttachment("a.txt", "2"),
		inMemoryAttachment("sub/a.txt", "3"),
		inMemoryAttachment("b", "4"),
	})
	if err != nil {
		t.Fatalf("resolveAttachmentNames: %v", err)
	}
	got := []string{resolved[0].finalName, resolved[1].finalName, resolved[2].finalName, resolved[3].finalName}
	want := []string{"a.txt", "a-1.txt", "a-2.txt", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finalName[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveAttachmentNamesRejectsEmpty(t *testing.T) {
	if _, err := resolveAttachmentNames([]uploadedAttachment{inMemoryAttachment("../", "x")}); err == nil {
		t.Fatal("expected error for unusable filename")
	}
}

func TestAttachmentWorkdirGoalPath(t *testing.T) {
	sandbox := attachmentWorkdirGoalPath(task.RunnerSandbox, "/runs", "task-1", "scope.txt")
	if sandbox != "/task/workdir/scope.txt" {
		t.Errorf("sandbox path = %q", sandbox)
	}
	host := attachmentWorkdirGoalPath(task.RunnerHost, "/runs", "task-1", "scope.txt")
	if host != "/runs/task-1/workdir/scope.txt" {
		t.Errorf("host path = %q", host)
	}
}

func TestLaunchGoalWithAttachmentsAppendsSection(t *testing.T) {
	goal, resolved, err := launchGoalWithAttachments(
		"enumerate example.com",
		task.RunnerSandbox,
		"/runs",
		"task-1",
		[]uploadedAttachment{inMemoryAttachment("scope.txt", "a"), inMemoryAttachment("report.pdf", "b")},
	)
	if err != nil {
		t.Fatalf("launchGoalWithAttachments: %v", err)
	}
	want := "enumerate example.com\n\nATTACHED FILES:\n- /task/workdir/scope.txt\n- /task/workdir/report.pdf"
	if goal != want {
		t.Errorf("goal = %q, want %q", goal, want)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved count = %d", len(resolved))
	}
}

func TestLaunchGoalWithAttachmentsNoFilesLeavesGoal(t *testing.T) {
	goal, resolved, err := launchGoalWithAttachments("just a goal", task.RunnerSandbox, "/runs", "task-1", nil)
	if err != nil {
		t.Fatalf("launchGoalWithAttachments: %v", err)
	}
	if goal != "just a goal" {
		t.Errorf("goal mutated: %q", goal)
	}
	if resolved != nil {
		t.Errorf("expected nil resolved, got %#v", resolved)
	}
}

func TestWriteTaskAttachmentsWritesFiles(t *testing.T) {
	workdir := t.TempDir()
	resolved, err := resolveAttachmentNames([]uploadedAttachment{
		inMemoryAttachment("scope.txt", "hello"),
		inMemoryAttachment("scope.txt", "world"),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := writeTaskAttachments(workdir, resolved); err != nil {
		t.Fatalf("writeTaskAttachments: %v", err)
	}
	assertFileContents(t, filepath.Join(workdir, "scope.txt"), "hello")
	assertFileContents(t, filepath.Join(workdir, "scope-1.txt"), "world")
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("%s = %q, want %q", path, string(data), want)
	}
}

func TestParseCreateTaskRequestJSON(t *testing.T) {
	body := `{"type":"pentest","goal":"enumerate","runner":"host"}`
	request := httptest.NewRequest(http.MethodPost, "/api/projects/p1/tasks", bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	input, attachments, err := parseCreateTaskRequest(request)
	if err != nil {
		t.Fatalf("parseCreateTaskRequest: %v", err)
	}
	if input.Goal != "enumerate" || input.Runner != task.RunnerHost {
		t.Errorf("input = %#v", input)
	}
	if attachments != nil {
		t.Errorf("expected no attachments, got %d", len(attachments))
	}
}

func TestParseCreateTaskRequestMultipart(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("payload", `{"type":"pentest","goal":"scan","runner":"sandbox"}`); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	part, err := writer.CreateFormFile("attachments", "scope.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("10.0.0.0/24")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/p1/tasks", &buf)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	input, attachments, err := parseCreateTaskRequest(request)
	if err != nil {
		t.Fatalf("parseCreateTaskRequest: %v", err)
	}
	if input.Goal != "scan" || input.Runner != task.RunnerSandbox {
		t.Errorf("input = %#v", input)
	}
	if len(attachments) != 1 || attachments[0].filename != "scope.txt" {
		t.Fatalf("attachments = %#v", attachments)
	}
	reader, err := attachments[0].open()
	if err != nil {
		t.Fatalf("open attachment: %v", err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if string(data) != "10.0.0.0/24" {
		t.Errorf("attachment contents = %q", string(data))
	}
}

func TestParseCreateTaskRequestMultipartMissingPayload(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/p1/tasks", &buf)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if _, _, err := parseCreateTaskRequest(request); err == nil {
		t.Fatal("expected error for missing payload field")
	}
}
