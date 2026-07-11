// This file begins the canonical graph Blackboard (graph contract, storage
// contract). The GraphService owns BlackboardGraphService.Apply — the single
// deep seam for all graph writes. While the store epoch is legacy_v1 the graph
// data stays dark: no production Project Interface wires GraphService, and the
// service is exercised only by tests and (later) migration transactions
// (slices §1, C02). The legacy Service remains canonical for production reads
// and writes until the M05 cutover.
package blackboard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"pentest/internal/store"
)

// stableKeyPattern is the graph contract §4 grammar for new stable keys:
// [a-z0-9][a-z0-9._:/-]{0,159}.
var stableKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]{0,159}$`)

// idempotencyKeyPattern is the graph contract §10 grammar for idempotency keys.
var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// GraphService implements the BlackboardGraphService.Apply seam (graph contract
// §1). It is the single deep module for all graph writes.
//
// Production wiring: NONE while canonical_store == legacy_v1. Construct only in
// tests and (later) migration transactions. Daemon/HTTP/MCP/CLI/runner must not
// reach this service until the M05 cutover (slices §1, C02 "keep graph data
// dark while the store epoch is legacy_v1").
type GraphService struct {
	db    *store.DB
	clock Clock
	ids   IDSource
}

type resolvedNode struct {
	identity, nodeID, opID, stableKey, entityKind string
	nodeType                                      NodeType
}

func (n resolvedNode) id(created map[string]string) string {
	if n.nodeID != "" {
		return n.nodeID
	}
	return created[n.opID]
}

func resolveNodeRef(tx *sql.Tx, projectID string, ref NodeRef, ops map[string]Operation) (resolvedNode, *ValidationError) {
	if ref.OpID != "" {
		op, ok := ops[ref.OpID]
		if !ok || op.Kind != OpCreateNode {
			return resolvedNode{}, validationError(ErrCodeEdgeEndpointNotFound, "op_id endpoint does not resolve", -1, "", "op_id")
		}
		kind, _ := operationProperties(op)["kind"].(string)
		return resolvedNode{identity: "op:" + ref.OpID, opID: ref.OpID, stableKey: op.Node.StableKey, nodeType: op.Node.NodeType, entityKind: kind}, nil
	}
	var id, typ, kind string
	if ref.ID != "" {
		err := tx.QueryRow(`SELECT h.node_id,h.node_type,h.entity_kind FROM blackboard_node_heads h WHERE h.project_id=? AND h.node_id=? AND h.disposition='main'`, projectID, ref.ID).Scan(&id, &typ, &kind)
		if err != nil {
			return resolvedNode{}, validationError(ErrCodeEdgeEndpointNotFound, "node id endpoint does not resolve", -1, "", "id")
		}
	}
	if ref.StableKey != "" && ref.NodeType != "" {
		err := tx.QueryRow(`SELECT h.node_id,h.node_type,h.entity_kind FROM blackboard_key_registry k JOIN blackboard_node_heads h ON h.project_id=k.project_id AND h.node_id=k.canonical_node_id WHERE k.project_id=? AND k.node_type=? AND k.key=? AND h.disposition='main'`, projectID, string(ref.NodeType), ref.StableKey).Scan(&id, &typ, &kind)
		if err != nil {
			return resolvedNode{}, validationError(ErrCodeEdgeEndpointNotFound, "stable-key endpoint does not resolve", -1, "", "stable_key")
		}
	}
	if id == "" {
		return resolvedNode{}, validationError(ErrCodeEdgeEndpointNotFound, "edge endpoint reference is empty", -1, "", "")
	}
	return resolvedNode{identity: "id:" + id, nodeID: id, stableKey: ref.StableKey, nodeType: NodeType(typ), entityKind: kind}, nil
}

func validateFinalCycles(tx *sql.Tx, projectID string, batch MutationBatch, resolved map[string][2]resolvedNode) *ValidationError {
	graphs := []map[string][]string{{}, {}, {}}
	rows, err := tx.Query(`SELECT edge_type,from_node_id,to_node_id FROM blackboard_edge_heads WHERE project_id=? AND state='active' AND edge_type IN ('part_of','depends_on','blocks','supersedes')`, projectID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var typ, from, to string
			_ = rows.Scan(&typ, &from, &to)
			g := -1
			switch EdgeType(typ) {
			case EdgeTypePartOf:
				g = 0
			case EdgeTypeDependsOn:
				g = 1
				from, to = to, from
			case EdgeTypeBlocks:
				g = 1
			case EdgeTypeSupersedes:
				g = 2
			}
			if g >= 0 {
				graphs[g]["id:"+from] = append(graphs[g]["id:"+from], "id:"+to)
			}
		}
	}
	for i, op := range batch.Operations {
		if op.Kind != OpPutEdge {
			continue
		}
		pair := resolved[op.OpID]
		from, to, g := pair[0].identity, pair[1].identity, -1
		switch op.PutEdge.EdgeType {
		case EdgeTypePartOf:
			if pair[0].nodeType == NodeTypeEntity {
				g = 0
			}
		case EdgeTypeDependsOn:
			g = 1
			from, to = to, from
		case EdgeTypeBlocks:
			g = 1
		case EdgeTypeSupersedes:
			g = 2
		}
		if g < 0 {
			continue
		}
		graphs[g][from] = append(graphs[g][from], to)
		if hasCycle(graphs[g]) {
			return validationError(ErrCodeGraphCycle, "controlled edge would create a cycle", i, op.OpID, fmt.Sprintf("operations[%d].from", i))
		}
	}
	return nil
}
func hasCycle(g map[string][]string) bool {
	visiting, done := map[string]bool{}, map[string]bool{}
	var walk func(string) bool
	walk = func(n string) bool {
		if visiting[n] {
			return true
		}
		if done[n] {
			return false
		}
		visiting[n] = true
		for _, x := range g[n] {
			if walk(x) {
				return true
			}
		}
		visiting[n] = false
		done[n] = true
		return false
	}
	for n := range g {
		if walk(n) {
			return true
		}
	}
	return false
}

// NewGraphService returns a GraphService wired with injected deterministic
// dependencies (slices §2.1). Production callers pass SystemClock and
// RandomIDSource; tests pass deterministic sources for byte-stable hashes.
func NewGraphService(db *store.DB, clock Clock, ids IDSource) *GraphService {
	if clock == nil {
		clock = SystemClock{}
	}
	if ids == nil {
		ids = RandomIDSource{}
	}
	return &GraphService{db: db, clock: clock, ids: ids}
}

// DBForTesting exposes the underlying database for storage-integrity assertions
// in tests (slices §2.2 allow direct SQL for integrity checks). It MUST NOT be
// used by production code.
func (s *GraphService) DBForTesting() *store.DB { return s.db }

// Apply applies a mutation batch atomically (graph contract §9, storage §9).
// C02 implements the create_node operation for project_fact nodes; other
// operation kinds fail closed with invalid_request until their owning slice.
//
//nolint:gocyclo // C02 Apply is one cohesive transaction; complexity drops as slices mature.
func (s *GraphService) Apply(ctx context.Context, batch MutationBatch) (result MutationResult, err error) {
	defer func() { err = classifyStorageBusy(err) }()
	if batch.SchemaVersion != GraphMutationSchemaVersion {
		return MutationResult{}, validationError(ErrCodeUnsupportedSchemaVersion,
			fmt.Sprintf("unsupported mutation schema version %d", batch.SchemaVersion), -1, "", "schema_version")
	}
	if !idempotencyKeyPattern.MatchString(batch.IdempotencyKey) {
		return MutationResult{}, validationError(ErrCodeInvalidRequest,
			"idempotency_key does not match required grammar", -1, "", "idempotency_key")
	}
	if len(batch.Operations) == 0 {
		return MutationResult{}, validationError(ErrCodeInvalidRequest, "batch has no operations", -1, "", "operations")
	}

	// Caller-declared Project must match the trusted, bound context.
	if batch.ProjectID != "" && batch.ProjectID != batch.Context.ProjectID {
		return MutationResult{}, validationError(ErrCodeProjectMismatch,
			fmt.Sprintf("batch project_id %q does not match trusted context project %q", batch.ProjectID, batch.Context.ProjectID),
			-1, "", "project_id")
	}

	projectID := batch.Context.ProjectID
	scope := batch.Context.idempotencyScope()
	if scope == "" {
		return MutationResult{}, validationError(ErrCodeProvenanceRequired,
			"actor type does not derive an idempotency scope", -1, "", "context.actor_type")
	}

	requestHash, err := computeRequestHash(batch)
	if err != nil {
		return MutationResult{}, fmt.Errorf("compute request hash: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult{}, graphStorageError("begin graph transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Revalidate trusted context inside the transaction (storage §9 step 3).
	if err := assertProjectExists(tx, batch.Context.ProjectID, batch.Context.ProjectKind); err != nil {
		return MutationResult{}, err
	}
	if err := validateExecutionContext(tx, batch.Context); err != nil {
		return MutationResult{}, err
	}
	if err := verifyMutationChain(ctx, tx, projectID); err != nil {
		return MutationResult{}, fmt.Errorf("verify graph ledger before Apply: %w", err)
	}

	// Idempotency: same scope/key/hash returns the exact stored result bytes;
	// different hash returns idempotency_conflict (storage §9 step 5).
	if stored, err := s.checkIdempotency(tx, projectID, scope, batch.IdempotencyKey, requestHash); err != nil {
		return MutationResult{}, err
	} else if stored != nil {
		return *stored, nil
	}

	result, err = s.applyOperations(tx, batch, requestHash)
	if err != nil {
		return MutationResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return MutationResult{}, graphStorageError("commit graph transaction", err)
	}
	return result, nil
}

func graphStorageError(action string, err error) error {
	return classifyStorageBusy(fmt.Errorf("%s: %w", action, err))
}

func classifyStorageBusy(err error) error {
	if err == nil {
		return nil
	}
	var alreadyClassified *StorageError
	if errors.As(err, &alreadyClassified) {
		return err
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy") || strings.Contains(message, "sqlite_busy") || strings.Contains(message, "sqlite_locked") {
		return &StorageError{Code: ErrCodeStorageBusy, Message: "SQLite writer lock is busy", Retryable: true, Cause: err}
	}
	return err
}

// applyOperations validates the batch, applies it, and returns the result.
func (s *GraphService) applyOperations(tx *sql.Tx, batch MutationBatch, requestHash []byte) (MutationResult, error) {
	projectID := batch.Context.ProjectID

	// Validate every operation against the closed envelope before allocating
	// any IDs or writing any row (storage §9 steps 6-8).
	seenOps := make(map[string]Operation, len(batch.Operations))
	for i, op := range batch.Operations {
		if op.OpID == "" {
			return MutationResult{}, validationError(ErrCodeInvalidRequest, "op_id is required", i, "", "operations[].op_id")
		}
		if _, duplicate := seenOps[op.OpID]; duplicate {
			return MutationResult{}, validationError(ErrCodeInvalidRequest, "op_id must be unique", i, op.OpID, fmt.Sprintf("operations[%d].op_id", i))
		}
		seenOps[op.OpID] = op
		if op.Kind != OpCreateNode && op.Kind != OpPutEdge && op.Kind != OpPatchNode && op.Kind != OpTransitionNode {
			return MutationResult{}, validationError(ErrCodeInvalidRequest,
				fmt.Sprintf("operation kind %q is not implemented", op.Kind), i, op.OpID, "operations[].kind")
		}
		if op.Kind == OpPutEdge {
			continue
		}
		if op.Kind == OpPatchNode || op.Kind == OpTransitionNode {
			if op.Node.ID == "" && (op.Node.NodeType == "" || op.Node.StableKey == "") {
				return MutationResult{}, validationError(ErrCodeInvalidRequest, "node reference is required", i, op.OpID, "operations[].node")
			}
			continue
		}
		if _, ok := nodeSchemas[op.Node.NodeType]; !ok {
			return MutationResult{}, validationError(ErrCodeUnknownNodeType, fmt.Sprintf("unknown node type %q", op.Node.NodeType), i, op.OpID, fmt.Sprintf("operations[%d].node_type", i))
		}
		if !stableKeyPattern.MatchString(op.Node.StableKey) {
			return MutationResult{}, validationError(ErrCodeInvalidProperty,
				"stable_key does not match grammar [a-z0-9][a-z0-9._:/-]{0,159}", i, op.OpID, "operations[].stable_key")
		}
		if len(op.Create.ExtraProperties) > 0 {
			// Closed envelope: any extra property key is unknown (graph §3.1).
			var keys []string
			for k := range op.Create.ExtraProperties {
				keys = append(keys, k)
			}
			return MutationResult{}, validationError(ErrCodeUnknownProperty,
				fmt.Sprintf("unknown project_fact property: %v", keys), i, op.OpID, "operations[].properties")
		}
		createProps := normalizedCreateProperties(op)
		if op.Node.NodeType == NodeTypeExplorationObjective {
			if status, _ := createProps["status"].(string); status != "open" {
				return MutationResult{}, validationError(ErrCodeInvalidTransition, "Exploration Objective must be created open", i, op.OpID, "operations[].properties.status")
			}
			if _, supplied := operationProperties(op)["resolved_at"]; supplied {
				return MutationResult{}, validationError(ErrCodeInvalidProperty, "resolved_at is system-managed", i, op.OpID, "operations[].properties.resolved_at")
			}
			if _, supplied := operationProperties(op)["resolution_summary"]; supplied {
				return MutationResult{}, validationError(ErrCodeInvalidProperty, "resolution_summary is transition-managed", i, op.OpID, "operations[].properties.resolution_summary")
			}
		}
		if op.Node.NodeType == NodeTypeAttempt {
			if status, _ := createProps["status"].(string); status != "open" {
				return MutationResult{}, validationError(ErrCodeInvalidTransition, "Attempt must be created open", i, op.OpID, "operations[].properties.status")
			}
			if _, supplied := operationProperties(op)["ended_at"]; supplied {
				return MutationResult{}, validationError(ErrCodeInvalidProperty, "ended_at is system-managed", i, op.OpID, "operations[].properties.ended_at")
			}
		}
		if err := validateNodeProperties(op.Node.NodeType, createProps); err != nil {
			err.OperationIndex = i
			err.OpID = op.OpID
			return MutationResult{}, err
		}
		if op.Node.NodeType == NodeTypeSolution && batch.Context.ProjectKind != "ctf_challenge" {
			return MutationResult{}, validationError(ErrCodeProjectKindViolation, "solution is valid only in a ctf_challenge Project", i, op.OpID, fmt.Sprintf("operations[%d].node_type", i))
		}
		if op.Node.NodeType == NodeTypeGoal {
			taskID, _ := createProps["task_id"].(string)
			wantKey := "task:" + taskID + ":goal"
			if batch.Context.ActorType != ActorTypeSystem || batch.Context.ActorID != taskGoalProjectorActor || batch.Context.TaskID != taskID {
				return MutationResult{}, validationError(ErrCodeInvalidRequest, "Goals are system-owned Task projections", i, op.OpID, "operations[].node_type")
			}
			if op.Node.StableKey != wantKey {
				return MutationResult{}, validationError(ErrCodeInvariantViolation, "Goal stable key must match its Task", i, op.OpID, "operations[].stable_key")
			}
			if err := validateGoalProjectionSource(tx, projectID, op.Node.StableKey, createProps); err != nil {
				err.OperationIndex = i
				err.OpID = op.OpID
				return MutationResult{}, err
			}
		}
	}
	if err := validateSourceEvents(tx, batch, seenOps); err != nil {
		return MutationResult{}, err
	}
	// Resolve same-batch and current-graph references and validate every controlled edge against
	// the final proposed node set before allocating IDs or writing rows.
	resolvedEdges := map[string][2]resolvedNode{}
	for i, op := range batch.Operations {
		if op.Kind != OpPutEdge {
			continue
		}
		allowed, known := edgeEndpoints[op.PutEdge.EdgeType]
		if !known {
			return MutationResult{}, validationError(ErrCodeUnknownEdgeType, fmt.Sprintf("unknown edge type %q", op.PutEdge.EdgeType), i, op.OpID, fmt.Sprintf("operations[%d].edge_type", i))
		}
		from, err := resolveNodeRef(tx, projectID, op.PutEdge.From, seenOps)
		if err != nil {
			err.OperationIndex = i
			err.OpID = op.OpID
			err.Path = fmt.Sprintf("operations[%d].from", i)
			return MutationResult{}, err
		}
		to, err := resolveNodeRef(tx, projectID, op.PutEdge.To, seenOps)
		if err != nil {
			err.OperationIndex = i
			err.OpID = op.OpID
			err.Path = fmt.Sprintf("operations[%d].to", i)
			return MutationResult{}, err
		}
		resolvedEdges[op.OpID] = [2]resolvedNode{from, to}
		if from.identity == to.identity {
			return MutationResult{}, validationError(ErrCodeSelfEdgeForbidden, "self edges are forbidden", i, op.OpID, fmt.Sprintf("operations[%d].from", i))
		}
		if !allowed(from.nodeType, to.nodeType) {
			return MutationResult{}, validationError(ErrCodeEdgeEndpointType, fmt.Sprintf("%s cannot connect %s to %s", op.PutEdge.EdgeType, from.nodeType, to.nodeType), i, op.OpID, fmt.Sprintf("operations[%d].from", i))
		}
		if op.PutEdge.EdgeType == EdgeTypePartOf && from.nodeType == NodeTypeEntity {
			if e := validateEntityPartOfKinds(from.entityKind, to.entityKind); e != nil {
				e.OperationIndex = i
				e.OpID = op.OpID
				e.Path = fmt.Sprintf("operations[%d].from", i)
				return MutationResult{}, e
			}
		}
	}
	if e := validateFinalCycles(tx, projectID, batch, resolvedEdges); e != nil {
		return MutationResult{}, e
	}
	if e := validateAttemptCreates(batch, resolvedEdges); e != nil {
		return MutationResult{}, e
	}
	if e := validateRuntimeProducedEdges(tx, projectID, batch, resolvedEdges); e != nil {
		return MutationResult{}, e
	}
	if e := validateCreatedConfirmations(tx, projectID, batch, resolvedEdges); e != nil {
		return MutationResult{}, e
	}

	// Load current graph state for the Project (storage §9 step 6). C02 has no
	// existing graph on the first create, so base revision is 0.
	state, err := loadGraphState(tx, projectID)
	if err != nil {
		return MutationResult{}, err
	}

	// Allocate the single shared server timestamp for all effects in the batch
	// (storage §9 step 8). recorded_at is never caller-supplied.
	recordedAt := s.clock.Now().UTC()

	result := MutationResult{
		GraphRevision: state.currentGraphRevision,
		Operations:    make([]OperationResult, len(batch.Operations)),
	}

	// Apply each create_node. C02's minimal round-trip has a single operation;
	// the loop keeps the shape for when multi-op batches arrive in later slices.
	stateChanged := false
	nodeIDs := map[string]string{}
	for i, op := range batch.Operations {
		provenanceID := s.ids.NextID()
		if err := insertProvenance(tx, projectID, provenanceID, batch.Context, recordedAt.Format("2006-01-02T15:04:05.000000000Z07:00")); err != nil {
			return MutationResult{}, err
		}
		for ordinal, eventID := range batch.SourceEventIDsByOp[op.OpID] {
			if _, err := tx.Exec(`INSERT INTO blackboard_graph_provenance_events(project_id,provenance_id,ordinal,event_id) VALUES(?,?,?,?)`, projectID, provenanceID, ordinal, eventID); err != nil {
				return MutationResult{}, fmt.Errorf("insert provenance event: %w", err)
			}
		}
		state.provenanceIDs = append(state.provenanceIDs, provenanceID)
		if op.Kind == OpPutEdge {
			resolved := resolvedEdges[op.OpID]
			fromID, toID := resolved[0].id(nodeIDs), resolved[1].id(nodeIDs)
			var existingID, existingSummary, existingSemanticHash string
			var existingVersion int
			err := tx.QueryRow(
				`SELECT h.edge_id, h.version, v.summary, h.semantic_hash
				   FROM blackboard_edge_heads h
				   JOIN blackboard_edge_versions v
				     ON v.project_id=h.project_id AND v.edge_id=h.edge_id AND v.version=h.version
				  WHERE h.project_id=? AND h.edge_type=? AND h.from_node_id=? AND h.to_node_id=? AND h.state='active'`,
				projectID, string(op.PutEdge.EdgeType), fromID, toID,
			).Scan(&existingID, &existingVersion, &existingSummary, &existingSemanticHash)
			if err == nil && existingSummary == op.PutEdge.Summary {
				result.Operations[i] = OperationResult{OpID: op.OpID, EdgeID: existingID, EdgeType: op.PutEdge.EdgeType, EdgeVersion: existingVersion, SemanticHash: existingSemanticHash, Changed: false}
				continue
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return MutationResult{}, fmt.Errorf("read existing edge: %w", err)
			}
			if err == nil {
				if op.PutEdge.ExpectedVersion <= 0 || op.PutEdge.ExpectedVersion != existingVersion {
					return MutationResult{}, validationError(ErrCodeVersionConflict, fmt.Sprintf("expected edge version %d does not match current version %d", op.PutEdge.ExpectedVersion, existingVersion), i, op.OpID, "operations[].put_edge.expected_version")
				}
				if !stateChanged {
					result.GraphRevision = state.currentGraphRevision + 1
					stateChanged = true
				}
				semHash := edgeSemanticHash(op.PutEdge.EdgeType, fromID, toID, "active", op.PutEdge.Summary)
				result.Operations[i] = OperationResult{OpID: op.OpID, EdgeID: existingID, EdgeType: op.PutEdge.EdgeType, EdgeVersion: existingVersion + 1, SemanticHash: hex.EncodeToString(semHash), Changed: true}
				state.pendingEdgeUpdates = append(state.pendingEdgeUpdates, pendingEdgeUpdate{id: existingID, edgeType: op.PutEdge.EdgeType, fromID: fromID, toID: toID, version: existingVersion + 1, summary: op.PutEdge.Summary, semHash: semHash, opIndex: i, graphRevision: result.GraphRevision})
				continue
			}
			if !stateChanged {
				result.GraphRevision = state.currentGraphRevision + 1
				stateChanged = true
			}
			edgeID := s.ids.NextID()
			semHash := edgeSemanticHash(op.PutEdge.EdgeType, fromID, toID, "active", op.PutEdge.Summary)
			result.Operations[i] = OperationResult{OpID: op.OpID, EdgeID: edgeID, EdgeType: op.PutEdge.EdgeType, EdgeVersion: 1, SemanticHash: hex.EncodeToString(semHash), Changed: true}
			state.pendingEdges = append(state.pendingEdges, pendingEdge{id: edgeID, edgeType: op.PutEdge.EdgeType, fromID: fromID, toID: toID, summary: op.PutEdge.Summary, semHash: semHash, opIndex: i, graphRevision: result.GraphRevision})
			continue
		}
		if op.Kind == OpPatchNode || op.Kind == OpTransitionNode {
			current, err := loadMutableNode(tx, projectID, op.Node)
			if err != nil {
				return MutationResult{}, err
			}
			props := clonePropertyMap(current.props)
			expectedVersion := op.Patch.ExpectedVersion
			if op.Kind == OpTransitionNode {
				expectedVersion = op.Transition.ExpectedVersion
			}
			if expectedVersion <= 0 || expectedVersion != current.version {
				return MutationResult{}, validationError(ErrCodeVersionConflict, fmt.Sprintf("expected version %d does not match current version %d", expectedVersion, current.version), i, op.OpID, "operations[].expected_version")
			}
			if op.Kind == OpPatchNode {
				if current.nodeType != NodeTypeGoal {
					return MutationResult{}, validationError(ErrCodeInvalidRequest, "patch_node is currently restricted to system Goal status projection", i, op.OpID, "operations[].kind")
				}
				if batch.Context.ActorType != ActorTypeSystem || batch.Context.ActorID != taskGoalProjectorActor || batch.Context.TaskID != current.props["task_id"] {
					return MutationResult{}, validationError(ErrCodeInvalidRequest, "Goals are system-owned Task projections", i, op.OpID, "operations[].patch")
				}
				for k := range op.Patch.Properties {
					if k != "task_status" {
						return MutationResult{}, validationError(ErrCodeImmutableField, "Goal identity and text are immutable", i, op.OpID, "operations[].patch.properties."+k)
					}
				}
				taskID, _ := current.props["task_id"].(string)
				var durableText, durableStatus string
				if err := tx.QueryRow(`SELECT goal,status FROM tasks WHERE id=? AND project_id=?`, taskID, projectID).Scan(&durableText, &durableStatus); err != nil {
					return MutationResult{}, validationError(ErrCodeInvariantViolation, "Goal source Task is missing", i, op.OpID, "operations[].patch")
				}
				if current.props["text"] != durableText {
					return MutationResult{}, validationError(ErrCodeInvariantViolation, "Task Goal immutable text drifted", i, op.OpID, "operations[].patch")
				}
				props["task_status"] = durableStatus
			} else {
				switch current.nodeType {
				case NodeTypeExplorationObjective:
					if err := applyObjectiveTransition(tx, projectID, current, op, batch, resolvedEdges, recordedAt, props); err != nil {
						return MutationResult{}, annotateOperationError(err, i)
					}
				case NodeTypeAttempt:
					if err := applyAttemptTransition(tx, projectID, current, op, batch, resolvedEdges, recordedAt, props); err != nil {
						return MutationResult{}, annotateOperationError(err, i)
					}
				case NodeTypeHypothesis:
					if err := applyHypothesisTransition(tx, projectID, current, op, batch, resolvedEdges, props); err != nil {
						return MutationResult{}, annotateOperationError(err, i)
					}
				case NodeTypeProjectFact:
					if err := applyProjectFactTransition(tx, projectID, current, op, batch, resolvedEdges, props); err != nil {
						return MutationResult{}, annotateOperationError(err, i)
					}
				case NodeTypeFinding:
					if err := applyFindingTransition(tx, projectID, current, op, batch, resolvedEdges, props); err != nil {
						return MutationResult{}, annotateOperationError(err, i)
					}
				case NodeTypeSolution:
					if err := applySolutionTransition(tx, projectID, current, op, batch, resolvedEdges, props); err != nil {
						return MutationResult{}, annotateOperationError(err, i)
					}
				default:
					return MutationResult{}, validationError(ErrCodeInvalidRequest, "transition_node is not supported for this node type", i, op.OpID, "operations[].node")
				}
			}
			if err := validateNodeProperties(current.nodeType, props); err != nil {
				err.OperationIndex = i
				err.OpID = op.OpID
				return MutationResult{}, err
			}
			propsJSON, err := canonicalJSON(props)
			if err != nil {
				return MutationResult{}, err
			}
			semHash := genericNodeSemanticHash(DispositionMain, "", props)
			changed := hex.EncodeToString(semHash) != current.semHash
			version := current.version
			if changed {
				if !stateChanged {
					result.GraphRevision = state.currentGraphRevision + 1
					stateChanged = true
				}
				version++
				state.pendingUpdates = append(state.pendingUpdates, pendingUpdate{nodeID: current.nodeID, nodeType: current.nodeType, stableKey: current.stableKey, version: version, propsJSON: propsJSON, semHash: semHash, opIndex: i, graphRevision: result.GraphRevision})
			}
			result.Operations[i] = OperationResult{OpID: op.OpID, NodeID: current.nodeID, NodeType: current.nodeType, StableKey: current.stableKey, NodeVersion: version, SemanticHash: hex.EncodeToString(semHash), Changed: changed}
			continue
		}

		// Key uniqueness across live keys and aliases (graph §4, storage §7.4).
		conflict, err := keyIsLive(tx, projectID, op.Node.NodeType, op.Node.StableKey)
		if err != nil {
			return MutationResult{}, err
		}
		if conflict {
			return MutationResult{}, validationError(ErrCodeNodeKeyConflict,
				fmt.Sprintf("stable key %q is already live or reserved by an alias", op.Node.StableKey), i, op.OpID, "operations[].stable_key")
		}

		props := normalizedCreateProperties(op)
		if op.Node.NodeType == NodeTypeProjectFact {
			n := normalizeProjectFactProperties(op.Create.Properties)
			if op.Create.PropertyMap == nil {
				props = map[string]any{"category": n.Category, "summary": n.Summary, "body": n.Body, "confidence": string(n.Confidence), "scope_status": string(n.ScopeStatus)}
			}
		}
		nodeID := s.ids.NextID()
		nodeIDs[op.OpID] = nodeID

		propsJSON, err := canonicalJSON(props)
		if err != nil {
			return MutationResult{}, fmt.Errorf("encode project_fact properties: %w", err)
		}
		semHash := genericNodeSemanticHash(DispositionMain, "", props)

		// A create always changes current semantic state: new node, version 1.
		if !stateChanged {
			result.GraphRevision = state.currentGraphRevision + 1
			stateChanged = true
		}
		nodeVersion := 1

		// We have not yet allocated the mutation_seq, so insert operation/node
		// rows after the mutation header is written. Collect pending inserts.
		result.Operations[i] = OperationResult{
			OpID:         op.OpID,
			NodeID:       nodeID,
			NodeType:     op.Node.NodeType,
			StableKey:    op.Node.StableKey,
			NodeVersion:  nodeVersion,
			SemanticHash: hex.EncodeToString(semHash),
			Changed:      true,
		}
		// Stash pending rows on a per-op struct via closures over the loop vars.
		pending := pendingCreate{
			nodeID:        nodeID,
			nodeType:      op.Node.NodeType,
			stableKey:     op.Node.StableKey,
			version:       nodeVersion,
			propsJSON:     propsJSON,
			semHash:       semHash,
			keySemHash:    keySemanticHash(op.Node.NodeType, op.Node.StableKey, "stable", nodeID, nodeID, false),
			provenanceID:  provenanceID,
			opIndex:       i,
			opID:          op.OpID,
			graphRevision: result.GraphRevision,
		}
		state.pending = append(state.pending, pending)
	}

	if !stateChanged {
		// First-seen all-no-op batch (storage §9.1). Not reachable for C02's
		// create_node, which always changes state, but the branch is kept for
		// parity with the contract.
		result.GraphRevision = state.currentGraphRevision
	}

	// Allocate the mutation identity and finalize all hashes/rows (storage §9
	// steps 8-13).
	mutationID := s.ids.NextID()
	mutationSeq := state.latestMutationSeq + 1
	result.MutationSequence = mutationSeq
	result.MutationID = mutationID
	result.RecordedAt = recordedAt.Format("2006-01-02T15:04:05.000000000Z07:00")
	result.RequestHash = hex.EncodeToString(requestHash)
	resultBytes, resultHash, resultingStateHash, _, err := s.finalizeAndPersist(
		tx, batch, state, mutationID, mutationSeq, requestHash, result, recordedAt,
	)
	if err != nil {
		return MutationResult{}, err
	}

	result.ResultHash = hex.EncodeToString(resultHash)
	result.ResultingStateHash = hex.EncodeToString(resultingStateHash)
	result.ResultBytes = append([]byte(nil), resultBytes...)

	return result, nil
}

// pendingCreate captures rows that must be inserted once the mutation header
// identity is allocated.
type pendingCreate struct {
	nodeID        string
	nodeType      NodeType
	stableKey     string
	version       int
	propsJSON     []byte
	semHash       []byte
	keySemHash    []byte
	provenanceID  string
	opIndex       int
	opID          string
	graphRevision int
}
type pendingUpdate struct {
	nodeID, stableKey      string
	nodeType               NodeType
	version                int
	propsJSON, semHash     []byte
	opIndex, graphRevision int
}

type pendingEdge struct {
	id                     string
	edgeType               EdgeType
	fromID, toID, summary  string
	semHash                []byte
	opIndex, graphRevision int
}

type pendingEdgeUpdate struct {
	id                     string
	edgeType               EdgeType
	fromID, toID, summary  string
	version                int
	semHash                []byte
	opIndex, graphRevision int
}

type pendingOperationRow struct {
	opJSON, resJSON []byte
	changed         bool
	provID, opID    string
}

func computeResultingStateHash(tx *sql.Tx, projectID, projectKind string, state graphState) ([]byte, error) {
	type nodeHashRecord struct {
		nodeType          NodeType
		stableKey, nodeID string
		semHash           []byte
	}
	nodes := map[string]nodeHashRecord{}
	rows, err := tx.Query(`SELECT h.node_id,h.node_type,n.original_stable_key,h.semantic_hash FROM blackboard_node_heads h JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id WHERE h.project_id=?`, projectID)
	if err != nil {
		return nil, fmt.Errorf("read nodes for state hash: %w", err)
	}
	for rows.Next() {
		var r nodeHashRecord
		var sem string
		if err := rows.Scan(&r.nodeID, &r.nodeType, &r.stableKey, &sem); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.semHash, err = hex.DecodeString(sem)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		nodes[r.nodeID] = r
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for _, p := range state.pending {
		nodes[p.nodeID] = nodeHashRecord{p.nodeType, p.stableKey, p.nodeID, p.semHash}
	}
	for _, p := range state.pendingUpdates {
		nodes[p.nodeID] = nodeHashRecord{p.nodeType, p.stableKey, p.nodeID, p.semHash}
	}
	ordered := make([]nodeHashRecord, 0, len(nodes))
	for _, r := range nodes {
		ordered = append(ordered, r)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].nodeType != ordered[j].nodeType {
			return nodeTypeOrdinal(ordered[i].nodeType) < nodeTypeOrdinal(ordered[j].nodeType)
		}
		if ordered[i].stableKey != ordered[j].stableKey {
			return ordered[i].stableKey < ordered[j].stableKey
		}
		return ordered[i].nodeID < ordered[j].nodeID
	})
	parts := [][]byte{[]byte(projectID), []byte(projectKind), u64Bytes(uint64(store.GraphSchemaVersion))}
	for _, r := range ordered {
		parts = append(parts, u64Bytes(uint64(nodeTypeOrdinal(r.nodeType))), []byte(r.stableKey), []byte(r.nodeID), r.semHash)
	}
	type edgeHashRecord struct {
		edgeType EdgeType
		id       string
		semHash  []byte
	}
	edgesByID := map[string]edgeHashRecord{}
	erows, err := tx.Query(`SELECT edge_id,edge_type,semantic_hash FROM blackboard_edge_heads WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	for erows.Next() {
		var r edgeHashRecord
		var sem string
		if err := erows.Scan(&r.id, &r.edgeType, &sem); err != nil {
			_ = erows.Close()
			return nil, err
		}
		r.semHash, err = hex.DecodeString(sem)
		if err != nil {
			_ = erows.Close()
			return nil, err
		}
		edgesByID[r.id] = r
	}
	if err := erows.Err(); err != nil {
		_ = erows.Close()
		return nil, err
	}
	_ = erows.Close()
	for _, e := range state.pendingEdges {
		edgesByID[e.id] = edgeHashRecord{e.edgeType, e.id, e.semHash}
	}
	for _, e := range state.pendingEdgeUpdates {
		edgesByID[e.id] = edgeHashRecord{e.edgeType, e.id, e.semHash}
	}
	edges := make([]edgeHashRecord, 0, len(edgesByID))
	for _, edge := range edgesByID {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].edgeType != edges[j].edgeType {
			return edgeTypeOrdinal(edges[i].edgeType) < edgeTypeOrdinal(edges[j].edgeType)
		}
		return edges[i].id < edges[j].id
	})
	for _, e := range edges {
		parts = append(parts, u64Bytes(uint64(edgeTypeOrdinal(e.edgeType))), []byte(e.id), e.semHash)
	}
	type keyHashRecord struct {
		nodeType NodeType
		key      string
		semHash  []byte
	}
	keys := map[string]keyHashRecord{}
	keyRows, err := tx.Query(`SELECT node_type,key,semantic_hash FROM blackboard_key_registry WHERE project_id=?`, projectID)
	if err != nil {
		return nil, fmt.Errorf("read keys for state hash: %w", err)
	}
	for keyRows.Next() {
		var record keyHashRecord
		var semanticHash string
		if err := keyRows.Scan(&record.nodeType, &record.key, &semanticHash); err != nil {
			keyRows.Close()
			return nil, err
		}
		record.semHash, err = hex.DecodeString(semanticHash)
		if err != nil {
			keyRows.Close()
			return nil, err
		}
		keys[string(record.nodeType)+"\x00"+record.key] = record
	}
	if err := keyRows.Err(); err != nil {
		keyRows.Close()
		return nil, err
	}
	keyRows.Close()
	for _, pending := range state.pending {
		keys[string(pending.nodeType)+"\x00"+pending.stableKey] = keyHashRecord{nodeType: pending.nodeType, key: pending.stableKey, semHash: pending.keySemHash}
	}
	orderedKeys := make([]keyHashRecord, 0, len(keys))
	for _, record := range keys {
		orderedKeys = append(orderedKeys, record)
	}
	sort.Slice(orderedKeys, func(i, j int) bool {
		if orderedKeys[i].nodeType != orderedKeys[j].nodeType {
			return nodeTypeOrdinal(orderedKeys[i].nodeType) < nodeTypeOrdinal(orderedKeys[j].nodeType)
		}
		return orderedKeys[i].key < orderedKeys[j].key
	})
	for _, key := range orderedKeys {
		parts = append(parts, u64Bytes(uint64(nodeTypeOrdinal(key.nodeType))), []byte(key.key), key.semHash)
	}
	return framedHash("CyberPenda.Blackboard.State.v1", parts...), nil
}

// finalizeAndPersist inserts the mutation header, operation, node identity,
// node version, key event, and rebuilds the materialized heads and graph state.
func (s *GraphService) finalizeAndPersist(
	tx *sql.Tx,
	batch MutationBatch,
	state graphState,
	mutationID string,
	mutationSeq int,
	requestHashRaw []byte,
	result MutationResult,
	recordedAt interface {
		Format(layout string) string
	},
) ([]byte, []byte, []byte, []byte, error) {
	projectID := batch.Context.ProjectID
	baseRev := state.currentGraphRevision
	resultRev := result.GraphRevision
	tsStr := recordedAt.Format("2006-01-02T15:04:05.000000000Z07:00")

	// Operation result JSON per op (operation_json + result_json).
	opRows := make([]pendingOperationRow, len(batch.Operations))
	for i, op := range batch.Operations {
		opJSON, err := canonicalJSON(op)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("encode operation json: %w", err)
		}
		resJSON, err := canonicalJSON(result.Operations[i])
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("encode operation result json: %w", err)
		}
		opRows[i] = pendingOperationRow{opJSON: opJSON, resJSON: resJSON, changed: result.Operations[i].Changed, provID: state.provenanceIDs[i], opID: op.OpID}
	}

	// Canonical request JSON for the ledger.
	requestJSON, err := canonicalJSON(batch.requestLedgerForm())
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("encode request json: %w", err)
	}

	// Semantic state hash covers the complete resulting materialized graph,
	// overlaying this batch's pending creates/updates/edges on current heads.
	resultingStateHash, err := computeResultingStateHash(tx, projectID, batch.Context.ProjectKind, state)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	result.ResultingStateHash = hex.EncodeToString(resultingStateHash)

	// Result JSON is finalized only after all replay-visible server fields and
	// the resulting state hash are known. ResultHash is metadata about these
	// exact bytes and is therefore not recursively embedded in result_json.
	resultJSON, err := canonicalJSON(result.ledgerForm())
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("encode result json: %w", err)
	}
	resultHashSum := sha256.Sum256(resultJSON)
	resultHash := resultHashSum[:]

	// Mutation integrity chain (storage §11.3) covers the complete header and
	// ordered changed-record hashes. The genesis previous hash seeds the
	// per-Project chain.
	prevHash := state.historyHeadHash
	if prevHash == nil {
		prevHash = genesisHash(projectID)
	}
	recordHashes, err := pendingIntegrityHashes(batch, state, opRows, mutationSeq, tsStr)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	mutationHash := computeMutationHash(mutationHashInput{
		ProjectID: projectID, MutationID: mutationID, PreviousHash: prevHash, MutationSeq: mutationSeq,
		BaseRevision: baseRev, ResultRevision: resultRev, SchemaVersion: GraphMutationSchemaVersion,
		MutationKind: string(MutationKindNormal), MaintenanceMetadataJSON: "{}",
		IdempotencyScope: batch.Context.idempotencyScope(), IdempotencyKey: batch.IdempotencyKey,
		RequestHash: requestHashRaw, ResultHash: resultHash, RecordedAt: tsStr,
		ResultingStateHash: resultingStateHash, ProjectionStatus: "dirty",
		RecordHashes: recordHashes,
	})

	// Insert mutation header.
	_, err = tx.Exec(
		`INSERT INTO blackboard_graph_mutations
		 (project_id, mutation_seq, mutation_id, base_graph_revision, result_graph_revision,
		  schema_version, mutation_kind, maintenance_metadata_json, maintenance_subject_id,
		  idempotency_scope, idempotency_key, request_hash, request_json, result_json, result_hash,
		  recorded_at, previous_mutation_hash, mutation_hash, resulting_state_hash,
		  projection_status, resulting_main_projection_hash, projection_renderer_version,
		  projection_estimator_version, projection_bytes, projection_estimated_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '{}', NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'dirty', NULL, '', '', NULL, NULL)`,
		projectID, mutationSeq, mutationID, baseRev, resultRev,
		GraphMutationSchemaVersion, string(MutationKindNormal),
		batch.Context.idempotencyScope(), batch.IdempotencyKey,
		hex.EncodeToString(requestHashRaw), string(requestJSON), string(resultJSON), hex.EncodeToString(resultHash),
		tsStr, hex.EncodeToString(prevHash), hex.EncodeToString(mutationHash), hex.EncodeToString(resultingStateHash),
	)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("insert graph mutation: %w", err)
	}

	// Every requested operation is recorded, including edge operations. Node
	// identity/version rows are emitted only for create_node operations.
	for i, op := range batch.Operations {
		_, err := tx.Exec(
			`INSERT INTO blackboard_graph_operations
			 (project_id, mutation_seq, operation_index, op_id, operation_kind, operation_json, result_json, changed, provenance_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, mutationSeq, i, op.OpID, string(op.Kind), string(opRows[i].opJSON), string(opRows[i].resJSON), boolToInt(opRows[i].changed), opRows[i].provID,
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert graph operation: %w", err)
		}
	}
	// Insert node identities, node versions, key events, and heads.
	for _, p := range state.pending {

		createdAt := tsStr
		_, err = tx.Exec(
			`INSERT INTO blackboard_nodes
			 (project_id, id, node_type, original_stable_key, created_mutation_seq, created_operation_index, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, p.nodeID, string(p.nodeType), p.stableKey, mutationSeq, p.opIndex, createdAt,
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert node identity: %w", err)
		}

		_, err = tx.Exec(
			`INSERT INTO blackboard_node_versions
			 (project_id, node_id, version, result_graph_revision, mutation_seq, operation_index,
			  schema_version, disposition, merge_target_id, properties_json, semantic_hash, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 'main', NULL, ?, ?, ?)`,
			projectID, p.nodeID, p.version, p.graphRevision, mutationSeq, p.opIndex,
			GraphMutationSchemaVersion, string(p.propsJSON), hex.EncodeToString(p.semHash), tsStr,
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert node version: %w", err)
		}

		_, err = tx.Exec(
			`INSERT INTO blackboard_key_events
			 (project_id, node_type, key, key_version, role, source_node_id, canonical_node_id,
			  legacy_nonconforming, result_graph_revision, mutation_seq, operation_index, semantic_hash)
			 VALUES (?, ?, ?, 1, 'stable', ?, ?, 0, ?, ?, ?, ?)`,
			projectID, string(p.nodeType), p.stableKey, p.nodeID, p.nodeID,
			p.graphRevision, mutationSeq, p.opIndex, hex.EncodeToString(p.keySemHash),
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert key event: %w", err)
		}

		// Rebuild materialized heads and registry (storage §9 step 13).
		lifecycle, entity, scope := genericDerivedFields(p.nodeType, p.propsJSON)
		_, err = tx.Exec(
			`INSERT INTO blackboard_node_heads
			 (project_id, node_id, node_type, version, graph_revision, disposition, merge_target_id,
			  lifecycle_state, entity_kind, scope_status, semantic_hash)
			 VALUES (?, ?, ?, ?, ?, 'main', NULL, ?, ?, ?, ?)`,
			projectID, p.nodeID, string(p.nodeType), p.version, p.graphRevision,
			lifecycle, entity, scope, hex.EncodeToString(p.semHash),
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert node head: %w", err)
		}

		_, err = tx.Exec(
			`INSERT INTO blackboard_key_registry
			 (project_id, node_type, key, latest_key_version, role, source_node_id, canonical_node_id, semantic_hash)
			 VALUES (?, ?, ?, 1, 'stable', ?, ?, ?)`,
			projectID, string(p.nodeType), p.stableKey, p.nodeID, p.nodeID, hex.EncodeToString(p.keySemHash),
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert key registry: %w", err)
		}
	}
	for _, p := range state.pendingUpdates {
		_, err = tx.Exec(`INSERT INTO blackboard_node_versions
			(project_id,node_id,version,result_graph_revision,mutation_seq,operation_index,schema_version,disposition,merge_target_id,properties_json,semantic_hash,updated_at)
			VALUES(?,?,?,?,?,?,?,'main',NULL,?,?,?)`, projectID, p.nodeID, p.version, p.graphRevision, mutationSeq, p.opIndex, GraphMutationSchemaVersion, string(p.propsJSON), hex.EncodeToString(p.semHash), tsStr)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert node update version: %w", err)
		}
		lifecycle, entity, scope := genericDerivedFields(p.nodeType, p.propsJSON)
		_, err = tx.Exec(`UPDATE blackboard_node_heads SET version=?,graph_revision=?,disposition='main',merge_target_id=NULL,lifecycle_state=?,entity_kind=?,scope_status=?,semantic_hash=? WHERE project_id=? AND node_id=?`, p.version, p.graphRevision, lifecycle, entity, scope, hex.EncodeToString(p.semHash), projectID, p.nodeID)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("update node head: %w", err)
		}
	}
	for _, e := range state.pendingEdges {
		_, err = tx.Exec(`INSERT INTO blackboard_edges(project_id,id,edge_type,created_mutation_seq,created_operation_index,created_at) VALUES(?,?,?,?,?,?)`, projectID, e.id, string(e.edgeType), mutationSeq, e.opIndex, tsStr)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert edge identity: %w", err)
		}
		_, err = tx.Exec(`INSERT INTO blackboard_edge_versions(project_id,edge_id,version,result_graph_revision,mutation_seq,operation_index,state,summary,semantic_hash,updated_at,from_node_id,to_node_id) VALUES(?,?,1,?,?,?,'active',?,?,?,?,?)`, projectID, e.id, e.graphRevision, mutationSeq, e.opIndex, e.summary, hex.EncodeToString(e.semHash), tsStr, e.fromID, e.toID)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert edge version: %w", err)
		}
		_, err = tx.Exec(`INSERT INTO blackboard_edge_heads(project_id,edge_id,edge_type,from_node_id,to_node_id,version,graph_revision,state,semantic_hash) VALUES(?,?,?,?,?,1,?,'active',?)`, projectID, e.id, string(e.edgeType), e.fromID, e.toID, e.graphRevision, hex.EncodeToString(e.semHash))
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert edge head: %w", err)
		}
	}
	for _, e := range state.pendingEdgeUpdates {
		_, err = tx.Exec(`INSERT INTO blackboard_edge_versions(project_id,edge_id,version,result_graph_revision,mutation_seq,operation_index,state,summary,semantic_hash,updated_at,from_node_id,to_node_id) VALUES(?,?,?,?,?,?,'active',?,?,?,?,?)`, projectID, e.id, e.version, e.graphRevision, mutationSeq, e.opIndex, e.summary, hex.EncodeToString(e.semHash), tsStr, e.fromID, e.toID)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert edge update version: %w", err)
		}
		_, err = tx.Exec(`UPDATE blackboard_edge_heads SET version=?,graph_revision=?,state='active',semantic_hash=? WHERE project_id=? AND edge_id=?`, e.version, e.graphRevision, hex.EncodeToString(e.semHash), projectID, e.id)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("update edge head: %w", err)
		}
	}

	// Update graph_state (storage §9 step 14).
	_, err = tx.Exec(
		`INSERT INTO blackboard_graph_state
		 (project_id, latest_mutation_seq, current_graph_revision, materialized_mutation_seq,
		  history_head_hash, current_semantic_state_hash, current_main_projection_hash,
		  projection_renderer_version, projection_estimator_version, projection_bytes,
		  projection_estimated_tokens, budget_state, projection_dirty_revision, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, '', '', NULL, NULL, 'unknown', ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   latest_mutation_seq = excluded.latest_mutation_seq,
		   current_graph_revision = excluded.current_graph_revision,
		   materialized_mutation_seq = excluded.materialized_mutation_seq,
		   history_head_hash = excluded.history_head_hash,
		   current_semantic_state_hash = excluded.current_semantic_state_hash,
		   budget_state = excluded.budget_state,
		   projection_dirty_revision = excluded.projection_dirty_revision,
		   updated_at = excluded.updated_at`,
		projectID, mutationSeq, resultRev, mutationSeq,
		hex.EncodeToString(mutationHash), hex.EncodeToString(resultingStateHash),
		resultRev, tsStr,
	)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("upsert graph state: %w", err)
	}

	return resultJSON, resultHash, resultingStateHash, mutationHash, nil
}

// requestLedgerForm returns the batch in the canonical form hashed/serialized
// for the ledger, excluding server-generated IDs and timestamps.
func (b MutationBatch) requestLedgerForm() any {
	return struct {
		SchemaVersion      int                 `json:"schema_version"`
		IdempotencyKey     string              `json:"idempotency_key"`
		ProjectID          string              `json:"project_id"`
		ActorType          ActorType           `json:"actor_type"`
		ActorID            string              `json:"actor_id"`
		TaskID             string              `json:"task_id,omitempty"`
		ContinuationID     string              `json:"continuation_id,omitempty"`
		RuntimeProfileID   string              `json:"runtime_profile_id,omitempty"`
		Runner             string              `json:"runner,omitempty"`
		Operations         []Operation         `json:"operations"`
		SourceEventIDsByOp map[string][]string `json:"source_event_ids_by_op,omitempty"`
	}{b.SchemaVersion, b.IdempotencyKey, b.Context.ProjectID, b.Context.ActorType, b.Context.ActorID, b.Context.TaskID, b.Context.ContinuationID, b.Context.RuntimeProfileID, b.Context.Runner, b.Operations, b.SourceEventIDsByOp}
}

// ledgerForm returns the result in the canonical form stored in result_json.
func (r MutationResult) ledgerForm() resultLedgerForm {
	ops := make([]operationResultLedgerForm, len(r.Operations))
	for i, o := range r.Operations {
		ops[i] = operationResultLedgerForm{
			OpID: o.OpID, NodeID: o.NodeID, NodeType: o.NodeType, StableKey: o.StableKey,
			NodeVersion: o.NodeVersion, EdgeID: o.EdgeID, EdgeType: o.EdgeType, EdgeVersion: o.EdgeVersion, SemanticHash: o.SemanticHash, Changed: o.Changed,
		}
	}
	return resultLedgerForm{
		MutationSequence: r.MutationSequence, MutationID: r.MutationID, RecordedAt: r.RecordedAt,
		GraphRevision: r.GraphRevision, RequestHash: r.RequestHash,
		ResultingStateHash: r.ResultingStateHash, Operations: ops,
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sortPendingByKey(p []pendingCreate) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0; j-- {
			if compareKey(p[j].nodeType, p[j].stableKey, p[j-1].nodeType, p[j-1].stableKey) < 0 {
				p[j], p[j-1] = p[j-1], p[j]
			} else {
				break
			}
		}
	}
}

func compareKey(tA NodeType, kA string, tB NodeType, kB string) int {
	if tA != tB {
		return nodeTypeOrdinal(tA) - nodeTypeOrdinal(tB)
	}
	if kA < kB {
		return -1
	}
	if kA > kB {
		return 1
	}
	return 0
}

// assertProjectExists revalidates the trusted context's Project inside the
// transaction (storage §9 step 3). The context's project_kind is checked
// against the stored kind to detect drift; a missing project is
// project_not_found.
func assertProjectExists(tx *sql.Tx, projectID, expectedKind string) error {
	var kind string
	err := tx.QueryRow(`SELECT kind FROM projects WHERE id = ?`, projectID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return validationError(ErrCodeProjectNotFound,
			fmt.Sprintf("project %q does not exist", projectID), -1, "", "context.project_id")
	}
	if err != nil {
		return fmt.Errorf("read project kind: %w", err)
	}
	if expectedKind != "" && kind != expectedKind {
		return validationError(ErrCodeProjectMismatch,
			fmt.Sprintf("project kind drift: context says %q, stored %q", expectedKind, kind), -1, "", "context.project_kind")
	}
	return nil
}

// graphState carries the loaded/current per-Project graph state plus the
// pending rows staged for insert within the current Apply transaction.
type graphState struct {
	latestMutationSeq    int
	currentGraphRevision int
	historyHeadHash      []byte
	pending              []pendingCreate
	pendingUpdates       []pendingUpdate
	pendingEdges         []pendingEdge
	pendingEdgeUpdates   []pendingEdgeUpdate
	provenanceIDs        []string
}

func loadGraphState(tx *sql.Tx, projectID string) (graphState, error) {
	var s graphState
	var historyHash, stateHash sql.NullString
	var matSeq, graphRev int
	err := tx.QueryRow(
		`SELECT latest_mutation_seq, current_graph_revision, materialized_mutation_seq,
		        history_head_hash, current_semantic_state_hash
		   FROM blackboard_graph_state WHERE project_id = ?`,
		projectID,
	).Scan(&s.latestMutationSeq, &s.currentGraphRevision, &matSeq, &historyHash, &stateHash)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil // first mutation for this project
	}
	if err != nil {
		return s, fmt.Errorf("load graph state: %w", err)
	}
	_ = graphRev
	if historyHash.Valid {
		h, err := hex.DecodeString(historyHash.String)
		if err != nil {
			return s, fmt.Errorf("decode history head hash: %w", err)
		}
		s.historyHeadHash = h
	}
	return s, nil
}

func validateGoalProjectionSource(tx *sql.Tx, projectID, stableKey string, props map[string]any) *ValidationError {
	taskID, _ := props["task_id"].(string)
	var text, status string
	if err := tx.QueryRow(`SELECT goal,status FROM tasks WHERE id=? AND project_id=?`, taskID, projectID).Scan(&text, &status); err != nil {
		return validationError(ErrCodeInvariantViolation, "Goal source Task is missing", -1, "", "properties.task_id")
	}
	if stableKey != "task:"+taskID+":goal" || props["text"] != text || props["task_status"] != status {
		return validationError(ErrCodeInvariantViolation, "Goal projection must exactly match durable Task state", -1, "", "properties")
	}
	return nil
}

type mutableNode struct {
	nodeID    string
	nodeType  NodeType
	stableKey string
	version   int
	props     map[string]any
	semHash   string
}

func loadMutableNode(tx *sql.Tx, projectID string, ref NodeRef) (mutableNode, error) {
	var n mutableNode
	var propsJSON string
	var row *sql.Row
	if ref.ID != "" {
		row = tx.QueryRow(`SELECT h.node_id,h.node_type,n.original_stable_key,h.version,v.properties_json,h.semantic_hash FROM blackboard_node_heads h JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version WHERE h.project_id=? AND h.node_id=?`, projectID, ref.ID)
	} else {
		row = tx.QueryRow(`SELECT h.node_id,h.node_type,n.original_stable_key,h.version,v.properties_json,h.semantic_hash FROM blackboard_key_registry k JOIN blackboard_node_heads h ON h.project_id=k.project_id AND h.node_id=k.canonical_node_id JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version WHERE k.project_id=? AND k.node_type=? AND k.key=?`, projectID, string(ref.NodeType), ref.StableKey)
	}
	if err := row.Scan(&n.nodeID, &n.nodeType, &n.stableKey, &n.version, &propsJSON, &n.semHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return n, validationError(ErrCodeNodeNotFound, "node does not exist", -1, "", "operations[].node")
		}
		return n, fmt.Errorf("load mutable node: %w", err)
	}
	if err := json.Unmarshal([]byte(propsJSON), &n.props); err != nil {
		return n, fmt.Errorf("decode mutable node: %w", err)
	}
	return n, nil
}

func clonePropertyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func hasIncomingEdge(tx *sql.Tx, projectID, nodeID string, edgeType EdgeType, batch MutationBatch, proposed map[string][2]resolvedNode) (bool, error) {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM blackboard_edge_heads WHERE project_id=? AND edge_type=? AND to_node_id=? AND state='active'`, projectID, string(edgeType), nodeID).Scan(&count); err != nil {
		return false, fmt.Errorf("check incoming edge: %w", err)
	}
	if count > 0 {
		return true, nil
	}
	for _, op := range batch.Operations {
		if op.Kind != OpPutEdge || op.PutEdge.EdgeType != edgeType {
			continue
		}
		pair := proposed[op.OpID]
		if pair[1].nodeID == nodeID {
			return true, nil
		}
	}
	return false, nil
}

// keyIsLive reports whether the given key is already a live stable key or
// alias in the Project's key registry (graph contract §4, storage §7.4).
func keyIsLive(tx *sql.Tx, projectID string, nodeType NodeType, key string) (bool, error) {
	var n int
	err := tx.QueryRow(
		`SELECT COUNT(*) FROM blackboard_key_registry WHERE project_id = ? AND node_type = ? AND key = ?`,
		projectID, string(nodeType), key,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check key liveness: %w", err)
	}
	return n > 0, nil
}

// checkIdempotency implements storage §9 step 5: an identical request returns
// the exact stored result, while a hash mismatch returns idempotency_conflict.
func (s *GraphService) checkIdempotency(tx *sql.Tx, projectID, scope, key string, requestHash []byte) (*MutationResult, error) {
	var storedHash, storedResultJSON, storedResultHash string
	err := tx.QueryRow(
		`SELECT request_hash, result_json, result_hash FROM blackboard_graph_mutations
		  WHERE project_id = ? AND idempotency_scope = ? AND idempotency_key = ?`,
		projectID, scope, key,
	).Scan(&storedHash, &storedResultJSON, &storedResultHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check idempotency: %w", err)
	}
	if storedHash != hex.EncodeToString(requestHash) {
		return nil, validationError(ErrCodeIdempotencyConflict,
			"idempotency key reused with a different request payload", -1, "", "idempotency_key")
	}
	res, err := decodeResultJSON(storedResultJSON)
	if err != nil {
		return nil, err
	}
	res.ResultHash = storedResultHash
	return res, nil
}

// validateProjectFactProperties checks the closed project_fact property set
// (graph contract §5.7).
func validateProjectFactProperties(p ProjectFactProperties) *ValidationError {
	if p.Category == "" {
		return validationError(ErrCodeMissingProperty, "project_fact.category is required", -1, "", "properties.category")
	}
	if p.Summary == "" {
		return validationError(ErrCodeMissingProperty, "project_fact.summary is required", -1, "", "properties.summary")
	}
	switch p.Confidence {
	case "", ConfidenceTentative, ConfidenceConfirmed, ConfidenceDeprecated:
	default:
		return validationError(ErrCodeInvalidProperty, "project_fact.confidence must be tentative, confirmed, or deprecated", -1, "", "properties.confidence")
	}
	switch p.ScopeStatus {
	case ScopeStatusInScope, ScopeStatusOutOfScope, ScopeStatusUnknown:
	default:
		return validationError(ErrCodeInvalidProperty, "project_fact.scope_status must be in_scope, out_of_scope, or unknown", -1, "", "properties.scope_status")
	}
	return nil
}

// normalizeProjectFactProperties applies the graph-contract default for
// confidence only (§5.7: "Required, default tentative"). scope_status is
// required with no default, so it is validated, not defaulted.
func normalizeProjectFactProperties(p ProjectFactProperties) ProjectFactProperties {
	out := p
	if out.Confidence == "" {
		out.Confidence = ConfidenceTentative
	}
	return out
}

// projectFactSemanticHash covers disposition, merge target, and normalized
// type-specific properties (storage §6.6: "The semantic hash covers
// disposition, merge target, and normalized type-specific properties"). It
// excludes immutable identity (node type, stable key), timestamps, and
// provenance so exact no-op updates produce no new version.
func projectFactSemanticHash(disposition Disposition, mergeTarget string, props ProjectFactProperties) []byte {
	propsJSON, err := canonicalJSON(props)
	if err != nil {
		// canonicalJSON of a struct should not fail; fall back to stable repr.
		propsJSON = []byte(props.Summary)
	}
	return framedHash("CyberPenda.Blackboard.NodeSemantic.v1",
		u64Bytes(uint64(dispositionOrdinal(string(disposition)))), nullableBytes(mergeTarget != "", []byte(mergeTarget)), propsJSON)
}

func genericNodeSemanticHash(disposition Disposition, mergeTarget string, props map[string]any) []byte {
	b, _ := canonicalJSON(props)
	return framedHash("CyberPenda.Blackboard.NodeSemantic.v1", u64Bytes(uint64(dispositionOrdinal(string(disposition)))), nullableBytes(mergeTarget != "", []byte(mergeTarget)), b)
}

func edgeSemanticHash(edgeType EdgeType, fromNodeID, toNodeID, state, summary string) []byte {
	return framedHash("CyberPenda.Blackboard.EdgeSemantic.v1", u64Bytes(uint64(edgeTypeOrdinal(edgeType))), []byte(fromNodeID), []byte(toNodeID), u64Bytes(uint64(edgeStateOrdinal(state))), []byte(summary))
}

func keySemanticHash(nodeType NodeType, key, role, sourceNodeID, canonicalNodeID string, legacyNonconforming bool) []byte {
	return framedHash("CyberPenda.Blackboard.KeySemantic.v1",
		u64Bytes(uint64(nodeTypeOrdinal(nodeType))), []byte(key), u64Bytes(uint64(keyRoleOrdinal(role))),
		[]byte(sourceNodeID), []byte(canonicalNodeID), u64Bytes(boolOrdinal(legacyNonconforming)))
}

// projectFactDerivedFields extracts the head-table derived lifecycle_state,
// entity_kind, and scope_status from the node's properties JSON (storage §7.2).
func projectFactDerivedFields(propsJSON []byte) (lifecycle, entity, scope string) {
	var props ProjectFactProperties
	_ = jsonUnmarshalProps(propsJSON, &props)
	return string(props.Confidence), "", string(props.ScopeStatus)
}

func genericDerivedFields(t NodeType, propsJSON []byte) (lifecycle, entity, scope string) {
	var p map[string]any
	_ = jsonUnmarshalProps(propsJSON, &p)
	if v, ok := p["status"].(string); ok {
		lifecycle = v
	}
	if t == NodeTypeProjectFact {
		if v, ok := p["confidence"].(string); ok {
			lifecycle = v
		}
	}
	if v, ok := p["kind"].(string); ok && t == NodeTypeEntity {
		entity = v
	}
	if v, ok := p["scope_status"].(string); ok {
		scope = v
	}
	return
}

// ComputeRequestHashForTesting exposes the canonical request hash for tests
// that prove determinism without driving a full Apply. It is the same function
// the service uses internally.
func ComputeRequestHashForTesting(batch MutationBatch) (string, error) {
	h, err := computeRequestHash(batch)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h), nil
}

// computeRequestHash canonicalizes the batch excluding server-generated fields
// and returns SHA-256 of those canonical bytes (storage §6.1 request_hash:
// "SHA-256 of the normalized batch plus trusted maintenance metadata,
// excluding server-generated IDs/timestamps").
func computeRequestHash(batch MutationBatch) ([]byte, error) {
	body, err := canonicalJSON(batch.requestLedgerForm())
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(body)
	return h[:], nil
}

// insertProvenance records one immutable provenance row (storage §6.2),
// including the trusted Runtime Task/Continuation/profile/runner binding.
func insertProvenance(tx *sql.Tx, projectID, provenanceID string, ec ExecutionContext, recordedAt string) error {
	var taskID, contID, profileID, runner any
	if ec.TaskID != "" {
		taskID = ec.TaskID
	}
	if ec.ContinuationID != "" {
		contID = ec.ContinuationID
	}
	if ec.RuntimeProfileID != "" {
		profileID = ec.RuntimeProfileID
	}
	if ec.Runner != "" {
		runner = ec.Runner
	}
	_, err := tx.Exec(
		`INSERT INTO blackboard_graph_provenance
		 (project_id, id, actor_type, actor_id, task_id, continuation_id, runtime_profile_id, runner, migration_source_json, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		projectID, provenanceID, string(ec.ActorType), ec.ActorID, taskID, contID, profileID, runner, recordedAt,
	)
	if err != nil {
		return fmt.Errorf("insert provenance: %w", err)
	}
	return nil
}

func validateSourceEvents(tx *sql.Tx, batch MutationBatch, operations map[string]Operation) error {
	for opID, eventIDs := range batch.SourceEventIDsByOp {
		if _, ok := operations[opID]; !ok {
			return validationError(ErrCodeInvalidRequest, "source Task Event mapping references an unknown op_id", -1, opID, "source_event_ids_by_op")
		}
		if len(eventIDs) > 32 {
			return validationError(ErrCodeInvalidRequest, "an operation may reference at most 32 source Task Events", -1, opID, "source_event_ids_by_op")
		}
		seen := make(map[string]struct{}, len(eventIDs))
		for _, eventID := range eventIDs {
			if eventID == "" {
				return validationError(ErrCodeInvalidRequest, "source Task Event ID is empty", -1, opID, "source_event_ids_by_op")
			}
			if _, duplicate := seen[eventID]; duplicate {
				return validationError(ErrCodeInvalidRequest, "source Task Event IDs must be ordered and deduplicated", -1, opID, "source_event_ids_by_op")
			}
			seen[eventID] = struct{}{}
			var taskID string
			var continuationID sql.NullString
			if err := tx.QueryRow(`SELECT task_id,continuation_id FROM task_events WHERE id=?`, eventID).Scan(&taskID, &continuationID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return validationError(ErrCodeProvenanceSpoofed, "source Task Event does not exist", -1, opID, "source_event_ids_by_op")
				}
				return fmt.Errorf("validate source Task Event: %w", err)
			}
			if batch.Context.TaskID == "" || taskID != batch.Context.TaskID {
				return validationError(ErrCodeProvenanceSpoofed, "source Task Event belongs to another Task", -1, opID, "source_event_ids_by_op")
			}
			if batch.Context.ActorType == ActorTypeRuntime && continuationID.Valid && continuationID.String != batch.Context.ContinuationID {
				return validationError(ErrCodeProvenanceSpoofed, "source Task Event belongs to another Continuation", -1, opID, "source_event_ids_by_op")
			}
		}
	}
	return nil
}
