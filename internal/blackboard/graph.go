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
	"errors"
	"fmt"
	"regexp"

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
func (s *GraphService) Apply(ctx context.Context, batch MutationBatch) (MutationResult, error) {
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
		return MutationResult{}, fmt.Errorf("begin graph transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Revalidate trusted context inside the transaction (storage §9 step 3).
	if err := assertProjectExists(tx, batch.Context.ProjectID, batch.Context.ProjectKind); err != nil {
		return MutationResult{}, err
	}

	// Idempotency: same scope/key/hash returns the stored result; different
	// hash returns idempotency_conflict (storage §9 step 5). Full exact-replay
	// semantics land in C07.
	if stored, err := s.checkIdempotency(tx, projectID, scope, batch.IdempotencyKey, requestHash); err != nil {
		return MutationResult{}, err
	} else if stored != nil {
		return *stored, nil
	}

	result, err := s.applyOperations(tx, batch, requestHash)
	if err != nil {
		return MutationResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return MutationResult{}, fmt.Errorf("commit graph transaction: %w", err)
	}
	return result, nil
}

// applyOperations validates the batch, applies it, and returns the result.
func (s *GraphService) applyOperations(tx *sql.Tx, batch MutationBatch, requestHash []byte) (MutationResult, error) {
	projectID := batch.Context.ProjectID

	// Validate every operation against the closed envelope before allocating
	// any IDs or writing any row (storage §9 steps 6-8).
	for i, op := range batch.Operations {
		if op.OpID == "" {
			return MutationResult{}, validationError(ErrCodeInvalidRequest, "op_id is required", i, "", "operations[].op_id")
		}
		if op.Kind != OpCreateNode {
			return MutationResult{}, validationError(ErrCodeInvalidRequest,
				fmt.Sprintf("operation kind %q is not implemented in C02", op.Kind), i, op.OpID, "operations[].kind")
		}
		if op.Node.NodeType != NodeTypeProjectFact {
			return MutationResult{}, validationError(ErrCodeUnknownNodeType,
				fmt.Sprintf("C02 create_node supports project_fact, got %q", op.Node.NodeType), i, op.OpID, "operations[].node_type")
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
		if err := validateProjectFactProperties(op.Create.Properties); err != nil {
			err.OperationIndex = i
			err.OpID = op.OpID
			return MutationResult{}, err
		}
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
	for i, op := range batch.Operations {
		// Key uniqueness across live keys and aliases (graph §4, storage §7.4).
		conflict, err := keyIsLive(tx, projectID, op.Node.NodeType, op.Node.StableKey)
		if err != nil {
			return MutationResult{}, err
		}
		if conflict {
			return MutationResult{}, validationError(ErrCodeNodeKeyConflict,
				fmt.Sprintf("stable key %q is already live or reserved by an alias", op.Node.StableKey), i, op.OpID, "operations[].stable_key")
		}

		props := normalizeProjectFactProperties(op.Create.Properties)
		nodeID := s.ids.NextID()
		provenanceID := s.ids.NextID()

		propsJSON, err := canonicalJSON(props)
		if err != nil {
			return MutationResult{}, fmt.Errorf("encode project_fact properties: %w", err)
		}
		semHash := projectFactSemanticHash(DispositionMain, "", props)

		// A create always changes current semantic state: new node, version 1.
		if !stateChanged {
			result.GraphRevision = state.currentGraphRevision + 1
			stateChanged = true
		}
		nodeVersion := 1

		if err := insertProvenance(tx, projectID, provenanceID, batch.Context, recordedAt.Format("2006-01-02T15:04:05.000000000Z07:00")); err != nil {
			return MutationResult{}, err
		}

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
	resultBytes, resultHash, resultingStateHash, _, err := s.finalizeAndPersist(
		tx, batch, state, mutationID, mutationSeq, requestHash, result, recordedAt,
	)
	if err != nil {
		return MutationResult{}, err
	}

	result.RequestHash = hex.EncodeToString(decodeOrEmpty(requestHash))
	result.ResultHash = hex.EncodeToString(resultHash)
	result.ResultingStateHash = hex.EncodeToString(resultingStateHash)
	_ = resultBytes

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
	provenanceID  string
	opIndex       int
	opID          string
	graphRevision int
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
	type opRow struct {
		opJSON  []byte
		resJSON []byte
		changed bool
		provID  string
		opID    string
	}
	opRows := make([]opRow, len(batch.Operations))
	for i, op := range batch.Operations {
		opJSON, err := canonicalJSON(op)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("encode operation json: %w", err)
		}
		resJSON, err := canonicalJSON(result.Operations[i])
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("encode operation result json: %w", err)
		}
		opRows[i] = opRow{opJSON: opJSON, resJSON: resJSON, changed: result.Operations[i].Changed, provID: state.pending[i].provenanceID, opID: op.OpID}
	}

	// Canonical request JSON for the ledger.
	requestJSON, err := canonicalJSON(batch.requestLedgerForm())
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("encode request json: %w", err)
	}

	// Result JSON (the canonical MutationResult stored for replay).
	resultJSON, err := canonicalJSON(result.ledgerForm())
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("encode result json: %w", err)
	}
	// result_hash is SHA-256 of the canonical result_json bytes (storage §6.1).
	resultHashSum := sha256.Sum256(resultJSON)
	resultHash := resultHashSum[:]

	// Semantic state hash (storage §11.3): identities + current semantic_hash
	// values for nodes, edges, keys. C02 has nodes and keys only.
	var stateParts [][]byte
	stateParts = append(stateParts, []byte(batch.Context.ProjectKind))
	stateParts = append(stateParts, u64Bytes(uint64(store.GraphSchemaVersion)))
	for _, p := range state.pending {
		// Nodes use type ordinal/stable key/ID ordering.
		stateParts = append(stateParts, u64Bytes(uint64(nodeTypeOrdinal(p.nodeType))))
		stateParts = append(stateParts, []byte(p.stableKey))
		stateParts = append(stateParts, []byte(p.nodeID))
		stateParts = append(stateParts, p.semHash)
	}
	// Keys use node type/key ordering.
	sortedKeys := make([]pendingCreate, len(state.pending))
	copy(sortedKeys, state.pending)
	sortPendingByKey(sortedKeys)
	for _, p := range sortedKeys {
		stateParts = append(stateParts, u64Bytes(uint64(nodeTypeOrdinal(p.nodeType))))
		stateParts = append(stateParts, []byte(p.stableKey))
		stateParts = append(stateParts, p.semHash)
	}
	resultingStateHash := framedHash("CyberPenda.Blackboard.State.v1", stateParts...)

	// Mutation integrity chain (storage §11.3). C02 covers the mutation-header
	// fields only; the ordered changed-record integrity hashes (provenance,
	// operation, node identity/version, key event — §11.3 ordinals 1-9) are
	// added by C07 when full replay and reconstruction land. The genesis
	// previous hash seeds the per-Project chain.
	prevHash := state.historyHeadHash
	if prevHash == nil {
		prevHash = genesisHash(projectID)
	}
	mutationHash := framedHash("CyberPenda.Blackboard.Mutation.v1",
		[]byte(projectID), []byte(mutationID), prevHash, u64Bytes(uint64(mutationSeq)),
		u64Bytes(uint64(baseRev)), u64Bytes(uint64(resultRev)),
		u64Bytes(uint64(GraphMutationSchemaVersion)), []byte(MutationKindNormal),
		nullableBytes(false, nil), // maintenance_subject_id
		[]byte("{}"),              // maintenance_metadata_json
		[]byte(batch.Context.idempotencyScope()), []byte(batch.IdempotencyKey),
		requestHashRaw, resultHash, []byte(tsStr),
		resultingStateHash, []byte("dirty"), // projection_status: C02 marks dirty
		[]byte(""),                // renderer version
		[]byte(""),                // estimator version
		nullableBytes(false, nil), // main projection hash
		nullableBytes(false, nil), // projection bytes
		nullableBytes(false, nil), // projection tokens
	)

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

	// Insert operations, node identities, node versions, key events, heads.
	for i, p := range state.pending {
		_, err := tx.Exec(
			`INSERT INTO blackboard_graph_operations
			 (project_id, mutation_seq, operation_index, op_id, operation_kind, operation_json, result_json, changed, provenance_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, mutationSeq, p.opIndex, p.opID, string(OpCreateNode), string(opRows[i].opJSON), string(opRows[i].resJSON), boolToInt(opRows[i].changed), p.provenanceID,
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert graph operation: %w", err)
		}

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
			p.graphRevision, mutationSeq, p.opIndex, hex.EncodeToString(p.semHash),
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert key event: %w", err)
		}

		// Rebuild materialized heads and registry (storage §9 step 13).
		lifecycle, entity, scope := projectFactDerivedFields(p.propsJSON)
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
			projectID, string(p.nodeType), p.stableKey, p.nodeID, p.nodeID, hex.EncodeToString(p.semHash),
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("insert key registry: %w", err)
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
	type opForm struct {
		OpID       string                `json:"op_id"`
		Kind       OperationKind         `json:"kind"`
		NodeType   NodeType              `json:"node_type"`
		StableKey  string                `json:"stable_key"`
		Properties ProjectFactProperties `json:"properties"`
	}
	ops := make([]opForm, len(b.Operations))
	for i, op := range b.Operations {
		ops[i] = opForm{
			OpID: op.OpID, Kind: op.Kind, NodeType: op.Node.NodeType, StableKey: op.Node.StableKey,
			Properties: normalizeProjectFactProperties(op.Create.Properties),
		}
	}
	return struct {
		SchemaVersion  int       `json:"schema_version"`
		IdempotencyKey string    `json:"idempotency_key"`
		ProjectID      string    `json:"project_id"`
		ActorType      ActorType `json:"actor_type"`
		ActorID        string    `json:"actor_id"`
		Operations     []opForm  `json:"operations"`
	}{
		SchemaVersion: b.SchemaVersion, IdempotencyKey: b.IdempotencyKey,
		ProjectID: b.Context.ProjectID, ActorType: b.Context.ActorType, ActorID: b.Context.ActorID,
		Operations: ops,
	}
}

// ledgerForm returns the result in the canonical form stored in result_json.
func (r MutationResult) ledgerForm() resultLedgerForm {
	ops := make([]operationResultLedgerForm, len(r.Operations))
	for i, o := range r.Operations {
		ops[i] = operationResultLedgerForm{
			OpID: o.OpID, NodeID: o.NodeID, NodeType: o.NodeType, StableKey: o.StableKey,
			NodeVersion: o.NodeVersion, SemanticHash: o.SemanticHash, Changed: o.Changed,
		}
	}
	return resultLedgerForm{
		GraphRevision: r.GraphRevision, RequestHash: r.RequestHash, ResultHash: r.ResultHash,
		ResultingStateHash: r.ResultingStateHash, Operations: ops,
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func decodeOrEmpty(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
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

// checkIdempotency implements storage §9 step 5 for first-seen matching. C02
// returns the stored result when an identical request hash was already
// recorded for this scope/key, and idempotency_conflict on a hash mismatch.
// Full exact-replay byte comparison lands in C07.
func (s *GraphService) checkIdempotency(tx *sql.Tx, projectID, scope, key string, requestHash []byte) (*MutationResult, error) {
	var storedHash, storedResultJSON string
	err := tx.QueryRow(
		`SELECT request_hash, result_json FROM blackboard_graph_mutations
		  WHERE project_id = ? AND idempotency_scope = ? AND idempotency_key = ?`,
		projectID, scope, key,
	).Scan(&storedHash, &storedResultJSON)
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
		[]byte(disposition), nullableBytes(mergeTarget != "", []byte(mergeTarget)), propsJSON)
}

// projectFactDerivedFields extracts the head-table derived lifecycle_state,
// entity_kind, and scope_status from the node's properties JSON (storage §7.2).
func projectFactDerivedFields(propsJSON []byte) (lifecycle, entity, scope string) {
	var props ProjectFactProperties
	_ = jsonUnmarshalProps(propsJSON, &props)
	return string(props.Confidence), "", string(props.ScopeStatus)
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

// insertProvenance records one immutable provenance row (storage §6.2). C02
// wires only the non-Runtime fields; Runtime binding (task_id,
// continuation_id, runtime_profile_id, runner) arrives in I01.
func insertProvenance(tx *sql.Tx, projectID, provenanceID string, ec ExecutionContext, recordedAt string) error {
	var taskID, contID, profileID, runner any
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
