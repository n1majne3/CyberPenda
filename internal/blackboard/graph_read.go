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
		GraphRevision: rf.GraphRevision, RequestHash: rf.RequestHash, ResultHash: rf.ResultHash,
		ResultingStateHash: rf.ResultingStateHash,
		Operations:         make([]OperationResult, len(rf.Operations)),
	}
	for i, o := range rf.Operations {
		out.Operations[i] = OperationResult{
			OpID: o.OpID, NodeID: o.NodeID, NodeType: o.NodeType, StableKey: o.StableKey,
			NodeVersion: o.NodeVersion, EdgeID: o.EdgeID, EdgeType: o.EdgeType, EdgeVersion: o.EdgeVersion, SemanticHash: o.SemanticHash, Changed: o.Changed,
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
	)
	// Read current node envelope by joining heads -> versions. original_stable_key
	// is the canonical stable key; for an alias read we still report the
	// canonical key.
	err = s.db.QueryRow(
		`SELECT h.node_id, h.node_type, n.original_stable_key, h.version, h.graph_revision,
		        h.disposition, v.properties_json, h.semantic_hash, n.created_at, v.updated_at
		   FROM blackboard_node_heads h
		   JOIN blackboard_nodes n ON n.project_id = h.project_id AND n.id = h.node_id
		   JOIN blackboard_node_versions v ON v.project_id = h.project_id AND v.node_id = h.node_id AND v.version = h.version
		  WHERE h.project_id = ? AND h.node_id = ?`,
		req.ProjectID, canonicalNodeID,
	).Scan(&nodeID, &nodeType, &stableKey, &version, &graphRev, &disposition, &propsJSON, &semHash, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReadNodeResult{}, validationError(ErrCodeNodeNotFound,
			fmt.Sprintf("node %s not found in project", canonicalNodeID), -1, "", "node_id")
	}
	if err != nil {
		return ReadNodeResult{}, fmt.Errorf("read node head: %w", err)
	}

	var props ProjectFactProperties
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		return ReadNodeResult{}, fmt.Errorf("decode properties: %w", err)
	}

	var stateHash string
	_ = s.db.QueryRow(
		`SELECT current_semantic_state_hash FROM blackboard_graph_state WHERE project_id = ?`,
		req.ProjectID,
	).Scan(&stateHash)

	return ReadNodeResult{
		Node: NodeRecord{
			ID: nodeID, ProjectID: req.ProjectID, NodeType: NodeType(nodeType),
			StableKey: stableKey, Version: version, Disposition: Disposition(disposition),
			ProjectFact: props, CreatedAt: createdAt, UpdatedAt: updatedAt,
			SemanticHash: semHash, StateHash: stateHash,
		},
		ObservedGraphRevision: graphRev,
		ResolvedFromAlias:     resolvedFromAlias,
	}, nil
}
