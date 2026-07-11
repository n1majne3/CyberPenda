package blackboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *GraphService) runHealth(ctx context.Context, projectID string, p CanonicalMainGraphProjection, blocked bool, stale bool, full bool, checkedAt string) (HealthRun, error) {
	started := checkedAt
	var stateHash, historyHash string
	if err := s.db.QueryRowContext(ctx, `SELECT current_semantic_state_hash,history_head_hash FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&stateHash, &historyHash); err != nil {
		return HealthRun{}, fmt.Errorf("read Health state hash: %w", err)
	}
	metrics := HealthMetrics{ProjectionBytes: p.ByteCount, EstimatedTokens: p.EstimatedTokens, BudgetState: budgetStateForEstimatedTokens(p.EstimatedTokens), NodeCounts: map[string]int{}, CurrentTruthCount: 0, FrontierCount: 0}
	var doc canonicalMainGraphDocument
	if err := json.Unmarshal(p.Bytes, &doc); err != nil {
		return HealthRun{}, fmt.Errorf("decode canonical projection for Health: %w", err)
	}
	metrics.CurrentTruthCount = len(doc.CurrentTruthNodeIDs)
	metrics.FrontierCount = len(doc.FrontierNodeIDs)
	countRows, err := s.db.QueryContext(ctx, `SELECT node_type,disposition,COUNT(*) FROM blackboard_node_heads WHERE project_id=? GROUP BY node_type,disposition ORDER BY node_type,disposition`, projectID)
	if err != nil {
		return HealthRun{}, err
	}
	for countRows.Next() {
		var nodeType, disposition string
		var count int
		if err := countRows.Scan(&nodeType, &disposition, &count); err != nil {
			countRows.Close()
			return HealthRun{}, err
		}
		metrics.NodeCounts[nodeType+":"+disposition] = count
	}
	if err := countRows.Close(); err != nil {
		return HealthRun{}, err
	}
	metrics.ActiveEdgeCount = len(doc.Edges)
	metrics.HistoryHash = historyHash
	metrics.StateHash = stateHash
	metrics.ProjectionHash = p.Hash
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_edge_heads WHERE project_id=? AND state='retired'`, projectID).Scan(&metrics.RetiredEdgeCount); err != nil {
		return HealthRun{}, fmt.Errorf("read Health retired edge count: %w", err)
	}
	plan, err := s.PlanCompaction(ctx, projectID)
	if err != nil {
		return HealthRun{}, fmt.Errorf("plan compaction for Health: %w", err)
	}
	metrics.EligibleComponentCount = plan.EligibleComponentCount
	metrics.ProtectedNodeCount = len(plan.PreservedAnchorIDs)
	metrics.ProtectedEstimatedTokens = plan.ProtectedEstimatedTokens
	metrics.ReclaimableEstimatedTokens = plan.ReclaimableEstimatedTokens
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(result_graph_revision),0) FROM blackboard_compactions WHERE project_id=?`, projectID).Scan(&metrics.LastCompactionRevision); err != nil {
		return HealthRun{}, fmt.Errorf("read last compaction revision: %w", err)
	}
	var results []HealthResult
	add := func(code string, severity HealthSeverity, details map[string]any) {
		raw, _ := canonicalJSON(details)
		fp := fmt.Sprintf("%x", framedHash("CyberPenda.Blackboard.HealthFingerprint.v1", []byte(code), raw))
		results = append(results, HealthResult{Fingerprint: fp, Code: code, Severity: severity, SubjectKind: "project", SubjectID: projectID, Details: details})
	}
	switch metrics.BudgetState {
	case BudgetAboveTarget:
		add("projection_above_target", "info", map[string]any{"estimated_tokens": p.EstimatedTokens})
	case BudgetWarning:
		add("projection_warning", "warning", map[string]any{"estimated_tokens": p.EstimatedTokens})
	case BudgetRequired:
		add("compaction_required", "critical", map[string]any{"estimated_tokens": p.EstimatedTokens})
	}
	if full {
		for _, detected := range projectionHealthResults(doc) {
			add(detected.Code, detected.Severity, detected.Details)
		}
		operational, operationalMetrics, err := s.operationalHealthResults(ctx, projectID, doc, checkedAt)
		if err != nil {
			return HealthRun{}, err
		}
		metrics.OpenAttemptsOnEndedContinuations = operationalMetrics.OpenAttemptsOnEndedContinuations
		metrics.MaxReconciliationAgeSeconds = operationalMetrics.MaxReconciliationAgeSeconds
		for _, detected := range operational {
			add(detected.Code, detected.Severity, detected.Details)
		}
	}
	for _, result := range results {
		switch result.Code {
		case "orphan_node":
			metrics.OrphanCount++
		case "evidence_missing":
			metrics.MissingEvidenceCount++
		case "duplicate_candidate":
			metrics.DuplicateCandidateCount++
		case "unresolved_contradiction":
			metrics.UnresolvedContradictionCount++
		case "objective_stranded":
			metrics.StrandedObjectiveCount++
		}
	}
	if stale {
		add("projection_stale", "warning", map[string]any{"checked_graph_revision": p.GraphRevision})
	}
	if blocked {
		add("compaction_blocked", "critical", map[string]any{"estimated_tokens": p.EstimatedTokens, "eligible_components": metrics.EligibleComponentCount})
	}
	status := healthStatus(results)
	completed := checkedAt
	runID := fmt.Sprintf("health:%x", framedHash("CyberPenda.Blackboard.HealthRun.v1", []byte(projectID), u64Bytes(uint64(p.GraphRevision)), []byte(p.Hash), []byte(started)))
	metricsJSON, _ := canonicalJSON(metrics)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HealthRun{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO blackboard_health_runs(project_id,run_id,checked_graph_revision,checked_state_hash,checked_projection_hash,checker_version,status,artifact_scan_status,started_at,completed_at,metrics_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, projectID, runID, p.GraphRevision, stateHash, p.Hash, blackboardHealthCheckerV1, status, "not_scanned", started, completed, string(metricsJSON))
	if err != nil {
		return HealthRun{}, err
	}
	for _, r := range results {
		details, _ := canonicalJSON(r.Details)
		if _, err = tx.ExecContext(ctx, `INSERT INTO blackboard_health_results(project_id,run_id,fingerprint,code,severity,subject_kind,subject_id,details_json) VALUES(?,?,?,?,?,?,?,?)`, projectID, runID, r.Fingerprint, r.Code, r.Severity, r.SubjectKind, r.SubjectID, string(details)); err != nil {
			return HealthRun{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return HealthRun{}, err
	}
	return HealthRun{RunID: runID, ProjectID: projectID, CheckedGraphRevision: p.GraphRevision, CheckedStateHash: stateHash, CheckedProjectionHash: p.Hash, Status: status, StartedAt: started, CompletedAt: completed, Metrics: metrics, Results: results, Stale: stale}, nil
}

func projectionHealthResults(doc canonicalMainGraphDocument) []HealthResult {
	byID := make(map[string]canonicalMainNode, len(doc.Nodes))
	adj := make(map[string][]string, len(doc.Nodes))
	roots := map[string]bool{}
	for _, node := range doc.Nodes {
		byID[node.ID] = node
		if protectedRoot(node) {
			roots[node.ID] = true
		}
	}
	for _, edge := range doc.Edges {
		adj[edge.FromNodeID] = append(adj[edge.FromNodeID], edge.ToNodeID)
		adj[edge.ToNodeID] = append(adj[edge.ToNodeID], edge.FromNodeID)
	}
	reachable := map[string]bool{}
	queue := make([]string, 0, len(roots))
	for id := range roots {
		reachable[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, next := range adj[id] {
			if !reachable[next] {
				reachable[next] = true
				queue = append(queue, next)
			}
		}
	}
	var out []HealthResult
	for _, node := range doc.Nodes {
		if !reachable[node.ID] {
			severity := HealthSeverity("info")
			if node.CreatedProvenance.ActorType == ActorTypeRuntime {
				severity = "warning"
			}
			out = append(out, HealthResult{Code: "orphan_node", Severity: severity, Details: map[string]any{"node_id": node.ID, "stable_key": node.StableKey}})
		}
		if node.NodeType == NodeTypeEvidenceArtifact && stringProp(node.Properties, "status") == "missing" {
			severity := HealthSeverity("warning")
			for _, edge := range doc.Edges {
				if edge.EdgeType == EdgeTypeEvidences && edge.FromNodeID == node.ID {
					target := byID[edge.ToNodeID]
					if (target.NodeType == NodeTypeFinding && stringProp(target.Properties, "status") == "confirmed") || (target.NodeType == NodeTypeSolution && stringProp(target.Properties, "status") == "verified") {
						severity = "critical"
					}
				}
			}
			out = append(out, HealthResult{Code: "evidence_missing", Severity: severity, Details: map[string]any{"node_id": node.ID, "stable_key": node.StableKey}})
		}
	}
	fingerprints := map[string][]canonicalMainNode{}
	for _, node := range doc.Nodes {
		if fp := duplicateFingerprint(node.NodeType, node.Properties); fp != "" {
			fingerprints[fp] = append(fingerprints[fp], node)
		}
	}
	keys := make([]string, 0, len(fingerprints))
	for fp, nodes := range fingerprints {
		if len(nodes) > 1 {
			keys = append(keys, fp)
		}
	}
	sort.Strings(keys)
	for _, fp := range keys {
		nodes := fingerprints[fp]
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
		ids := make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID
		}
		out = append(out, HealthResult{Code: "duplicate_candidate", Severity: "info", Details: map[string]any{"fingerprint": fp, "node_ids": ids}})
	}
	for _, edge := range doc.Edges {
		from, to := byID[edge.FromNodeID], byID[edge.ToNodeID]
		if edge.EdgeType == EdgeTypeContradicts && semanticallyLive(from) && semanticallyLive(to) {
			severity := HealthSeverity("warning")
			if confirmedConclusion(from) && confirmedConclusion(to) {
				severity = "critical"
			}
			out = append(out, HealthResult{Code: "unresolved_contradiction", Severity: severity, Details: map[string]any{"edge_id": edge.ID, "from_node_id": from.ID, "to_node_id": to.ID}})
		}
		if edge.EdgeType == EdgeTypeSatisfies && to.NodeType == NodeTypeExplorationObjective && stringProp(to.Properties, "status") == "open" {
			out = append(out, HealthResult{Code: "objective_satisfied_but_open", Severity: "warning", Details: map[string]any{"edge_id": edge.ID, "objective_id": to.ID}})
		}
		if edge.EdgeType == EdgeTypeDependsOn && from.NodeType == NodeTypeExplorationObjective && stringProp(from.Properties, "status") == "open" {
			status := stringProp(to.Properties, "status")
			if status == "abandoned" || status == "superseded" {
				out = append(out, HealthResult{Code: "objective_stranded", Severity: "warning", Details: map[string]any{"objective_id": from.ID, "prerequisite_id": to.ID, "prerequisite_status": status}})
			}
		}
	}
	openObjectives, openAttempts := 0, 0
	for _, n := range doc.Nodes {
		if n.NodeType == NodeTypeExplorationObjective && stringProp(n.Properties, "status") == "open" {
			openObjectives++
		}
		if n.NodeType == NodeTypeAttempt && stringProp(n.Properties, "status") == "open" {
			openAttempts++
		}
	}
	if openObjectives > 0 && len(doc.FrontierNodeIDs) == 0 && openAttempts == 0 {
		out = append(out, HealthResult{Code: "frontier_stalled", Severity: "warning", Details: map[string]any{"open_objectives": openObjectives}})
	}
	for _, node := range doc.Nodes {
		if node.NodeType == NodeTypeProjectFact && stringProp(node.Properties, "confidence") == "confirmed" && node.CreatedProvenance.ActorType == ActorTypeMigration {
			out = append(out, HealthResult{Code: "legacy_confirmed_fact_without_basis", Severity: "warning", Details: map[string]any{"node_id": node.ID}})
		}
		if node.NodeType == NodeTypeFinding && stringProp(node.Properties, "status") == "confirmed" && node.CreatedProvenance.ActorType == ActorTypeMigration {
			supported := false
			for _, edge := range doc.Edges {
				if edge.ToNodeID == node.ID && (edge.EdgeType == EdgeTypeEvidences || edge.EdgeType == EdgeTypeSupports) {
					supported = true
				}
			}
			if !supported {
				out = append(out, HealthResult{Code: "legacy_confirmed_finding_without_support", Severity: "warning", Details: map[string]any{"node_id": node.ID}})
			}
		}
	}
	return out
}

func (s *GraphService) operationalHealthResults(ctx context.Context, projectID string, doc canonicalMainGraphDocument, checkedAt string) ([]HealthResult, HealthMetrics, error) {
	var out []HealthResult
	var metrics HealthMetrics
	appendResult := func(code string, severity HealthSeverity, details map[string]any) {
		out = append(out, HealthResult{Code: code, Severity: severity, Details: details})
	}
	var quick string
	if err := s.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quick); err != nil {
		return nil, metrics, fmt.Errorf("run SQLite quick_check: %w", err)
	}
	if quick != "ok" {
		appendResult("sqlite_integrity_failure", "critical", map[string]any{"quick_check": quick})
	}
	fkRows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, metrics, fmt.Errorf("run SQLite foreign_key_check: %w", err)
	}
	if fkRows.Next() {
		appendResult("sqlite_integrity_failure", "critical", map[string]any{"foreign_key_check": "failed"})
	}
	if err := fkRows.Close(); err != nil {
		return nil, metrics, err
	}
	var dangling int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_edge_heads e LEFT JOIN blackboard_node_heads f ON f.project_id=e.project_id AND f.node_id=e.from_node_id LEFT JOIN blackboard_node_heads t ON t.project_id=e.project_id AND t.node_id=e.to_node_id WHERE e.project_id=? AND e.state='active' AND (f.node_id IS NULL OR t.node_id IS NULL OR f.disposition<>'main' OR t.disposition<>'main')`, projectID).Scan(&dangling); err != nil {
		return nil, metrics, err
	}
	if dangling > 0 {
		appendResult("active_dangling_edge", "critical", map[string]any{"count": dangling})
	}
	var aliases int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_key_registry k LEFT JOIN blackboard_node_heads h ON h.project_id=k.project_id AND h.node_id=k.canonical_node_id WHERE k.project_id=? AND (h.node_id IS NULL OR h.node_type<>k.node_type OR (k.role<>'stable' AND (h.disposition<>'main' OR k.source_node_id=k.canonical_node_id)))`, projectID).Scan(&aliases); err != nil {
		return nil, metrics, err
	}
	if aliases > 0 {
		appendResult("alias_redirect_invalid", "critical", map[string]any{"count": aliases})
	}
	var missing int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT v.mutation_seq,v.operation_index FROM blackboard_node_heads h JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version WHERE h.project_id=? UNION ALL SELECT v.mutation_seq,v.operation_index FROM blackboard_edge_heads h JOIN blackboard_edge_versions v ON v.project_id=h.project_id AND v.edge_id=h.edge_id AND v.version=h.version WHERE h.project_id=?) x LEFT JOIN blackboard_graph_operations o ON o.project_id=? AND o.mutation_seq=x.mutation_seq AND o.operation_index=x.operation_index LEFT JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id WHERE p.id IS NULL`, projectID, projectID, projectID).Scan(&missing); err != nil {
		return nil, metrics, err
	}
	if missing > 0 {
		appendResult("missing_provenance", "critical", map[string]any{"count": missing})
	}
	checked, err := time.Parse(time.RFC3339Nano, checkedAt)
	if err != nil {
		return nil, metrics, err
	}
	for _, node := range doc.Nodes {
		if node.NodeType == NodeTypeGoal {
			taskID := stringProp(node.Properties, "task_id")
			var goal, status string
			err := s.db.QueryRowContext(ctx, `SELECT goal,status FROM tasks WHERE id=? AND project_id=?`, taskID, projectID).Scan(&goal, &status)
			if err != nil {
				appendResult("goal_projection_drift", "warning", map[string]any{"goal_node_id": node.ID, "reason": "task_missing"})
			} else if goal != stringProp(node.Properties, "text") || taskID == "" {
				appendResult("goal_projection_drift", "critical", map[string]any{"goal_node_id": node.ID, "reason": "immutable_mismatch"})
			} else if status != stringProp(node.Properties, "task_status") {
				appendResult("goal_projection_drift", "warning", map[string]any{"goal_node_id": node.ID, "reason": "status_drift"})
			}
		}
		if node.NodeType != NodeTypeAttempt || stringProp(node.Properties, "status") != "open" || node.CreatedProvenance.ContinuationID == nil {
			continue
		}
		continuationID := *node.CreatedProvenance.ContinuationID
		var status, endedAt, reconciliation string
		if err := s.db.QueryRowContext(ctx, `SELECT status,ended_at,blackboard_reconciliation_status FROM task_continuations WHERE id=?`, continuationID).Scan(&status, &endedAt, &reconciliation); err != nil {
			continue
		}
		if endedAt == "" {
			continue
		}
		ended, err := time.Parse(time.RFC3339Nano, endedAt)
		if err != nil {
			continue
		}
		age := int(checked.Sub(ended).Seconds())
		if age < 0 {
			age = 0
		}
		metrics.OpenAttemptsOnEndedContinuations++
		if age > metrics.MaxReconciliationAgeSeconds {
			metrics.MaxReconciliationAgeSeconds = age
		}
		if status == "completed" && reconciliation == "completed" {
			code := "completion_protocol_gap"
			severity := HealthSeverity("warning")
			if age >= 300 {
				code = "completion_protocol_stuck"
				severity = "critical"
			}
			appendResult(code, severity, map[string]any{"attempt_id": node.ID, "continuation_id": continuationID, "age_seconds": age})
		} else {
			code := "reconciliation_pending"
			severity := HealthSeverity("info")
			if age >= 300 {
				code = "reconciliation_stuck"
				severity = "critical"
			} else if age >= 30 {
				code = "reconciliation_lag"
				severity = "warning"
			}
			appendResult(code, severity, map[string]any{"attempt_id": node.ID, "continuation_id": continuationID, "age_seconds": age})
		}
	}

	if err := verifyMutationChain(ctx, s.db.DB, projectID); err != nil {
		appendResult("history_chain_broken", "critical", map[string]any{"error": err.Error()})
	}
	mismatch, err := s.materializationMismatch(ctx, projectID, doc.GraphRevision)
	if err != nil {
		return nil, metrics, err
	}
	if mismatch {
		appendResult("materialization_mismatch", "critical", map[string]any{"graph_revision": doc.GraphRevision})
	}
	manifestMismatch, err := s.archiveManifestMismatch(ctx, projectID)
	if err != nil {
		return nil, metrics, err
	}
	if manifestMismatch {
		appendResult("archive_manifest_mismatch", "critical", map[string]any{})
	}
	return out, metrics, nil
}

func (s *GraphService) materializationMismatch(ctx context.Context, projectID string, revision int) (bool, error) {
	snapshot, err := reconstructGraph(ctx, s.db.DB, projectID, revision)
	if err != nil {
		return false, err
	}
	wantNodes := make([]string, len(snapshot.Nodes))
	for i, n := range snapshot.Nodes {
		wantNodes[i] = fmt.Sprintf("%s|%d|%s|%s", n.ID, n.Version, n.Disposition, n.SemanticHash)
	}
	sort.Strings(wantNodes)
	rows, err := s.db.QueryContext(ctx, `SELECT node_id,version,disposition,semantic_hash FROM blackboard_node_heads WHERE project_id=? ORDER BY node_id`, projectID)
	if err != nil {
		return false, err
	}
	var gotNodes []string
	for rows.Next() {
		var id, disp, hash string
		var version int
		if err := rows.Scan(&id, &version, &disp, &hash); err != nil {
			rows.Close()
			return false, err
		}
		gotNodes = append(gotNodes, fmt.Sprintf("%s|%d|%s|%s", id, version, disp, hash))
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	wantEdges := make([]string, len(snapshot.Edges))
	for i, e := range snapshot.Edges {
		wantEdges[i] = fmt.Sprintf("%s|%d|%s|%s", e.ID, e.Version, e.State, e.SemanticHash)
	}
	sort.Strings(wantEdges)
	edgeRows, err := s.db.QueryContext(ctx, `SELECT edge_id,version,state,semantic_hash FROM blackboard_edge_heads WHERE project_id=? ORDER BY edge_id`, projectID)
	if err != nil {
		return false, err
	}
	var gotEdges []string
	for edgeRows.Next() {
		var id, state, hash string
		var version int
		if err := edgeRows.Scan(&id, &version, &state, &hash); err != nil {
			edgeRows.Close()
			return false, err
		}
		gotEdges = append(gotEdges, fmt.Sprintf("%s|%d|%s|%s", id, version, state, hash))
	}
	if err := edgeRows.Close(); err != nil {
		return false, err
	}
	return strings.Join(wantNodes, "\n") != strings.Join(gotNodes, "\n") || strings.Join(wantEdges, "\n") != strings.Join(gotEdges, "\n"), nil
}

func (s *GraphService) archiveManifestMismatch(ctx context.Context, projectID string) (bool, error) {
	type manifest struct {
		nodesJSON, edgesJSON string
		seq                  sql.NullInt64
	}
	var manifests []manifest
	rows, err := s.db.QueryContext(ctx, `SELECT c.archived_node_ids_json,c.retired_edge_ids_json,m.mutation_seq FROM blackboard_compactions c LEFT JOIN blackboard_graph_mutations m ON m.project_id=c.project_id AND m.mutation_id=c.mutation_id WHERE c.project_id=?`, projectID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var m manifest
		if err := rows.Scan(&m.nodesJSON, &m.edgesJSON, &m.seq); err != nil {
			rows.Close()
			return false, err
		}
		manifests = append(manifests, m)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, m := range manifests {
		if !m.seq.Valid {
			return true, nil
		}
		var nodes, edges []string
		if json.Unmarshal([]byte(m.nodesJSON), &nodes) != nil || json.Unmarshal([]byte(m.edgesJSON), &edges) != nil {
			return true, nil
		}
		for _, id := range nodes {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_node_versions WHERE project_id=? AND node_id=? AND mutation_seq=? AND disposition='archived'`, projectID, id, m.seq.Int64).Scan(&count); err != nil {
				return false, err
			}
			if count != 1 {
				return true, nil
			}
		}
		for _, id := range edges {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_edge_versions WHERE project_id=? AND edge_id=? AND mutation_seq=? AND state='retired'`, projectID, id, m.seq.Int64).Scan(&count); err != nil {
				return false, err
			}
			if count != 1 {
				return true, nil
			}
		}
	}
	return false, nil
}

func protectedRoot(node canonicalMainNode) bool {
	switch node.NodeType {
	case NodeTypeGoal:
		status := stringProp(node.Properties, "task_status")
		return status != "completed" && status != "failed" && status != "stopped" && status != "interrupted"
	case NodeTypeExplorationObjective:
		return stringProp(node.Properties, "status") == "open"
	case NodeTypeAttempt:
		status := stringProp(node.Properties, "status")
		return status == "open" || status == "failed" || status == "blocked" || status == "inconclusive" || status == "interrupted"
	case NodeTypeProjectDirective:
		return stringProp(node.Properties, "status") == "active"
	case NodeTypeProjectFact:
		status := stringProp(node.Properties, "confidence")
		return status == "tentative" || status == "confirmed"
	case NodeTypeFinding:
		return stringProp(node.Properties, "status") == "confirmed"
	case NodeTypeSolution:
		return stringProp(node.Properties, "status") == "verified"
	}
	return false
}

func semanticallyLive(node canonicalMainNode) bool {
	switch node.NodeType {
	case NodeTypeProjectFact:
		s := stringProp(node.Properties, "confidence")
		return s == "tentative" || s == "confirmed"
	case NodeTypeFinding:
		s := stringProp(node.Properties, "status")
		return s == "unconfirmed" || s == "confirmed"
	case NodeTypeSolution:
		s := stringProp(node.Properties, "status")
		return s == "candidate" || s == "verified"
	case NodeTypeHypothesis:
		return stringProp(node.Properties, "status") != "superseded"
	case NodeTypeObservation:
		return stringProp(node.Properties, "status") != "superseded"
	}
	return false
}

func confirmedConclusion(node canonicalMainNode) bool {
	return (node.NodeType == NodeTypeProjectFact && stringProp(node.Properties, "confidence") == "confirmed") ||
		(node.NodeType == NodeTypeFinding && stringProp(node.Properties, "status") == "confirmed") ||
		(node.NodeType == NodeTypeSolution && stringProp(node.Properties, "status") == "verified")
}

func healthStatus(results []HealthResult) HealthStatus {
	status := HealthStatusHealthy
	for _, result := range results {
		if result.Severity == "critical" {
			return HealthStatusCritical
		}
		if result.Severity == "warning" {
			status = HealthStatusDegraded
		} else if result.Severity == "info" && status == HealthStatusHealthy {
			status = HealthStatusAttention
		}
	}
	return status
}

func (s *GraphService) LatestHealth(ctx context.Context, projectID string) (HealthRun, error) {
	var health HealthRun
	var metricsJSON string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,checked_graph_revision,checked_state_hash,checked_projection_hash,status,started_at,COALESCE(completed_at,''),metrics_json FROM blackboard_health_runs WHERE project_id=? ORDER BY started_at DESC,rowid DESC LIMIT 1`, projectID).Scan(&health.RunID, &health.CheckedGraphRevision, &health.CheckedStateHash, &health.CheckedProjectionHash, &health.Status, &health.StartedAt, &health.CompletedAt, &metricsJSON)
	if err != nil {
		return health, err
	}
	health.ProjectID = projectID
	if err := json.Unmarshal([]byte(metricsJSON), &health.Metrics); err != nil {
		return health, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT fingerprint,code,severity,subject_kind,subject_id,details_json FROM blackboard_health_results WHERE project_id=? AND run_id=? ORDER BY fingerprint`, projectID, health.RunID)
	if err != nil {
		return health, err
	}
	for rows.Next() {
		var result HealthResult
		var details string
		if err := rows.Scan(&result.Fingerprint, &result.Code, &result.Severity, &result.SubjectKind, &result.SubjectID, &details); err != nil {
			rows.Close()
			return health, err
		}
		if err := json.Unmarshal([]byte(details), &result.Details); err != nil {
			rows.Close()
			return health, err
		}
		health.Results = append(health.Results, result)
	}
	if err := rows.Close(); err != nil {
		return health, err
	}
	var currentRevision int
	var currentStateHash, currentProjectionHash, renderer string
	if err := s.db.QueryRowContext(ctx, `SELECT current_graph_revision,current_semantic_state_hash,COALESCE(current_main_projection_hash,''),projection_renderer_version FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&currentRevision, &currentStateHash, &currentProjectionHash, &renderer); err != nil {
		return health, err
	}
	health.Stale = health.CheckedGraphRevision != currentRevision || health.CheckedStateHash != currentStateHash || health.CheckedProjectionHash != currentProjectionHash || renderer != CanonicalMainGraphRendererV1
	return health, nil
}

func (s *GraphService) persistUnknownHealth(ctx context.Context, projectID string, revision int, checkedAt string, cause error) error {
	runID := fmt.Sprintf("health:%x", framedHash("CyberPenda.Blackboard.HealthRun.v1", []byte(projectID), u64Bytes(uint64(revision)), []byte("unknown"), []byte(checkedAt)))
	metricsJSON, _ := canonicalJSON(HealthMetrics{BudgetState: BudgetUnknown, NodeCounts: map[string]int{}})
	details, _ := canonicalJSON(map[string]any{"error": cause.Error()})
	fingerprint := fmt.Sprintf("%x", framedHash("CyberPenda.Blackboard.HealthFingerprint.v1", []byte("budget_unknown"), details))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO blackboard_health_runs(project_id,run_id,checked_graph_revision,checked_state_hash,checked_projection_hash,checker_version,status,artifact_scan_status,started_at,completed_at,metrics_json) VALUES(?,?,?,'','',?,'unknown','unknown',?,?,?)`, projectID, runID, revision, blackboardHealthCheckerV1, checkedAt, checkedAt, string(metricsJSON)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO blackboard_health_results(project_id,run_id,fingerprint,code,severity,subject_kind,subject_id,details_json) VALUES(?,?,?,?,?,?,?,?)`, projectID, runID, fingerprint, "budget_unknown", "warning", "project", projectID, string(details)); err != nil {
		return err
	}
	lower := strings.ToLower(cause.Error())
	if strings.Contains(lower, "integrity") || strings.Contains(lower, "mutation chain") || strings.Contains(lower, "hash mismatch") {
		chainFingerprint := fmt.Sprintf("%x", framedHash("CyberPenda.Blackboard.HealthFingerprint.v1", []byte("history_chain_broken"), details))
		if _, err = tx.ExecContext(ctx, `INSERT INTO blackboard_health_results(project_id,run_id,fingerprint,code,severity,subject_kind,subject_id,details_json) VALUES(?,?,?,?,?,?,?,?)`, projectID, runID, chainFingerprint, "history_chain_broken", "critical", "project", projectID, string(details)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RunHealth performs a fresh canonical measurement and persists a derived Health run.
func (s *GraphService) RunHealth(ctx context.Context, projectID string) (HealthRun, error) {
	checkedAt := s.clock.Now().UTC().Format(time.RFC3339Nano)
	var revision, dirty int
	var hash, renderer string
	if err := s.db.QueryRowContext(ctx, `SELECT current_graph_revision,projection_dirty_revision,COALESCE(current_main_projection_hash,''),projection_renderer_version FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&revision, &dirty, &hash, &renderer); err != nil {
		if persistErr := s.persistUnknownHealth(context.Background(), projectID, revision, checkedAt, err); persistErr != nil {
			return HealthRun{}, fmt.Errorf("read Health projection state: %v; persist unknown Health run: %w", err, persistErr)
		}
		return HealthRun{}, fmt.Errorf("read Health projection state: %w", err)
	}
	stale := dirty != 0 || hash == "" || renderer != CanonicalMainGraphRendererV1
	projection, err := s.remeasureCanonicalMainGraphAt(ctx, projectID, checkedAt)
	if err != nil {
		if persistErr := s.persistUnknownHealth(context.Background(), projectID, revision, checkedAt, err); persistErr != nil {
			return HealthRun{}, fmt.Errorf("%v; persist unknown Health run: %w", err, persistErr)
		}
		return HealthRun{}, err
	}
	return s.runHealth(ctx, projectID, projection, false, stale, true, checkedAt)
}
