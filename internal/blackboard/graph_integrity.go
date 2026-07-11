package blackboard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

type mutationHashInput struct {
	ProjectID, MutationID                               string
	PreviousHash                                        []byte
	MutationSeq, BaseRevision, ResultRevision           int
	SchemaVersion                                       int
	MutationKind, MaintenanceMetadataJSON               string
	MaintenanceSubjectID                                *string
	IdempotencyScope, IdempotencyKey                    string
	RequestHash, ResultHash                             []byte
	RecordedAt                                          string
	ResultingStateHash                                  []byte
	ProjectionStatus, RendererVersion, EstimatorVersion string
	MainProjectionHash                                  []byte
	ProjectionBytes, ProjectionTokens                   *int
	RecordHashes                                        [][]byte
}

func computeMutationHash(in mutationHashInput) []byte {
	parts := [][]byte{
		[]byte(in.ProjectID), []byte(in.MutationID), in.PreviousHash,
		u64Bytes(uint64(in.MutationSeq)), u64Bytes(uint64(in.BaseRevision)), u64Bytes(uint64(in.ResultRevision)),
		u64Bytes(uint64(in.SchemaVersion)), u64Bytes(uint64(mutationKindOrdinal(in.MutationKind))),
		nullableString(in.MaintenanceSubjectID), []byte(in.MaintenanceMetadataJSON),
		[]byte(in.IdempotencyScope), []byte(in.IdempotencyKey), in.RequestHash, in.ResultHash,
		[]byte(in.RecordedAt), in.ResultingStateHash, u64Bytes(uint64(projectionStatusOrdinal(in.ProjectionStatus))),
		[]byte(in.RendererVersion), []byte(in.EstimatorVersion),
		nullableBytes(len(in.MainProjectionHash) > 0, in.MainProjectionHash), nullableInt(in.ProjectionBytes), nullableInt(in.ProjectionTokens),
	}
	parts = append(parts, in.RecordHashes...)
	return framedHash("CyberPenda.Blackboard.Mutation.v1", parts...)
}

// computeLegacyMutationHash verifies rows committed before C07 introduced
// changed-record integrity hashes and enum ordinals. The migration records an
// immutable per-Project cutover sequence; legacy rows are never rewritten.
func computeLegacyMutationHash(in mutationHashInput) []byte {
	parts := [][]byte{
		[]byte(in.ProjectID), []byte(in.MutationID), in.PreviousHash,
		u64Bytes(uint64(in.MutationSeq)), u64Bytes(uint64(in.BaseRevision)), u64Bytes(uint64(in.ResultRevision)),
		u64Bytes(uint64(in.SchemaVersion)), []byte(in.MutationKind),
		nullableString(in.MaintenanceSubjectID), []byte(in.MaintenanceMetadataJSON),
		[]byte(in.IdempotencyScope), []byte(in.IdempotencyKey), in.RequestHash, in.ResultHash,
		[]byte(in.RecordedAt), in.ResultingStateHash, []byte(in.ProjectionStatus),
		[]byte(in.RendererVersion), []byte(in.EstimatorVersion),
		nullableBytes(len(in.MainProjectionHash) > 0, in.MainProjectionHash), nullableInt(in.ProjectionBytes), nullableInt(in.ProjectionTokens),
	}
	return framedHash("CyberPenda.Blackboard.Mutation.v1", parts...)
}

func nullableString(value *string) []byte {
	if value == nil {
		return nullableBytes(false, nil)
	}
	return nullableBytes(true, []byte(*value))
}

func nullableInt(value *int) []byte {
	if value == nil {
		return nullableBytes(false, nil)
	}
	return nullableBytes(true, u64Bytes(uint64(*value)))
}

type sourceEventIntegrity struct {
	Ordinal int    `json:"ordinal"`
	EventID string `json:"event_id"`
}

type provenanceIntegrity struct {
	ProjectID, ID, ActorType, ActorID                string
	TaskID, ContinuationID, RuntimeProfileID, Runner string
	MigrationSourceJSON                              string
	RecordedAt                                       string
	Events                                           []sourceEventIntegrity
	TaskIDPresent, ContinuationIDPresent             bool
	RuntimeProfileIDPresent, RunnerPresent           bool
	MigrationSourcePresent                           bool
}

type operationIntegrity struct {
	ProjectID                                      string
	MutationSeq, OperationIndex                    int
	OpID, OperationKind, OperationJSON, ResultJSON string
	Changed                                        bool
	ProvenanceID, ProvenanceHash                   string
}

type nodeIdentityIntegrity struct {
	ProjectID, ID, NodeType, StableKey        string
	CreatedMutationSeq, CreatedOperationIndex int
	CreatedAt, ProvenanceHash                 string
}

type nodeVersionIntegrity struct {
	ProjectID, NodeID                                                                   string
	Version, ResultGraphRevision, MutationSeq, OperationIndex, SchemaVersion            int
	Disposition, MergeTargetID, PropertiesJSON, SemanticHash, UpdatedAt, ProvenanceHash string
	MergeTargetPresent                                                                  bool
}

type edgeIdentityIntegrity struct {
	ProjectID, ID, EdgeType                   string
	CreatedMutationSeq, CreatedOperationIndex int
	CreatedAt, ProvenanceHash                 string
}

type edgeVersionIntegrity struct {
	ProjectID, EdgeID                                                             string
	Version, ResultGraphRevision, MutationSeq, OperationIndex                     int
	FromNodeID, ToNodeID, State, Summary, SemanticHash, UpdatedAt, ProvenanceHash string
}

type keyEventIntegrity struct {
	ProjectID, NodeType, Key                         string
	KeyVersion                                       int
	Role, SourceNodeID, CanonicalNodeID              string
	LegacyNonconforming                              bool
	ResultGraphRevision, MutationSeq, OperationIndex int
	SemanticHash, ProvenanceHash                     string
}

type integrityKind uint64

const (
	integrityProvenance integrityKind = iota + 1
	integrityOperation
	integrityNodeIdentity
	integrityNodeVersion
	integrityEdgeIdentity
	integrityEdgeVersion
	integrityKeyEvent
)

type integrityRecord struct {
	kind     integrityKind
	identity string
	data     any
}

func hashIntegrityRecords(records []integrityRecord) ([][]byte, error) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].kind != records[j].kind {
			return records[i].kind < records[j].kind
		}
		return records[i].identity < records[j].identity
	})
	hashes := make([][]byte, len(records))
	for i, record := range records {
		hash, err := hashIntegrityRecord(record)
		if err != nil {
			return nil, err
		}
		hashes[i] = hash
	}
	return hashes, nil
}

func hashIntegrityRecord(record integrityRecord) ([]byte, error) {
	parts := [][]byte{u64Bytes(uint64(record.kind))}
	appendHash := func(value string) error {
		decoded, err := hex.DecodeString(value)
		if err != nil {
			return err
		}
		parts = append(parts, decoded)
		return nil
	}
	switch row := record.data.(type) {
	case provenanceIntegrity:
		parts = append(parts, []byte(row.ProjectID), []byte(row.ID), u64Bytes(uint64(actorTypeOrdinal(row.ActorType))), []byte(row.ActorID),
			nullableBytes(row.TaskIDPresent, []byte(row.TaskID)), nullableBytes(row.ContinuationIDPresent, []byte(row.ContinuationID)),
			nullableBytes(row.RuntimeProfileIDPresent, []byte(row.RuntimeProfileID)), nullableBytes(row.RunnerPresent, []byte(row.Runner)),
			nullableBytes(row.MigrationSourcePresent, []byte(row.MigrationSourceJSON)), []byte(row.RecordedAt))
		for _, event := range row.Events {
			parts = append(parts, u64Bytes(uint64(event.Ordinal)), []byte(event.EventID))
		}
	case operationIntegrity:
		parts = append(parts, []byte(row.ProjectID), u64Bytes(uint64(row.MutationSeq)), u64Bytes(uint64(row.OperationIndex)), []byte(row.OpID), u64Bytes(uint64(operationKindOrdinal(row.OperationKind))), []byte(row.OperationJSON), []byte(row.ResultJSON), u64Bytes(boolOrdinal(row.Changed)), []byte(row.ProvenanceID))
		if err := appendHash(row.ProvenanceHash); err != nil {
			return nil, err
		}
	case nodeIdentityIntegrity:
		parts = append(parts, []byte(row.ProjectID), []byte(row.ID), u64Bytes(uint64(nodeTypeOrdinal(NodeType(row.NodeType)))), []byte(row.StableKey), u64Bytes(uint64(row.CreatedMutationSeq)), u64Bytes(uint64(row.CreatedOperationIndex)), []byte(row.CreatedAt))
		if err := appendHash(row.ProvenanceHash); err != nil {
			return nil, err
		}
	case nodeVersionIntegrity:
		parts = append(parts, []byte(row.ProjectID), []byte(row.NodeID), u64Bytes(uint64(row.Version)), u64Bytes(uint64(row.ResultGraphRevision)), u64Bytes(uint64(row.MutationSeq)), u64Bytes(uint64(row.OperationIndex)), u64Bytes(uint64(row.SchemaVersion)), u64Bytes(uint64(dispositionOrdinal(row.Disposition))), nullableBytes(row.MergeTargetPresent, []byte(row.MergeTargetID)), []byte(row.PropertiesJSON))
		if err := appendHash(row.SemanticHash); err != nil {
			return nil, err
		}
		parts = append(parts, []byte(row.UpdatedAt))
		if err := appendHash(row.ProvenanceHash); err != nil {
			return nil, err
		}
	case edgeIdentityIntegrity:
		parts = append(parts, []byte(row.ProjectID), []byte(row.ID), u64Bytes(uint64(edgeTypeOrdinal(EdgeType(row.EdgeType)))), u64Bytes(uint64(row.CreatedMutationSeq)), u64Bytes(uint64(row.CreatedOperationIndex)), []byte(row.CreatedAt))
		if err := appendHash(row.ProvenanceHash); err != nil {
			return nil, err
		}
	case edgeVersionIntegrity:
		parts = append(parts, []byte(row.ProjectID), []byte(row.EdgeID), u64Bytes(uint64(row.Version)), u64Bytes(uint64(row.ResultGraphRevision)), u64Bytes(uint64(row.MutationSeq)), u64Bytes(uint64(row.OperationIndex)), []byte(row.FromNodeID), []byte(row.ToNodeID), u64Bytes(uint64(edgeStateOrdinal(row.State))), []byte(row.Summary))
		if err := appendHash(row.SemanticHash); err != nil {
			return nil, err
		}
		parts = append(parts, []byte(row.UpdatedAt))
		if err := appendHash(row.ProvenanceHash); err != nil {
			return nil, err
		}
	case keyEventIntegrity:
		parts = append(parts, []byte(row.ProjectID), u64Bytes(uint64(nodeTypeOrdinal(NodeType(row.NodeType)))), []byte(row.Key), u64Bytes(uint64(row.KeyVersion)), u64Bytes(uint64(keyRoleOrdinal(row.Role))), []byte(row.SourceNodeID), []byte(row.CanonicalNodeID), u64Bytes(boolOrdinal(row.LegacyNonconforming)), u64Bytes(uint64(row.ResultGraphRevision)), u64Bytes(uint64(row.MutationSeq)), u64Bytes(uint64(row.OperationIndex)))
		if err := appendHash(row.SemanticHash); err != nil {
			return nil, err
		}
		if err := appendHash(row.ProvenanceHash); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported integrity record kind %d", record.kind)
	}
	return framedHash("CyberPenda.Blackboard.IntegrityRecord.v1", parts...), nil
}

func boolOrdinal(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func actorTypeOrdinal(value string) int {
	return stringOrdinal(value, "runtime", "operator", "system", "migration")
}
func mutationKindOrdinal(value string) int {
	return stringOrdinal(value, "normal", "merge", "compaction", "restore", "reconciliation", "projection", "migration")
}
func projectionStatusOrdinal(value string) int { return stringOrdinal(value, "measured", "dirty") }
func operationKindOrdinal(value string) int {
	return stringOrdinal(value, "create_node", "patch_node", "transition_node", "put_edge", "retire_edge", "set_disposition", "merge_nodes")
}
func dispositionOrdinal(value string) int { return stringOrdinal(value, "main", "archived", "merged") }
func edgeStateOrdinal(value string) int   { return stringOrdinal(value, "active", "retired") }
func keyRoleOrdinal(value string) int     { return stringOrdinal(value, "stable", "alias") }
func stringOrdinal(value string, ordered ...string) int {
	for i, candidate := range ordered {
		if value == candidate {
			return i
		}
	}
	return len(ordered)
}

func pendingIntegrityHashes(batch MutationBatch, state graphState, operations []pendingOperationRow, mutationSeq int, recordedAt string) ([][]byte, error) {
	records := make([]integrityRecord, 0, len(operations)*2)
	provenanceHashes := make(map[string]string, len(operations))
	for i, op := range batch.Operations {
		events := make([]sourceEventIntegrity, len(batch.SourceEventIDsByOp[op.OpID]))
		for ordinal, eventID := range batch.SourceEventIDsByOp[op.OpID] {
			events[ordinal] = sourceEventIntegrity{ordinal, eventID}
		}
		row := provenanceIntegrity{ProjectID: batch.Context.ProjectID, ID: state.provenanceIDs[i], ActorType: string(batch.Context.ActorType), ActorID: batch.Context.ActorID, TaskID: batch.Context.TaskID, ContinuationID: batch.Context.ContinuationID, RuntimeProfileID: batch.Context.RuntimeProfileID, Runner: batch.Context.Runner, RecordedAt: recordedAt, Events: events, TaskIDPresent: batch.Context.TaskID != "", ContinuationIDPresent: batch.Context.ContinuationID != "", RuntimeProfileIDPresent: batch.Context.RuntimeProfileID != "", RunnerPresent: batch.Context.Runner != ""}
		h, err := hashIntegrityRecord(integrityRecord{kind: integrityProvenance, identity: row.ID, data: row})
		if err != nil {
			return nil, err
		}
		provenanceHashes[row.ID] = hex.EncodeToString(h)
		records = append(records, integrityRecord{kind: integrityProvenance, identity: row.ID, data: row})
	}
	for i, row := range operations {
		records = append(records, integrityRecord{kind: integrityOperation, identity: fmt.Sprintf("%020d:%020d", mutationSeq, i), data: operationIntegrity{ProjectID: batch.Context.ProjectID, MutationSeq: mutationSeq, OperationIndex: i, OpID: row.opID, OperationKind: string(batch.Operations[i].Kind), OperationJSON: string(row.opJSON), ResultJSON: string(row.resJSON), Changed: row.changed, ProvenanceID: row.provID, ProvenanceHash: provenanceHashes[row.provID]}})
	}
	provenanceForOp := func(index int) string { return provenanceHashes[state.provenanceIDs[index]] }
	for _, row := range state.pending {
		records = append(records,
			integrityRecord{kind: integrityNodeIdentity, identity: row.nodeID, data: nodeIdentityIntegrity{ProjectID: batch.Context.ProjectID, ID: row.nodeID, NodeType: string(row.nodeType), StableKey: row.stableKey, CreatedMutationSeq: mutationSeq, CreatedOperationIndex: row.opIndex, CreatedAt: recordedAt, ProvenanceHash: provenanceForOp(row.opIndex)}},
			integrityRecord{kind: integrityNodeVersion, identity: fmt.Sprintf("%s:%020d", row.nodeID, row.version), data: nodeVersionIntegrity{ProjectID: batch.Context.ProjectID, NodeID: row.nodeID, Version: row.version, ResultGraphRevision: row.graphRevision, MutationSeq: mutationSeq, OperationIndex: row.opIndex, SchemaVersion: GraphMutationSchemaVersion, Disposition: string(DispositionMain), PropertiesJSON: string(row.propsJSON), SemanticHash: hex.EncodeToString(row.semHash), UpdatedAt: recordedAt, ProvenanceHash: provenanceForOp(row.opIndex)}},
			integrityRecord{kind: integrityKeyEvent, identity: fmt.Sprintf("%020d:%s:%020d", nodeTypeOrdinal(row.nodeType), row.stableKey, 1), data: keyEventIntegrity{ProjectID: batch.Context.ProjectID, NodeType: string(row.nodeType), Key: row.stableKey, KeyVersion: 1, Role: "stable", SourceNodeID: row.nodeID, CanonicalNodeID: row.nodeID, ResultGraphRevision: row.graphRevision, MutationSeq: mutationSeq, OperationIndex: row.opIndex, SemanticHash: hex.EncodeToString(row.keySemHash), ProvenanceHash: provenanceForOp(row.opIndex)}},
		)
	}
	for _, row := range state.pendingUpdates {
		records = append(records, integrityRecord{kind: integrityNodeVersion, identity: fmt.Sprintf("%s:%020d", row.nodeID, row.version), data: nodeVersionIntegrity{ProjectID: batch.Context.ProjectID, NodeID: row.nodeID, Version: row.version, ResultGraphRevision: row.graphRevision, MutationSeq: mutationSeq, OperationIndex: row.opIndex, SchemaVersion: GraphMutationSchemaVersion, Disposition: string(DispositionMain), PropertiesJSON: string(row.propsJSON), SemanticHash: hex.EncodeToString(row.semHash), UpdatedAt: recordedAt, ProvenanceHash: provenanceForOp(row.opIndex)}})
	}
	for _, row := range state.pendingEdges {
		records = append(records,
			integrityRecord{kind: integrityEdgeIdentity, identity: row.id, data: edgeIdentityIntegrity{ProjectID: batch.Context.ProjectID, ID: row.id, EdgeType: string(row.edgeType), CreatedMutationSeq: mutationSeq, CreatedOperationIndex: row.opIndex, CreatedAt: recordedAt, ProvenanceHash: provenanceForOp(row.opIndex)}},
			integrityRecord{kind: integrityEdgeVersion, identity: fmt.Sprintf("%s:%020d", row.id, 1), data: edgeVersionIntegrity{ProjectID: batch.Context.ProjectID, EdgeID: row.id, Version: 1, ResultGraphRevision: row.graphRevision, MutationSeq: mutationSeq, OperationIndex: row.opIndex, FromNodeID: row.fromID, ToNodeID: row.toID, State: "active", Summary: row.summary, SemanticHash: hex.EncodeToString(row.semHash), UpdatedAt: recordedAt, ProvenanceHash: provenanceForOp(row.opIndex)}},
		)
	}
	for _, row := range state.pendingEdgeUpdates {
		records = append(records, integrityRecord{kind: integrityEdgeVersion, identity: fmt.Sprintf("%s:%020d", row.id, row.version), data: edgeVersionIntegrity{ProjectID: batch.Context.ProjectID, EdgeID: row.id, Version: row.version, ResultGraphRevision: row.graphRevision, MutationSeq: mutationSeq, OperationIndex: row.opIndex, FromNodeID: row.fromID, ToNodeID: row.toID, State: "active", Summary: row.summary, SemanticHash: hex.EncodeToString(row.semHash), UpdatedAt: recordedAt, ProvenanceHash: provenanceForOp(row.opIndex)}})
	}
	return hashIntegrityRecords(records)
}

func ledgerIntegrityHashes(ctx context.Context, q graphQuerier, projectID string, mutationSeq int) ([][]byte, error) {
	type rawOperation struct {
		index                          int
		opID, kind, opJSON, resultJSON string
		changed                        int
		provID                         string
	}
	rows, err := q.QueryContext(ctx, `SELECT operation_index,op_id,operation_kind,operation_json,result_json,changed,provenance_id FROM blackboard_graph_operations WHERE project_id=? AND mutation_seq=? ORDER BY operation_index`, projectID, mutationSeq)
	if err != nil {
		return nil, fmt.Errorf("read integrity operations: %w", err)
	}
	var operations []rawOperation
	for rows.Next() {
		var row rawOperation
		if err := rows.Scan(&row.index, &row.opID, &row.kind, &row.opJSON, &row.resultJSON, &row.changed, &row.provID); err != nil {
			rows.Close()
			return nil, err
		}
		operations = append(operations, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	records := make([]integrityRecord, 0, len(operations)*2)
	provHashes := map[string]string{}
	for _, op := range operations {
		if _, ok := provHashes[op.provID]; ok {
			continue
		}
		var row provenanceIntegrity
		var taskID, contID, profileID, runner, migration sql.NullString
		if err := q.QueryRowContext(ctx, `SELECT project_id,id,actor_type,actor_id,task_id,continuation_id,runtime_profile_id,runner,migration_source_json,recorded_at FROM blackboard_graph_provenance WHERE project_id=? AND id=?`, projectID, op.provID).Scan(&row.ProjectID, &row.ID, &row.ActorType, &row.ActorID, &taskID, &contID, &profileID, &runner, &migration, &row.RecordedAt); err != nil {
			return nil, fmt.Errorf("read integrity provenance: %w", err)
		}
		row.TaskID, row.ContinuationID, row.RuntimeProfileID, row.Runner, row.MigrationSourceJSON = taskID.String, contID.String, profileID.String, runner.String, migration.String
		row.TaskIDPresent, row.ContinuationIDPresent, row.RuntimeProfileIDPresent, row.RunnerPresent, row.MigrationSourcePresent = taskID.Valid, contID.Valid, profileID.Valid, runner.Valid, migration.Valid
		eventRows, err := q.QueryContext(ctx, `SELECT ordinal,event_id FROM blackboard_graph_provenance_events WHERE project_id=? AND provenance_id=? ORDER BY ordinal`, projectID, op.provID)
		if err != nil {
			return nil, err
		}
		row.Events = []sourceEventIntegrity{}
		for eventRows.Next() {
			var event sourceEventIntegrity
			if err := eventRows.Scan(&event.Ordinal, &event.EventID); err != nil {
				eventRows.Close()
				return nil, err
			}
			row.Events = append(row.Events, event)
		}
		if err := eventRows.Err(); err != nil {
			return nil, err
		}
		eventRows.Close()
		h, err := hashIntegrityRecord(integrityRecord{kind: integrityProvenance, identity: row.ID, data: row})
		if err != nil {
			return nil, err
		}
		provHashes[row.ID] = hex.EncodeToString(h)
		records = append(records, integrityRecord{kind: integrityProvenance, identity: row.ID, data: row})
	}
	provenanceForIndex := func(index int) string {
		for _, op := range operations {
			if op.index == index {
				return provHashes[op.provID]
			}
		}
		return ""
	}
	nodeRows, err := q.QueryContext(ctx, `SELECT id,node_type,original_stable_key,created_operation_index,created_at FROM blackboard_nodes WHERE project_id=? AND created_mutation_seq=? ORDER BY id`, projectID, mutationSeq)
	if err != nil {
		return nil, err
	}
	for nodeRows.Next() {
		var id, nodeType, key, at string
		var opIndex int
		if err := nodeRows.Scan(&id, &nodeType, &key, &opIndex, &at); err != nil {
			nodeRows.Close()
			return nil, err
		}
		records = append(records, integrityRecord{kind: integrityNodeIdentity, identity: id, data: nodeIdentityIntegrity{ProjectID: projectID, ID: id, NodeType: nodeType, StableKey: key, CreatedMutationSeq: mutationSeq, CreatedOperationIndex: opIndex, CreatedAt: at, ProvenanceHash: provenanceForIndex(opIndex)}})
	}
	if err := nodeRows.Err(); err != nil {
		nodeRows.Close()
		return nil, err
	}
	nodeRows.Close()
	nodeVersionRows, err := q.QueryContext(ctx, `SELECT node_id,version,result_graph_revision,operation_index,schema_version,disposition,merge_target_id,properties_json,semantic_hash,updated_at FROM blackboard_node_versions WHERE project_id=? AND mutation_seq=? ORDER BY node_id,version`, projectID, mutationSeq)
	if err != nil {
		return nil, err
	}
	for nodeVersionRows.Next() {
		var row nodeVersionIntegrity
		var merge sql.NullString
		if err := nodeVersionRows.Scan(&row.NodeID, &row.Version, &row.ResultGraphRevision, &row.OperationIndex, &row.SchemaVersion, &row.Disposition, &merge, &row.PropertiesJSON, &row.SemanticHash, &row.UpdatedAt); err != nil {
			nodeVersionRows.Close()
			return nil, err
		}
		row.ProjectID, row.MutationSeq, row.MergeTargetID, row.MergeTargetPresent, row.ProvenanceHash = projectID, mutationSeq, merge.String, merge.Valid, provenanceForIndex(row.OperationIndex)
		records = append(records, integrityRecord{kind: integrityNodeVersion, identity: fmt.Sprintf("%s:%020d", row.NodeID, row.Version), data: row})
	}
	if err := nodeVersionRows.Err(); err != nil {
		nodeVersionRows.Close()
		return nil, err
	}
	nodeVersionRows.Close()
	edgeRows, err := q.QueryContext(ctx, `SELECT id,edge_type,created_operation_index,created_at FROM blackboard_edges WHERE project_id=? AND created_mutation_seq=? ORDER BY id`, projectID, mutationSeq)
	if err != nil {
		return nil, err
	}
	for edgeRows.Next() {
		var row edgeIdentityIntegrity
		if err := edgeRows.Scan(&row.ID, &row.EdgeType, &row.CreatedOperationIndex, &row.CreatedAt); err != nil {
			edgeRows.Close()
			return nil, err
		}
		row.ProjectID, row.CreatedMutationSeq, row.ProvenanceHash = projectID, mutationSeq, provenanceForIndex(row.CreatedOperationIndex)
		records = append(records, integrityRecord{kind: integrityEdgeIdentity, identity: row.ID, data: row})
	}
	if err := edgeRows.Err(); err != nil {
		edgeRows.Close()
		return nil, err
	}
	edgeRows.Close()
	edgeVersionRows, err := q.QueryContext(ctx, `SELECT edge_id,version,result_graph_revision,operation_index,from_node_id,to_node_id,state,summary,semantic_hash,updated_at FROM blackboard_edge_versions WHERE project_id=? AND mutation_seq=? ORDER BY edge_id,version`, projectID, mutationSeq)
	if err != nil {
		return nil, err
	}
	for edgeVersionRows.Next() {
		var row edgeVersionIntegrity
		if err := edgeVersionRows.Scan(&row.EdgeID, &row.Version, &row.ResultGraphRevision, &row.OperationIndex, &row.FromNodeID, &row.ToNodeID, &row.State, &row.Summary, &row.SemanticHash, &row.UpdatedAt); err != nil {
			edgeVersionRows.Close()
			return nil, err
		}
		row.ProjectID, row.MutationSeq, row.ProvenanceHash = projectID, mutationSeq, provenanceForIndex(row.OperationIndex)
		records = append(records, integrityRecord{kind: integrityEdgeVersion, identity: fmt.Sprintf("%s:%020d", row.EdgeID, row.Version), data: row})
	}
	if err := edgeVersionRows.Err(); err != nil {
		edgeVersionRows.Close()
		return nil, err
	}
	edgeVersionRows.Close()
	keyRows, err := q.QueryContext(ctx, `SELECT node_type,key,key_version,role,source_node_id,canonical_node_id,legacy_nonconforming,result_graph_revision,operation_index,semantic_hash FROM blackboard_key_events WHERE project_id=? AND mutation_seq=? ORDER BY node_type,key,key_version`, projectID, mutationSeq)
	if err != nil {
		return nil, err
	}
	for keyRows.Next() {
		var row keyEventIntegrity
		var legacy int
		if err := keyRows.Scan(&row.NodeType, &row.Key, &row.KeyVersion, &row.Role, &row.SourceNodeID, &row.CanonicalNodeID, &legacy, &row.ResultGraphRevision, &row.OperationIndex, &row.SemanticHash); err != nil {
			keyRows.Close()
			return nil, err
		}
		row.ProjectID, row.MutationSeq, row.LegacyNonconforming, row.ProvenanceHash = projectID, mutationSeq, legacy == 1, provenanceForIndex(row.OperationIndex)
		records = append(records, integrityRecord{kind: integrityKeyEvent, identity: fmt.Sprintf("%020d:%s:%020d", nodeTypeOrdinal(NodeType(row.NodeType)), row.Key, row.KeyVersion), data: row})
	}
	if err := keyRows.Err(); err != nil {
		keyRows.Close()
		return nil, err
	}
	keyRows.Close()
	for _, op := range operations {
		records = append(records, integrityRecord{kind: integrityOperation, identity: fmt.Sprintf("%020d:%020d", mutationSeq, op.index), data: operationIntegrity{ProjectID: projectID, MutationSeq: mutationSeq, OperationIndex: op.index, OpID: op.opID, OperationKind: op.kind, OperationJSON: op.opJSON, ResultJSON: op.resultJSON, Changed: op.changed == 1, ProvenanceID: op.provID, ProvenanceHash: provHashes[op.provID]}})
	}
	return hashIntegrityRecords(records)
}

// VerifyIntegrity recomputes request/result hashes and the complete per-Project
// mutation chain from append-only rows. Materialized heads are deliberately
// excluded because they are repairable caches.
func (s *GraphService) VerifyIntegrity(ctx context.Context, projectID string) error {
	return verifyMutationChain(ctx, s.db, projectID)
}

func verifyMutationChain(ctx context.Context, q graphQuerier, projectID string) error {
	legacyThrough, err := legacyIntegrityThrough(ctx, q, projectID)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT mutation_seq,mutation_id,base_graph_revision,result_graph_revision,schema_version,mutation_kind,maintenance_metadata_json,maintenance_subject_id,idempotency_scope,idempotency_key,request_hash,request_json,result_hash,result_json,recorded_at,previous_mutation_hash,mutation_hash,resulting_state_hash,projection_status,resulting_main_projection_hash,projection_renderer_version,projection_estimator_version,projection_bytes,projection_estimated_tokens FROM blackboard_graph_mutations WHERE project_id=? ORDER BY mutation_seq`, projectID)
	if err != nil {
		return fmt.Errorf("read mutation chain: %w", err)
	}
	type header struct {
		seq, base, result, schema                                                                                          int
		id, kind, meta, scope, key, reqHash, reqJSON, resHash, resJSON, at, prev, hash, state, status, renderer, estimator string
		subject, main                                                                                                      sql.NullString
		bytes, tokens                                                                                                      sql.NullInt64
	}
	var headers []header
	for rows.Next() {
		var h header
		if err := rows.Scan(&h.seq, &h.id, &h.base, &h.result, &h.schema, &h.kind, &h.meta, &h.subject, &h.scope, &h.key, &h.reqHash, &h.reqJSON, &h.resHash, &h.resJSON, &h.at, &h.prev, &h.hash, &h.state, &h.status, &h.main, &h.renderer, &h.estimator, &h.bytes, &h.tokens); err != nil {
			rows.Close()
			return err
		}
		headers = append(headers, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	previous := genesisHash(projectID)
	lastRevision := 0
	for i, h := range headers {
		if h.seq != i+1 {
			return fmt.Errorf("mutation sequence is not contiguous at %d", h.seq)
		}
		if h.base != lastRevision || (h.result != h.base && h.result != h.base+1) {
			return fmt.Errorf("graph revision chain is broken at mutation %d", h.seq)
		}
		storedPrev, err := hex.DecodeString(h.prev)
		if err != nil {
			return err
		}
		if !equalBytes(storedPrev, previous) {
			return fmt.Errorf("previous mutation hash mismatch at mutation %d", h.seq)
		}
		reqSum := sha256.Sum256([]byte(h.reqJSON))
		resSum := sha256.Sum256([]byte(h.resJSON))
		if hex.EncodeToString(reqSum[:]) != h.reqHash {
			return fmt.Errorf("request hash mismatch at mutation %d", h.seq)
		}
		if hex.EncodeToString(resSum[:]) != h.resHash {
			return fmt.Errorf("result hash mismatch at mutation %d", h.seq)
		}
		reqHash, _ := hex.DecodeString(h.reqHash)
		resHash, _ := hex.DecodeString(h.resHash)
		stateHash, err := hex.DecodeString(h.state)
		if err != nil {
			return err
		}
		recordHashes, err := ledgerIntegrityHashes(ctx, q, projectID, h.seq)
		if err != nil {
			return err
		}
		var subject *string
		if h.subject.Valid {
			v := h.subject.String
			subject = &v
		}
		var mainHash []byte
		if h.main.Valid {
			mainHash, err = hex.DecodeString(h.main.String)
			if err != nil {
				return err
			}
		}
		var projectionBytes, projectionTokens *int
		if h.bytes.Valid {
			v := int(h.bytes.Int64)
			projectionBytes = &v
		}
		if h.tokens.Valid {
			v := int(h.tokens.Int64)
			projectionTokens = &v
		}
		input := mutationHashInput{ProjectID: projectID, MutationID: h.id, PreviousHash: previous, MutationSeq: h.seq, BaseRevision: h.base, ResultRevision: h.result, SchemaVersion: h.schema, MutationKind: h.kind, MaintenanceMetadataJSON: h.meta, MaintenanceSubjectID: subject, IdempotencyScope: h.scope, IdempotencyKey: h.key, RequestHash: reqHash, ResultHash: resHash, RecordedAt: h.at, ResultingStateHash: stateHash, ProjectionStatus: h.status, RendererVersion: h.renderer, EstimatorVersion: h.estimator, MainProjectionHash: mainHash, ProjectionBytes: projectionBytes, ProjectionTokens: projectionTokens, RecordHashes: recordHashes}
		calculated := computeMutationHash(input)
		if h.seq <= legacyThrough {
			if err := verifyLegacyRecordAnchors(ctx, q, projectID, h.seq); err != nil {
				return err
			}
			calculated = computeLegacyMutationHash(input)
		}
		stored, err := hex.DecodeString(h.hash)
		if err != nil {
			return err
		}
		if !equalBytes(calculated, stored) {
			return fmt.Errorf("mutation integrity hash mismatch at mutation %d", h.seq)
		}
		previous = stored
		lastRevision = h.result
	}
	return nil
}

func verifyLegacyRecordAnchors(ctx context.Context, q graphQuerier, projectID string, mutationSeq int) error {
	var missingCurrent int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_graph_legacy_record_anchors a
		WHERE a.project_id=? AND a.mutation_seq=? AND NOT EXISTS (
			SELECT 1 FROM blackboard_graph_legacy_current_records r
			WHERE r.project_id=a.project_id AND r.mutation_seq=a.mutation_seq AND r.record_kind=a.record_kind AND r.record_identity=a.record_identity AND r.record_json=a.record_json)`, projectID, mutationSeq).Scan(&missingCurrent); err != nil {
		return fmt.Errorf("verify legacy record anchors: %w", err)
	}
	var missingAnchor int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_graph_legacy_current_records r
		WHERE r.project_id=? AND r.mutation_seq=? AND NOT EXISTS (
			SELECT 1 FROM blackboard_graph_legacy_record_anchors a
			WHERE a.project_id=r.project_id AND a.mutation_seq=r.mutation_seq AND a.record_kind=r.record_kind AND a.record_identity=r.record_identity AND a.record_json=r.record_json)`, projectID, mutationSeq).Scan(&missingAnchor); err != nil {
		return fmt.Errorf("verify legacy current records: %w", err)
	}
	if missingCurrent != 0 || missingAnchor != 0 {
		return fmt.Errorf("legacy changed-record integrity mismatch at mutation %d", mutationSeq)
	}
	return nil
}

func legacyIntegrityThrough(ctx context.Context, q graphQuerier, projectID string) (int, error) {
	var legacyThrough int
	if err := q.QueryRowContext(ctx, `SELECT legacy_through_mutation_seq FROM blackboard_graph_integrity_cutovers WHERE project_id=?`, projectID).Scan(&legacyThrough); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("read integrity cutover: %w", err)
	}
	return legacyThrough, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
