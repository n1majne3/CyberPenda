package asset

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}

type rowScanner interface {
	Scan(...any) error
}

const assetColumns = `id,project_id,type,value,normalized_value,scheme,host,port,path,parent_id,imported_from_project_id,imported_from_asset_id,first_seen_at,last_seen_at,created_at,updated_at`

func scanAsset(row rowScanner) (Asset, error) {
	var item Asset
	var kind, firstSeen, lastSeen, created, updated string
	err := row.Scan(&item.ID, &item.ProjectID, &kind, &item.Value, &item.NormalizedValue, &item.Scheme, &item.Host, &item.Port, &item.Path,
		&item.ParentID, &item.ImportedFromProjectID, &item.ImportedFromAssetID, &firstSeen, &lastSeen, &created, &updated)
	if err != nil {
		return Asset{}, err
	}
	item.Type = Type(kind)
	timestamps := []struct {
		value  string
		target *time.Time
	}{
		{firstSeen, &item.FirstSeenAt},
		{lastSeen, &item.LastSeenAt},
		{created, &item.CreatedAt},
		{updated, &item.UpdatedAt},
	}
	for _, timestamp := range timestamps {
		parsed, err := time.Parse(time.RFC3339Nano, timestamp.value)
		if err != nil {
			return Asset{}, err
		}
		*timestamp.target = parsed
	}
	return item, nil
}

func readAssetsTx(ctx context.Context, q queryer, projectID string) ([]Asset, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+assetColumns+` FROM project_assets WHERE project_id=? ORDER BY normalized_value`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets := []Asset{}
	for rows.Next() {
		item, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, item)
	}
	return assets, rows.Err()
}

func scanAnchor(row rowScanner) (Anchor, error) {
	var anchor Anchor
	var created string
	err := row.Scan(&anchor.ProjectID, &anchor.AssetID, &anchor.NodeKey, &anchor.NodeType, &anchor.TaskID, &anchor.ContinuationID, &anchor.ActorID, &created)
	if err != nil {
		return Anchor{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Anchor{}, err
	}
	anchor.CreatedAt = parsed
	return anchor, nil
}

func readAnchors(ctx context.Context, q queryer, projectID string) ([]Anchor, error) {
	rows, err := q.QueryContext(ctx, `SELECT project_id,asset_id,node_key,node_type,task_id,continuation_id,actor_id,created_at
		FROM project_asset_anchors WHERE project_id=? ORDER BY created_at,node_key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	anchors := []Anchor{}
	for rows.Next() {
		anchor, err := scanAnchor(rows)
		if err != nil {
			return nil, err
		}
		anchors = append(anchors, anchor)
	}
	return anchors, rows.Err()
}
