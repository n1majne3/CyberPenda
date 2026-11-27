package asset

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
)

type Service struct {
	db       *store.DB
	projects *project.Service
}

func NewService(db *store.DB, projects *project.Service) *Service {
	return &Service{db: db, projects: projects}
}

func (s *Service) validateOrigin(ctx context.Context, projectID string, origin Origin) error {
	switch origin.Kind {
	case OriginOperator:
		if origin.TaskID != "" || origin.ContinuationID != "" {
			return ErrTrustedOrigin
		}
		return nil
	case OriginRuntime:
		var foundProjectID, foundTaskID string
		err := s.db.QueryRowContext(ctx, `SELECT tasks.project_id,task_continuations.task_id FROM task_continuations
			JOIN tasks ON tasks.id=task_continuations.task_id WHERE task_continuations.id=?`, origin.ContinuationID).Scan(&foundProjectID, &foundTaskID)
		if err != nil || foundProjectID != projectID || foundTaskID != origin.TaskID {
			return ErrTrustedOrigin
		}
		return nil
	default:
		return ErrTrustedOrigin
	}
}

func (s *Service) Upsert(ctx context.Context, projectID string, inputs []Input, origin Origin) (BatchResult, error) {
	if len(inputs) == 0 || len(inputs) > 200 {
		return BatchResult{}, ErrInvalidInput
	}
	if _, err := s.projects.Get(projectID); err != nil {
		return BatchResult{}, err
	}
	if err := s.validateOrigin(ctx, projectID, origin); err != nil {
		return BatchResult{}, err
	}
	type preparedInput struct {
		normalized normalizedAsset
		parent     *normalizedAsset
		observed   time.Time
	}
	prepared := []preparedInput{}
	seen := map[string]bool{}
	for _, input := range inputs {
		normalized, err := normalizeReference(Reference{Type: input.Type, Value: input.Value})
		if err != nil {
			return BatchResult{}, err
		}
		key := string(normalized.Type) + "\x00" + normalized.NormalizedValue
		if seen[key] {
			continue
		}
		seen[key] = true
		item := preparedInput{normalized: normalized, observed: time.Now().UTC()}
		if strings.TrimSpace(input.ObservedAt) != "" {
			observed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(input.ObservedAt))
			if err != nil {
				return BatchResult{}, ErrInvalidInput
			}
			item.observed = observed.UTC()
		}
		if input.Parent != nil {
			parent, err := normalizeReference(*input.Parent)
			if err != nil {
				return BatchResult{}, ErrInvalidParent
			}
			item.parent = &parent
		}
		prepared = append(prepared, item)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	ids := []string{}
	createdCount := 0
	for _, item := range prepared {
		id, created, err := upsertNormalizedTx(ctx, tx, projectID, item.normalized, item.observed, now, "", "")
		if err != nil {
			return BatchResult{}, err
		}
		if created {
			createdCount++
		}
		ids = append(ids, id)
	}
	assets, err := readAssetsTx(ctx, tx, projectID)
	if err != nil {
		return BatchResult{}, err
	}
	byIdentity := map[string]Asset{}
	byID := map[string]Asset{}
	for _, item := range assets {
		byIdentity[identityKey(item.Type, item.NormalizedValue)] = item
		byID[item.ID] = item
	}
	for index, item := range prepared {
		assetID := ids[index]
		child := byID[assetID]
		parentID := ""
		if item.parent != nil {
			parent, ok := byIdentity[identityKey(item.parent.Type, item.parent.NormalizedValue)]
			if !ok {
				return BatchResult{}, ErrInvalidParent
			}
			if !compatibleAssetParent(child, parent) {
				return BatchResult{}, ErrInvalidParent
			}
			parentID = parent.ID
		} else {
			parentID = inferParentID(child, assets)
		}
		if parentID != child.ParentID {
			if _, err := tx.ExecContext(ctx, `UPDATE project_assets SET parent_id=?,updated_at=? WHERE id=?`, parentID, now.Format(time.RFC3339Nano), assetID); err != nil {
				return BatchResult{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, err
	}
	views, err := s.listByIDs(ctx, projectID, ids)
	if err != nil {
		return BatchResult{}, err
	}
	return BatchResult{Assets: views, Created: createdCount, Updated: len(ids) - createdCount}, nil
}

func upsertNormalizedTx(ctx context.Context, tx *sql.Tx, projectID string, item normalizedAsset, observed, now time.Time, importProjectID, importAssetID string) (string, bool, error) {
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_assets
		(id,project_id,type,value,normalized_value,scheme,host,port,path,parent_id,imported_from_project_id,imported_from_asset_id,first_seen_at,last_seen_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		newID(), projectID, string(item.Type), item.Value, item.NormalizedValue, item.Scheme, item.Host, item.Port, item.Path, "", importProjectID, importAssetID,
		stamp(observed), stamp(observed), stamp(now), stamp(now))
	if err != nil {
		return "", false, fmt.Errorf("store asset: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM project_assets WHERE project_id=? AND type=? AND normalized_value=?`, projectID, string(item.Type), item.NormalizedValue).Scan(&id); err != nil {
			return "", false, err
		}
		return id, true, nil
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM project_assets WHERE project_id=? AND type=? AND normalized_value=?`, projectID, string(item.Type), item.NormalizedValue).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE project_assets SET last_seen_at=max(last_seen_at,?),updated_at=? WHERE id=?`, stamp(observed), stamp(now), id); err != nil {
		return "", false, err
	}
	return id, false, nil
}

func identityKey(kind Type, normalized string) string {
	return string(kind) + "\x00" + normalized
}

func inferParentID(child Asset, assets []Asset) string {
	if child.Type == TypeRootDomain {
		return ""
	}
	best := Asset{}
	bestScore := -1
	consider := func(candidate Asset, score int) {
		if score > bestScore && compatibleAssetParent(child, candidate) {
			best, bestScore = candidate, score
		}
	}
	for _, candidate := range assets {
		if candidate.ID == child.ID {
			continue
		}
		switch child.Type {
		case TypeSubdomain:
			if (candidate.Type == TypeRootDomain || candidate.Type == TypeSubdomain) && len(candidate.Host) < len(child.Host) && hostRuleMatch(child.Host, candidate.Host) {
				consider(candidate, len(candidate.Host))
			}
		case TypeService:
			if candidate.Type == TypeIP && candidate.Host == child.Host {
				consider(candidate, 1000)
			}
			if (candidate.Type == TypeRootDomain || candidate.Type == TypeSubdomain) && hostRuleMatch(child.Host, candidate.Host) {
				consider(candidate, len(candidate.Host))
			}
		case TypeEndpoint:
			if candidate.Type == TypeService && candidate.Scheme == child.Scheme && candidate.Host == child.Host && candidate.Port == child.Port {
				consider(candidate, 2000)
			}
			if candidate.Type == TypeIP && candidate.Host == child.Host {
				consider(candidate, 1000)
			}
			if (candidate.Type == TypeRootDomain || candidate.Type == TypeSubdomain) && hostRuleMatch(child.Host, candidate.Host) {
				consider(candidate, len(candidate.Host))
			}
		}
	}
	return best.ID
}

func compatibleAssetParent(child, parent Asset) bool {
	if child.ID == parent.ID || !compatibleParent(child.Type, parent.Type) {
		return false
	}
	if child.Type == TypeSubdomain || parent.Type == TypeRootDomain || parent.Type == TypeSubdomain {
		if child.Type == TypeSubdomain && parent.Type == TypeSubdomain && len(parent.Host) >= len(child.Host) {
			return false
		}
		return hostRuleMatch(child.Host, parent.Host)
	}
	if parent.Type == TypeIP {
		return child.Host == parent.Host
	}
	if parent.Type == TypeService {
		return child.Type == TypeEndpoint && child.Scheme == parent.Scheme && child.Host == parent.Host && child.Port == parent.Port
	}
	return false
}

func (s *Service) List(ctx context.Context, projectID string) ([]View, error) {
	if _, err := s.projects.Get(projectID); err != nil {
		return nil, err
	}
	return s.listViews(ctx, projectID)
}

func (s *Service) listViews(ctx context.Context, projectID string) ([]View, error) {
	found, err := s.projects.Get(projectID)
	if err != nil {
		return nil, err
	}
	assets, err := readAssetsTx(ctx, s.db, projectID)
	if err != nil {
		return nil, err
	}
	anchors, err := readAnchors(ctx, s.db, projectID)
	if err != nil {
		return nil, err
	}
	byAsset := map[string][]Anchor{}
	for _, anchor := range anchors {
		byAsset[anchor.AssetID] = append(byAsset[anchor.AssetID], anchor)
	}
	views := make([]View, 0, len(assets))
	for _, item := range assets {
		view := View{Asset: item, ScopeStatus: ScopeStatusFor(item, found.Scope), Anchors: byAsset[item.ID]}
		if view.Anchors == nil {
			view.Anchors = []Anchor{}
		}
		for _, anchor := range view.Anchors {
			switch anchor.NodeType {
			case "goal":
				view.GoalCount++
			case "step":
				view.StepCount++
			case "fact":
				view.FactCount++
			}
		}
		view.Covered = view.FactCount > 0
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if parentRank(views[i].Type) != parentRank(views[j].Type) {
			return parentRank(views[i].Type) < parentRank(views[j].Type)
		}
		return views[i].NormalizedValue < views[j].NormalizedValue
	})
	return views, nil
}

func (s *Service) listByIDs(ctx context.Context, projectID string, ids []string) ([]View, error) {
	views, err := s.listViews(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byID := map[string]View{}
	for _, view := range views {
		byID[view.ID] = view
	}
	result := make([]View, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		view, ok := byID[id]
		if !ok {
			return nil, ErrNotFound
		}
		result = append(result, view)
	}
	return result, nil
}

func (s *Service) Coverage(ctx context.Context, projectID string) (Coverage, error) {
	views, err := s.List(ctx, projectID)
	if err != nil {
		return Coverage{}, err
	}
	coverage := Coverage{Schema: CoverageSchema, ProjectID: projectID, UncoveredInScopeAssetIDs: []string{}}
	for _, view := range views {
		coverage.TotalAssets++
		switch view.ScopeStatus {
		case ScopeStatusInScope:
			coverage.InScopeAssets++
			if view.Covered {
				coverage.CoveredInScopeAssets++
			} else {
				coverage.UncoveredInScopeAssetIDs = append(coverage.UncoveredInScopeAssetIDs, view.ID)
			}
		case ScopeStatusExcluded:
			coverage.ExcludedAssets++
		default:
			coverage.OutOfScopeAssets++
		}
	}
	if coverage.InScopeAssets > 0 {
		coverage.CoveragePercent = float64(coverage.CoveredInScopeAssets) * 100 / float64(coverage.InScopeAssets)
	}
	return coverage, nil
}
