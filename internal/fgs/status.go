package fgs

import (
	"context"
	"encoding/json"
	"pentest/internal/owner"
)

type Status struct {
	ActionRequired int       `json:"action_required"`
	LastAcceptedAt string    `json:"last_accepted_at,omitempty"`
	Receipts       []Receipt `json:"receipts"`
}

// Status reports durable delivery state for the accepted board. It does not
// infer Runtime activity from node state or from the age of an update.
func (s *Service) Status(ctx context.Context, c owner.Contract) (Status, error) {
	result := Status{Receipts: []Receipt{}}
	kind, id, err := boardIdentity(c)
	if err != nil {
		return result, err
	}
	where := `owner_kind='session' AND owner_id=?`
	if kind == "project" {
		where = `owner_kind='task' AND owner_id IN (SELECT id FROM tasks WHERE project_id=?)`
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(CASE WHEN json_extract(receipt_json,'$.state')='applied' THEN json_extract(receipt_json,'$.updated_at') END),'') FROM fgs_receipts WHERE `+where+` AND (json_extract(receipt_json,'$.state')='applied' OR (json_extract(receipt_json,'$.state')='action_required' AND json_extract(payload_json,'$.resolves') IS NULL))`, id).Scan(&result.ActionRequired, &result.LastAcceptedAt)
	if err != nil {
		return result, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fgs_receipts WHERE `+where+` AND json_extract(receipt_json,'$.state')='action_required' AND json_extract(payload_json,'$.resolves') IS NULL`, id).Scan(&result.ActionRequired)
	if err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT receipt_json FROM fgs_receipts WHERE `+where+` ORDER BY rowid DESC LIMIT 100`, id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return result, err
		}
		var r Receipt
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return result, err
		}
		result.Receipts = append(result.Receipts, r)
	}
	return result, rows.Err()
}
