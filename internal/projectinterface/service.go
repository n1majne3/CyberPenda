package projectinterface

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/blackboard"
	"pentest/internal/store"
)

// Deps are the domain services the project-interface module orchestrates. The
// module never writes graph tables directly and never duplicates graph or read
// logic: it binds trusted context, authorizes, and maps errors (runtime
// protocol §1, deletion test).
type Deps struct {
	DB     *store.DB
	Graph  *blackboard.GraphService
	Grants *GrantStore
}

// Service is the transport-neutral owner of the six Runtime capabilities. I01
// implements the first three: Apply Mutation, Resolve Records, and Current
// Runtime Graph. Retain Evidence (I03), Checkpoint Attempt (I04), and Finish
// Continuation (I04) land in later slices.
type Service struct {
	db     *store.DB
	graph  *blackboard.GraphService
	grants *GrantStore
}

// NewService wires a Service from its domain dependencies.
func NewService(deps Deps) *Service {
	return &Service{db: deps.DB, graph: deps.Graph, grants: deps.Grants}
}

// Principal is a trusted, resolved caller. It is the only authority for
// Project, Task, Continuation, Runtime Profile, Runner, actor, and timestamp
// binding; capability methods trust it absolutely and never read those fields
// from a request body (runtime protocol §4.1).
type Principal struct {
	Grant             Grant
	DeclaredProjectID string
}

// Authenticate resolves a bearer token to a Continuation Interface Grant and
// verifies the declared Project (HTTP path or CLI flag) matches the grant. It
// does not gate on grant lifecycle: reads and exact replay remain available
// after finish, revocation, or a terminal Continuation (runtime protocol §4.2).
// Write capabilities re-check lifecycle themselves.
func (s *Service) Authenticate(ctx context.Context, token, declaredProjectID string) (Principal, error) {
	grant, err := s.grants.Resolve(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	if declaredProjectID != "" && declaredProjectID != grant.ProjectID {
		return Principal{}, ValidationError(ErrCodeProjectMismatch,
			fmt.Sprintf("declared project %q does not match grant project %q", declaredProjectID, grant.ProjectID),
			"path.project_id")
	}
	return Principal{Grant: grant, DeclaredProjectID: declaredProjectID}, nil
}

// RequestBatch is the Runtime-supplied mutation batch (runtime protocol §6.1).
// It carries no Project or provenance fields: those are bound from the grant.
type RequestBatch struct {
	SchemaVersion  int                    `json:"schema_version"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Operations     []blackboard.Operation `json:"operations"`
}

// ApplyMutationRequest is the ApplyMutationV1 envelope (runtime protocol §6).
type ApplyMutationRequest struct {
	ProtocolVersion    int                 `json:"protocol_version"`
	Batch              RequestBatch        `json:"batch"`
	SourceEventIDsByOp map[string][]string `json:"source_event_ids_by_op,omitempty"`
}

// ApplyMutationResponse is the canonical Apply success envelope (runtime
// protocol §3, §6.2). project_id is returned for operator clarity and is
// always the grant's bound Project.
type ApplyMutationResponse struct {
	ProtocolVersion       int                      `json:"protocol_version"`
	RequestKind           string                   `json:"request_kind"`
	ProjectID             string                   `json:"project_id"`
	ObservedGraphRevision int                      `json:"observed_graph_revision"`
	Result                blackboard.MutationResult `json:"result"`
}

// Apply applies one atomic typed graph mutation batch on behalf of a Runtime
// (runtime protocol §6). The Runtime supplies only the batch and optional
// source Event mappings; Project and provenance are bound from the principal's
// grant. A finished, revoked, or terminal grant rejects new writes while exact
// replay remains possible inside the graph service.
func (s *Service) Apply(ctx context.Context, principal Principal, req ApplyMutationRequest) (ApplyMutationResponse, error) {
	if err := requireGraph(s.graph); err != nil {
		return ApplyMutationResponse{}, err
	}
	if req.ProtocolVersion != RuntimeProtocolVersion {
		return ApplyMutationResponse{}, ValidationError(ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported protocol version %d", req.ProtocolVersion), "protocol_version")
	}
	if req.Batch.SchemaVersion != blackboard.GraphMutationSchemaVersion {
		return ApplyMutationResponse{}, ValidationError(ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported mutation schema version %d", req.Batch.SchemaVersion), "batch.schema_version")
	}
	if err := validateNoProvenanceInBatch(req.Batch); err != nil {
		return ApplyMutationResponse{}, err
	}

	projectKind, err := s.loadProjectKind(ctx, principal.Grant.ProjectID)
	if err != nil {
		return ApplyMutationResponse{}, err
	}
	execCtx := blackboard.ExecutionContext{
		ProjectID:        principal.Grant.ProjectID,
		ProjectKind:      projectKind,
		ActorType:        blackboard.ActorTypeRuntime,
		ActorID:          principal.Grant.ActorID,
		TaskID:           principal.Grant.TaskID,
		ContinuationID:   principal.Grant.ContinuationID,
		RuntimeProfileID: principal.Grant.RuntimeProfileID,
		Runner:           principal.Grant.Runner,
	}
	batch := blackboard.MutationBatch{
		SchemaVersion:      req.Batch.SchemaVersion,
		IdempotencyKey:     req.Batch.IdempotencyKey,
		Context:            execCtx,
		Operations:         req.Batch.Operations,
		SourceEventIDsByOp: req.SourceEventIDsByOp,
	}

	// Revalidate lifecycle authoritatively. The principal may be a snapshot from
	// before the grant closed, and Finish/Revoke/terminal marking can race with
	// a long-lived caller (runtime protocol §11.2).
	current, err := s.currentGrant(ctx, principal)
	if err != nil {
		return ApplyMutationResponse{}, err
	}
	status := current.Status()
	// Revocation rejects every use, including exact replay (runtime protocol
	// §4.2). Finish and a terminal Continuation close only new writes.
	if !status.IsReadable() {
		return ApplyMutationResponse{}, continuationClosedError(status)
	}

	// Exact idempotent replay remains available for non-revoked closed grants,
	// so probe the stored result before applying the new-write gate (runtime
	// protocol §4.2). A probe hit returns the byte-identical stored result.
	if replay, found, err := s.graph.ReplayResult(ctx, batch); err != nil {
		return ApplyMutationResponse{}, mapGraphError(err)
	} else if found {
		return ApplyMutationResponse{
			ProtocolVersion:       RuntimeProtocolVersion,
			RequestKind:           "apply",
			ProjectID:             principal.Grant.ProjectID,
			ObservedGraphRevision: replay.GraphRevision,
			Result:                replay,
		}, nil
	}

	// New write requires an open grant.
	if !status.IsWriteable() {
		return ApplyMutationResponse{}, continuationClosedError(status)
	}

	result, err := s.graph.Apply(ctx, batch)
	if err != nil {
		return ApplyMutationResponse{}, mapGraphError(err)
	}
	return ApplyMutationResponse{
		ProtocolVersion:       RuntimeProtocolVersion,
		RequestKind:           "apply",
		ProjectID:             principal.Grant.ProjectID,
		ObservedGraphRevision: result.GraphRevision,
		Result:                result,
	}, nil
}

// NodeLookup references one node by (node_type, stable_key) or by immutable id
// (runtime protocol §7).
type NodeLookup struct {
	NodeType  string `json:"node_type,omitempty"`
	StableKey string `json:"stable_key,omitempty"`
	ID        string `json:"id,omitempty"`
}

// ResolveRecordsRequest is the ResolveRecordsV1 narrow optimistic-concurrency
// read (runtime protocol §7). At most 100 node references plus edge IDs per
// request.
type ResolveRecordsRequest struct {
	ProtocolVersion int          `json:"protocol_version"`
	Nodes           []NodeLookup `json:"nodes"`
	EdgeIDs         []string     `json:"edge_ids,omitempty"`
}

// ResolvedNode is one resolved record with alias/merge resolution metadata.
type ResolvedNode struct {
	Node                 any `json:"node"`
	ResolvedFromAlias    string `json:"resolved_from_alias,omitempty"`
	ResolvedFromMergedID string `json:"resolved_from_merged_id,omitempty"`
}

// ResolvedEdge is one resolved edge record.
type ResolvedEdge struct {
	Edge blackboard.EdgeRecord `json:"edge"`
}

// MissingNode references a requested node that could not be resolved.
type MissingNode NodeLookup

// MissingEdge references a requested edge ID that could not be resolved.
type MissingEdge string

// ResolveRecordsResponse is the ResolveRecordsV1 result (runtime protocol §7).
type ResolveRecordsResponse struct {
	ProtocolVersion       int            `json:"protocol_version"`
	RequestKind           string         `json:"request_kind"`
	ProjectID             string         `json:"project_id"`
	ObservedGraphRevision int            `json:"observed_graph_revision"`
	Nodes                 []ResolvedNode `json:"nodes"`
	Edges                 []ResolvedEdge `json:"edges,omitempty"`
	Missing               []MissingNode  `json:"missing,omitempty"`
	MissingEdges          []MissingEdge  `json:"missing_edges,omitempty"`
}

// ResolveRecords resolves current graph records after alias and merge
// resolution (runtime protocol §7). Reads never mutate or repin a Continuation.
// Finish and a terminal Continuation leave reads available; revocation rejects
// every use (runtime protocol §4.2). A missing reference is reported in Missing
// rather than failing the whole request.
func (s *Service) ResolveRecords(ctx context.Context, principal Principal, req ResolveRecordsRequest) (ResolveRecordsResponse, error) {
	if err := requireGraph(s.graph); err != nil {
		return ResolveRecordsResponse{}, err
	}
	if req.ProtocolVersion != RuntimeProtocolVersion {
		return ResolveRecordsResponse{}, ValidationError(ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported protocol version %d", req.ProtocolVersion), "protocol_version")
	}
	if len(req.Nodes) == 0 && len(req.EdgeIDs) == 0 {
		return ResolveRecordsResponse{}, ValidationError(ErrCodeInvalidRequest, "at least one node or edge reference is required", "nodes")
	}
	if len(req.Nodes) > 100 {
		return ResolveRecordsResponse{}, ValidationError(ErrCodeInvalidRequest, "at most 100 node references per request", "nodes")
	}
	if len(req.Nodes)+len(req.EdgeIDs) > 100 {
		return ResolveRecordsResponse{}, ValidationError(ErrCodeInvalidRequest, "at most 100 node references plus edge IDs per request", "edge_ids")
	}
	current, err := s.currentGrant(ctx, principal)
	if err != nil {
		return ResolveRecordsResponse{}, err
	}
	if !current.Status().IsReadable() {
		return ResolveRecordsResponse{}, continuationClosedError(current.Status())
	}
	observedRevision, err := s.currentGraphRevision(ctx, principal.Grant.ProjectID)
	if err != nil {
		return ResolveRecordsResponse{}, err
	}
	response := ResolveRecordsResponse{
		ProtocolVersion:       RuntimeProtocolVersion,
		RequestKind:           "resolve_records",
		ProjectID:             principal.Grant.ProjectID,
		ObservedGraphRevision: observedRevision,
	}
	for _, lookup := range req.Nodes {
		resolved, missing, err := s.resolveOne(ctx, principal.Grant.ProjectID, lookup)
		if err != nil {
			return ResolveRecordsResponse{}, err
		}
		if missing {
			response.Missing = append(response.Missing, MissingNode(lookup))
			continue
		}
		response.Nodes = append(response.Nodes, resolved)
	}
	for _, edgeID := range req.EdgeIDs {
		edge, err := s.graph.ReadEdge(ctx, blackboard.ReadEdgeRequest{ProjectID: principal.Grant.ProjectID, EdgeID: edgeID})
		if err != nil {
			if isMissingNode(err) {
				response.MissingEdges = append(response.MissingEdges, MissingEdge(edgeID))
				continue
			}
			return ResolveRecordsResponse{}, mapGraphError(err)
		}
		response.Edges = append(response.Edges, ResolvedEdge{Edge: edge})
	}
	return response, nil
}

// CurrentGraphRequest is the current Runtime graph read (runtime protocol §8).
type CurrentGraphRequest struct {
	ProtocolVersion int `json:"protocol_version"`
}

// CurrentGraphResponse returns the exact current CanonicalMainGraphV1 bytes
// and observed projection metadata (runtime protocol §8). Graph is the decoded
// canonical document; the HTTP adapter also offers the raw bytes via ETag.
type CurrentGraphResponse struct {
	ProtocolVersion       int    `json:"protocol_version"`
	RequestKind           string `json:"request_kind"`
	ProjectID             string `json:"project_id"`
	ObservedGraphRevision int    `json:"observed_graph_revision"`
	Result                struct {
		RendererVersion  string `json:"renderer_version"`
		ProjectionHash   string `json:"projection_hash"`
		ProjectionBytes  int    `json:"projection_bytes"`
		EstimatedTokens  int    `json:"estimated_tokens"`
		Graph            any    `json:"graph"`
	} `json:"result"`
}

// CurrentGraph reads the current full Runtime graph (runtime protocol §8). It
// does not repin or rewrite the Continuation snapshot and may expose writes
// from concurrent Tasks. The full graph is never paginated or relevance
// filtered.
func (s *Service) CurrentGraph(ctx context.Context, principal Principal, req CurrentGraphRequest) (CurrentGraphResponse, error) {
	if err := requireGraph(s.graph); err != nil {
		return CurrentGraphResponse{}, err
	}
	if req.ProtocolVersion != RuntimeProtocolVersion {
		return CurrentGraphResponse{}, ValidationError(ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported protocol version %d", req.ProtocolVersion), "protocol_version")
	}
	current, err := s.currentGrant(ctx, principal)
	if err != nil {
		return CurrentGraphResponse{}, err
	}
	if !current.Status().IsReadable() {
		return CurrentGraphResponse{}, continuationClosedError(current.Status())
	}
	revision, err := s.currentGraphRevision(ctx, principal.Grant.ProjectID)
	if err != nil {
		return CurrentGraphResponse{}, err
	}
	projection, err := s.graph.CanonicalMainGraph(ctx, principal.Grant.ProjectID, revision)
	if err != nil {
		return CurrentGraphResponse{}, mapGraphError(err)
	}
	response := CurrentGraphResponse{
		ProtocolVersion:       RuntimeProtocolVersion,
		RequestKind:           "current_graph",
		ProjectID:             principal.Grant.ProjectID,
		ObservedGraphRevision: projection.GraphRevision,
	}
	response.Result.RendererVersion = projection.RendererVersion
	response.Result.ProjectionHash = projection.Hash
	response.Result.ProjectionBytes = projection.ByteCount
	response.Result.EstimatedTokens = projection.EstimatedTokens
	response.Result.Graph = jsonRaw(projection.Bytes)
	return response, nil
}

// resolveOne resolves a single node lookup, reporting whether it was missing.
func (s *Service) resolveOne(ctx context.Context, projectID string, lookup NodeLookup) (ResolvedNode, bool, error) {
	switch {
	case lookup.ID != "":
		literal, err := s.graph.ReadLiteralNode(ctx, blackboard.ReadLiteralNodeRequest{ProjectID: projectID, NodeID: lookup.ID})
		if err != nil {
			if isMissingNode(err) {
				return ResolvedNode{}, true, nil
			}
			return ResolvedNode{}, false, mapGraphError(err)
		}
		// A literal read does not follow merge redirects; report the redirect so
		// callers recover the canonical record (runtime protocol §7).
		resolved := ResolvedNode{Node: literal.Node}
		if literal.Node.MergeTargetID != "" {
			canonical, err := s.graph.ReadLiteralNode(ctx, blackboard.ReadLiteralNodeRequest{ProjectID: projectID, NodeID: literal.Node.MergeTargetID})
			if err != nil {
				if isMissingNode(err) {
					return ResolvedNode{}, true, nil
				}
				return ResolvedNode{}, false, mapGraphError(err)
			}
			resolved.ResolvedFromMergedID = literal.Node.ID
			resolved.Node = canonical.Node
		}
		return resolved, false, nil
	case lookup.NodeType != "" && lookup.StableKey != "":
		read, err := s.graph.ReadNode(ctx, blackboard.ReadNodeRequest{
			ProjectID: projectID,
			NodeType:  blackboard.NodeType(lookup.NodeType),
			Key:       lookup.StableKey,
		})
		if err != nil {
			if isMissingNode(err) {
				return ResolvedNode{}, true, nil
			}
			return ResolvedNode{}, false, mapGraphError(err)
		}
		return ResolvedNode{Node: read.Node, ResolvedFromAlias: read.ResolvedFromAlias}, false, nil
	default:
		return ResolvedNode{}, false, ValidationError(ErrCodeInvalidRequest,
			"node reference must supply id or (node_type, stable_key)", "nodes")
	}
}

func isMissingNode(err error) bool {
	var validation *blackboard.ValidationError
	if errors.As(err, &validation) {
		return validation.Code == blackboard.ErrCodeNodeNotFound || validation.Code == blackboard.ErrCodeEdgeEndpointNotFound
	}
	return false
}

// loadProjectKind reads the Project kind so the trusted execution context
// matches the graph service's project-existence check (graph contract §3.3).
func (s *Service) loadProjectKind(ctx context.Context, projectID string) (string, error) {
	var kind string
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id = ?`, projectID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ValidationError(ErrCodeProjectNotFound, "bound Project does not exist", "context.project_id")
	}
	if err != nil {
		return "", fmt.Errorf("read bound Project kind: %w", err)
	}
	return kind, nil
}

// currentGraphRevision reads the current graph revision for a Project, defaulting
// to 0 when the Project has no graph state yet.
func (s *Service) currentGraphRevision(ctx context.Context, projectID string) (int, error) {
	var revision int
	err := s.db.QueryRowContext(ctx,
		`SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id = ?`,
		projectID,
	).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read current graph revision: %w", err)
	}
	return revision, nil
}

func requireGraph(graph *blackboard.GraphService) error {
	if graph == nil {
		return ValidationError(ErrCodeInvalidRequest, "graph Blackboard is not active for this store epoch", "store_epoch")
	}
	return nil
}

// currentGrant re-reads the grant bound to the principal so capabilities
// revalidate lifecycle authoritatively at request time rather than trusting a
// snapshot cached at authentication (runtime protocol §11.2).
func (s *Service) currentGrant(ctx context.Context, principal Principal) (Grant, error) {
	grant, err := s.grants.Get(ctx, principal.Grant.ID)
	if err == nil {
		return grant, nil
	}
	// grant_not_found is a structured *Error; anything else is an internal
	// failure that must surface as 500, not 400 (runtime protocol §12.4).
	if AsError(err) != nil {
		return Grant{}, err
	}
	return Grant{}, InternalError(err.Error())
}

// validateNoProvenanceInBatch rejects any operation that smuggles trusted
// provenance through create/patch property maps. The structural RequestBatch
// already omits Project and context fields; this guards against a caller
// embedding actor/task/continuation/profile/runner keys inside a property map,
// which the graph service would reject as unknown_property anyway but should
// surface here as provenance_spoofed for clear transport mapping.
func validateNoProvenanceInBatch(batch RequestBatch) *Error {
	if batch.IdempotencyKey == "" {
		return ValidationError(ErrCodeInvalidRequest, "idempotency_key is required", "batch.idempotency_key")
	}
	if len(batch.Operations) == 0 {
		return ValidationError(ErrCodeInvalidRequest, "batch has no operations", "batch.operations")
	}
	if len(batch.Operations) > 128 {
		return ValidationError(ErrCodeInvalidRequest, "batch has more than 128 operations", "batch.operations")
	}
	for i, op := range batch.Operations {
		if op.OpID == "" {
			return ValidationError(ErrCodeInvalidRequest, "op_id is required", fmt.Sprintf("batch.operations[%d].op_id", i))
		}
		if forbidden := spoofedProvenanceKeys(op); forbidden != "" {
			return ValidationError(ErrCodeProvenanceSpoofed,
				"Runtime request must not supply provenance fields: "+forbidden,
				fmt.Sprintf("batch.operations[%d]", i))
		}
	}
	return nil
}

// spoofedProvenanceKeys returns a comma-joined list of trusted context keys a
// Runtime attempted to embed in an operation's create or patch property map, or
// "" when the operation is clean.
func spoofedProvenanceKeys(op blackboard.Operation) string {
	var hit []string
	check := func(props map[string]any) {
		for _, key := range provenancePropertyKeys {
			if _, present := props[key]; present {
				hit = append(hit, key)
			}
		}
	}
	check(op.Create.PropertyMap)
	check(op.Create.ExtraProperties)
	check(op.Patch.Properties)
	if len(hit) == 0 {
		return ""
	}
	return strings.Join(hit, ", ")
}

// provenancePropertyKeys are the trusted-context field names a Runtime request
// must never embed inside a property map (runtime protocol §4.1).
var provenancePropertyKeys = []string{
	"project_id", "task_id", "continuation_id", "runtime_profile_id",
	"runtime_plugin_id", "runner", "actor_id", "actor_type", "recorded_at",
}

// continuationClosedError reports that a grant no longer admits the requested
// use. The grant_status detail lets HTTP map revocation to 403 (every use
// rejected) while finish/terminal stay 409 (new writes rejected, reads and
// replay remain) (runtime protocol §4.2, §12.4).
func continuationClosedError(status GrantStatus) *Error {
	message := fmt.Sprintf("continuation grant is %s; new writes are rejected", status)
	if status.IsReadable() {
		message += " but reads and exact replay remain available"
	}
	return &Error{
		ProtocolVersion: RuntimeProtocolVersion,
		Code:            ErrCodeContinuationClosed,
		Message:         message,
		Path:            "authorization",
		Details:         map[string]any{"grant_status": string(status)},
	}
}

// mapGraphError converts a graph-service error into a ProjectInterfaceErrorV1.
// Graph domain validation codes pass through with their operation-scoped fields;
// retryable storage-busy becomes storage_busy; any other (unexpected) failure
// becomes an internal error so transport adapters map it to 500 rather than
// leaking unstructured text as a 400 (runtime protocol §12.4).
func mapGraphError(err error) *Error {
	if err == nil {
		return nil
	}
	var validation *blackboard.ValidationError
	if errors.As(err, &validation) {
		mapped := &Error{
			ProtocolVersion: RuntimeProtocolVersion,
			Code:            validation.Code,
			Message:         validation.Message,
			Path:            validation.Path,
			Retryable:       validation.Retryable,
			Details:         validation.Details,
		}
		if validation.OperationIndex >= 0 {
			index := validation.OperationIndex
			mapped.OperationIndex = &index
		}
		mapped.OpID = validation.OpID
		return mapped
	}
	var storageErr *blackboard.StorageError
	if errors.As(err, &storageErr) {
		return &Error{ProtocolVersion: RuntimeProtocolVersion, Code: ErrCodeStorageBusy, Message: storageErr.Message, Retryable: storageErr.Retryable, Path: "storage"}
	}
	return InternalError(err.Error())
}
