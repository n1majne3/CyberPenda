package blackboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"pentest/internal/project"
)

type verifiedFlagProjection struct {
	summary VerifiedFlagSummary
	value   string
}

// ReadCTFSolvedState derives the reversible CTF completion state from current
// main verified flag Solutions. No solved boolean or synthetic node is stored.
func (s *GraphService) ReadCTFSolvedState(ctx context.Context, projectID string) (CTFSolvedState, error) {
	var kind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id=?`, projectID).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CTFSolvedState{}, validationError(ErrCodeProjectNotFound, "Project does not exist", -1, "", "project_id")
		}
		return CTFSolvedState{}, fmt.Errorf("read CTF Project kind: %w", err)
	}
	if kind != project.KindCTFChallenge {
		return CTFSolvedState{}, validationError(ErrCodeProjectKindViolation, "CTF solved state is valid only for a ctf_challenge Project", -1, "", "project_id")
	}

	rows, err := s.db.QueryContext(ctx, `SELECT h.node_id,n.original_stable_key,v.properties_json
		FROM blackboard_node_heads h
		JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id
		JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		WHERE h.project_id=? AND h.node_type='solution' AND h.disposition='main'
		  AND json_extract(v.properties_json, '$.kind')='flag'
		  AND json_extract(v.properties_json, '$.status')='verified'
		ORDER BY n.original_stable_key ASC,h.node_id ASC`, projectID)
	if err != nil {
		return CTFSolvedState{}, fmt.Errorf("list verified flag Solutions: %w", err)
	}

	projections := make([]verifiedFlagProjection, 0)
	for rows.Next() {
		var id, stableKey, raw string
		if err := rows.Scan(&id, &stableKey, &raw); err != nil {
			return CTFSolvedState{}, fmt.Errorf("scan verified flag Solution: %w", err)
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(raw), &props); err != nil {
			return CTFSolvedState{}, fmt.Errorf("decode verified flag Solution: %w", err)
		}
		projections = append(projections, verifiedFlagProjection{
			summary: VerifiedFlagSummary{
				ID:                  id,
				StableKey:           stableKey,
				Summary:             stringProp(props, "summary"),
				VerificationSummary: stringProp(props, "verification_summary"),
			},
			value: stringProp(props, "value"),
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CTFSolvedState{}, fmt.Errorf("iterate verified flag Solutions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return CTFSolvedState{}, fmt.Errorf("close verified flag Solutions: %w", err)
	}
	state := CTFSolvedState{ProjectID: projectID, VerifiedFlags: make([]VerifiedFlagSummary, len(projections))}
	values := make(map[string]struct{}, len(projections))
	for i, flag := range projections {
		state.VerifiedFlags[i] = flag.summary
		values[flag.value] = struct{}{}
	}
	state.Solved = len(state.VerifiedFlags) > 0
	state.ConflictingVerifiedFlags = len(values) > 1
	if state.Solved {
		primary := state.VerifiedFlags[0]
		state.PrimaryVerifiedFlag = &primary
	}
	return state, nil
}
