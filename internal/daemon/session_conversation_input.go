package daemon

import (
	"net/http"
	"strings"

	"pentest/internal/session"
)

func sessionConversationInput(message string, uploads []uploadedAttachment) session.ConversationInput {
	attachments := make([]session.Attachment, 0, len(uploads))
	for _, upload := range uploads {
		attachments = append(attachments, session.Attachment{Name: upload.filename, Size: upload.size, Open: upload.open})
	}
	return session.ConversationInput{Role: "user", Text: message, Attachments: attachments}
}

func (server *Server) appendSessionConversationInput(id, continuationID, message string, uploads []uploadedAttachment) ([]session.Event, session.Event, error) {
	input := sessionConversationInput(message, uploads)
	return server.sessions.AppendConversationInput(id, continuationID, input.Role, input.Text, input.Attachments)
}

func createSessionInputFromRuntime(message string, input sessionRuntimeInput) createSessionInput {
	return createSessionInput{
		Input: message, RuntimeProfileID: input.RuntimeProfileID, Provider: input.Provider,
		RuntimeProvider: input.RuntimeProvider, ModelProviderID: input.ModelProviderID,
		Model: input.Model, ModelOverride: input.ModelOverride, ReasoningEffort: input.ReasoningEffort,
		Runner: input.Runner, HostActivated: input.HostActivated, RuntimeConfig: input.RuntimeConfig,
	}
}

func (server *Server) handleSessionQueueSteer(response http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		request.Body = http.MaxBytesReader(response, request.Body, maxTotalUploadBytes)
	}
	input, uploads, err := parseCreateSessionRequest(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	id := request.PathValue("id")
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	// Queue-steering a Session is a directive, not a plain message: record the
	// same steering event tasks emit so the timeline and transcript surface it
	// identically for both owner kinds. The send below stays the Session queue
	// path.
	if directive := strings.TrimSpace(input.value()); directive != "" {
		payload := session.EventPayload{
			"directive": directive,
			"phase":     "steering_requested",
			"mode":      "queue",
		}
		if _, err := server.sessions.AppendEvent(id, session.EventKindSteering, payload); err != nil {
			writeSessionError(response, err)
			return
		}
	}
	server.handleSessionMessageInput(response, request, found, input, uploads)
}

// writeDecoratedSessionWithSteeringOutcome returns the decorated Session with
// the durable Accepted Steering outcome of the repeated request identity.
func (server *Server) writeDecoratedSessionWithSteeringOutcome(response http.ResponseWriter, status int, id, outcome string) {
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	decorated, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, status, struct {
		session.Session
		AcceptedSteeringOutcome string `json:"accepted_steering_outcome,omitempty"`
	}{Session: decorated, AcceptedSteeringOutcome: outcome})
}

func (server *Server) writeDecoratedSession(response http.ResponseWriter, status int, id string) {
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	decorated, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, status, decorated)
}
