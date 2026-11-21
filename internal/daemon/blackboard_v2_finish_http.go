package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"pentest/internal/blackboardv2"
)

func (server *Server) handleBlackboardV2Finish(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, true)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
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
	// Finish body is empty; only the transport Idempotency-Key is required.
	if request.Body != nil {
		raw, readErr := io.ReadAll(io.LimitReader(request.Body, blackboardV2HTTPInputLimit+1))
		if readErr != nil {
			writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "read request body: "+readErr.Error(), "body"), nil)
			return
		}
		if len(raw) > blackboardV2HTTPInputLimit {
			writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "request body exceeds 4 MiB", "body"), nil)
			return
		}
		if len(strings.TrimSpace(string(raw))) != 0 {
			var empty map[string]json.RawMessage
			if err := json.Unmarshal(raw, &empty); err != nil || len(empty) != 0 {
				writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "Finish body must be empty or {}", "body"), nil)
				return
			}
		}
	}
	req := blackboardv2.FinishContinuationRequest{IdempotencyKey: idempotencyKey}
	// Initial live Finish may carry pending synchronization; exact Finish replay
	// redelivers the durable attachment via the request fingerprint contract.
	server.serveBlackboardV2(response, request, principal, false, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		return server.blackboardV2.FinishContinuation(ctx, principal.projectID, principal.continuationID, req)
	})
}

func (server *Server) serveBlackboardV2(response http.ResponseWriter, request *http.Request, principal blackboardV2Principal, requireLive, attachSync bool, action func(context.Context, blackboardv2.ContinuationAuthority) (any, error)) {
	server.serveBlackboardV2Result(response, request, principal, requireLive, attachSync, false, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		result, err := action(ctx, live)
		return result, 0, err
	})
}

// serveBlackboardV2Conditional serves Snapshot/detail with a revision ETag and
// honest If-None-Match behavior (200 body or 304 empty).
func (server *Server) serveBlackboardV2Conditional(response http.ResponseWriter, request *http.Request, principal blackboardV2Principal, requireLive, attachSync bool, action func(context.Context, blackboardv2.ContinuationAuthority) (any, int, error)) {
	server.serveBlackboardV2Result(response, request, principal, requireLive, attachSync, true, action)
}

// serveBlackboardV2Result authenticates Continuation binding, runs the action,
// and optionally attaches same-Project synchronization. requireLive gates
// offline read/current knowledge authority. Mutating tools that support exact
// replay pass requireLive=false so stored non-mutating replays reach the
// service after Finish/supersession; the service still rejects changed retries
// and new writes. attachSync uses CaptureTrustedSynchronization so Pending
// deliveries and exact response-loss replays of the same Idempotency-Key share
// one deterministic attachment, while ordinary later responses stay clean.
func (server *Server) serveBlackboardV2Result(response http.ResponseWriter, request *http.Request, principal blackboardV2Principal, requireLive, attachSync bool, conditional bool, action func(context.Context, blackboardv2.ContinuationAuthority) (any, int, error)) {
	ctx := request.Context()
	var authority blackboardv2.ContinuationAuthority
	if !principal.operator {
		binding, err := server.blackboardV2.AuthorizeContinuationBinding(ctx, principal.projectID, principal.taskID, principal.continuationID, requireLive)
		if err != nil {
			writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
			return
		}
		authority = binding
	}
	fingerprint := blackboardV2SyncFingerprint(request)
	// Claim the pending notice before the action so concurrent different
	// fingerprints cannot both deliver, and so Finish/action can absorb Pending
	// while still finalizing this request's durable receipt afterward.
	if !principal.operator && attachSync && fingerprint != "" && authority.Sync.Pending {
		if _, claimErr := server.blackboardV2.ClaimTrustedSynchronization(ctx, principal.projectID, principal.taskID, principal.continuationID, fingerprint, authority.Sync); claimErr != nil {
			writeBlackboardV2Error(response, asBlackboardV2Error(claimErr), nil)
			return
		}
	}
	result, revision, err := action(ctx, authority)
	if err != nil {
		var sync *blackboardv2.SynchronizationAttachment
		if !principal.operator && attachSync {
			if attachment, syncErr := server.blackboardV2.CaptureTrustedSynchronization(ctx, principal.projectID, principal.taskID, principal.continuationID, authority.Sync, authority.Live, fingerprint); syncErr == nil {
				sync = attachment
			}
		}
		writeBlackboardV2Error(response, asBlackboardV2Error(err), sync)
		return
	}
	var sync *blackboardv2.SynchronizationAttachment
	if !principal.operator && attachSync {
		attachment, syncErr := server.blackboardV2.CaptureTrustedSynchronization(ctx, principal.projectID, principal.taskID, principal.continuationID, authority.Sync, authority.Live, fingerprint)
		if syncErr != nil {
			writeBlackboardV2Error(response, asBlackboardV2Error(syncErr), nil)
			return
		}
		sync = attachment
		if sync != nil && revision == 0 {
			revision = sync.Revision
		}
	}
	if conditional {
		writeBlackboardV2ConditionalSuccess(response, request, revision, result, sync)
		return
	}
	writeBlackboardV2Success(response, result, sync)
}

// blackboardV2SyncFingerprint binds POST Idempotency-Key deliveries for exact
// response-loss replay. GET reads stay Pending-only (empty fingerprint).
func blackboardV2SyncFingerprint(request *http.Request) string {
	if request == nil || request.Method == http.MethodGet {
		return ""
	}
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		return ""
	}
	path := request.URL.Path
	switch {
	case strings.HasSuffix(path, "/blackboard/changes"):
		return blackboardv2.SynchronizationDeliveryFingerprint("change", key)
	case strings.HasSuffix(path, "/blackboard/evidence:retain"):
		return blackboardv2.SynchronizationDeliveryFingerprint("evidence", key)
	case strings.HasSuffix(path, ":checkpoint"):
		return blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", key)
	case strings.HasSuffix(path, "/continuation:finish"):
		return blackboardv2.SynchronizationDeliveryFingerprint("finish", key)
	default:
		return blackboardv2.SynchronizationDeliveryFingerprint("http:"+request.Method+":"+path, key)
	}
}
