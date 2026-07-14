package blackboard

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// LegacyImportPlanV1 is the sealed, in-process migration input described by
// the migration contract. It is deliberately accepted only together with an
// already-open transaction and is not part of MutationBatch's JSON envelope.
type LegacyImportPlanV1 struct {
	ProjectID      string
	ProjectKind    string
	SourceDigest   string
	PlanDigest     string
	IdempotencyKey string
	Nodes          []LegacyImportNodeV1
	Aliases        []LegacyImportAliasV1
	Edges          []LegacyImportEdgeV1
	Merges         []LegacyImportMergeV1
}

type LegacyImportNodeV1 struct {
	OperationID string
	ID          string
	NodeType    NodeType
	StableKey   string
	Disposition Disposition
	CreatedAt   string
	Versions    []LegacyImportNodeVersionV1
	Sources     []LegacyImportSourceV1
}

type LegacyImportNodeVersionV1 struct {
	Version    int
	Properties map[string]any
	UpdatedAt  string
}

type LegacyImportAliasV1 struct {
	NodeType            NodeType
	Key                 string
	CanonicalNodeID     string
	LegacyNonconforming bool
	Source              LegacyImportSourceV1
}

type LegacyImportEdgeV1 struct {
	OperationID string
	ID          string
	EdgeType    EdgeType
	FromNodeID  string
	ToNodeID    string
	Summary     string
	CreatedAt   string
	UpdatedAt   string
	Source      LegacyImportSourceV1
}

type LegacyImportMergeV1 struct {
	OperationID              string
	SourceNodeID             string
	CanonicalNodeID          string
	SourceExpectedVersion    int
	CanonicalExpectedVersion int
	Source                   LegacyImportSourceV1
	MergedAt                 string
}

type LegacyImportSourceV1 struct {
	Table     string `json:"table"`
	PrimaryID string `json:"primary_id"`
	Key       string `json:"key,omitempty"`
	Version   *int   `json:"version,omitempty"`
}

type migrationImportBatch struct {
	digest     string
	operations map[string]*migrationImportOperation
	aliases    []migrationImportAlias
}

type migrationImportOperation struct {
	node     *migrationImportNode
	edge     *migrationImportEdge
	sources  []LegacyImportSourceV1
	mergedAt string
}

type migrationImportEdge struct {
	id                   string
	createdAt, updatedAt string
}

type migrationImportNode struct {
	id          string
	disposition Disposition
	createdAt   string
	versions    []pendingImportedVersion
}

type migrationImportAlias struct {
	nodeType            NodeType
	key                 string
	canonicalNodeID     string
	legacyNonconforming bool
	opIndex             int
}

// ApplyLegacyImportPlan expands a sealed import plan into the ordinary graph
// operation pipeline while keeping transaction ownership with the migration
// coordinator. The caller commits or rolls back the supplied transaction.
func (s *GraphService) ApplyLegacyImportPlan(ctx context.Context, tx *sql.Tx, plan LegacyImportPlanV1) (MutationResult, error) {
	if tx == nil {
		return MutationResult{}, fmt.Errorf("migration transaction is required")
	}
	if plan.ProjectID == "" || plan.ProjectKind == "" || plan.PlanDigest == "" {
		return MutationResult{}, validationError(ErrCodeInvalidRequest, "sealed legacy import plan is incomplete", -1, "", "legacy_import_plan")
	}
	metadata := &migrationImportBatch{
		digest:     plan.PlanDigest,
		operations: make(map[string]*migrationImportOperation, len(plan.Nodes)+len(plan.Edges)+len(plan.Merges)),
	}
	operations := make([]Operation, 0, len(plan.Nodes)+len(plan.Edges)+len(plan.Merges))
	nodeOperation := make(map[string]int, len(plan.Nodes))
	nodeOperationID := make(map[string]string, len(plan.Nodes))
	for _, node := range plan.Nodes {
		if node.OperationID == "" || node.ID == "" || len(node.Versions) == 0 {
			return MutationResult{}, validationError(ErrCodeInvalidRequest, "legacy import node is incomplete", -1, node.OperationID, "legacy_import_plan.nodes")
		}
		versions := make([]pendingImportedVersion, 0, len(node.Versions))
		disposition := node.Disposition
		if disposition == "" {
			disposition = DispositionMain
		}
		for _, version := range node.Versions {
			if version.Version <= 0 {
				return MutationResult{}, validationError(ErrCodeInvalidRequest, "legacy import version must be positive", -1, node.OperationID, "legacy_import_plan.nodes.versions")
			}
			if err := validateNodeProperties(node.NodeType, version.Properties); err != nil {
				return MutationResult{}, err
			}
			propertiesJSON, err := canonicalJSON(version.Properties)
			if err != nil {
				return MutationResult{}, err
			}
			versions = append(versions, pendingImportedVersion{
				version: version.Version, propsJSON: propertiesJSON,
				semHash: genericNodeSemanticHash(disposition, "", version.Properties), disposition: disposition, updatedAt: version.UpdatedAt,
			})
		}
		current := node.Versions[len(node.Versions)-1].Properties
		nodeOperation[node.ID] = len(operations)
		nodeOperationID[node.ID] = node.OperationID
		operations = append(operations, Operation{
			OpID: node.OperationID, Kind: OpCreateNode,
			Node:   NodeRef{NodeType: node.NodeType, StableKey: node.StableKey},
			Create: CreateNodeInput{PropertyMap: current},
		})
		metadata.operations[node.OperationID] = &migrationImportOperation{
			node:    &migrationImportNode{id: node.ID, disposition: disposition, createdAt: node.CreatedAt, versions: versions},
			sources: append([]LegacyImportSourceV1(nil), node.Sources...),
		}
	}
	for _, edge := range plan.Edges {
		fromOperationID, fromPending := nodeOperationID[edge.FromNodeID]
		toOperationID, toPending := nodeOperationID[edge.ToNodeID]
		from := NodeRef{ID: edge.FromNodeID}
		to := NodeRef{ID: edge.ToNodeID}
		if fromPending {
			from = NodeRef{OpID: fromOperationID}
		}
		if toPending {
			to = NodeRef{OpID: toOperationID}
		}
		operations = append(operations, Operation{
			OpID: edge.OperationID, Kind: OpPutEdge,
			PutEdge: PutEdgeInput{EdgeType: edge.EdgeType, From: from, To: to, Summary: edge.Summary},
		})
		metadata.operations[edge.OperationID] = &migrationImportOperation{
			edge:    &migrationImportEdge{id: edge.ID, createdAt: edge.CreatedAt, updatedAt: edge.UpdatedAt},
			sources: []LegacyImportSourceV1{edge.Source},
		}
	}
	for _, merge := range plan.Merges {
		source := NodeRef{ID: merge.SourceNodeID}
		canonical := NodeRef{ID: merge.CanonicalNodeID}
		if operationID, ok := nodeOperationID[merge.SourceNodeID]; ok {
			source = NodeRef{OpID: operationID}
		}
		if operationID, ok := nodeOperationID[merge.CanonicalNodeID]; ok {
			canonical = NodeRef{OpID: operationID}
		}
		operations = append(operations, Operation{
			OpID: merge.OperationID, Kind: OpMergeNodes,
			Merge: MergeNodesInput{
				Source: source, Canonical: canonical,
				SourceExpectedVersion: merge.SourceExpectedVersion, CanonicalExpectedVersion: merge.CanonicalExpectedVersion,
			},
		})
		metadata.operations[merge.OperationID] = &migrationImportOperation{sources: []LegacyImportSourceV1{merge.Source}, mergedAt: merge.MergedAt}
	}
	for _, alias := range plan.Aliases {
		opIndex, ok := nodeOperation[alias.CanonicalNodeID]
		if !ok {
			return MutationResult{}, validationError(ErrCodeInvalidRequest, "legacy alias target is not in the import plan", -1, "", "legacy_import_plan.aliases")
		}
		metadata.aliases = append(metadata.aliases, migrationImportAlias{
			nodeType: alias.NodeType, key: alias.Key, canonicalNodeID: alias.CanonicalNodeID,
			legacyNonconforming: alias.LegacyNonconforming, opIndex: opIndex,
		})
		operationID := plan.Nodes[opIndex].OperationID
		metadata.operations[operationID].sources = append(metadata.operations[operationID].sources, alias.Source)
	}
	batch := MutationBatch{
		SchemaVersion: GraphMutationSchemaVersion, IdempotencyKey: plan.IdempotencyKey,
		Context:    ExecutionContext{ProjectID: plan.ProjectID, ProjectKind: plan.ProjectKind, ActorType: ActorTypeMigration, ActorID: "legacy-blackboard-v1"},
		Operations: operations, migrationImport: metadata,
	}
	expectedIdempotencyKey := "legacy-blackboard-v1:" + plan.SourceDigest + ":" + plan.ProjectID
	if batch.IdempotencyKey != expectedIdempotencyKey {
		return MutationResult{}, validationError(ErrCodeInvalidRequest, "migration idempotency key does not match the sealed source digest and Project", -1, "", "idempotency_key")
	}
	requestHash, err := computeRequestHash(batch)
	if err != nil {
		return MutationResult{}, err
	}
	return s.applyInTransaction(ctx, tx, batch, requestHash)
}

// InitializeLegacyImportProject creates the rebuildable revision-zero graph
// state for a legacy Project whose sealed import plan has no semantic records.
// Transaction ownership remains with the migration coordinator.
func (s *GraphService) InitializeLegacyImportProject(ctx context.Context, tx *sql.Tx, projectID, recordedAt string) (CanonicalMainGraphProjection, error) {
	if tx == nil {
		return CanonicalMainGraphProjection{}, fmt.Errorf("migration transaction is required")
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&existing); err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	if existing != 0 {
		return s.PinCurrentCanonicalMainGraph(ctx, tx, projectID)
	}
	var projectKind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id=?`, projectID).Scan(&projectKind); err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	stateHash, err := computeResultingStateHash(tx, projectID, projectKind, graphState{})
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	doc, err := canonicalMainGraphDocumentAt(ctx, tx, projectID, 0)
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	projection, err := measureCanonicalMainGraphDocument(projectID, 0, doc)
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_graph_state(project_id,latest_mutation_seq,current_graph_revision,materialized_mutation_seq,history_head_hash,current_semantic_state_hash,current_main_projection_hash,projection_renderer_version,projection_estimator_version,projection_bytes,projection_estimated_tokens,budget_state,projection_dirty_revision,updated_at) VALUES(?,0,0,0,'',?,?,?,?,?,?,?,0,?)`,
		projectID, hex.EncodeToString(stateHash), projection.Hash, projection.RendererVersion, projection.EstimatorVersion, projection.ByteCount, projection.EstimatedTokens, budgetStateForEstimatedTokens(projection.EstimatedTokens), recordedAt); err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_projection_metrics(project_id,graph_revision,projection_hash,renderer_version,estimator_version,projection_bytes,estimated_tokens,budget_state,measured_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		projectID, 0, projection.Hash, projection.RendererVersion, projection.EstimatorVersion, projection.ByteCount, projection.EstimatedTokens, budgetStateForEstimatedTokens(projection.EstimatedTokens), recordedAt); err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	return projection, nil
}

// MeasureLegacyImportProject renders and stores the imported current graph
// inside the caller-owned cutover transaction. Post-commit verification can
// then validate the committed projection cache without repairing it first.
func (s *GraphService) MeasureLegacyImportProject(ctx context.Context, tx *sql.Tx, projectID, recordedAt string) (CanonicalMainGraphProjection, error) {
	if tx == nil {
		return CanonicalMainGraphProjection{}, fmt.Errorf("migration transaction is required")
	}
	projection, err := s.PinCurrentCanonicalMainGraph(ctx, tx, projectID)
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE blackboard_graph_state
		SET current_main_projection_hash=?,projection_renderer_version=?,projection_estimator_version=?,projection_bytes=?,projection_estimated_tokens=?,budget_state=?,projection_dirty_revision=0,updated_at=?
		WHERE project_id=? AND current_graph_revision=?`,
		projection.Hash, projection.RendererVersion, projection.EstimatorVersion, projection.ByteCount, projection.EstimatedTokens, budgetStateForEstimatedTokens(projection.EstimatedTokens), recordedAt, projectID, projection.GraphRevision)
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	if updated != 1 {
		return CanonicalMainGraphProjection{}, fmt.Errorf("imported graph changed during projection measurement")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_projection_metrics(project_id,graph_revision,projection_hash,renderer_version,estimator_version,projection_bytes,estimated_tokens,budget_state,measured_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,graph_revision) DO UPDATE SET projection_hash=excluded.projection_hash,renderer_version=excluded.renderer_version,estimator_version=excluded.estimator_version,projection_bytes=excluded.projection_bytes,estimated_tokens=excluded.estimated_tokens,budget_state=excluded.budget_state,measured_at=excluded.measured_at`,
		projectID, projection.GraphRevision, projection.Hash, projection.RendererVersion, projection.EstimatorVersion, projection.ByteCount, projection.EstimatedTokens, budgetStateForEstimatedTokens(projection.EstimatedTokens), recordedAt); err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	return projection, nil
}
