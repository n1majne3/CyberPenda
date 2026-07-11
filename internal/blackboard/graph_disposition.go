package blackboard

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

func (s *GraphService) stageSetDisposition(tx *sql.Tx, batch MutationBatch, op Operation, opIndex, graphRevision int, state *graphState) (OperationResult, error) {
	projectID := batch.Context.ProjectID
	node, err := loadMutableNodeWithState(tx, projectID, op.Node, state)
	if err != nil {
		return OperationResult{}, err
	}
	if op.Disposition.ExpectedVersion != node.version {
		return OperationResult{}, validationError(ErrCodeVersionConflict, "set_disposition expected version does not match current version", opIndex, op.OpID, "operations[].set_disposition.expected_version")
	}
	want := op.Disposition.Disposition
	if node.disposition == want {
		return OperationResult{OpID: op.OpID, NodeID: node.nodeID, NodeType: node.nodeType, StableKey: node.stableKey, NodeVersion: node.version, SemanticHash: node.semHash, Changed: false}, nil
	}
	if want == DispositionArchived {
		protected, protectErr := archiveProtected(tx, projectID, node.nodeID)
		if protectErr != nil {
			return OperationResult{}, protectErr
		}
		if node.disposition != DispositionMain || !archiveEligible(node) || protected {
			return OperationResult{}, validationError(ErrCodeArchiveGuardFailed, "node is protected live meaning and cannot be archived", opIndex, op.OpID, "operations[].set_disposition")
		}
		if err := stageRetireTouchingEdges(tx, projectID, node.nodeID, opIndex, graphRevision, state); err != nil {
			return OperationResult{}, err
		}
	} else {
		if node.disposition != DispositionArchived {
			return OperationResult{}, validationError(ErrCodeInvalidTransition, "only archived nodes can be restored", opIndex, op.OpID, "operations[].set_disposition")
		}
		manifest := batch.Context.restoreManifest
		if (batch.Context.ActorType != ActorTypeSystem && batch.Context.ActorType != ActorTypeOperator) || manifest == nil || manifest.ID == "" || manifest.ID != op.Disposition.RestoreManifestID || !manifestContainsNode(manifest, node.nodeID) {
			return OperationResult{}, validationError(ErrCodeArchiveGuardFailed, "restore requires a matching trusted manifest", opIndex, op.OpID, "operations[].set_disposition.restore_manifest_id")
		}
		if err := s.stageRestoreEdges(tx, batch, node.nodeID, opIndex, graphRevision, state); err != nil {
			return OperationResult{}, err
		}
		if mergedGraphHasCycle(tx, projectID, state) {
			return OperationResult{}, validationError(ErrCodeInvariantViolation, "restore manifest topology would create a controlled graph cycle", opIndex, op.OpID, "operations[].set_disposition.restore_manifest_id")
		}
	}
	propsJSON, err := canonicalJSON(node.props)
	if err != nil {
		return OperationResult{}, err
	}
	semHash := genericNodeSemanticHash(want, "", node.props)
	state.pendingUpdates = append(state.pendingUpdates, pendingUpdate{nodeID: node.nodeID, nodeType: node.nodeType, stableKey: node.stableKey, version: node.version + 1, propsJSON: propsJSON, semHash: semHash, opIndex: opIndex, graphRevision: graphRevision, disposition: want})
	return OperationResult{OpID: op.OpID, NodeID: node.nodeID, NodeType: node.nodeType, StableKey: node.stableKey, NodeVersion: node.version + 1, SemanticHash: hex.EncodeToString(semHash), ResolvedFromAlias: node.resolvedFromAlias, Changed: true}, nil
}

// archiveProtected computes the storage-contract protected closure needed by
// an archive guard. It intentionally follows only relationships that preserve
// live meaning; ordinary weak connectivity does not make a node protected.
func archiveProtected(tx *sql.Tx, projectID, subjectID string) (bool, error) {
	protected := map[string]bool{}
	rows, err := tx.Query(`SELECT node_id,node_type,lifecycle_state FROM blackboard_node_heads WHERE project_id=? AND disposition='main'`, projectID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id, nodeType, lifecycle string
		if err := rows.Scan(&id, &nodeType, &lifecycle); err != nil {
			rows.Close()
			return false, err
		}
		switch NodeType(nodeType) {
		case NodeTypeGoal:
			protected[id] = lifecycle != "completed" && lifecycle != "failed" && lifecycle != "stopped" && lifecycle != "interrupted"
		case NodeTypeExplorationObjective:
			protected[id] = lifecycle == "open"
		case NodeTypeAttempt:
			protected[id] = lifecycle == "open" || lifecycle == "failed" || lifecycle == "blocked" || lifecycle == "inconclusive" || lifecycle == "interrupted"
		case NodeTypeProjectDirective:
			protected[id] = lifecycle == "active"
		case NodeTypeProjectFact:
			protected[id] = lifecycle == "tentative" || lifecycle == "confirmed"
		case NodeTypeFinding:
			protected[id] = lifecycle == "confirmed"
		case NodeTypeSolution:
			protected[id] = lifecycle == "verified"
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()

	type relation struct{ edgeType, from, to string }
	edgeRows, err := tx.Query(`SELECT edge_type,from_node_id,to_node_id FROM blackboard_edge_heads WHERE project_id=? AND state='active'`, projectID)
	if err != nil {
		return false, err
	}
	var relations []relation
	for edgeRows.Next() {
		var relation relation
		if err := edgeRows.Scan(&relation.edgeType, &relation.from, &relation.to); err != nil {
			edgeRows.Close()
			return false, err
		}
		relations = append(relations, relation)
		if relation.edgeType == string(EdgeTypeContradicts) {
			protected[relation.from], protected[relation.to] = true, true
		}
	}
	if err := edgeRows.Err(); err != nil {
		edgeRows.Close()
		return false, err
	}
	edgeRows.Close()
	for changed := true; changed; {
		changed = false
		protect := func(id string) {
			if !protected[id] {
				protected[id], changed = true, true
			}
		}
		for _, relation := range relations {
			switch EdgeType(relation.edgeType) {
			case EdgeTypeDependsOn, EdgeTypeBlocks:
				if protected[relation.from] || protected[relation.to] {
					protect(relation.from)
					protect(relation.to)
				}
			case EdgeTypeTests:
				if protected[relation.from] {
					protect(relation.to)
				}
			case EdgeTypeSupports, EdgeTypeEvidences:
				if protected[relation.to] {
					protect(relation.from)
				}
			case EdgeTypeSatisfies:
				if protected[relation.from] {
					protect(relation.to)
				}
			case EdgeTypeAbout:
				if protected[relation.from] {
					protect(relation.to)
				}
			case EdgeTypePartOf:
				if protected[relation.from] {
					protect(relation.to)
				}
			}
		}
	}
	return protected[subjectID], nil
}

func archiveEligible(node mutableNode) bool {
	status, _ := node.props["status"].(string)
	switch node.nodeType {
	case NodeTypeGoal:
		taskStatus, _ := node.props["task_status"].(string)
		return taskStatus == "completed" || taskStatus == "failed" || taskStatus == "stopped" || taskStatus == "interrupted"
	case NodeTypeEntity:
		return status == "retired" || status == "superseded"
	case NodeTypeExplorationObjective:
		return status == "resolved" || status == "abandoned" || status == "superseded"
	case NodeTypeAttempt:
		return status == "succeeded"
	case NodeTypeObservation:
		return status == "superseded"
	case NodeTypeHypothesis:
		return status == "superseded"
	case NodeTypeProjectFact:
		confidence, _ := node.props["confidence"].(string)
		return confidence == "deprecated"
	case NodeTypeFinding:
		return status == "false_positive"
	case NodeTypeSolution:
		return status == "rejected" || status == "superseded"
	case NodeTypeEvidenceArtifact:
		return status == "superseded"
	case NodeTypeProjectDirective:
		return status == "retired" || status == "superseded"
	default:
		return false
	}
}

func stageRetireTouchingEdges(tx *sql.Tx, projectID, nodeID string, opIndex, graphRevision int, state *graphState) error {
	rows, err := tx.Query(`SELECT h.edge_id,h.edge_type,h.from_node_id,h.to_node_id,h.version,h.state,v.summary,v.updated_at,e.created_mutation_seq,e.created_operation_index,v.mutation_seq,v.operation_index FROM blackboard_edge_heads h JOIN blackboard_edge_versions v ON v.project_id=h.project_id AND v.edge_id=h.edge_id AND v.version=h.version JOIN blackboard_edges e ON e.project_id=h.project_id AND e.id=h.edge_id WHERE h.project_id=? AND h.state='active' AND (h.from_node_id=? OR h.to_node_id=?) ORDER BY h.edge_id`, projectID, nodeID, nodeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var edge mergeEdge
		if err := rows.Scan(&edge.id, &edge.edgeType, &edge.fromID, &edge.toID, &edge.version, &edge.state, &edge.summary, &edge.updatedAt, &edge.createdMutation, &edge.createdOperation, &edge.updatedMutation, &edge.updatedOperation); err != nil {
			return err
		}
		for _, pending := range state.pendingEdgeUpdates {
			if pending.id == edge.id {
				edge.version, edge.fromID, edge.toID, edge.state, edge.summary = pending.version, pending.fromID, pending.toID, pending.state, pending.summary
			}
		}
		if edge.state != "retired" {
			stageMergedEdge(edge, edge.fromID, edge.toID, "retired", edge.summary, opIndex, graphRevision, state)
		}
	}
	return rows.Err()
}

func manifestContainsNode(manifest *RestoreManifest, nodeID string) bool {
	for _, candidate := range manifest.Nodes {
		if candidate == nodeID {
			return true
		}
	}
	return false
}

func (s *GraphService) stageRestoreEdges(tx *sql.Tx, batch MutationBatch, restoringNodeID string, opIndex, graphRevision int, state *graphState) error {
	for _, requested := range batch.Context.restoreManifest.Edges {
		from, err := loadMutableNodeWithState(tx, batch.Context.ProjectID, requested.From, state)
		if err != nil {
			return err
		}
		to, err := loadMutableNodeWithState(tx, batch.Context.ProjectID, requested.To, state)
		if err != nil {
			return err
		}
		if from.nodeID != restoringNodeID && from.disposition != DispositionMain {
			if !batchRestoresNode(tx, batch, from.nodeID) {
				continue
			}
		}
		if to.nodeID != restoringNodeID && to.disposition != DispositionMain {
			if !batchRestoresNode(tx, batch, to.nodeID) {
				continue
			}
		}
		if from.nodeID == to.nodeID {
			continue
		}
		allowed, known := edgeEndpoints[requested.EdgeType]
		if !known || !allowed(from.nodeType, to.nodeType) {
			continue
		}
		var existingID string
		err = tx.QueryRow(`SELECT edge_id FROM blackboard_edge_heads WHERE project_id=? AND edge_type=? AND from_node_id=? AND to_node_id=? AND state='active'`, batch.Context.ProjectID, string(requested.EdgeType), from.nodeID, to.nodeID).Scan(&existingID)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check restored edge: %w", err)
		}
		alreadyPending := false
		for _, pending := range state.pendingEdges {
			if pending.edgeType == requested.EdgeType && pending.fromID == from.nodeID && pending.toID == to.nodeID {
				alreadyPending = true
				break
			}
		}
		if alreadyPending {
			continue
		}
		edgeID := s.ids.NextID()
		semHash := edgeSemanticHash(requested.EdgeType, from.nodeID, to.nodeID, "active", requested.Summary)
		state.pendingEdges = append(state.pendingEdges, pendingEdge{id: edgeID, edgeType: requested.EdgeType, fromID: from.nodeID, toID: to.nodeID, summary: requested.Summary, semHash: semHash, opIndex: opIndex, graphRevision: graphRevision})
	}
	return nil
}

func batchRestoresNode(tx *sql.Tx, batch MutationBatch, nodeID string) bool {
	if batch.Context.restoreManifest == nil || !manifestContainsNode(batch.Context.restoreManifest, nodeID) {
		return false
	}
	for _, operation := range batch.Operations {
		if operation.Kind != OpSetDisposition || operation.Disposition.Disposition != DispositionMain {
			continue
		}
		if operation.Node.ID == nodeID {
			return true
		}
		if operation.Node.NodeType != "" && operation.Node.StableKey != "" {
			resolved, err := loadMutableNode(tx, batch.Context.ProjectID, operation.Node)
			if err == nil && resolved.nodeID == nodeID {
				return true
			}
		}
	}
	return false
}
