package task

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RuntimeControlEvents reads permission history and the latest recovery boundary.
// Runtime output and earlier recovery boundaries are not part of this projection.
func (s *Service) RuntimeControlEvents(taskID string) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, task_id, continuation_id, attempt_node_id, seq, kind, payload_json, created_at
 FROM task_events WHERE task_id=? AND kind='lifecycle' AND (
 json_extract(payload_json,'$.phase') IN ('provider_permission_requested','provider_permission_response_applied')
 OR seq=(SELECT seq FROM task_events WHERE task_id=? AND kind='lifecycle'
 AND json_extract(payload_json,'$.phase') IN ('started','provider_session_recovery_required') ORDER BY seq DESC LIMIT 1)) ORDER BY seq ASC`, taskID, taskID)
	if err != nil {
		return nil, fmt.Errorf("read Runtime controls: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// LatestRuntimeConfig reads only the last captured Runtime configuration.
func (s *Service) LatestRuntimeConfig(taskID string) (*RuntimeConfigVersion, error) {
	var version RuntimeConfigVersion
	var configJSON, createdAt string
	err := s.db.QueryRow(`SELECT id,task_id,version,runtime_profile_id,config_json,created_at FROM task_runtime_config_versions WHERE task_id=? ORDER BY version DESC LIMIT 1`, taskID).Scan(&version.ID, &version.TaskID, &version.Version, &version.RuntimeProfileID, &configJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest Runtime configuration: %w", err)
	}
	if err := json.Unmarshal([]byte(configJSON), &version.Config); err != nil {
		return nil, fmt.Errorf("decode latest Runtime configuration: %w", err)
	}
	version.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse latest Runtime configuration time: %w", err)
	}
	return &version, nil
}

// LatestTurnSelectionEvent reads the newest Conversation or Steering event with
// a usable turn selection. The whitespace set is Go unicode.White_Space, as used
// by strings.TrimSpace in the turn projection.
func (s *Service) LatestTurnSelectionEvent(taskID string) (*Event, error) {
	const whitespace = "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"
	rows, err := s.db.Query(`WITH candidates AS (
 SELECT *,
 CASE WHEN json_type(payload_json,'$.model_provider_id')='text' THEN json_extract(payload_json,'$.model_provider_id') ELSE '' END AS provider,
 CASE WHEN json_type(payload_json,'$.model')='text' THEN json_extract(payload_json,'$.model') ELSE '' END AS model,
 COALESCE(NULLIF(CASE WHEN json_type(payload_json,'$.requested_reasoning_effort')='text' THEN json_extract(payload_json,'$.requested_reasoning_effort') ELSE '' END,''), CASE WHEN json_type(payload_json,'$.reasoning_effort')='text' THEN json_extract(payload_json,'$.reasoning_effort') ELSE '' END) AS effort
 FROM task_events WHERE task_id=? AND kind IN ('conversation','steering'))
 SELECT id,task_id,continuation_id,attempt_node_id,seq,kind,payload_json,created_at FROM candidates
 WHERE trim(provider,?)!='' OR trim(model,?)!='' OR (kind='conversation' AND trim(effort,?)!='') ORDER BY seq DESC LIMIT 1`, taskID, whitespace, whitespace, whitespace)
	if err != nil {
		return nil, fmt.Errorf("read latest Runtime Turn Selection: %w", err)
	}
	defer rows.Close()
	events, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	return &events[0], nil
}
