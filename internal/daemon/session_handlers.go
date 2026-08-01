package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"pentest/internal/session"
)

type createSessionInput struct {
	Input        string `json:"input"`
	InitialInput string `json:"initial_input"`
}

var errInvalidSessionBody = errors.New("invalid request body")

func (input createSessionInput) value() string {
	if strings.TrimSpace(input.Input) != "" {
		return input.Input
	}
	return input.InitialInput
}

// parseCreateSessionRequest accepts the JSON surface and the established
// multipart payload/attachments shape used by Task launch.
func parseCreateSessionRequest(request *http.Request) (createSessionInput, []uploadedAttachment, error) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		return parseMultipartCreateSessionRequest(request)
	}
	var input createSessionInput
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		return createSessionInput{}, nil, errInvalidSessionBody
	}
	return input, nil, nil
}

func parseMultipartCreateSessionRequest(request *http.Request) (createSessionInput, []uploadedAttachment, error) {
	if err := request.ParseMultipartForm(32 << 20); err != nil {
		return createSessionInput{}, nil, errInvalidSessionBody
	}
	payload := strings.TrimSpace(request.FormValue("payload"))
	var input createSessionInput
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &input); err != nil {
			return createSessionInput{}, nil, errInvalidSessionBody
		}
	} else {
		input.Input = request.FormValue("input")
		input.InitialInput = request.FormValue("initial_input")
	}
	var headers []*multipart.FileHeader
	if request.MultipartForm != nil {
		headers = request.MultipartForm.File["attachments"]
	}
	if len(headers) > session.MaxAttachmentCount {
		return createSessionInput{}, nil, fmt.Errorf("too many attachments (max %d)", session.MaxAttachmentCount)
	}
	attachments := make([]uploadedAttachment, 0, len(headers))
	for _, header := range headers {
		if header.Size > session.MaxAttachmentFileBytes {
			return createSessionInput{}, nil, fmt.Errorf("attachment %q exceeds the %d MiB limit", header.Filename, session.MaxAttachmentFileBytes>>20)
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

func (server *Server) handleCreateSession(response http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		request.Body = http.MaxBytesReader(response, request.Body, maxTotalUploadBytes)
	}
	input, uploads, err := parseCreateSessionRequest(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	attachments := make([]session.Attachment, 0, len(uploads))
	for _, upload := range uploads {
		attachments = append(attachments, session.Attachment{Name: upload.filename, Size: upload.size, Open: upload.open})
	}
	created, err := server.sessions.Create(session.CreateRequest{Input: input.value(), Attachments: attachments})
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, created)
}

func (server *Server) handleListSessions(response http.ResponseWriter, request *http.Request) {
	lifecycle := session.Lifecycle(request.URL.Query().Get("lifecycle"))
	if request.URL.Path == "/api/sessions/archived" {
		lifecycle = session.LifecycleArchived
	}
	found, err := server.sessions.List(lifecycle)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Sessions []session.Session `json:"sessions"`
	}{Sessions: found})
}

func (server *Server) handleGetSession(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, found)
}

func (server *Server) handleSessionEvents(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Events(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Events []session.Event `json:"events"`
	}{Events: found})
}

func (server *Server) handleRenameSession(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(input.Title) == "" {
		input.Title = input.Name
	}
	found, err := server.sessions.Rename(request.PathValue("id"), input.Title)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, found)
}

func (server *Server) handleArchiveSession(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Archive(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, found)
}

func (server *Server) handleRestoreSession(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Restore(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, found)
}

func (server *Server) handleDeleteSession(response http.ResponseWriter, request *http.Request) {
	if err := server.sessions.Delete(request.PathValue("id")); err != nil {
		writeSessionError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func writeSessionError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		writeError(response, http.StatusNotFound, session.ErrNotFound.Error())
	case errors.Is(err, session.ErrMissingInput), errors.Is(err, session.ErrMissingTitle), errors.Is(err, session.ErrInvalidLifecycle), errors.Is(err, session.ErrInvalidAttachment):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, session.ErrAlreadyArchived), errors.Is(err, session.ErrNotArchived), errors.Is(err, session.ErrOpenSession):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "session operation failed")
	}
}
