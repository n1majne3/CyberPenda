package blackboard

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// GraphKey is one key/alias entry reconstructed at a graph revision.
type GraphKey struct {
	NodeType        NodeType `json:"node_type"`
	Key             string   `json:"key"`
	Version         int      `json:"version"`
	Role            string   `json:"role"`
	SourceNodeID    string   `json:"source_node_id"`
	CanonicalNodeID string   `json:"canonical_node_id"`
	SemanticHash    string   `json:"semantic_hash"`
}

// GraphSnapshot is the deterministic storage reconstruction at one semantic
// graph revision. It is intentionally below the U01 read-projection envelope:
// C07 uses it to rebuild materialized heads and prove historical stability.
type GraphSnapshot struct {
	ProjectID     string       `json:"project_id"`
	ProjectKind   string       `json:"project_kind"`
	GraphRevision int          `json:"graph_revision"`
	StateHash     string       `json:"state_hash"`
	Nodes         []NodeRecord `json:"nodes"`
	Edges         []EdgeRecord `json:"edges"`
	Keys          []GraphKey   `json:"keys"`
}

type graphQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Reconstruct selects the latest complete node, edge, and key post-image at
// or before revision. It reads only append-only ledger tables, so deleting or
// corrupting rebuildable heads cannot change the result.
func (s *GraphService) Reconstruct(ctx context.Context, projectID string, revision int) (GraphSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GraphSnapshot{}, graphStorageError("begin graph reconstruction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := verifyMutationChain(ctx, tx, projectID); err != nil {
		return GraphSnapshot{}, fmt.Errorf("verify graph ledger before reconstruction: %w", err)
	}
	snapshot, err := reconstructGraph(ctx, tx, projectID, revision)
	if err != nil {
		return GraphSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return GraphSnapshot{}, graphStorageError("commit graph reconstruction", err)
	}
	return snapshot, nil
}

func reconstructGraph(ctx context.Context, q graphQuerier, projectID string, revision int) (GraphSnapshot, error) {
	var projectKind string
	if err := q.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id=?`, projectID).Scan(&projectKind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GraphSnapshot{}, validationError(ErrCodeProjectNotFound, "project does not exist", -1, "", "project_id")
		}
		return GraphSnapshot{}, fmt.Errorf("read reconstruction project: %w", err)
	}
	var latestRevision int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(result_graph_revision),0) FROM blackboard_graph_mutations WHERE project_id=?`, projectID).Scan(&latestRevision); err != nil {
		return GraphSnapshot{}, fmt.Errorf("read latest graph revision: %w", err)
	}
	if revision < 0 || revision > latestRevision {
		return GraphSnapshot{}, validationError(ErrCodeInvalidRequest, fmt.Sprintf("graph revision %d is outside 0..%d", revision, latestRevision), -1, "", "graph_revision")
	}

	snapshot := GraphSnapshot{ProjectID: projectID, ProjectKind: projectKind, GraphRevision: revision, Nodes: []NodeRecord{}, Edges: []EdgeRecord{}, Keys: []GraphKey{}}
	if revision > 0 {
		_ = q.QueryRowContext(ctx, `SELECT resulting_state_hash FROM blackboard_graph_mutations WHERE project_id=? AND result_graph_revision<=? ORDER BY mutation_seq DESC LIMIT 1`, projectID, revision).Scan(&snapshot.StateHash)
	}

	nodeRows, err := q.QueryContext(ctx, `
		SELECT n.id,n.node_type,n.original_stable_key,v.version,v.disposition,v.merge_target_id,v.properties_json,
		       n.created_at,v.updated_at,v.semantic_hash
		  FROM blackboard_nodes n
		  JOIN blackboard_node_versions v ON v.project_id=n.project_id AND v.node_id=n.id
		 WHERE n.project_id=? AND v.version=(
		       SELECT MAX(v2.version) FROM blackboard_node_versions v2
		        WHERE v2.project_id=n.project_id AND v2.node_id=n.id AND v2.result_graph_revision<=?)
		 ORDER BY n.node_type COLLATE BINARY,n.original_stable_key COLLATE BINARY,n.id COLLATE BINARY`, projectID, revision)
	if err != nil {
		return GraphSnapshot{}, fmt.Errorf("reconstruct nodes: %w", err)
	}
	defer nodeRows.Close()
	for nodeRows.Next() {
		var node NodeRecord
		var nodeType, disposition, propertiesJSON string
		var mergeTarget sql.NullString
		if err := nodeRows.Scan(&node.ID, &nodeType, &node.StableKey, &node.Version, &disposition, &mergeTarget, &propertiesJSON, &node.CreatedAt, &node.UpdatedAt, &node.SemanticHash); err != nil {
			return GraphSnapshot{}, fmt.Errorf("scan reconstructed node: %w", err)
		}
		node.ProjectID, node.NodeType, node.Disposition, node.MergeTargetID, node.StateHash = projectID, NodeType(nodeType), Disposition(disposition), mergeTarget.String, snapshot.StateHash
		if err := json.Unmarshal([]byte(propertiesJSON), &node.PropertyMap); err != nil {
			return GraphSnapshot{}, fmt.Errorf("decode reconstructed node properties: %w", err)
		}
		if node.NodeType == NodeTypeProjectFact {
			if err := json.Unmarshal([]byte(propertiesJSON), &node.ProjectFact); err != nil {
				return GraphSnapshot{}, fmt.Errorf("decode reconstructed project fact: %w", err)
			}
		}
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	if err := nodeRows.Err(); err != nil {
		return GraphSnapshot{}, fmt.Errorf("iterate reconstructed nodes: %w", err)
	}

	edgeRows, err := q.QueryContext(ctx, `
		SELECT e.id,e.edge_type,v.from_node_id,v.to_node_id,v.version,v.state,v.summary,v.semantic_hash
		  FROM blackboard_edges e
		  JOIN blackboard_edge_versions v ON v.project_id=e.project_id AND v.edge_id=e.id
		 WHERE e.project_id=? AND v.version=(
		       SELECT MAX(v2.version) FROM blackboard_edge_versions v2
		        WHERE v2.project_id=e.project_id AND v2.edge_id=e.id AND v2.result_graph_revision<=?)
		 ORDER BY e.edge_type COLLATE BINARY,v.from_node_id COLLATE BINARY,v.to_node_id COLLATE BINARY,e.id COLLATE BINARY`, projectID, revision)
	if err != nil {
		return GraphSnapshot{}, fmt.Errorf("reconstruct edges: %w", err)
	}
	defer edgeRows.Close()
	for edgeRows.Next() {
		var edge EdgeRecord
		var edgeType string
		if err := edgeRows.Scan(&edge.ID, &edgeType, &edge.FromNodeID, &edge.ToNodeID, &edge.Version, &edge.State, &edge.Summary, &edge.SemanticHash); err != nil {
			return GraphSnapshot{}, fmt.Errorf("scan reconstructed edge: %w", err)
		}
		edge.ProjectID, edge.EdgeType = projectID, EdgeType(edgeType)
		snapshot.Edges = append(snapshot.Edges, edge)
	}
	if err := edgeRows.Err(); err != nil {
		return GraphSnapshot{}, fmt.Errorf("iterate reconstructed edges: %w", err)
	}

	keyRows, err := q.QueryContext(ctx, `
		SELECT k.node_type,k.key,k.key_version,k.role,k.source_node_id,k.canonical_node_id,k.semantic_hash
		  FROM blackboard_key_events k
		 WHERE k.project_id=? AND k.key_version=(
		       SELECT MAX(k2.key_version) FROM blackboard_key_events k2
		        WHERE k2.project_id=k.project_id AND k2.node_type=k.node_type AND k2.key=k.key AND k2.result_graph_revision<=?)
		 ORDER BY k.node_type COLLATE BINARY,k.key COLLATE BINARY`, projectID, revision)
	if err != nil {
		return GraphSnapshot{}, fmt.Errorf("reconstruct keys: %w", err)
	}
	defer keyRows.Close()
	for keyRows.Next() {
		var key GraphKey
		if err := keyRows.Scan(&key.NodeType, &key.Key, &key.Version, &key.Role, &key.SourceNodeID, &key.CanonicalNodeID, &key.SemanticHash); err != nil {
			return GraphSnapshot{}, fmt.Errorf("scan reconstructed key: %w", err)
		}
		snapshot.Keys = append(snapshot.Keys, key)
	}
	if err := keyRows.Err(); err != nil {
		return GraphSnapshot{}, fmt.Errorf("iterate reconstructed keys: %w", err)
	}
	return snapshot, nil
}

// Rebuild verifies reconstruction input and atomically replaces only the
// mutable per-Project materialization from the append-only ledger.
func (s *GraphService) Rebuild(ctx context.Context, projectID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin graph rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := verifyMutationChain(ctx, tx, projectID); err != nil {
		return fmt.Errorf("verify graph ledger before rebuild: %w", err)
	}
	var latestSeq, latestRevision int
	var historyHash, stateHash, recordedAt string
	if err := tx.QueryRowContext(ctx, `SELECT mutation_seq,result_graph_revision,mutation_hash,resulting_state_hash,recorded_at FROM blackboard_graph_mutations WHERE project_id=? ORDER BY mutation_seq DESC LIMIT 1`, projectID).Scan(&latestSeq, &latestRevision, &historyHash, &stateHash, &recordedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return validationError(ErrCodeInvalidRequest, "project has no graph ledger", -1, "", "project_id")
		}
		return fmt.Errorf("read rebuild head: %w", err)
	}
	snapshot, err := reconstructGraph(ctx, tx, projectID, latestRevision)
	if err != nil {
		return err
	}
	if snapshot.StateHash != stateHash {
		return fmt.Errorf("reconstructed state hash %s does not match ledger %s", snapshot.StateHash, stateHash)
	}
	for _, table := range []string{"blackboard_key_registry", "blackboard_edge_heads", "blackboard_node_heads", "blackboard_graph_state"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE project_id=?", projectID); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	for _, node := range snapshot.Nodes {
		propertiesJSON, err := canonicalJSON(node.PropertyMap)
		if err != nil {
			return fmt.Errorf("encode rebuilt node properties: %w", err)
		}
		lifecycle, entity, scope := genericDerivedFields(node.NodeType, propertiesJSON)
		var graphRevision int
		if err := tx.QueryRowContext(ctx, `SELECT result_graph_revision FROM blackboard_node_versions WHERE project_id=? AND node_id=? AND version=?`, projectID, node.ID, node.Version).Scan(&graphRevision); err != nil {
			return fmt.Errorf("read rebuilt node revision: %w", err)
		}
		var mergeTarget any
		if node.MergeTargetID != "" {
			mergeTarget = node.MergeTargetID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_node_heads(project_id,node_id,node_type,version,graph_revision,disposition,merge_target_id,lifecycle_state,entity_kind,scope_status,semantic_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, projectID, node.ID, string(node.NodeType), node.Version, graphRevision, string(node.Disposition), mergeTarget, lifecycle, entity, scope, node.SemanticHash); err != nil {
			return fmt.Errorf("insert rebuilt node head: %w", err)
		}
	}
	for _, edge := range snapshot.Edges {
		var graphRevision int
		if err := tx.QueryRowContext(ctx, `SELECT result_graph_revision FROM blackboard_edge_versions WHERE project_id=? AND edge_id=? AND version=?`, projectID, edge.ID, edge.Version).Scan(&graphRevision); err != nil {
			return fmt.Errorf("read rebuilt edge revision: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_edge_heads(project_id,edge_id,edge_type,from_node_id,to_node_id,version,graph_revision,state,semantic_hash) VALUES(?,?,?,?,?,?,?,?,?)`, projectID, edge.ID, string(edge.EdgeType), edge.FromNodeID, edge.ToNodeID, edge.Version, graphRevision, edge.State, edge.SemanticHash); err != nil {
			return fmt.Errorf("insert rebuilt edge head: %w", err)
		}
	}
	for _, key := range snapshot.Keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_key_registry(project_id,node_type,key,latest_key_version,role,source_node_id,canonical_node_id,semantic_hash) VALUES(?,?,?,?,?,?,?,?)`, projectID, string(key.NodeType), key.Key, key.Version, key.Role, key.SourceNodeID, key.CanonicalNodeID, key.SemanticHash); err != nil {
			return fmt.Errorf("insert rebuilt key: %w", err)
		}
	}
	rebuiltStateHash, err := computeResultingStateHash(tx, projectID, snapshot.ProjectKind, graphState{})
	if err != nil {
		return fmt.Errorf("hash rebuilt graph state: %w", err)
	}
	legacyThrough, err := legacyIntegrityThrough(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if latestSeq > legacyThrough && hex.EncodeToString(rebuiltStateHash) != stateHash {
		return fmt.Errorf("rebuilt semantic state hash %s does not match ledger %s", hex.EncodeToString(rebuiltStateHash), stateHash)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_graph_state(project_id,latest_mutation_seq,current_graph_revision,materialized_mutation_seq,history_head_hash,current_semantic_state_hash,current_main_projection_hash,projection_renderer_version,projection_estimator_version,projection_bytes,projection_estimated_tokens,budget_state,projection_dirty_revision,updated_at) VALUES(?,?,?,?,?,?,NULL,'','',NULL,NULL,'unknown',?,?)`, projectID, latestSeq, latestRevision, latestSeq, historyHash, stateHash, latestRevision, recordedAt); err != nil {
		return fmt.Errorf("insert rebuilt graph state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit graph rebuild: %w", err)
	}
	return nil
}
