package blackboard

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
)

type mergeEdge struct {
	id, edgeType, fromID, toID, state, summary, updatedAt string
	version, createdMutation, createdOperation            int
	updatedMutation, updatedOperation                     int
}

// stageMergeNodes validates and stages every atomic effect of graph contract
// section 11. Persistence remains centralized in finalizeAndPersist so merge
// participates in the same ledger, idempotency, and integrity chain as every
// other Apply operation.
func stageMergeNodes(tx *sql.Tx, projectID string, op Operation, opIndex, graphRevision int, state *graphState) (OperationResult, error) {
	source, err := loadMutableNodeWithState(tx, projectID, op.Merge.Source, state)
	if err != nil {
		return OperationResult{}, err
	}
	canonical, err := loadMutableNodeWithState(tx, projectID, op.Merge.Canonical, state)
	if err != nil {
		return OperationResult{}, err
	}
	if source.nodeID == canonical.nodeID {
		return OperationResult{}, validationError(ErrCodeMergeSelf, "source and canonical resolve to the same node", opIndex, op.OpID, "operations[].merge.source")
	}
	if source.nodeType != canonical.nodeType || source.nodeType == NodeTypeGoal || source.nodeType == NodeTypeAttempt {
		return OperationResult{}, validationError(ErrCodeMergeTypeMismatch, "merge requires distinct mergeable nodes of the same type", opIndex, op.OpID, "operations[].merge")
	}
	if op.Merge.SourceExpectedVersion != source.version || op.Merge.CanonicalExpectedVersion != canonical.version {
		return OperationResult{}, validationError(ErrCodeVersionConflict, "merge expected versions do not match current versions", opIndex, op.OpID, "operations[].merge.expected_version")
	}
	if source.disposition == DispositionMerged || canonical.disposition == DispositionMerged {
		return OperationResult{}, validationError(ErrCodeMergeConflict, "merge endpoints must be current canonical identities", opIndex, op.OpID, "operations[].merge")
	}
	if canonical.disposition == DispositionArchived {
		touching, err := finalActiveEdgeTouches(tx, projectID, source.nodeID, state)
		if err != nil {
			return OperationResult{}, err
		}
		if touching {
			return OperationResult{}, validationError(ErrCodeMergeConflict, "cannot rewire active source edges to an archived canonical node", opIndex, op.OpID, "operations[].merge.canonical")
		}
	}

	canonicalVersion := canonical.version
	canonicalHash := canonical.semHash
	if len(op.Merge.CanonicalPatch) > 0 {
		props := clonePropertyMap(canonical.props)
		for key, value := range op.Merge.CanonicalPatch {
			props[key] = value
		}
		if validation := validateNodeProperties(canonical.nodeType, props); validation != nil {
			validation.OperationIndex, validation.OpID = opIndex, op.OpID
			return OperationResult{}, validation
		}
		propsJSON, encodeErr := canonicalJSON(props)
		if encodeErr != nil {
			return OperationResult{}, encodeErr
		}
		semHash := genericNodeSemanticHash(canonical.disposition, "", props)
		canonicalHash = hex.EncodeToString(semHash)
		if canonicalHash != canonical.semHash {
			canonicalVersion++
			state.pendingUpdates = append(state.pendingUpdates, pendingUpdate{nodeID: canonical.nodeID, nodeType: canonical.nodeType, stableKey: canonical.stableKey, version: canonicalVersion, propsJSON: propsJSON, semHash: semHash, opIndex: opIndex, graphRevision: graphRevision, disposition: canonical.disposition})
		}
	}

	sourceJSON, encodeErr := canonicalJSON(source.props)
	if encodeErr != nil {
		return OperationResult{}, encodeErr
	}
	sourceHash := genericNodeSemanticHash(DispositionMerged, canonical.nodeID, source.props)
	state.pendingUpdates = append(state.pendingUpdates, pendingUpdate{nodeID: source.nodeID, nodeType: source.nodeType, stableKey: source.stableKey, version: source.version + 1, propsJSON: sourceJSON, semHash: sourceHash, opIndex: opIndex, graphRevision: graphRevision, disposition: DispositionMerged, mergeTargetID: canonical.nodeID})

	keyRows, queryErr := tx.Query(`SELECT key,latest_key_version,source_node_id FROM blackboard_key_registry WHERE project_id=? AND node_type=? AND canonical_node_id=? ORDER BY key`, projectID, string(source.nodeType), source.nodeID)
	if queryErr != nil {
		return OperationResult{}, fmt.Errorf("read source aliases: %w", queryErr)
	}
	for keyRows.Next() {
		var key, sourceNodeID string
		var version int
		if scanErr := keyRows.Scan(&key, &version, &sourceNodeID); scanErr != nil {
			keyRows.Close()
			return OperationResult{}, scanErr
		}
		semHash := keySemanticHash(source.nodeType, key, "alias", sourceNodeID, canonical.nodeID, false)
		state.pendingKeys = append(state.pendingKeys, pendingKeyUpdate{nodeType: source.nodeType, key: key, role: "alias", sourceNodeID: sourceNodeID, canonicalNodeID: canonical.nodeID, keyVersion: version + 1, semHash: semHash, opIndex: opIndex, graphRevision: graphRevision})
	}
	if rowsErr := keyRows.Err(); rowsErr != nil {
		keyRows.Close()
		return OperationResult{}, rowsErr
	}
	keyRows.Close()
	latestPendingKeys := map[string]pendingKeyUpdate{}
	for _, key := range state.pendingKeys {
		latestPendingKeys[string(key.nodeType)+"\x00"+key.key] = key
	}
	for _, key := range latestPendingKeys {
		if key.canonicalNodeID != source.nodeID {
			continue
		}
		semHash := keySemanticHash(key.nodeType, key.key, "alias", key.sourceNodeID, canonical.nodeID, key.legacyNonconforming)
		state.pendingKeys = append(state.pendingKeys, pendingKeyUpdate{nodeType: key.nodeType, key: key.key, role: "alias", sourceNodeID: key.sourceNodeID, canonicalNodeID: canonical.nodeID, keyVersion: key.keyVersion + 1, legacyNonconforming: key.legacyNonconforming, semHash: semHash, opIndex: opIndex, graphRevision: graphRevision})
	}
	for _, created := range state.pending {
		if created.nodeID != source.nodeID {
			continue
		}
		alreadyStaged := false
		for _, key := range state.pendingKeys {
			if key.nodeType == source.nodeType && key.key == created.stableKey {
				alreadyStaged = true
				break
			}
		}
		if !alreadyStaged {
			semHash := keySemanticHash(source.nodeType, created.stableKey, "alias", source.nodeID, canonical.nodeID, false)
			state.pendingKeys = append(state.pendingKeys, pendingKeyUpdate{nodeType: source.nodeType, key: created.stableKey, role: "alias", sourceNodeID: source.nodeID, canonicalNodeID: canonical.nodeID, keyVersion: 2, semHash: semHash, opIndex: opIndex, graphRevision: graphRevision})
		}
	}

	if err := stageMergeEdges(tx, projectID, source.nodeID, canonical.nodeID, opIndex, graphRevision, state); err != nil {
		return OperationResult{}, err
	}
	if mergedGraphHasCycle(tx, projectID, state) {
		return OperationResult{}, validationError(ErrCodeMergeConflict, "merge rewiring would create a controlled graph cycle", opIndex, op.OpID, "operations[].merge")
	}
	return OperationResult{OpID: op.OpID, NodeID: canonical.nodeID, NodeType: canonical.nodeType, StableKey: canonical.stableKey, NodeVersion: canonicalVersion, SemanticHash: canonicalHash, Changed: true}, nil
}

func finalActiveEdgeTouches(tx *sql.Tx, projectID, nodeID string, state *graphState) (bool, error) {
	type edgeState struct{ from, to, state string }
	edges := map[string]edgeState{}
	rows, err := tx.Query(`SELECT edge_id,from_node_id,to_node_id,state FROM blackboard_edge_heads WHERE project_id=?`, projectID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id string
		var edge edgeState
		if err := rows.Scan(&id, &edge.from, &edge.to, &edge.state); err != nil {
			rows.Close()
			return false, err
		}
		edges[id] = edge
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	for _, edge := range state.pendingEdges {
		edges[edge.id] = edgeState{from: edge.fromID, to: edge.toID, state: "active"}
	}
	for _, edge := range state.pendingEdgeUpdates {
		edges[edge.id] = edgeState{from: edge.fromID, to: edge.toID, state: edge.state}
	}
	for _, edge := range edges {
		if edge.state == "active" && (edge.from == nodeID || edge.to == nodeID) {
			return true, nil
		}
	}
	return false, nil
}

func mergedGraphHasCycle(tx *sql.Tx, projectID string, state *graphState) bool {
	type finalEdge struct {
		edgeType        EdgeType
		from, to, state string
	}
	edges := map[string]finalEdge{}
	rows, err := tx.Query(`SELECT edge_id,edge_type,from_node_id,to_node_id,state FROM blackboard_edge_heads WHERE project_id=? AND edge_type IN ('part_of','depends_on','blocks','supersedes')`, projectID)
	if err != nil {
		return true
	}
	for rows.Next() {
		var id string
		var edge finalEdge
		if err := rows.Scan(&id, &edge.edgeType, &edge.from, &edge.to, &edge.state); err != nil {
			rows.Close()
			return true
		}
		edges[id] = edge
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return true
	}
	rows.Close()
	for _, update := range state.pendingEdgeUpdates {
		edges[update.id] = finalEdge{edgeType: update.edgeType, from: update.fromID, to: update.toID, state: update.state}
	}
	for _, created := range state.pendingEdges {
		edges[created.id] = finalEdge{edgeType: created.edgeType, from: created.fromID, to: created.toID, state: "active"}
	}
	graphs := []map[string][]string{{}, {}, {}}
	for _, edge := range edges {
		if edge.state != "active" {
			continue
		}
		from, to, graph := edge.from, edge.to, -1
		switch edge.edgeType {
		case EdgeTypePartOf:
			graph = 0
		case EdgeTypeDependsOn:
			graph, from, to = 1, to, from
		case EdgeTypeBlocks:
			graph = 1
		case EdgeTypeSupersedes:
			graph = 2
		}
		if graph >= 0 {
			graphs[graph][from] = append(graphs[graph][from], to)
		}
	}
	for _, graph := range graphs {
		if hasCycle(graph) {
			return true
		}
	}
	return false
}

func stageMergeEdges(tx *sql.Tx, projectID, sourceID, canonicalID string, opIndex, graphRevision int, state *graphState) error {
	rows, err := tx.Query(`SELECT h.edge_id,h.edge_type,h.from_node_id,h.to_node_id,h.version,h.state,v.summary,v.updated_at,e.created_mutation_seq,e.created_operation_index,v.mutation_seq,v.operation_index FROM blackboard_edge_heads h JOIN blackboard_edge_versions v ON v.project_id=h.project_id AND v.edge_id=h.edge_id AND v.version=h.version JOIN blackboard_edges e ON e.project_id=h.project_id AND e.id=h.edge_id WHERE h.project_id=? AND h.state='active'`, projectID)
	if err != nil {
		return fmt.Errorf("read active edges for merge: %w", err)
	}
	var all []mergeEdge
	byID := map[string]mergeEdge{}
	for rows.Next() {
		var edge mergeEdge
		if err := rows.Scan(&edge.id, &edge.edgeType, &edge.fromID, &edge.toID, &edge.version, &edge.state, &edge.summary, &edge.updatedAt, &edge.createdMutation, &edge.createdOperation, &edge.updatedMutation, &edge.updatedOperation); err != nil {
			rows.Close()
			return err
		}
		byID[edge.id] = edge
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, edge := range state.pendingEdges {
		byID[edge.id] = mergeEdge{id: edge.id, edgeType: string(edge.edgeType), fromID: edge.fromID, toID: edge.toID, version: 1, state: "active", summary: edge.summary, createdMutation: state.latestMutationSeq + 1, createdOperation: edge.opIndex, updatedMutation: state.latestMutationSeq + 1, updatedOperation: edge.opIndex}
	}
	for _, update := range state.pendingEdgeUpdates {
		edge := byID[update.id]
		edge.id, edge.edgeType, edge.fromID, edge.toID, edge.version, edge.state, edge.summary = update.id, string(update.edgeType), update.fromID, update.toID, update.version, update.state, update.summary
		edge.updatedMutation, edge.updatedOperation = state.latestMutationSeq+1, update.opIndex
		byID[update.id] = edge
	}
	for _, edge := range byID {
		if edge.state == "active" {
			all = append(all, edge)
		}
	}

	type transformed struct {
		edge     mergeEdge
		from, to string
	}
	groups := map[string][]transformed{}
	affected := map[string]bool{}
	for _, edge := range all {
		from, to := edge.fromID, edge.toID
		if from == sourceID {
			from = canonicalID
			affected[edge.id] = true
		}
		if to == sourceID {
			to = canonicalID
			affected[edge.id] = true
		}
		key := edge.edgeType + "\x00" + from + "\x00" + to
		groups[key] = append(groups[key], transformed{edge: edge, from: from, to: to})
	}
	for _, candidates := range groups {
		groupAffected := false
		for _, candidate := range candidates {
			groupAffected = groupAffected || affected[candidate.edge.id]
		}
		if !groupAffected {
			continue
		}
		if candidates[0].from == candidates[0].to {
			for _, candidate := range candidates {
				if affected[candidate.edge.id] {
					stageMergedEdge(candidate.edge, candidate.from, candidate.to, "retired", candidate.edge.summary, opIndex, graphRevision, state)
				}
			}
			continue
		}
		sort.Slice(candidates, func(i, j int) bool {
			iCanonical := candidates[i].edge.fromID == canonicalID || candidates[i].edge.toID == canonicalID
			jCanonical := candidates[j].edge.fromID == canonicalID || candidates[j].edge.toID == canonicalID
			if iCanonical != jCanonical {
				return iCanonical
			}
			if candidates[i].edge.createdMutation != candidates[j].edge.createdMutation {
				return candidates[i].edge.createdMutation < candidates[j].edge.createdMutation
			}
			if candidates[i].edge.createdOperation != candidates[j].edge.createdOperation {
				return candidates[i].edge.createdOperation < candidates[j].edge.createdOperation
			}
			return candidates[i].edge.id < candidates[j].edge.id
		})
		winner := candidates[0]
		summary, latestMutation, latestOperation, latestID := "", -1, -1, ""
		for _, candidate := range candidates {
			newer := candidate.edge.updatedMutation > latestMutation || (candidate.edge.updatedMutation == latestMutation && candidate.edge.updatedOperation > latestOperation) || (candidate.edge.updatedMutation == latestMutation && candidate.edge.updatedOperation == latestOperation && candidate.edge.id > latestID)
			if candidate.edge.summary != "" && newer {
				summary, latestMutation, latestOperation, latestID = candidate.edge.summary, candidate.edge.updatedMutation, candidate.edge.updatedOperation, candidate.edge.id
			}
		}
		if affected[winner.edge.id] || summary != winner.edge.summary {
			stageMergedEdge(winner.edge, winner.from, winner.to, "active", summary, opIndex, graphRevision, state)
		}
		for _, loser := range candidates[1:] {
			if affected[loser.edge.id] {
				stageMergedEdge(loser.edge, loser.from, loser.to, "retired", loser.edge.summary, opIndex, graphRevision, state)
			}
		}
	}
	return nil
}

func stageMergedEdge(edge mergeEdge, from, to, edgeState, summary string, opIndex, graphRevision int, state *graphState) {
	semHash := edgeSemanticHash(EdgeType(edge.edgeType), from, to, edgeState, summary)
	state.pendingEdgeUpdates = append(state.pendingEdgeUpdates, pendingEdgeUpdate{id: edge.id, edgeType: EdgeType(edge.edgeType), fromID: from, toID: to, summary: summary, version: edge.version + 1, semHash: semHash, opIndex: opIndex, graphRevision: graphRevision, state: edgeState})
}
