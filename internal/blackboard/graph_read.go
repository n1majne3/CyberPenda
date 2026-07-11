package blackboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// jsonUnmarshalProps decodes canonical properties JSON into a typed struct.
func jsonUnmarshalProps(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// decodeResultJSON rebuilds a MutationResult from the stored canonical result
// bytes (storage §6.1 result_json).
func decodeResultJSON(data string) (*MutationResult, error) {
	var rf resultLedgerForm
	if err := json.Unmarshal([]byte(data), &rf); err != nil {
		return nil, fmt.Errorf("decode result json: %w", err)
	}
	out := &MutationResult{
		MutationSequence: rf.MutationSequence, MutationID: rf.MutationID, RecordedAt: rf.RecordedAt,
		GraphRevision: rf.GraphRevision, RequestHash: rf.RequestHash,
		ResultingStateHash: rf.ResultingStateHash,
		Operations:         make([]OperationResult, len(rf.Operations)),
		ResultBytes:        append([]byte(nil), []byte(data)...),
	}
	for i, o := range rf.Operations {
		out.Operations[i] = OperationResult{
			OpID: o.OpID, NodeID: o.NodeID, NodeType: o.NodeType, StableKey: o.StableKey,
			NodeVersion: o.NodeVersion, EdgeID: o.EdgeID, EdgeType: o.EdgeType, EdgeVersion: o.EdgeVersion, SemanticHash: o.SemanticHash, ResolvedFromAlias: o.ResolvedFromAlias, Changed: o.Changed,
		}
	}
	return out, nil
}

func (s *GraphService) ReadEdge(ctx context.Context, req ReadEdgeRequest) (EdgeRecord, error) {
	var e EdgeRecord
	var typ string
	err := s.db.QueryRowContext(ctx, `SELECT h.edge_id,h.edge_type,h.from_node_id,h.to_node_id,h.version,h.state,v.summary,h.semantic_hash FROM blackboard_edge_heads h JOIN blackboard_edge_versions v ON v.project_id=h.project_id AND v.edge_id=h.edge_id AND v.version=h.version WHERE h.project_id=? AND h.edge_id=?`, req.ProjectID, req.EdgeID).Scan(&e.ID, &typ, &e.FromNodeID, &e.ToNodeID, &e.Version, &e.State, &e.Summary, &e.SemanticHash)
	if errors.Is(err, sql.ErrNoRows) {
		return EdgeRecord{}, validationError(ErrCodeEdgeEndpointNotFound, "edge not found", -1, "", "edge_id")
	}
	if err != nil {
		return EdgeRecord{}, fmt.Errorf("read edge head: %w", err)
	}
	e.ProjectID = req.ProjectID
	e.EdgeType = EdgeType(typ)
	return e, nil
}

func (s *GraphService) ReadActiveEdge(ctx context.Context, req ReadActiveEdgeRequest) (EdgeRecord, error) {
	var edgeID string
	err := s.db.QueryRowContext(ctx, `SELECT edge_id FROM blackboard_edge_heads WHERE project_id=? AND edge_type=? AND from_node_id=? AND to_node_id=? AND state='active'`, req.ProjectID, string(req.EdgeType), req.FromNodeID, req.ToNodeID).Scan(&edgeID)
	if errors.Is(err, sql.ErrNoRows) {
		return EdgeRecord{}, validationError(ErrCodeEdgeEndpointNotFound, "active edge not found", -1, "", "edge")
	}
	if err != nil {
		return EdgeRecord{}, fmt.Errorf("read active edge identity: %w", err)
	}
	return s.ReadEdge(ctx, ReadEdgeRequest{ProjectID: req.ProjectID, EdgeID: edgeID})
}

// ReadNode resolves a node by key through the alias-resolving key registry and
// returns its current envelope at the committed graph revision (C02 minimal
// green path; full read service is U01). The read enforces Project isolation
// and reports alias redirection (graph contract §4).
func (s *GraphService) ReadNode(ctx context.Context, req ReadNodeRequest) (ReadNodeResult, error) {
	var (
		canonicalNodeID string
		role            string
		sourceNodeID    string
	)
	err := s.db.QueryRow(
		`SELECT canonical_node_id, role, source_node_id FROM blackboard_key_registry
		  WHERE project_id = ? AND node_type = ? AND key = ?`,
		req.ProjectID, string(req.NodeType), req.Key,
	).Scan(&canonicalNodeID, &role, &sourceNodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return ReadNodeResult{}, validationError(ErrCodeNodeNotFound,
			fmt.Sprintf("no %s node for key %q in project", req.NodeType, req.Key), -1, "", "key")
	}
	if err != nil {
		return ReadNodeResult{}, fmt.Errorf("read key registry: %w", err)
	}

	resolvedFromAlias := ""
	if role == "alias" {
		resolvedFromAlias = req.Key
	}

	var (
		nodeID      string
		nodeType    string
		stableKey   string
		version     int
		graphRev    int
		disposition string
		propsJSON   string
		semHash     string
		createdAt   string
		updatedAt   string
		mergeTarget sql.NullString
	)
	// Read current node envelope by joining heads -> versions. original_stable_key
	// is the canonical stable key; for an alias read we still report the
	// canonical key.
	err = s.db.QueryRow(
		`SELECT h.node_id, h.node_type, n.original_stable_key, h.version, h.graph_revision,
		        h.disposition, h.merge_target_id, v.properties_json, h.semantic_hash, n.created_at, v.updated_at
		   FROM blackboard_node_heads h
		   JOIN blackboard_nodes n ON n.project_id = h.project_id AND n.id = h.node_id
		   JOIN blackboard_node_versions v ON v.project_id = h.project_id AND v.node_id = h.node_id AND v.version = h.version
		  WHERE h.project_id = ? AND h.node_id = ?`,
		req.ProjectID, canonicalNodeID,
	).Scan(&nodeID, &nodeType, &stableKey, &version, &graphRev, &disposition, &mergeTarget, &propsJSON, &semHash, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReadNodeResult{}, validationError(ErrCodeNodeNotFound,
			fmt.Sprintf("node %s not found in project", canonicalNodeID), -1, "", "node_id")
	}
	if err != nil {
		return ReadNodeResult{}, fmt.Errorf("read node head: %w", err)
	}

	var propertyMap map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &propertyMap); err != nil {
		return ReadNodeResult{}, fmt.Errorf("decode properties: %w", err)
	}
	var props ProjectFactProperties
	if NodeType(nodeType) == NodeTypeProjectFact {
		if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
			return ReadNodeResult{}, fmt.Errorf("decode project_fact properties: %w", err)
		}
	}

	var stateHash string
	_ = s.db.QueryRow(
		`SELECT current_semantic_state_hash FROM blackboard_graph_state WHERE project_id = ?`,
		req.ProjectID,
	).Scan(&stateHash)

	return ReadNodeResult{
		Node: NodeRecord{
			ID: nodeID, ProjectID: req.ProjectID, NodeType: NodeType(nodeType),
			StableKey: stableKey, Version: version, Disposition: Disposition(disposition), MergeTargetID: mergeTarget.String,
			ProjectFact: props, PropertyMap: propertyMap, CreatedAt: createdAt, UpdatedAt: updatedAt,
			SemanticHash: semHash, StateHash: stateHash,
		},
		ObservedGraphRevision: graphRev,
		ResolvedFromAlias:     resolvedFromAlias,
	}, nil
}

// ReadLiteralNode returns the immutable identity and its complete version
// history without following merge redirects. Ordinary callers should use
// ReadNode; this method exists for the contract's explicit audit/history path.
func (s *GraphService) ReadLiteralNode(ctx context.Context, req ReadLiteralNodeRequest) (ReadLiteralNodeResult, error) {
	var node NodeRecord
	var nodeType, disposition, propsJSON string
	var mergeTarget sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT h.node_id,h.node_type,n.original_stable_key,h.version,h.disposition,
		       h.merge_target_id,v.properties_json,n.created_at,v.updated_at,h.semantic_hash
		  FROM blackboard_node_heads h
		  JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id
		  JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		 WHERE h.project_id=? AND h.node_id=?`, req.ProjectID, req.NodeID).
		Scan(&node.ID, &nodeType, &node.StableKey, &node.Version, &disposition, &mergeTarget, &propsJSON, &node.CreatedAt, &node.UpdatedAt, &node.SemanticHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ReadLiteralNodeResult{}, validationError(ErrCodeNodeNotFound, "literal node does not exist", -1, "", "node_id")
	}
	if err != nil {
		return ReadLiteralNodeResult{}, fmt.Errorf("read literal node: %w", err)
	}
	node.ProjectID, node.NodeType, node.Disposition, node.MergeTargetID = req.ProjectID, NodeType(nodeType), Disposition(disposition), mergeTarget.String
	if err := json.Unmarshal([]byte(propsJSON), &node.PropertyMap); err != nil {
		return ReadLiteralNodeResult{}, fmt.Errorf("decode literal node properties: %w", err)
	}
	if node.NodeType == NodeTypeProjectFact {
		if err := json.Unmarshal([]byte(propsJSON), &node.ProjectFact); err != nil {
			return ReadLiteralNodeResult{}, fmt.Errorf("decode literal project fact: %w", err)
		}
	}
	_ = s.db.QueryRowContext(ctx, `SELECT current_semantic_state_hash FROM blackboard_graph_state WHERE project_id=?`, req.ProjectID).Scan(&node.StateHash)

	rows, err := s.db.QueryContext(ctx, `SELECT version,disposition,merge_target_id,properties_json,semantic_hash FROM blackboard_node_versions WHERE project_id=? AND node_id=? ORDER BY version`, req.ProjectID, req.NodeID)
	if err != nil {
		return ReadLiteralNodeResult{}, fmt.Errorf("read literal node versions: %w", err)
	}
	defer rows.Close()
	versions := []NodeVersionRecord{}
	for rows.Next() {
		var version NodeVersionRecord
		var merge sql.NullString
		var properties string
		if err := rows.Scan(&version.Version, &version.Disposition, &merge, &properties, &version.SemanticHash); err != nil {
			return ReadLiteralNodeResult{}, err
		}
		version.MergeTargetID = merge.String
		if err := json.Unmarshal([]byte(properties), &version.PropertyMap); err != nil {
			return ReadLiteralNodeResult{}, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return ReadLiteralNodeResult{}, err
	}
	return ReadLiteralNodeResult{Node: node, Versions: versions}, nil
}
