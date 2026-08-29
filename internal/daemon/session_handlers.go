package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"pentest/internal/owner"
	"pentest/internal/session"
)

type createSessionInput struct {
	Input                    string                           `json:"input"`
	InitialInput             string                           `json:"initial_input"`
	Message                  string                           `json:"message"`
	Directive                string                           `json:"directive"`
	RuntimeProfileID         string                           `json:"runtime_profile_id"`
	Provider                 string                           `json:"provider"`
	RuntimeProvider          string                           `json:"runtime_provider"`
	ModelProviderID          string                           `json:"model_provider_id"`
	Model                    string                           `json:"model"`
	ModelOverride            string                           `json:"model_override"`
	ReasoningEffort          string                           `json:"reasoning_effort"`
	Runner                   string                           `json:"runner"`
	HostActivated            bool                             `json:"host_activated"`
	RuntimeConfig            map[string]any                   `json:"runtime_config"`
	BlackboardConclusionMode session.BlackboardConclusionMode `json:"blackboard_conclusion_mode"`
	RunControls              session.RunControls              `json:"run_controls"`
}

var errInvalidSessionBody = errors.New("invalid request body")

func (input createSessionInput) value() string {
	if strings.TrimSpace(input.Input) != "" {
		return input.Input
	}
	if strings.TrimSpace(input.Message) != "" {
		return input.Message
	}
	if strings.TrimSpace(input.Directive) != "" {
		return input.Directive
	}
	return input.InitialInput
}

func (input createSessionInput) selectedModel() string {
	if model := strings.TrimSpace(input.Model); model != "" {
		return model
	}
	return strings.TrimSpace(input.ModelOverride)
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
		input.Message = request.FormValue("message")
		input.Directive = request.FormValue("directive")
		input.RuntimeProfileID = request.FormValue("runtime_profile_id")
		input.Provider = request.FormValue("provider")
		input.RuntimeProvider = request.FormValue("runtime_provider")
		input.ModelProviderID = request.FormValue("model_provider_id")
		input.Model = request.FormValue("model")
		input.ModelOverride = request.FormValue("model_override")
		input.ReasoningEffort = request.FormValue("reasoning_effort")
		input.Runner = request.FormValue("runner")
		input.HostActivated = strings.EqualFold(request.FormValue("host_activated"), "true")
		input.BlackboardConclusionMode = session.BlackboardConclusionMode(request.FormValue("blackboard_conclusion_mode"))
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
	mode := input.BlackboardConclusionMode
	if mode == "" {
		mode = input.RunControls.BlackboardConclusionMode
	}
	runtimeInput := sessionRuntimeInputFromCreate(input)
	prepared, err := server.prepareSessionRuntime(request.Context(), mode, runtimeInput, nil)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	created, err := server.sessions.Create(session.CreateRequest{Input: input.value(), Attachments: attachments, BlackboardConclusionMode: mode})
	if err != nil {
		writeSessionError(response, err)
		return
	}
	initialLaunch := resolveOwnerBlackboardRuntimeLaunch(
		input.value(), created.RunControls.BlackboardConclusionMode == session.BlackboardConclusionModeDisabled,
	)
	if _, launchErr := server.startPreparedSessionRuntimeForBlackboardProjection(
		request.Context(), created, initialLaunch.goal, runtimeInput, nil, prepared, nil, initialLaunch.projection,
	); launchErr != nil {
		server.recordSessionLaunchDiagnostic(created.ID, "launch_failed", launchErr)
		writeSessionError(response, launchErr)
		return
	}
	server.writeDecoratedSession(response, http.StatusCreated, created.ID)
}

func (server *Server) handleListSessions(response http.ResponseWriter, request *http.Request) {
	lifecycle := session.Lifecycle(request.URL.Query().Get("lifecycle"))
	if request.URL.Path == "/api/sessions/archived" {
		lifecycle = session.LifecycleArchived
	}
	limit := 0
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			writeError(response, http.StatusBadRequest, session.ErrInvalidLimit.Error())
			return
		}
		limit = parsed
	}
	found, err := server.sessions.ListLimited(lifecycle, limit)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if limit > 0 && server.sessionProviderSessions != nil {
		wantedLifecycle := lifecycle
		if wantedLifecycle == "" {
			wantedLifecycle = session.LifecycleOpen
		}
		seen := make(map[string]bool, len(found))
		for _, item := range found {
			seen[item.ID] = true
		}
		for _, id := range server.sessionProviderSessions.busyOwnerIDs() {
			if seen[id] {
				continue
			}
			item, getErr := server.sessions.Get(id)
			if getErr == nil && item.Lifecycle == wantedLifecycle {
				found = append(found, item)
			}
		}
	}
	decorated := make([]session.Session, 0, len(found))
	for _, item := range found {
		projected, decorateErr := server.decorateSession(item)
		if decorateErr != nil {
			writeSessionError(response, decorateErr)
			return
		}
		decorated = append(decorated, projected)
	}
	if limit > 0 {
		sort.SliceStable(decorated, func(i, j int) bool {
			iBusy := decorated[i].RuntimeActivity.TurnActivity == runtimeTurnBusy
			jBusy := decorated[j].RuntimeActivity.TurnActivity == runtimeTurnBusy
			if iBusy != jBusy {
				return iBusy
			}
			if !decorated[i].LastActivityAt.Equal(decorated[j].LastActivityAt) {
				return decorated[i].LastActivityAt.After(decorated[j].LastActivityAt)
			}
			return decorated[i].ID < decorated[j].ID
		})
		if len(decorated) > limit {
			decorated = decorated[:limit]
		}
	}
	writeJSON(response, http.StatusOK, struct {
		Sessions []session.Session `json:"sessions"`
	}{Sessions: decorated})
}

func (server *Server) handleGetSession(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	decorated, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, decorated)
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
	decorated, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, decorated)
}

func (server *Server) handleArchiveSession(response http.ResponseWriter, request *http.Request) {
	if err := server.stopSessionRuntime(request.PathValue("id")); err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	found, err := server.sessions.Archive(request.PathValue("id"))
	if err == nil {
		server.settleSessionAcceptedSteering(found.ID, owner.SteeringReasonOwnerArchived, "Session archived with queued accepted steering")
	}
	if err != nil {
		writeSessionError(response, err)
		return
	}
	decorated, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, decorated)
}

func (server *Server) handleRestoreSession(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Restore(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	decorated, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, decorated)
}

func (server *Server) handleDeleteSession(response http.ResponseWriter, request *http.Request) {
	if err := server.stopSessionRuntime(request.PathValue("id")); err != nil && !errors.Is(err, session.ErrNotFound) {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
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
	case errors.Is(err, session.ErrMissingInput), errors.Is(err, session.ErrMissingTitle), errors.Is(err, session.ErrInvalidLifecycle), errors.Is(err, session.ErrInvalidAttachment), errors.Is(err, session.ErrInvalidRunner), errors.Is(err, session.ErrMissingRuntimeProfile), errors.Is(err, session.ErrInvalidBlackboardConclusionMode):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, session.ErrContinuationNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, session.ErrAlreadyArchived), errors.Is(err, session.ErrNotArchived), errors.Is(err, session.ErrOpenSession), errors.Is(err, session.ErrSessionNotOpen), errors.Is(err, session.ErrActiveContinuation), errors.Is(err, session.ErrContinuationStatusConflict):
		writeError(response, http.StatusConflict, err.Error())
	default:
		// Local-first: surface launch/runtime failures (podman create, preflight,
		// image pull) so the operator can fix Desktop mounts and CLI setup.
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "session operation failed"
		}
		status := http.StatusInternalServerError
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "preflight") ||
			strings.Contains(lower, "sandbox") ||
			strings.Contains(lower, "container") ||
			strings.Contains(lower, "podman") ||
			strings.Contains(lower, "docker") ||
			strings.Contains(lower, "image") ||
			strings.Contains(lower, "mount") ||
			strings.Contains(lower, "network") {
			status = http.StatusBadRequest
		}
		writeError(response, status, msg)
	}
}
