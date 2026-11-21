package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type countingOversizedJSONReader struct {
	prefix  string
	fill    byte
	suffix  string
	total   int64
	readPos int64
}

func newOversizedJSONReader(prefix, suffix string, total int64) *countingOversizedJSONReader {
	return &countingOversizedJSONReader{
		prefix: prefix,
		fill:   'a',
		suffix: suffix,
		total:  total,
	}
}

func (r *countingOversizedJSONReader) Read(p []byte) (int, error) {
	if r.readPos >= r.total {
		return 0, http.ErrBodyReadAfterClose
	}
	n := 0
	for n < len(p) && r.readPos < r.total {
		pos := r.readPos
		switch {
		case pos < int64(len(r.prefix)):
			p[n] = r.prefix[pos]
		case pos >= r.total-int64(len(r.suffix)):
			p[n] = r.suffix[pos-(r.total-int64(len(r.suffix)))]
		default:
			p[n] = r.fill
		}
		n++
		r.readPos++
	}
	return n, nil
}

func TestParseCreateTaskRequestBoundsJSONBody(t *testing.T) {
	const limit = 4 << 20
	reader := newOversizedJSONReader(`{"type":"pentest","goal":"`, `","runner":"host"}`, limit+1024)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/p1/tasks", reader)
	request.Header.Set("Content-Type", "application/json")

	if _, _, err := parseCreateTaskRequest(request); err == nil {
		t.Fatal("expected error for oversized Task JSON body")
	}
	if reader.readPos > limit+1 {
		t.Fatalf("read %d bytes, want at most %d", reader.readPos, limit+1)
	}
}

func TestParseCreateSessionRequestBoundsJSONBody(t *testing.T) {
	const limit = 4 << 20
	reader := newOversizedJSONReader(`{"input":"`, `"}`, limit+1024)
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", reader)
	request.Header.Set("Content-Type", "application/json")

	if _, _, err := parseCreateSessionRequest(request); err == nil {
		t.Fatal("expected error for oversized Session JSON body")
	}
	if reader.readPos > limit+1 {
		t.Fatalf("read %d bytes, want at most %d", reader.readPos, limit+1)
	}
}

func TestDecodeOptionalJSONBoundsBody(t *testing.T) {
	const limit = 4 << 20
	reader := newOversizedJSONReader(`{"message":"`, `"}`, limit+1024)
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/steer", reader)
	request.Header.Set("Content-Type", "application/json")

	var target struct {
		Message string `json:"message"`
	}
	if err := decodeOptionalJSON(request, &target); err == nil {
		t.Fatal("expected error for oversized optional JSON body")
	}
	if reader.readPos > limit+1 {
		t.Fatalf("read %d bytes, want at most %d", reader.readPos, limit+1)
	}
}

func TestTaskAndSessionHandlersReturn413OnOversizedJSONBody(t *testing.T) {
	server, err := NewServer(Config{Version: "test-version", DBPath: ":memory:", RuntimeRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	createProjReq := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Limit Test","kind":"pentest","scope":{"targets":["10.0.0.1"],"capabilities":["recon"]}}`))
	createProjReq.Header.Set("Content-Type", "application/json")
	createProjResp := httptest.NewRecorder()
	server.ServeHTTP(createProjResp, createProjReq)
	if createProjResp.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", createProjResp.Code, createProjResp.Body.String())
	}
	var proj struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createProjResp.Body).Decode(&proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	projectID := proj.ID
	oversized := `{"type":"pentest","goal":"` + strings.Repeat("a", (4<<20)+16) + `","runner":"sandbox"}`

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "create task",
			method: http.MethodPost,
			path:   "/api/projects/" + projectID + "/tasks",
			body:   oversized,
		},
		{
			name:   "create session",
			method: http.MethodPost,
			path:   "/api/sessions",
			body:   `{"input":"` + strings.Repeat("a", (4<<20)+16) + `"}`,
		},
		{
			name:   "session message",
			method: http.MethodPost,
			path:   "/api/sessions/session-1/messages",
			body:   `{"input":"` + strings.Repeat("a", (4<<20)+16) + `"}`,
		},
		{
			name:   "session queue steer",
			method: http.MethodPost,
			path:   "/api/sessions/session-1/steer/queue",
			body:   `{"input":"` + strings.Repeat("a", (4<<20)+16) + `"}`,
		},
		{
			name:   "session steer",
			method: http.MethodPost,
			path:   "/api/sessions/session-1/steer",
			body:   `{"message":"` + strings.Repeat("a", (4<<20)+16) + `"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
			}
		})
	}
}
