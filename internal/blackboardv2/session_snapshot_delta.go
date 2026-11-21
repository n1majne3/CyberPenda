package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// sessionSnapshotWithChangesTx reuses the previous committed projection and
// reads only changed current records and relationships. Snapshot encoding,
// hashing, and persistence still cost O(snapshot bytes); they remain in the
// mutation transaction so the Working Snapshot and its pin advance atomically.
func (s *Service) sessionSnapshotWithChangesTx(ctx context.Context, tx *sql.Tx, sessionID string, baseRevision int, changes semanticChangeSetResult) (RuntimeSnapshot, error) {
	var raw string
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT revision,snapshot_json FROM blackboard_v2_session_snapshots WHERE session_id=?`, sessionID).Scan(&revision, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RuntimeSnapshot{}, fmt.Errorf("read prior Session Working Snapshot: %w", err)
	}
	var snapshot RuntimeSnapshot
	if errors.Is(err, sql.ErrNoRows) || revision != baseRevision || json.Unmarshal([]byte(raw), &snapshot) != nil || snapshot.Revision != baseRevision || snapshot.Schema != snapshotSchema || snapshot.Semantics != snapshotSemantics {
		return s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
	}
	removed := make(map[string]bool)
	for key := range changes.ChangedRecords {
		delete(snapshot.Knowledge.Entities, key)
		delete(snapshot.Knowledge.Facts, key)
		delete(snapshot.Work.Objectives, key)
		delete(snapshot.Work.Attempts, key)
		record, err := loadSessionCurrentRecord(ctx, tx, sessionID, key)
		if errors.Is(err, sql.ErrNoRows) {
			removed[key] = true
			continue
		}
		if err != nil {
			return RuntimeSnapshot{}, err
		}
		switch record.typ {
		case "entity":
			if snapshot.Knowledge.Entities == nil {
				snapshot.Knowledge.Entities = map[string]SnapshotEntity{}
			}
			entity := record.record.entityRecord()
			snapshot.Knowledge.Entities[key] = SnapshotEntity{Version: record.version, Status: entity.Status, Kind: entity.Kind, Name: entity.Name, Locator: entity.Locator, Description: entity.Description, CredentialRef: entity.CredentialRef}
		case "fact":
			if snapshot.Knowledge.Facts == nil {
				snapshot.Knowledge.Facts = map[string]SnapshotFact{}
			}
			fact := record.record.factRecord()
			snapshot.Knowledge.Facts[key] = SnapshotFact{Version: record.version, Category: fact.Category, Summary: fact.Summary, Confidence: fact.Confidence}
		case "objective":
			if snapshot.Work.Objectives == nil {
				snapshot.Work.Objectives = map[string]SnapshotObjective{}
			}
			objective := record.record.objectiveRecord()
			snapshot.Work.Objectives[key] = SnapshotObjective{Version: record.version, Status: objective.Status, Objective: objective.Objective}
		case "attempt":
			if snapshot.Work.Attempts == nil {
				snapshot.Work.Attempts = map[string]SnapshotAttempt{}
			}
			attempt := record.record.attemptRecord()
			snapshot.Work.Attempts[key] = SnapshotAttempt{Version: record.version, Status: attempt.Status, Summary: attempt.Summary}
		}
	}
	if len(removed) == 0 && len(changes.ChangedRelations) == 0 {
		snapshot.Revision = changes.Revision
		return snapshot, nil
	}
	// Terminal records move all incident relationships to Semantic History, even
	// when those relationships are not listed separately by the change kernel.
	relations := make(map[[3]string]RelationshipTuple, len(snapshot.Relations))
	for _, tuple := range snapshot.Relations {
		if len(tuple) < 3 || len(tuple) > 4 {
			return s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
		}
		from, fromOK := tuple[0].(string)
		relation, relationOK := tuple[1].(string)
		to, toOK := tuple[2].(string)
		if !fromOK || !relationOK || !toOK {
			return s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
		}
		if !removed[from] && !removed[to] {
			relations[[3]string{from, relation, to}] = tuple
		}
	}
	for _, tuple := range changes.ChangedRelations {
		from, relation, to := tuple[0].(string), tuple[1].(string), tuple[2].(string)
		key := [3]string{from, relation, to}
		delete(relations, key)
		var reason string
		err := tx.QueryRowContext(ctx, `SELECT reason FROM blackboard_v2_session_relationships WHERE session_id=? AND from_key=? AND relation=? AND to_key=?`, sessionID, from, relation, to).Scan(&reason)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("read changed Session relationship: %w", err)
		}
		current := RelationshipTuple{from, relation, to}
		if reason != "" {
			current = append(current, reason)
		}
		relations[key] = current
	}
	snapshot.Relations = make([]RelationshipTuple, 0, len(relations))
	for _, tuple := range relations {
		snapshot.Relations = append(snapshot.Relations, tuple)
	}
	sort.Slice(snapshot.Relations, func(i, j int) bool {
		a, b := snapshot.Relations[i], snapshot.Relations[j]
		for k := 0; k < 3; k++ {
			if a[k] != b[k] {
				return a[k].(string) < b[k].(string)
			}
		}
		return false
	})
	snapshot.Revision = changes.Revision
	return snapshot, nil
}
