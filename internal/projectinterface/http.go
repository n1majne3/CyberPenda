package projectinterface

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"pentest/internal/blackboard"
)

// HTTPHandler is the grant-authed transport adapter for the project-interface
// capabilities (runtime protocol §12.4). It extracts the Continuation Interface
// Grant bearer token and path Project, authenticates them through the Service,
// and maps every failure to a ProjectInterfaceErrorV1 response. Operator/UI
// traffic stays on the daemon bearer credential and does not reach this handler.
type HTTPHandler struct {
	service                      *Service
	operatorToken                string
	allowUnauthenticatedOperator bool
}

// OperatorActorHeader carries the stable local operator identity on daemon-
// authenticated project-interface requests. Runtime requests must omit it.
const OperatorActorHeader = "CyberPenda-Actor-ID"

// NewHTTPHandler wires a grant-authed HTTP adapter.
func NewHTTPHandler(service *Service) *HTTPHandler {
	return &HTTPHandler{service: service}
}

// WithOperatorAuth enables operator HTTP mode behind the daemon credential.
// allowUnauthenticated is used only by the daemon's loopback no-token mode.
func (h *HTTPHandler) WithOperatorAuth(daemonToken string, allowUnauthenticated bool) *HTTPHandler {
	h.operatorToken = strings.TrimSpace(daemonToken)
	h.allowUnauthenticatedOperator = allowUnauthenticated
	return h
}

// BearerToken extracts the Continuation Interface Grant token from an
// Authorization: Bearer header or a ?token= query fallback (runtime protocol
// §4.2 permits the query form only as a Continuation-scoped transport fallback
// for MCP transports that cannot attach headers). It returns "" when no token
// is present.
func BearerToken(request *http.Request) string {
	if header := strings.TrimSpace(request.Header.Get("Authorization")); header != "" {
		if scheme, token, ok := strings.Cut(header, " "); ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(token)
		}
	}
	if token := request.URL.Query().Get("token"); token != "" {
		return token
	}
	return ""
}

// AuthenticateRequest extracts the grant token and path Project and resolves
// them to a Principal. It returns a project-interface Error so the caller can
// write the structured response without reclassification.
func (h *HTTPHandler) AuthenticateRequest(request *http.Request) (Principal, *Error) {
	token := BearerToken(request)
	operatorRequest := token == "" && h.allowUnauthenticatedOperator
	if token != "" && h.operatorToken != "" &&
		subtle.ConstantTimeCompare([]byte(token), []byte(h.operatorToken)) == 1 {
		operatorRequest = true
	}
	if operatorRequest {
		actorID := strings.TrimSpace(request.Header.Get(OperatorActorHeader))
		if actorID == "" {
			actorID = "local-operator"
		}
		principal, err := OperatorPrincipal(request.PathValue("id"), actorID)
		if err != nil {
			return Principal{}, AsError(err)
		}
		return principal, nil
	}
	if token == "" {
		return Principal{}, ValidationError(ErrCodeGrantNotFound, "continuation interface grant token is required", "authorization")
	}
	principal, err := h.service.Authenticate(request.Context(), token, request.PathValue("id"))
	if err != nil {
		return Principal{}, AsError(err)
	}
	return principal, nil
}

// Apply handles POST /api/projects/{project_id}/blackboard/mutations.
func (h *HTTPHandler) Apply(response http.ResponseWriter, request *http.Request) {
	principal, authErr := h.AuthenticateRequest(request)
	if authErr != nil {
		writeProjectInterfaceError(response, authErr)
		return
	}
	var req ApplyMutationRequest
	if decodeErr := decodeStrictJSON(request, &req); decodeErr != nil {
		writeProjectInterfaceError(response, &Error{Code: ErrCodeInvalidRequest, Message: decodeErr.Error(), Path: "body", Retryable: false})
		return
	}
	result, err := h.service.Apply(request.Context(), principal, req)
	if err != nil {
		writeProjectInterfaceError(response, AsError(err))
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, result)
}

// ResolveRecords handles POST /api/projects/{project_id}/blackboard/records:resolve.
func (h *HTTPHandler) ResolveRecords(response http.ResponseWriter, request *http.Request) {
	principal, authErr := h.AuthenticateRequest(request)
	if authErr != nil {
		writeProjectInterfaceError(response, authErr)
		return
	}
	var req ResolveRecordsRequest
	if decodeErr := decodeStrictJSON(request, &req); decodeErr != nil {
		writeProjectInterfaceError(response, &Error{Code: ErrCodeInvalidRequest, Message: decodeErr.Error(), Path: "body", Retryable: false})
		return
	}
	result, err := h.service.ResolveRecords(request.Context(), principal, req)
	if err != nil {
		writeProjectInterfaceError(response, AsError(err))
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, result)
}

// CurrentGraph handles GET /api/projects/{project_id}/blackboard/runtime-graph.
// It honors If-None-Match against the projection hash with a 304 response.
func (h *HTTPHandler) CurrentGraph(response http.ResponseWriter, request *http.Request) {
	principal, authErr := h.AuthenticateRequest(request)
	if authErr != nil {
		writeProjectInterfaceError(response, authErr)
		return
	}
	result, err := h.service.CurrentGraph(request.Context(), principal, CurrentGraphRequest{
		ProtocolVersion: RuntimeProtocolVersion,
	})
	if err != nil {
		writeProjectInterfaceError(response, AsError(err))
		return
	}
	etag := `"` + result.Result.ProjectionHash + `"`
	response.Header().Set("ETag", etag)
	response.Header().Set("Cache-Control", "private, no-cache")
	if match := request.Header.Get("If-None-Match"); match == etag || match == "*" {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

// decodeStrictJSON decodes one JSON object into target with unknown-field
// rejection so a Runtime request cannot smuggle provenance or Project fields
// past the structural envelope (runtime protocol §4.1, §3 versioning).
func decodeStrictJSON(request *http.Request, target any) error {
	if request.Body == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty")
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

// writeProjectInterfaceError maps an Error to its HTTP status and writes the
// ProjectInterfaceErrorV1 envelope (runtime protocol §3, §12.4). A nil or
// non-Error err is an unexpected internal failure and maps to 500, not 400.
func writeProjectInterfaceError(response http.ResponseWriter, err *Error) {
	if err == nil {
		err = InternalError("unexpected failure")
	}
	if err.ProtocolVersion == 0 {
		err.ProtocolVersion = RuntimeProtocolVersion
	}
	if err.RequestID == "" {
		err.RequestID = newRequestID()
	}
	type errorEnvelope struct {
		Error Error `json:"error"`
	}
	status := httpStatusFor(err)
	response.Header().Set("Cache-Control", "no-store")
	if status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests {
		response.Header().Set("Retry-After", "1")
	}
	writeJSON(response, status, errorEnvelope{Error: *err})
}

// httpStatusFor selects the HTTP status for a ProjectInterfaceErrorV1. Revoked
// grants map to 403 (every use rejected) while finish/terminal map to 409 (new
// writes rejected, reads/replay remain) (runtime protocol §12.4).
func httpStatusFor(err *Error) int {
	switch err.Code {
	case ErrCodeGrantNotFound:
		return http.StatusUnauthorized
	case ErrCodeProjectMismatch, ErrCodeActorForbidden, ErrCodeSourceEventMismatch:
		return http.StatusForbidden
	case ErrCodeProjectNotFound, ErrCodeSourceEventNotFound:
		return http.StatusNotFound
	case ErrCodeContinuationClosed:
		if status, _ := err.Details["grant_status"].(string); status == string(GrantStatusRevoked) {
			return http.StatusForbidden
		}
		return http.StatusConflict
	case blackboard.ErrCodeVersionConflict,
		blackboard.ErrCodeIdempotencyConflict,
		blackboard.ErrCodeNodeKeyConflict,
		blackboard.ErrCodeMergeConflict,
		blackboard.ErrCodeTransitionGuardFailed,
		blackboard.ErrCodeInvalidTransition,
		blackboard.ErrCodeArchiveGuardFailed:
		return http.StatusConflict
	case blackboard.ErrCodeNodeNotFound,
		blackboard.ErrCodeEdgeEndpointNotFound:
		return http.StatusNotFound
	case blackboard.ErrCodeSelfEdgeForbidden,
		blackboard.ErrCodeGraphCycle,
		blackboard.ErrCodeEdgeEndpointType,
		blackboard.ErrCodeUnknownNodeType,
		blackboard.ErrCodeUnknownEdgeType,
		blackboard.ErrCodeUnknownProperty,
		blackboard.ErrCodeMissingProperty,
		blackboard.ErrCodeInvalidProperty,
		blackboard.ErrCodeImmutableField,
		blackboard.ErrCodeInvariantViolation,
		blackboard.ErrCodeProjectKindViolation,
		blackboard.ErrCodeUnsupportedSchemaVersion,
		blackboard.ErrCodeProvenanceRequired:
		return http.StatusUnprocessableEntity
	case ErrCodeStorageBusy:
		return http.StatusServiceUnavailable
	case ErrCodeInternal:
		return http.StatusInternalServerError
	}
	if err.Retryable {
		return http.StatusServiceUnavailable
	}
	// invalid_request, provenance_spoofed, and unmapped structural codes.
	return http.StatusBadRequest
}

// newRequestID returns an opaque per-request identifier for the error envelope
// (runtime protocol §3). It is not correlated with any other identifier.
func newRequestID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "req"
	}
	return hex.EncodeToString(buf[:])
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
