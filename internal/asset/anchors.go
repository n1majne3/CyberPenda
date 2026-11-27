package asset

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (s *Service) AnchorNode(ctx context.Context, projectID, assetID, nodeKey string, origin Origin) (Anchor, error) {
	projectID, assetID, nodeKey = strings.TrimSpace(projectID), strings.TrimSpace(assetID), strings.TrimSpace(nodeKey)
	if projectID == "" || assetID == "" || nodeKey == "" {
		return Anchor{}, ErrInvalidInput
	}
	if _, err := s.projects.Get(projectID); err != nil {
		return Anchor{}, err
	}
	if err := s.validateOrigin(ctx, projectID, origin); err != nil {
		return Anchor{}, err
	}
	var foundAsset string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM project_assets WHERE project_id=? AND id=?`, projectID, assetID).Scan(&foundAsset)
	if errors.Is(err, sql.ErrNoRows) {
		return Anchor{}, ErrNotFound
	}
	if err != nil {
		return Anchor{}, err
	}
	var nodeType string
	err = s.db.QueryRowContext(ctx, `SELECT json_extract(body_json,'$.type') FROM fgs_nodes WHERE board_kind='project' AND board_id=? AND node_key=?`, projectID, nodeKey).Scan(&nodeType)
	if errors.Is(err, sql.ErrNoRows) {
		return Anchor{}, ErrNodeNotFound
	}
	if err != nil {
		return Anchor{}, err
	}
	if nodeType != "goal" && nodeType != "step" && nodeType != "fact" {
		return Anchor{}, ErrInvalidInput
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO project_asset_anchors
		(project_id,asset_id,node_key,node_type,task_id,continuation_id,actor_id,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		projectID, assetID, nodeKey, nodeType, origin.TaskID, origin.ContinuationID, origin.ActorID, now); err != nil {
		return Anchor{}, err
	}
	return scanAnchor(s.db.QueryRowContext(ctx, `SELECT project_id,asset_id,node_key,node_type,task_id,continuation_id,actor_id,created_at
		FROM project_asset_anchors WHERE project_id=? AND asset_id=? AND node_key=?`, projectID, assetID, nodeKey))
}
