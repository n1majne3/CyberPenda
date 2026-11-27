package asset

import (
	"context"
	"sort"
	"strings"
	"time"
)

func (s *Service) Import(ctx context.Context, targetProjectID, sourceProjectID string, assetIDs []string, origin Origin) (BatchResult, error) {
	targetProjectID, sourceProjectID = strings.TrimSpace(targetProjectID), strings.TrimSpace(sourceProjectID)
	if targetProjectID == "" || sourceProjectID == "" || targetProjectID == sourceProjectID || origin.Kind != OriginOperator {
		return BatchResult{}, ErrInvalidInput
	}
	if _, err := s.projects.Get(targetProjectID); err != nil {
		return BatchResult{}, err
	}
	if _, err := s.projects.Get(sourceProjectID); err != nil {
		return BatchResult{}, err
	}
	if err := s.validateOrigin(ctx, targetProjectID, origin); err != nil {
		return BatchResult{}, err
	}
	sourceAssets, err := readAssetsTx(ctx, s.db, sourceProjectID)
	if err != nil {
		return BatchResult{}, err
	}
	selected := []Asset{}
	if len(assetIDs) == 0 {
		selected = sourceAssets
	} else {
		want := map[string]bool{}
		for _, id := range assetIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				return BatchResult{}, ErrInvalidInput
			}
			want[id] = true
		}
		seen := map[string]bool{}
		for _, item := range sourceAssets {
			if want[item.ID] {
				selected = append(selected, item)
				seen[item.ID] = true
			}
		}
		if len(seen) != len(want) {
			return BatchResult{}, ErrNotFound
		}
	}
	if len(selected) == 0 {
		return BatchResult{}, ErrInvalidInput
	}
	sort.Slice(selected, func(i, j int) bool {
		if parentRank(selected[i].Type) != parentRank(selected[j].Type) {
			return parentRank(selected[i].Type) < parentRank(selected[j].Type)
		}
		return selected[i].NormalizedValue < selected[j].NormalizedValue
	})

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	idMap := map[string]string{}
	ids := []string{}
	createdCount := 0
	for _, source := range selected {
		normalized := normalizedAsset{
			Type: source.Type, Value: source.Value, NormalizedValue: source.NormalizedValue,
			Scheme: source.Scheme, Host: source.Host, Port: source.Port, Path: source.Path,
		}
		id, created, err := upsertNormalizedTx(ctx, tx, targetProjectID, normalized, source.LastSeenAt, now, sourceProjectID, source.ID)
		if err != nil {
			return BatchResult{}, err
		}
		if created {
			if _, err := tx.ExecContext(ctx, `UPDATE project_assets SET first_seen_at=? WHERE id=?`, source.FirstSeenAt.UTC().Format(time.RFC3339Nano), id); err != nil {
				return BatchResult{}, err
			}
			createdCount++
		}
		idMap[source.ID] = id
		ids = append(ids, id)
	}
	targetAssets, err := readAssetsTx(ctx, tx, targetProjectID)
	if err != nil {
		return BatchResult{}, err
	}
	byIdentity := map[string]Asset{}
	for _, item := range targetAssets {
		byIdentity[identityKey(item.Type, item.NormalizedValue)] = item
	}
	sourceByID := map[string]Asset{}
	for _, item := range sourceAssets {
		sourceByID[item.ID] = item
	}
	for _, source := range selected {
		if source.ParentID == "" {
			continue
		}
		parentID := idMap[source.ParentID]
		if parentID == "" {
			if sourceParent, ok := sourceByID[source.ParentID]; ok {
				if targetParent, ok := byIdentity[identityKey(sourceParent.Type, sourceParent.NormalizedValue)]; ok {
					parentID = targetParent.ID
				}
			}
		}
		if parentID == "" {
			continue
		}
		childID := idMap[source.ID]
		child := Asset{}
		parent := Asset{}
		for _, item := range targetAssets {
			if item.ID == childID {
				child = item
			}
			if item.ID == parentID {
				parent = item
			}
		}
		if child.ID == "" || parent.ID == "" || !compatibleAssetParent(child, parent) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_assets SET parent_id=?,updated_at=? WHERE id=?`, parentID, now.Format(time.RFC3339Nano), childID); err != nil {
			return BatchResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, err
	}
	views, err := s.listByIDs(ctx, targetProjectID, ids)
	if err != nil {
		return BatchResult{}, err
	}
	return BatchResult{Assets: views, Created: createdCount, Updated: len(ids) - createdCount}, nil
}
