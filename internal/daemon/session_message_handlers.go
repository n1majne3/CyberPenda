package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"pentest/internal/runtime"
	"pentest/internal/session"
)

func (server *Server) handleSessionMessage(response http.ResponseWriter, request *http.Request) {
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
	server.handleSessionMessageInput(response, request, found, input, uploads)
}

func (server *Server) handleSessionMessageInput(response http.ResponseWriter, request *http.Request, found session.Session, input createSessionInput, uploads []uploadedAttachment) {
	id := found.ID
	if strings.TrimSpace(input.RuntimeProfileID) != "" {
		writeError(response, http.StatusBadRequest, "runtime_profile_locked")
		return
	}
	if found.Lifecycle != session.LifecycleOpen {
		writeSessionError(response, session.ErrSessionNotOpen)
		return
	}
	latest, err := server.sessions.LatestContinuation(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if latest != nil {
		if _, supported := server.runtimePlugins.Get(latest.RuntimeProvider); !supported {
			writeError(response, http.StatusBadRequest, fmt.Sprintf("runtime %q is not supported", latest.RuntimeProvider))
			return
		}
	}
	if err := server.waitForSessionWorkingGraphSettlement(request.Context(), id, false); err != nil {
		if errors.Is(err, errSemanticConclusionActionRequired) {
			writeError(response, http.StatusConflict, "semantic_conclusion_action_required")
			return
		}
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	message := strings.TrimSpace(input.value())
	if message == "" {
		writeError(response, http.StatusBadRequest, "Session message is required")
		return
	}
	active, activeErr := server.sessions.ActiveContinuation(id)
	if activeErr != nil {
		writeSessionError(response, activeErr)
		return
	}
	if active != nil {
		if conflict := sessionContinuationSelectionConflict(*active, sessionRuntimeInputFromCreate(input)); conflict != "" {
			writeError(response, http.StatusConflict, conflict)
			return
		}
		if provider, bound := server.sessionProviderSessions.get(id); bound && server.sessionHarness.IsActive(id) {
			selection, selectionErr := server.sessionCurrentSelection(*active)
			if selectionErr != nil {
				writeSessionError(response, selectionErr)
				return
			}
			selection.TurnKind = runtime.RuntimeTurnKindWork
			runtimeInput := sessionRuntimeInputFromCreate(input)
			if requestedProvider := strings.TrimSpace(runtimeInput.ModelProviderID); requestedProvider != "" && requestedProvider != selection.ModelProviderID {
				writeError(response, http.StatusConflict, "Session Runtime model provider changes require a fresh continuation")
				return
			}
			if runtimeInput.ModelProviderID != "" || runtimeInput.selectedModel() != "" || runtimeInput.ReasoningEffort != "" {
				selection, selectionErr = sessionSelectionFromInput(selection, runtimeInput)
				if selectionErr != nil {
					writeError(response, http.StatusBadRequest, selectionErr.Error())
					return
				}
			}
			if sessionInputChangesTurnSelection(runtimeInput) {
				if err := server.recordSessionTurnSelection(id, *active, selection); err != nil {
					writeSessionError(response, err)
					return
				}
			}
			requestID := sessionRequestID(request, "message")
			reservation, reserved := server.reserveSessionProviderTurn(id, provider, active.ID, requestID, runtime.ProviderSessionModeSendTurn)
			if !reserved {
				writeError(response, http.StatusConflict, "Session Runtime control operation is unavailable")
				return
			}
			uploadedEvents, _, uploadErr := server.appendSessionConversationInput(id, active.ID, message, uploads)
			if uploadErr != nil {
				reservation.cancel()
				writeSessionError(response, uploadErr)
				return
			}
			runtimeMessage := sessionGoalWithAttachmentEvents(message, found.Workdir, active.Runner, uploadedEvents)
			reservation.commit(runtimeMessage, selection)
			server.writeDecoratedSession(response, http.StatusAccepted, id)
			return
		}
		if err := server.stopSessionRuntime(id); err != nil {
			writeError(response, http.StatusConflict, err.Error())
			return
		}
	}
	conversationInput := sessionConversationInput(message, uploads)
	_, launchErr := server.startSessionRuntimeWithConversationInput(request.Context(), found, message, sessionRuntimeInputFromCreate(input), &conversationInput)
	var commitErr sessionConversationInputCommitError
	if errors.As(launchErr, &commitErr) {
		writeSessionError(response, commitErr.cause)
		return
	}
	if launchErr != nil {
		server.writeDecoratedSession(response, http.StatusAccepted, id)
		return
	}
	server.writeDecoratedSession(response, http.StatusAccepted, id)
}
