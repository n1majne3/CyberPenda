package blackboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

type BlackboardHealthRequest struct{}

type HealthCurrentGraphV1 struct {
	Revision           int    `json:"revision"`
	StateHash          string `json:"state_hash"`
	MainProjectionHash string `json:"main_projection_hash"`
}

type HealthSeverityCountsV1 struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

type BlackboardHealthRunV1 struct {
	RunID                   string                 `json:"run_id"`
	CheckerVersion          string                 `json:"checker_version"`
	Status                  string                 `json:"status"`
	Overall                 HealthStatus           `json:"overall"`
	CheckedGraphRevision    int                    `json:"checked_graph_revision"`
	CheckedStateHash        string                 `json:"checked_state_hash"`
	CheckedProjectionHash   string                 `json:"checked_projection_hash"`
	ArtifactScanStatus      string                 `json:"artifact_scan_status"`
	ArtifactScanFingerprint string                 `json:"artifact_scan_fingerprint,omitempty"`
	StartedAt               string                 `json:"started_at"`
	CompletedAt             *string                `json:"completed_at"`
	Stale                   bool                   `json:"stale"`
	Metrics                 HealthMetrics          `json:"metrics"`
	Counts                  HealthSeverityCountsV1 `json:"counts"`
	TopResults              []HealthResultV1       `json:"top_results"`
}

type BlackboardHealthV1 struct {
	CurrentGraph HealthCurrentGraphV1   `json:"current_graph"`
	LatestRun    *BlackboardHealthRunV1 `json:"latest_run"`
	Overall      HealthStatus           `json:"overall"`
}

type HealthRunRequest struct{ RunID string }

type HealthRunV1 struct {
	BlackboardHealthRunV1
	Results []HealthResultV1 `json:"results"`
}

type HealthResultsRequest struct {
	RunID       string
	Severity    []string
	Code        []string
	SubjectKind string
	SubjectID   string
	Limit       int
	Cursor      string
}

type HealthSubjectV1 struct {
	Kind      string     `json:"kind"`
	ID        string     `json:"id"`
	StableKey string     `json:"stable_key,omitempty"`
	Ref       *NodeRefV1 `json:"ref"`
}

type OperatorLinkV1 struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

type HealthResultV1 struct {
	Fingerprint   string           `json:"fingerprint"`
	Code          string           `json:"code"`
	Severity      HealthSeverity   `json:"severity"`
	Subject       HealthSubjectV1  `json:"subject"`
	Details       map[string]any   `json:"details"`
	OperatorLinks []OperatorLinkV1 `json:"operator_links"`
}

type HealthResultCollectionV1 struct {
	Items []HealthResultV1 `json:"items"`
	Page  PageV1           `json:"page"`
}

func buildBlackboardHealth(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, artifactRoot string) (BlackboardHealthV1, error) {
	current, err := currentHealthGraph(ctx, tx, snapshot)
	if err != nil {
		return BlackboardHealthV1{}, err
	}
	latest, results, err := loadHealthRun(ctx, tx, snapshot, "", artifactRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return BlackboardHealthV1{CurrentGraph: current, Overall: HealthStatusUnknown}, nil
	}
	if err != nil {
		return BlackboardHealthV1{}, err
	}
	latest.TopResults = results
	if len(latest.TopResults) > 10 {
		latest.TopResults = latest.TopResults[:10]
	}
	return BlackboardHealthV1{CurrentGraph: current, LatestRun: &latest, Overall: latest.Overall}, nil
}

func buildHealthRun(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request HealthRunRequest, artifactRoot string) (HealthRunV1, error) {
	if request.RunID == "" {
		return HealthRunV1{}, readValidationError(ErrCodeInvalidQuery, "run_id is required", "run_id")
	}
	run, results, err := loadHealthRun(ctx, tx, snapshot, request.RunID, artifactRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return HealthRunV1{}, readValidationError(ErrCodeHealthRunNotFound, "Health run does not exist in this Project", "run_id")
	}
	if err != nil {
		return HealthRunV1{}, err
	}
	return HealthRunV1{BlackboardHealthRunV1: run, Results: results}, nil
}

func buildHealthResults(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request HealthResultsRequest, cursor *readCursor, cursorKey []byte, artifactRoot string) (HealthResultCollectionV1, error) {
	if request.RunID == "" {
		return HealthResultCollectionV1{}, readValidationError(ErrCodeInvalidQuery, "run_id is required", "run_id")
	}
	if request.Limit == 0 {
		request.Limit = 100
	}
	if request.Limit < 1 || request.Limit > 500 {
		return HealthResultCollectionV1{}, readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 500", "limit")
	}
	run, results, err := loadHealthRun(ctx, tx, snapshot, request.RunID, artifactRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return HealthResultCollectionV1{}, readValidationError(ErrCodeHealthRunNotFound, "Health run does not exist in this Project", "run_id")
	}
	if err != nil {
		return HealthResultCollectionV1{}, err
	}
	if run.Status == "running" {
		return HealthResultCollectionV1{}, readValidationError(ErrCodeHealthRunInProgress, "Health run is still in progress", "run_id")
	}
	queryShape := struct {
		RunID       string   `json:"run_id"`
		Severity    []string `json:"severity"`
		Code        []string `json:"code"`
		SubjectKind string   `json:"subject_kind"`
		SubjectID   string   `json:"subject_id"`
		Limit       int      `json:"limit"`
	}{request.RunID, sortedUniqueStrings(request.Severity), sortedUniqueStrings(request.Code), request.SubjectKind, request.SubjectID, request.Limit}
	queryBytes, _ := canonicalJSON(queryShape)
	queryHash := fmt.Sprintf("%x", framedHash("CyberPenda.Blackboard.HealthResultsQuery.v1", queryBytes))
	if cursor != nil && (cursor.QueryHash != queryHash || cursor.Sort != "health" || cursor.Limit != request.Limit || cursor.SourcePins["health_run_id"] != request.RunID) {
		return HealthResultCollectionV1{}, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
	}
	allowedSeverity := stringSet(request.Severity)
	allowedCode := stringSet(request.Code)
	filtered := []HealthResultV1{}
	for _, result := range results {
		if len(allowedSeverity) > 0 && !allowedSeverity[string(result.Severity)] {
			continue
		}
		if len(allowedCode) > 0 && !allowedCode[result.Code] {
			continue
		}
		if request.SubjectKind != "" && result.Subject.Kind != request.SubjectKind {
			continue
		}
		if request.SubjectID != "" && result.Subject.ID != request.SubjectID {
			continue
		}
		filtered = append(filtered, result)
	}
	start := 0
	if cursor != nil && len(cursor.Last) > 0 {
		for i := range filtered {
			if filtered[i].Fingerprint == cursor.Last[0] {
				start = i + 1
				break
			}
		}
	}
	end := start + request.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := PageV1{Limit: request.Limit, TotalItems: len(filtered)}
	if end < len(filtered) {
		page.NextCursor, err = encodeReadCursor(readCursor{Version: 1, Projection: ReadKindHealthResultsV1, ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash, Sort: "health", Limit: request.Limit, Last: []string{filtered[end-1].Fingerprint}, SourcePins: map[string]string{"health_run_id": request.RunID}}, cursorKey)
		if err != nil {
			return HealthResultCollectionV1{}, err
		}
	}
	return HealthResultCollectionV1{Items: filtered[start:end], Page: page}, nil
}

func currentHealthGraph(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot) (HealthCurrentGraphV1, error) {
	var current HealthCurrentGraphV1
	if err := tx.QueryRowContext(ctx, `SELECT current_graph_revision,current_semantic_state_hash,COALESCE(current_main_projection_hash,'') FROM blackboard_graph_state WHERE project_id=?`, snapshot.ProjectID).Scan(&current.Revision, &current.StateHash, &current.MainProjectionHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) && snapshot.GraphRevision == 0 {
			return HealthCurrentGraphV1{Revision: 0, StateHash: snapshot.StateHash}, nil
		}
		return current, fmt.Errorf("read current Health graph: %w", err)
	}
	return current, nil
}

func loadHealthRun(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, runID, artifactRoot string) (BlackboardHealthRunV1, []HealthResultV1, error) {
	var run BlackboardHealthRunV1
	var runStatus, persistedOverall string
	var completed sql.NullString
	var metricsJSON string
	query := `SELECT run_id,checker_version,run_status,overall,checked_graph_revision,checked_state_hash,checked_projection_hash,artifact_scan_status,artifact_scan_fingerprint,started_at,completed_at,metrics_json FROM blackboard_health_runs WHERE project_id=?`
	args := []any{snapshot.ProjectID}
	if runID == "" {
		query += ` AND run_status IN ('completed','failed','cancelled') ORDER BY started_at DESC,rowid DESC LIMIT 1`
	} else {
		query += ` AND run_id=?`
		args = append(args, runID)
	}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&run.RunID, &run.CheckerVersion, &runStatus, &persistedOverall, &run.CheckedGraphRevision, &run.CheckedStateHash, &run.CheckedProjectionHash, &run.ArtifactScanStatus, &run.ArtifactScanFingerprint, &run.StartedAt, &completed, &metricsJSON); err != nil {
		return run, nil, err
	}
	run.Overall = HealthStatus(persistedOverall)
	run.Status = runStatus
	if completed.Valid {
		run.CompletedAt = &completed.String
	}
	if err := json.Unmarshal([]byte(metricsJSON), &run.Metrics); err != nil {
		return run, nil, err
	}
	current, err := currentHealthGraph(ctx, tx, snapshot)
	if err != nil {
		return run, nil, err
	}
	run.Stale = run.CheckedGraphRevision != current.Revision || run.CheckedStateHash != current.StateHash || run.CheckedProjectionHash != current.MainProjectionHash
	if !run.Stale && run.ArtifactScanStatus == "completed" {
		doc, err := canonicalMainGraphDocumentAt(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision)
		if err != nil {
			return run, nil, err
		}
		_, fingerprint, err := inspectEvidenceArtifacts(ctx, doc, artifactRoot)
		if err != nil {
			return run, nil, err
		}
		run.Stale = fingerprint != run.ArtifactScanFingerprint
	}
	results, err := loadHealthResults(ctx, tx, snapshot, run.RunID)
	if err != nil {
		return run, nil, err
	}
	for _, result := range results {
		switch result.Severity {
		case HealthSeverity("critical"):
			run.Counts.Critical++
		case HealthSeverity("warning"):
			run.Counts.Warning++
		case HealthSeverity("info"):
			run.Counts.Info++
		}
	}
	return run, results, nil
}

func loadHealthResults(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, runID string) ([]HealthResultV1, error) {
	rows, err := tx.QueryContext(ctx, `SELECT fingerprint,code,severity,subject_kind,subject_id,details_json FROM blackboard_health_results WHERE project_id=? AND run_id=?`, snapshot.ProjectID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	results := []HealthResultV1{}
	for rows.Next() {
		var result HealthResultV1
		var detailsJSON string
		if err := rows.Scan(&result.Fingerprint, &result.Code, &result.Severity, &result.Subject.Kind, &result.Subject.ID, &detailsJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(detailsJSON), &result.Details); err != nil {
			return nil, err
		}
		if node, ok := byID[result.Subject.ID]; ok {
			ref := nodeRefForNode(node)
			result.Subject.Ref, result.Subject.StableKey = &ref, node.StableKey
			result.OperatorLinks = []OperatorLinkV1{{Label: "Open " + ref.Label, Href: "/projects/" + snapshot.ProjectID + "/blackboard?record=" + node.ID}}
		} else {
			result.OperatorLinks = []OperatorLinkV1{}
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		si, sj := healthSeverityRank(results[i].Severity), healthSeverityRank(results[j].Severity)
		if si != sj {
			return si < sj
		}
		if results[i].Code != results[j].Code {
			return results[i].Code < results[j].Code
		}
		if results[i].Subject.Kind != results[j].Subject.Kind {
			return results[i].Subject.Kind < results[j].Subject.Kind
		}
		if results[i].Subject.ID != results[j].Subject.ID {
			return results[i].Subject.ID < results[j].Subject.ID
		}
		return results[i].Fingerprint < results[j].Fingerprint
	})
	return results, rows.Err()
}

func healthSeverityRank(severity HealthSeverity) int {
	switch severity {
	case HealthSeverity("critical"):
		return 0
	case HealthSeverity("warning"):
		return 1
	default:
		return 2
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
