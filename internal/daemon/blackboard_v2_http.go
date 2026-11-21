package daemon

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2input"
	"pentest/internal/projectinterface"
)

const blackboardV2HTTPInputLimit = 4 << 20

type blackboardV2Principal struct {
	operator       bool
	actorID        string
	projectID      string
	sessionID      string
	taskID         string
	continuationID string
	grant          projectinterface.Grant
}

func requireBlackboardV2WriteAccess(principal blackboardV2Principal) *blackboardv2.Error {
	if principal.operator || principal.grant.Access == projectinterface.GrantAccessFull {
		return nil
	}
	return blackboardV2HTTPError("authority_denied", "Continuation Interface capability is read-only", "authority")
}

func (server *Server) registerBlackboardV2Routes() {
	if server.blackboardV2 == nil {
		return
	}
	server.mux.HandleFunc("POST /api/v2/projects/{id}/blackboard/changes", server.handleBlackboardV2Change)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/blackboard/snapshot", server.handleBlackboardV2Snapshot)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/blackboard/health", server.handleBlackboardV2Health)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/blackboard/records/{key}", server.handleBlackboardV2Read)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/blackboard/records/{key}/history", server.handleBlackboardV2History)
	server.mux.HandleFunc("POST /api/v2/projects/{id}/blackboard/evidence:retain", server.handleBlackboardV2EvidenceRetain)
	server.mux.HandleFunc("POST /api/v2/projects/{id}/tasks/{task_id}/blackboard/evidence:retain", server.handleDisabledTaskOutputEvidenceRetain)
	// net/http patterns cannot suffix a wildcard with ":checkpoint". Capture the
	// final segment and validate/remove that exact suffix in the handler.
	server.mux.HandleFunc("POST /api/v2/projects/{id}/blackboard/attempts/{attempt_action}", server.handleBlackboardV2Checkpoint)
	server.mux.HandleFunc("POST /api/v2/projects/{id}/continuation:finish", server.handleBlackboardV2Finish)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/reports/pentest", server.handleBlackboardV2PentestReport)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/reports/ctf-solution", server.handleBlackboardV2CTFSolution)
	server.mux.HandleFunc("POST /api/v2/sessions/{id}/blackboard/changes", server.handleSessionBlackboardV2Change)
	server.mux.HandleFunc("GET /api/v2/sessions/{id}/blackboard/snapshot", server.handleSessionBlackboardV2Snapshot)
	server.mux.HandleFunc("GET /api/v2/sessions/{id}/blackboard/health", server.handleSessionBlackboardV2Health)
	server.mux.HandleFunc("GET /api/v2/sessions/{id}/blackboard/records/{key}", server.handleSessionBlackboardV2Read)
	server.mux.HandleFunc("GET /api/v2/sessions/{id}/blackboard/records/{key}/history", server.handleSessionBlackboardV2History)
	server.mux.HandleFunc("POST /api/v2/sessions/{id}/blackboard/attempts/{attempt_action}", server.handleSessionBlackboardV2Checkpoint)
	server.mux.HandleFunc("POST /api/v2/sessions/{id}/continuation:finish", server.handleSessionBlackboardV2Finish)
}

func isBlackboardV2HTTPTransport(request *http.Request) bool {
	return strings.HasPrefix(request.URL.Path, "/api/v2/projects/") || strings.HasPrefix(request.URL.Path, "/api/v2/sessions/")
}

func (server *Server) authenticateBlackboardV2(request *http.Request, requireContinuation bool) (blackboardV2Principal, *blackboardv2.Error) {
	if request.URL.Query().Get("token") != "" {
		return blackboardV2Principal{}, blackboardV2HTTPError("invalid_schema", "v2 does not accept bearer credentials in query strings", "authorization")
	}
	projectID := strings.TrimSpace(request.PathValue("id"))
	if projectID == "" {
		return blackboardV2Principal{}, blackboardV2HTTPError("invalid_schema", "project id is required", "path.project_id")
	}
	token := projectinterface.BearerToken(request)
	operatorRequest := server.operatorRequest(request)
	if operatorRequest {
		if requireContinuation {
			return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "this Blackboard capability requires a trusted Continuation", "authority")
		}
		actorID := strings.TrimSpace(request.Header.Get(projectinterface.OperatorActorHeader))
		if actorID == "" {
			actorID = "local-operator"
		}
		return blackboardV2Principal{operator: true, actorID: actorID, projectID: projectID}, nil
	}
	if token == "" {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "Continuation Interface capability is required", "authorization")
	}
	if server.projectInterfaceGrants == nil {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "Continuation Interface capability is unavailable", "authorization")
	}
	grant, err := server.projectInterfaceGrants.Resolve(request.Context(), token)
	if err != nil {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "Continuation Interface capability is invalid", "authorization")
	}
	if !grant.Status().IsReadable() {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "Continuation Interface capability is revoked", "authorization")
	}
	if grant.Owner.ProjectID != projectID {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "declared Project does not match Continuation Interface capability", "path.project_id")
	}
	return blackboardV2Principal{
		projectID: grant.Owner.ProjectID, taskID: grant.Owner.TaskID, continuationID: grant.ContinuationID, grant: grant,
	}, nil
}

func (server *Server) handleBlackboardV2Change(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	if !server.allowLegacyGraphWrite(response, principal) {
		return
	}

	if accessErr := requireBlackboardV2WriteAccess(principal); accessErr != nil {
		writeBlackboardV2Error(response, accessErr, nil)
		return
	}
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	contractRaw, decodeErr := prepareBlackboardV2HTTPContractJSON(request, map[string]string{"idempotency_key": idempotencyKey})
	if decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	var batch blackboardv2.ChangeBatch
	// Exact replay remains available after Finish/supersession; only live
	// Continuations may attach synchronization.
	server.serveBlackboardV2(response, request, principal, false, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		if contractErr := blackboardv2input.DecodeContractInput("changeBatch", false, contractRaw, &batch); contractErr != nil {
			return nil, contractErr
		}
		if principal.operator {
			return server.blackboardV2.ApplyForOperator(ctx, principal.projectID, principal.actorID, batch)
		}
		return server.blackboardV2.ApplyForContinuation(ctx, principal.projectID, principal.continuationID, batch)
	})
}

func (server *Server) handleBlackboardV2Snapshot(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	// Live read/current knowledge authority only; closed Continuations keep
	// exact write/finish replay but not current knowledge reads.
	server.serveBlackboardV2Conditional(response, request, principal, true, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		projection, err := server.blackboardV2.ProjectRuntimeSnapshot(ctx, principal.projectID)
		if err != nil {
			return nil, 0, err
		}
		return projection.Snapshot, projection.Snapshot.Revision, nil
	})
}

func (server *Server) handleBlackboardV2Health(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	// Operator/UI semantic health is a live current-state read, same authority
	// model as Snapshot/detail. Closed Continuations lose live read access.
	server.serveBlackboardV2Conditional(response, request, principal, true, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		health, err := server.blackboardV2.ProjectSemanticHealth(ctx, principal.projectID)
		if err != nil {
			return nil, 0, err
		}
		return health, health.Revision, nil
	})
}

func (server *Server) handleBlackboardV2Read(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	key := request.PathValue("key")
	server.serveBlackboardV2Conditional(response, request, principal, true, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		detail, err := server.blackboardV2.ReadCurrent(ctx, principal.projectID, key)
		if err != nil {
			return nil, 0, err
		}
		return detail, detail.Revision, nil
	})
}

func (server *Server) handleBlackboardV2History(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	key := request.PathValue("key")
	limit := 0
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "limit must be an integer", "limit"), nil)
			return
		}
		limit = parsed
	}
	options := blackboardv2.HistoryOptions{Cursor: request.URL.Query().Get("cursor"), Limit: limit}
	server.serveBlackboardV2(response, request, principal, true, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		return server.blackboardV2.ReadHistory(ctx, principal.projectID, key, options)
	})
}
