package daemon

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"pentest/internal/owner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/steering"
)

func (server *Server) handleSessionSteer(response http.ResponseWriter, request *http.Request) {
	var input sessionRuntimeInput
	var message string
	var uploads []uploadedAttachment
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		request.Body = http.MaxBytesReader(response, request.Body, maxTotalUploadBytes)
		parsed, foundUploads, err := parseCreateSessionRequest(request)
		if err != nil {
			writeError(response, http.StatusBadRequest, err.Error())
			return
		}
		input = sessionRuntimeInputFromCreate(parsed)
		message = strings.TrimSpace(parsed.value())
		uploads = foundUploads
	} else {
		var envelope struct {
			Message   string `json:"message"`
			Directive string `json:"directive"`
			sessionRuntimeInput
		}
		if err := decodeOptionalJSON(request, &envelope); err != nil {
			if errors.Is(err, errRequestBodyTooLarge) {
				writeError(response, http.StatusRequestEntityTooLarge, err.Error())
				return
			}
			writeError(response, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input = envelope.sessionRuntimeInput
		message = strings.TrimSpace(envelope.Message)
		if message == "" {
			message = strings.TrimSpace(envelope.Directive)
		}
	}
	if message == "" {
		writeError(response, http.StatusBadRequest, "steer message is required")
		return
	}
	id := request.PathValue("id")
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	requestID := sessionRequestID(request, "steer")
	adapter := sessionSteeringAdapter(server, id, server.sessionConclusionSettlementForID(id))
	clientSelectionIdentity := canonicalSteeringClientSelectionIdentity(input.ModelProviderID, input.selectedModel(), input.ReasoningEffort)
	replayIdentity := steeringReplayIdentity{
		requestID: requestID, operatorMessage: message,
		clientSelectionIdentity: clientSelectionIdentity,
		modelProviderID:         input.ModelProviderID, model: input.selectedModel(),
		requestedReasoningEffort: input.ReasoningEffort,
	}
	if record, replayed, replayErr := server.findAcceptedSteeringReplayByIdentity(adapter, replayIdentity); replayErr != nil || replayed {
		if replayErr != nil {
			var conflict *steeringRequestConflictError
			if errors.As(replayErr, &conflict) {
				writeError(response, http.StatusConflict, conflict.Error())
			} else {
				writeSessionError(response, replayErr)
			}
			return
		}
		server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
		return
	}
	provider, bound := server.sessionProviderSessions.get(id)
	active, activeErr := server.sessions.ActiveContinuation(id)
	if activeErr != nil {
		writeSessionError(response, activeErr)
		return
	}
	if !bound || active == nil || !server.sessionHarness.IsActive(id) {
		server.handleSessionMessageInput(response, request, found, createSessionInputFromRuntime(message, input), uploads, false)
		return
	}
	if conflict := sessionContinuationSelectionConflict(*active, input); conflict != "" {
		writeError(response, http.StatusConflict, conflict)
		return
	}
	selection, err := server.sessionCurrentSelection(*active)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if requestedProvider := strings.TrimSpace(input.ModelProviderID); requestedProvider != "" && requestedProvider != selection.ModelProviderID {
		if runtimeprofile.Provider(active.RuntimeProvider) != runtimeprofile.ProviderPi || !server.sessionPiProjectedProviderAllowed(id, requestedProvider) {
			writeError(response, http.StatusConflict, "Session Runtime model provider changes require a fresh continuation")
			return
		}
	}
	currentSelection := selection
	selection, err = sessionSelectionFromInput(selection, input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	prepared, err := prepareAcceptedNativeSteer(
		request.Context(), provider,
		providerSelectionRequiresReplacement(runtimeprofile.Provider(active.RuntimeProvider), currentSelection, selection),
		input.ForceReplace, adapter.settlement,
	)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	if nativeSteerOperation(provider, prepared.Mode) == nil {
		writeError(response, http.StatusConflict, "provider session does not support native steer")
		return
	}
	mode := prepared.Mode
	expectedProviderTurnID := prepared.ExpectedProviderTurnID
	releaseSteeringRequest := server.steering.AcquireRequest(owner.KindSession, id, requestID)
	defer releaseSteeringRequest()

	acceptInput := steering.AcceptRequest{
		RequestID:                requestID,
		ClientSelectionIdentity:  clientSelectionIdentity,
		OperatorMessage:          message,
		Message:                  message,
		Mode:                     owner.SteeringMode(mode),
		ModelProviderID:          selection.ModelProviderID,
		Model:                    selection.Model,
		RequestedReasoningEffort: selection.RequestedReasoningEffort,
		SessionID:                provider.SessionID(),
		ExpectedProviderTurnID:   expectedProviderTurnID,
	}
	if record, replayed, replayErr := server.findAcceptedSteeringReplay(adapter, acceptInput); replayErr != nil || replayed {
		if replayErr != nil {
			var conflict *steeringRequestConflictError
			if errors.As(replayErr, &conflict) {
				writeError(response, http.StatusConflict, conflict.Error())
			} else {
				writeSessionError(response, replayErr)
			}
			return
		}
		server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
		return
	}

	var turnSelectionConfig map[string]any
	if sessionInputChangesTurnSelection(input) {
		turnSelectionConfig, err = server.sessionTurnSelectionConfig(id, *active, selection)
		if err != nil {
			writeSessionError(response, err)
			return
		}
	}

	// Durable acceptance: the operator message, attachments, Turn Selection,
	// and dispatch record commit in one transaction before the request returns
	// 202. The accepted steering is dispatched by the owner-neutral FIFO queue after the
	// Working Graph settlement completes.
	accept := func(runtimeMessage string, preparedInput *session.PreparedConversationInput) (*owner.SteeringRecord, error) {
		acceptInput.Message = runtimeMessage
		record, _, acceptErr := server.acceptSteeringOrReplay(request.Context(), adapter, acceptInput, func(tx *sql.Tx) (string, error) {
			if preparedInput != nil {
				_, conversation, appendErr := preparedInput.AppendTx(tx)
				if appendErr != nil {
					return "", appendErr
				}
				if turnSelectionConfig != nil {
					if _, configErr := server.sessions.RecordRuntimeConfigTx(tx, id, active.RuntimeProfileID, turnSelectionConfig); configErr != nil {
						return "", configErr
					}
				}
				return conversation.ID, nil
			}
			payload := session.EventPayload{
				"role": "user", "text": message, "request_id": requestID,
				"delivery": "native_steer", "outcome": "pending", "mode": string(mode),
				"session_id":        provider.SessionID(),
				"model_provider_id": selection.ModelProviderID, "model": selection.Model,
				"requested_reasoning_effort": selection.RequestedReasoningEffort,
			}
			if expectedProviderTurnID != "" {
				payload["provider_turn_id"] = expectedProviderTurnID
			}
			if active != nil {
				payload["continuation_id"] = active.ID
			}
			event, eventErr := server.sessions.AppendEventTx(tx, id, session.EventKindConversation, payload)
			if eventErr != nil {
				return "", eventErr
			}
			if turnSelectionConfig != nil {
				if _, configErr := server.sessions.RecordRuntimeConfigTx(tx, id, active.RuntimeProfileID, turnSelectionConfig); configErr != nil {
					return "", configErr
				}
			}
			return event.ID, nil
		})
		return record, acceptErr
	}

	runtimeMessage := message
	if len(uploads) > 0 {
		input := sessionConversationInput(message, uploads)
		preparedInput, prepareErr := server.sessions.PrepareConversationInput(id, active.ID, input.Role, input.Text, input.Attachments)
		if prepareErr != nil {
			writeSessionError(response, prepareErr)
			return
		}
		defer preparedInput.Rollback()
		runtimeMessage = sessionGoalWithAttachmentReferences(message, found.Workdir, active.Runner, preparedInput.AttachmentReferences())
		record, err := accept(runtimeMessage, preparedInput)
		if err != nil {
			var conflict *steeringRequestConflictError
			if errors.As(err, &conflict) {
				writeError(response, http.StatusConflict, conflict.Error())
				return
			}
			writeSessionError(response, err)
			return
		}
		preparedInput.Commit()
		server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
		return
	}
	record, err := accept(runtimeMessage, nil)
	if err != nil {
		var conflict *steeringRequestConflictError
		if errors.As(err, &conflict) {
			writeError(response, http.StatusConflict, conflict.Error())
			return
		}
		writeSessionError(response, err)
		return
	}
	server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
}
