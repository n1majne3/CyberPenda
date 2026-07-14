package blackboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ProjectBlackboardSummaryRequest is the versioned Blackboard read shape for this projection.
type ProjectBlackboardSummaryRequest struct{}

// DashboardScopeV1 is the versioned Blackboard read shape for this projection.
type DashboardScopeV1 struct {
	Ready            bool `json:"ready"`
	Domains          int  `json:"domains"`
	IPs              int  `json:"ips"`
	CIDRs            int  `json:"cidrs"`
	URLs             int  `json:"urls"`
	Ports            int  `json:"ports"`
	Excluded         int  `json:"excluded"`
	HasTestingLimits bool `json:"has_testing_limits"`
	HasNotes         bool `json:"has_notes"`
}

// DashboardTasksV1 is the versioned Blackboard read shape for this projection.
type DashboardTasksV1 struct {
	Total          int `json:"total"`
	Running        int `json:"running"`
	Paused         int `json:"paused"`
	NeedsAttention int `json:"needs_attention"`
}

// DashboardBlackboardV1 is the versioned Blackboard read shape for this projection.
type DashboardBlackboardV1 struct {
	ObservedGraphRevision int            `json:"observed_graph_revision"`
	NodesByType           map[string]int `json:"nodes_by_type"`
	CurrentTruth          int            `json:"current_truth"`
	Frontier              int            `json:"frontier"`
	OpenAttempts          int            `json:"open_attempts"`
	ConfirmedFindings     int            `json:"confirmed_findings"`
	UnconfirmedFindings   int            `json:"unconfirmed_findings"`
	AvailableEvidence     int            `json:"available_evidence"`
	MissingEvidence       int            `json:"missing_evidence"`
	BudgetState           BudgetState    `json:"budget_state"`
	EstimatedTokens       int            `json:"estimated_tokens"`
}

// DashboardHealthV1 is the versioned Blackboard read shape for this projection.
type DashboardHealthV1 struct {
	Status      HealthStatus `json:"status"`
	Stale       bool         `json:"stale"`
	Critical    int          `json:"critical"`
	Warning     int          `json:"warning"`
	Info        int          `json:"info"`
	LatestRunID string       `json:"latest_run_id"`
}

// DashboardCTFV1 is the versioned Blackboard read shape for this projection.
type DashboardCTFV1 struct {
	Solved                 bool       `json:"solved"`
	VerifiedFlagCount      int        `json:"verified_flag_count"`
	CandidateSolutionCount int        `json:"candidate_solution_count"`
	PrimarySolution        *NodeRefV1 `json:"primary_solution"`
}

// DashboardCountsV1 preserves the legacy Dashboard counts shape alongside
// the additive Blackboard/Health/CTF fields (read contract §18.5).
type DashboardCountsV1 struct {
	Tasks    int `json:"tasks"`
	Facts    int `json:"facts"`
	Findings int `json:"findings"`
	Evidence int `json:"evidence"`
}

// ProjectBlackboardSummaryV1 is the versioned Blackboard read shape for this projection.
type ProjectBlackboardSummaryV1 struct {
	ProjectID   string                `json:"project_id"`
	Name        string                `json:"name"`
	ProjectKind string                `json:"project_kind"`
	Scope       DashboardScopeV1      `json:"scope"`
	Tasks       DashboardTasksV1      `json:"tasks"`
	Blackboard  DashboardBlackboardV1 `json:"blackboard"`
	Health      DashboardHealthV1     `json:"health"`
	CTF         *DashboardCTFV1       `json:"ctf"`
	Counts      DashboardCountsV1     `json:"counts"`
	NextActions []string              `json:"next_actions"`
}

func buildProjectBlackboardSummary(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, projectKind string) (ProjectBlackboardSummaryV1, error) {
	var name, scopeRaw string
	if err := tx.QueryRowContext(ctx, `SELECT name,scope_json FROM projects WHERE id=?`, snapshot.ProjectID).Scan(&name, &scopeRaw); err != nil {
		return ProjectBlackboardSummaryV1{}, fmt.Errorf("read Project summary metadata: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(scopeRaw), &raw); err != nil {
		return ProjectBlackboardSummaryV1{}, fmt.Errorf("decode Project scope: %w", err)
	}
	count := func(key string) int {
		var values []json.RawMessage
		_ = json.Unmarshal(raw[key], &values)
		return len(values)
	}
	var notes string
	_ = json.Unmarshal(raw["notes"], &notes)
	scope := DashboardScopeV1{Domains: count("domains"), IPs: count("ips"), CIDRs: count("cidrs"), URLs: count("urls"), Ports: count("ports"), Excluded: count("excluded"), HasTestingLimits: count("testing_limits") > 0, HasNotes: notes != ""}
	scope.Ready = scope.Domains+scope.IPs+scope.CIDRs+scope.URLs+scope.Ports > 0
	tasks := DashboardTasksV1{}
	rows, err := tx.QueryContext(ctx, `SELECT status,COUNT(*) FROM tasks WHERE project_id=? AND deleted_at='' GROUP BY status`, snapshot.ProjectID)
	if err != nil {
		return ProjectBlackboardSummaryV1{}, fmt.Errorf("count Project Tasks: %w", err)
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return ProjectBlackboardSummaryV1{}, err
		}
		tasks.Total += n
		switch status {
		case "running":
			tasks.Running += n
		case "paused":
			tasks.Paused += n
		case "failed", "interrupted":
			tasks.NeedsAttention += n
		}
	}
	if err := rows.Close(); err != nil {
		return ProjectBlackboardSummaryV1{}, err
	}
	work, err := buildBlackboardWork(ctx, tx, snapshot)
	if err != nil {
		return ProjectBlackboardSummaryV1{}, err
	}
	bb := DashboardBlackboardV1{ObservedGraphRevision: snapshot.GraphRevision, NodesByType: work.Summary.NodeCounts, CurrentTruth: work.Summary.CurrentTruth, Frontier: work.Summary.Frontier, OpenAttempts: work.Summary.OpenAttempts, ConfirmedFindings: work.Summary.ConfirmedFindings, UnconfirmedFindings: work.Summary.UnconfirmedFindings}
	counts := DashboardCountsV1{Tasks: tasks.Total}
	for _, n := range snapshot.Nodes {
		if n.Disposition != DispositionMain {
			continue
		}
		switch n.NodeType {
		case NodeTypeProjectFact:
			counts.Facts++
		case NodeTypeFinding:
			counts.Findings++
		case NodeTypeEvidenceArtifact:
			counts.Evidence++
			if n.PropertyMap["status"] == "available" {
				bb.AvailableEvidence++
			}
			if n.PropertyMap["status"] == "missing" {
				bb.MissingEvidence++
			}
		}
	}
	doc, err := canonicalMainGraphDocumentAt(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision)
	if err != nil {
		return ProjectBlackboardSummaryV1{}, err
	}
	projection, err := measureCanonicalMainGraphDocument(snapshot.ProjectID, snapshot.GraphRevision, doc)
	if err != nil {
		return ProjectBlackboardSummaryV1{}, err
	}
	bb.EstimatedTokens = projection.EstimatedTokens
	bb.BudgetState = budgetStateForEstimatedTokens(projection.EstimatedTokens)
	health, err := dashboardHealth(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision)
	if err != nil {
		return ProjectBlackboardSummaryV1{}, err
	}
	var ctf *DashboardCTFV1
	if projectKind == "ctf_challenge" {
		value := DashboardCTFV1{}
		for _, node := range snapshot.Nodes {
			if node.NodeType != NodeTypeSolution || node.Disposition != DispositionMain {
				continue
			}
			status, kind := propertyString(node, "status"), propertyString(node, "kind")
			if status == "candidate" {
				value.CandidateSolutionCount++
			}
			if kind == "flag" && status == "verified" {
				value.VerifiedFlagCount++
				if value.PrimarySolution == nil {
					ref := nodeRefForNode(node)
					value.PrimarySolution = &ref
				}
			}
		}
		value.Solved = value.VerifiedFlagCount > 0
		ctf = &value
	}
	actions := dashboardNextActions(scope, tasks, bb, health)
	return ProjectBlackboardSummaryV1{ProjectID: snapshot.ProjectID, Name: name, ProjectKind: projectKind, Scope: scope, Tasks: tasks, Blackboard: bb, Health: health, CTF: ctf, Counts: counts, NextActions: actions}, nil
}
func dashboardHealth(ctx context.Context, tx *sql.Tx, projectID string, revision int) (DashboardHealthV1, error) {
	var h DashboardHealthV1
	var checked int
	err := tx.QueryRowContext(ctx, `SELECT run_id,checked_graph_revision,status FROM blackboard_health_runs WHERE project_id=? AND checked_graph_revision<=? AND completed_at IS NOT NULL ORDER BY started_at DESC,rowid DESC LIMIT 1`, projectID, revision).Scan(&h.LatestRunID, &checked, &h.Status)
	if errors.Is(err, sql.ErrNoRows) {
		h.Status = HealthStatusUnknown
		h.Stale = true
		return h, nil
	}
	if err != nil {
		return h, fmt.Errorf("read dashboard Health: %w", err)
	}
	h.Stale = checked != revision
	rows, err := tx.QueryContext(ctx, `SELECT severity,COUNT(*) FROM blackboard_health_results WHERE project_id=? AND run_id=? GROUP BY severity`, projectID, h.LatestRunID)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var severity string
		var n int
		if err := rows.Scan(&severity, &n); err != nil {
			return h, err
		}
		switch severity {
		case "critical":
			h.Critical = n
		case "warning":
			h.Warning = n
		case "info":
			h.Info = n
		}
	}
	return h, rows.Err()
}
func dashboardNextActions(scope DashboardScopeV1, tasks DashboardTasksV1, bb DashboardBlackboardV1, h DashboardHealthV1) []string {
	out := []string{}
	add := func(v string) {
		if len(out) < 5 {
			out = append(out, v)
		}
	}
	if !scope.Ready {
		add("scope")
	}
	if h.Critical > 0 {
		add("blackboard_health")
	}
	if bb.BudgetState == BudgetRequired {
		add("blackboard_compaction")
	}
	if bb.MissingEvidence > 0 {
		add("evidence")
	}
	if bb.Frontier == 0 {
		add("blackboard_frontier")
	}
	if bb.Frontier > 0 {
		add("blackboard_work")
	}
	if bb.UnconfirmedFindings > 0 {
		add("findings")
	}
	if tasks.Total == 0 {
		add("new_task")
	}
	return out
}

func projectionQueryHash(domain string, value any) (string, error) {
	data, err := canonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("encode projection query: %w", err)
	}
	return fmt.Sprintf("%x", framedHash(domain, data)), nil
}
func projectionPageStart(cursor *readCursor, queryHash, sortName string, limit, total int, tuple func(int) []string) (int, error) {
	if cursor == nil {
		return 0, nil
	}
	if cursor.QueryHash != queryHash || cursor.Sort != sortName || cursor.Limit != limit {
		return 0, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
	}
	return sort.Search(total, func(i int) bool { return compareRowTuple(tuple(i), cursor.Last) > 0 }), nil
}

func validateEnumFilters(path string, values []string, allowed ...string) error {
	if len(values) > 50 {
		return readValidationError(ErrCodeInvalidQuery, "repeated filter exceeds 50 values", path)
	}
	set := map[string]bool{}
	for _, v := range allowed {
		set[v] = true
	}
	for _, v := range values {
		if !set[v] {
			return readValidationError(ErrCodeInvalidQuery, "unsupported "+path+" value", path)
		}
	}
	return nil
}

func recordArchiveLifecycleEligible(node NodeRecord) bool {
	status := propertyString(node, "status")
	switch node.NodeType {
	case NodeTypeGoal:
		return propertyString(node, "task_status") == "completed" || propertyString(node, "task_status") == "failed" || propertyString(node, "task_status") == "stopped" || propertyString(node, "task_status") == "interrupted"
	case NodeTypeEntity:
		return status == "retired" || status == "superseded"
	case NodeTypeExplorationObjective:
		return status == "resolved" || status == "abandoned" || status == "superseded"
	case NodeTypeAttempt:
		return status == "succeeded"
	case NodeTypeObservation, NodeTypeHypothesis:
		return status == "superseded"
	case NodeTypeProjectFact:
		return propertyString(node, "confidence") == "deprecated"
	case NodeTypeFinding:
		return status == "false_positive"
	case NodeTypeSolution:
		return status == "rejected" || status == "superseded"
	case NodeTypeEvidenceArtifact:
		return status == "superseded"
	case NodeTypeProjectDirective:
		return status == "retired" || status == "superseded"
	}
	return false
}

func objectiveBlockingReasons(snapshot GraphSnapshot, byID map[string]NodeRecord, id string) []string {
	reasons := []string{}
	for _, e := range snapshot.Edges {
		if e.State != "active" {
			continue
		}
		other := ""
		prefix := ""
		if e.EdgeType == EdgeTypeDependsOn && e.FromNodeID == id {
			other = e.ToNodeID
			prefix = "depends_on"
		}
		if e.EdgeType == EdgeTypeBlocks && e.ToNodeID == id {
			other = e.FromNodeID
			prefix = "blocks"
		}
		if other == "" {
			continue
		}
		n, ok := byID[other]
		if !ok {
			reasons = append(reasons, "missing:"+other)
		} else if n.Disposition != DispositionMain {
			reasons = append(reasons, string(n.Disposition)+":"+other)
		} else if propertyString(n, "status") != "resolved" {
			reasons = append(reasons, prefix+":"+other)
		}
	}
	sort.Strings(reasons)
	return compactStrings(reasons)
}
func recordHealthAt(ctx context.Context, tx *sql.Tx, projectID string, revision int, nodeID string) (RecordHealthV1, string, error) {
	var runID string
	err := tx.QueryRowContext(ctx, `SELECT run_id FROM blackboard_health_runs WHERE project_id=? AND checked_graph_revision<=? AND completed_at IS NOT NULL ORDER BY started_at DESC,rowid DESC LIMIT 1`, projectID, revision).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return RecordHealthV1{}, "", nil
	}
	if err != nil {
		return RecordHealthV1{}, "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT severity,subject_id,details_json FROM blackboard_health_results WHERE project_id=? AND run_id=? ORDER BY fingerprint`, projectID, runID)
	if err != nil {
		return RecordHealthV1{}, "", err
	}
	defer rows.Close()
	rank := 99
	var out RecordHealthV1
	for rows.Next() {
		var severity, subject, raw string
		if err := rows.Scan(&severity, &subject, &raw); err != nil {
			return out, "", err
		}
		matches := subject == nodeID
		if !matches {
			var details map[string]any
			if json.Unmarshal([]byte(raw), &details) == nil {
				for _, key := range []string{"node_id", "objective_id", "subject_id"} {
					if details[key] == nodeID {
						matches = true
					}
				}
			}
		}
		if !matches {
			continue
		}
		out.ResultCount++
		r := map[string]int{"critical": 0, "warning": 1, "info": 2}[severity]
		if r < rank {
			rank = r
			value := severity
			out.HighestSeverity = &value
		}
	}
	return out, runID, rows.Err()
}

func semanticChangesAt(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, nodeRows []NodeRowV1, limit int) (SemanticChangeCollectionV1, error) {
	changes := make([]SemanticChangeV1, 0, len(nodeRows)+len(snapshot.Edges))
	for i := range nodeRows {
		row := nodeRows[i]
		changes = append(changes, SemanticChangeV1{Kind: "node", Node: &row, UpdatedAt: row.UpdatedAt})
	}
	byID := map[string]NodeRecord{}
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	for _, edge := range snapshot.Edges {
		from, fromOK := byID[edge.FromNodeID]
		to, toOK := byID[edge.ToNodeID]
		if !fromOK || !toOK {
			continue
		}
		var updatedAt string
		if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM blackboard_edge_versions WHERE project_id=? AND edge_id=? AND version=?`, snapshot.ProjectID, edge.ID, edge.Version).Scan(&updatedAt); err != nil {
			return SemanticChangeCollectionV1{}, fmt.Errorf("read edge change timestamp: %w", err)
		}
		provenance, err := provenanceSummaryForEdgeVersion(ctx, tx, snapshot.ProjectID, edge.ID, edge.Version)
		if err != nil {
			return SemanticChangeCollectionV1{}, err
		}
		row := EdgeRowV1{ID: edge.ID, EdgeType: edge.EdgeType, From: nodeRefForNode(from), To: nodeRefForNode(to), Version: edge.Version, State: edge.State, Summary: edge.Summary, UpdatedAt: updatedAt, UpdatedProvenance: provenance}
		changes = append(changes, SemanticChangeV1{Kind: "edge", Edge: &row, UpdatedAt: updatedAt})
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].UpdatedAt != changes[j].UpdatedAt {
			return changes[i].UpdatedAt > changes[j].UpdatedAt
		}
		return semanticChangeTuple(changes[i]) < semanticChangeTuple(changes[j])
	})
	total := len(changes)
	if len(changes) > limit {
		changes = changes[:limit]
	}
	return SemanticChangeCollectionV1{Items: changes, Page: PageV1{Limit: limit, TotalItems: total}}, nil
}
func semanticChangeTuple(change SemanticChangeV1) string {
	if change.Node != nil {
		return fmt.Sprintf("0:%02d:%s:%s", nodeTypeOrdinal(change.Node.Ref.NodeType), change.Node.Ref.StableKey, change.Node.Ref.ID)
	}
	if change.Edge != nil {
		return fmt.Sprintf("1:%02d:%02d:%s:%02d:%s:%s", edgeTypeOrdinal(change.Edge.EdgeType), nodeTypeOrdinal(change.Edge.From.NodeType), change.Edge.From.StableKey, nodeTypeOrdinal(change.Edge.To.NodeType), change.Edge.To.StableKey, change.Edge.ID)
	}
	return "9"
}
func provenanceSummaryForEdgeVersion(ctx context.Context, tx *sql.Tx, projectID, edgeID string, version int) (ProvenanceSummaryV1, error) {
	var p ProvenanceSummaryV1
	var actorType string
	var taskID, continuationID, profileID, runner, migration sql.NullString
	var provenanceID string
	err := tx.QueryRowContext(ctx, `SELECT p.id,p.actor_type,p.actor_id,p.task_id,p.continuation_id,p.runtime_profile_id,p.runner,p.migration_source_json,p.recorded_at FROM blackboard_edge_versions v JOIN blackboard_graph_operations o ON o.project_id=v.project_id AND o.mutation_seq=v.mutation_seq AND o.operation_index=v.operation_index JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id WHERE v.project_id=? AND v.edge_id=? AND v.version=?`, projectID, edgeID, version).Scan(&provenanceID, &actorType, &p.ActorID, &taskID, &continuationID, &profileID, &runner, &migration, &p.RecordedAt)
	if err != nil {
		return p, fmt.Errorf("read edge provenance summary: %w", err)
	}
	p.ActorType = ActorType(actorType)
	p.TaskID, p.ContinuationID, p.RuntimeProfileID, p.Runner = nullStringPointer(taskID), nullStringPointer(continuationID), nullStringPointer(profileID), nullStringPointer(runner)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_graph_provenance_events WHERE project_id=? AND provenance_id=?`, projectID, provenanceID).Scan(&p.SourceEventCount); err != nil {
		return p, err
	}
	if migration.Valid {
		if err := json.Unmarshal([]byte(migration.String), &p.MigrationSource); err != nil {
			return p, err
		}
	}
	return p, nil
}

func edgeRowAt(ctx context.Context, tx *sql.Tx, projectID string, edge EdgeRecord, from, to NodeRecord) (EdgeRowV1, error) {
	var updatedAt string
	if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM blackboard_edge_versions WHERE project_id=? AND edge_id=? AND version=?`, projectID, edge.ID, edge.Version).Scan(&updatedAt); err != nil {
		return EdgeRowV1{}, fmt.Errorf("read edge timestamp: %w", err)
	}
	provenance, err := provenanceSummaryForEdgeVersion(ctx, tx, projectID, edge.ID, edge.Version)
	if err != nil {
		return EdgeRowV1{}, err
	}
	return EdgeRowV1{ID: edge.ID, EdgeType: edge.EdgeType, From: nodeRefForNode(from), To: nodeRefForNode(to), Version: edge.Version, State: edge.State, Summary: edge.Summary, UpdatedAt: updatedAt, UpdatedProvenance: provenance}, nil
}
