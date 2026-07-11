package blackboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func (s *GraphService) PlanCompaction(ctx context.Context, projectID string) (CompactionPlan, error) {
	return s.PlanCompactionWithOptions(ctx, projectID, CompactionOptions{})
}

func (s *GraphService) PlanCompactionWithOptions(ctx context.Context, projectID string, options CompactionOptions) (CompactionPlan, error) {
	var revision int
	if err := s.db.QueryRowContext(ctx, `SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&revision); err != nil {
		return CompactionPlan{}, graphStorageError("read compaction base revision", err)
	}
	before, err := s.CanonicalMainGraph(ctx, projectID, revision)
	if err != nil {
		return CompactionPlan{}, err
	}
	plan := CompactionPlan{ProjectID: projectID, BaseGraphRevision: revision, BeforeHash: before.Hash, BeforeBytes: before.ByteCount, BeforeTokens: before.EstimatedTokens, ExpectedNodeVersions: map[string]int{}, ExpectedEdgeVersions: map[string]int{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CompactionPlan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	type candidateNode struct {
		id, key                          string
		typ                              NodeType
		version, graphRevision, estimate int
		props                            map[string]any
		eligible, protected, held        bool
		policy                           int
	}
	nodes := map[string]*candidateNode{}
	rows, err := tx.QueryContext(ctx, `SELECT h.node_id,h.node_type,n.original_stable_key,h.version,h.graph_revision,v.properties_json FROM blackboard_node_heads h JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version WHERE h.project_id=? AND h.disposition='main' ORDER BY h.node_type,n.original_stable_key COLLATE BINARY,h.node_id`, projectID)
	if err != nil {
		return CompactionPlan{}, err
	}
	for rows.Next() {
		var n candidateNode
		var typ, propsJSON string
		if err := rows.Scan(&n.id, &typ, &n.key, &n.version, &n.graphRevision, &propsJSON); err != nil {
			rows.Close()
			return CompactionPlan{}, err
		}
		n.typ = NodeType(typ)
		if err := json.Unmarshal([]byte(propsJSON), &n.props); err != nil {
			rows.Close()
			return CompactionPlan{}, err
		}
		n.estimate = len(propsJSON) + len(n.key) + 768
		nodes[n.id] = &n
	}
	if err := rows.Close(); err != nil {
		return CompactionPlan{}, err
	}
	for _, n := range nodes {
		protected, err := archiveProtected(tx, projectID, n.id, &graphState{})
		if err != nil {
			return CompactionPlan{}, err
		}
		n.protected = protected
		if protected {
			plan.PreservedAnchorIDs = append(plan.PreservedAnchorIDs, n.id)
			plan.ProtectedEstimatedTokens += (n.estimate + 3) / 4
			continue
		}
		mutable := mutableNode{nodeID: n.id, nodeType: n.typ, stableKey: n.key, version: n.version, disposition: DispositionMain, props: n.props}
		n.eligible = archiveEligible(mutable)
		if !options.OverrideRestoreHold {
			n.held, err = s.restoreHoldActiveTx(ctx, tx, projectID, n.id)
		}
		if err != nil {
			return CompactionPlan{}, err
		}
		n.policy = compactionPolicyClass(mutable)
	}
	adj := map[string][]string{}
	edgeRows, err := tx.QueryContext(ctx, `SELECT from_node_id,to_node_id FROM blackboard_edge_heads WHERE project_id=? AND state='active' ORDER BY edge_id`, projectID)
	if err != nil {
		return CompactionPlan{}, err
	}
	for edgeRows.Next() {
		var from, to string
		if err := edgeRows.Scan(&from, &to); err != nil {
			edgeRows.Close()
			return CompactionPlan{}, err
		}
		if nodes[from] != nil && nodes[to] != nil && !nodes[from].protected && !nodes[to].protected {
			adj[from] = append(adj[from], to)
			adj[to] = append(adj[to], from)
		}
	}
	if err := edgeRows.Close(); err != nil {
		return CompactionPlan{}, err
	}
	type component struct {
		nodes                                     []*candidateNode
		policy, reclaim, lastRevision, minOrdinal int
		minKey, minID                             string
	}
	visited := map[string]bool{}
	var components []component
	orderedIDs := make([]string, 0, len(nodes))
	for id := range nodes {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)
	for _, root := range orderedIDs {
		if visited[root] || nodes[root].protected {
			continue
		}
		visited[root] = true
		queue := []string{root}
		c := component{policy: 99, minOrdinal: 99, lastRevision: 1 << 30}
		valid := true
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			n := nodes[id]
			c.nodes = append(c.nodes, n)
			c.reclaim += n.estimate
			if !n.eligible || n.held {
				valid = false
			}
			if n.policy < c.policy {
				c.policy = n.policy
			}
			if n.graphRevision < c.lastRevision {
				c.lastRevision = n.graphRevision
			}
			ord := nodeTypeOrdinal(n.typ)
			if ord < c.minOrdinal {
				c.minOrdinal = ord
			}
			if c.minKey == "" || n.key < c.minKey {
				c.minKey = n.key
			}
			if c.minID == "" || n.id < c.minID {
				c.minID = n.id
			}
			for _, next := range adj[id] {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
		if valid {
			components = append(components, c)
		}
	}
	plan.EligibleComponentCount = len(components)
	for _, component := range components {
		plan.ReclaimableEstimatedTokens += (component.reclaim + 3) / 4
	}
	sort.Slice(components, func(i, j int) bool {
		a, b := components[i], components[j]
		if a.policy != b.policy {
			return a.policy < b.policy
		}
		if a.reclaim != b.reclaim {
			return a.reclaim > b.reclaim
		}
		if a.lastRevision != b.lastRevision {
			return a.lastRevision < b.lastRevision
		}
		if a.minOrdinal != b.minOrdinal {
			return a.minOrdinal < b.minOrdinal
		}
		if a.minKey != b.minKey {
			return a.minKey < b.minKey
		}
		return a.minID < b.minID
	})
	simulated := before.EstimatedTokens
	for index, c := range components {
		if simulated <= budgetTargetTokens {
			break
		}
		sort.Slice(c.nodes, func(i, j int) bool {
			if c.nodes[i].typ != c.nodes[j].typ {
				return nodeTypeOrdinal(c.nodes[i].typ) < nodeTypeOrdinal(c.nodes[j].typ)
			}
			if c.nodes[i].key != c.nodes[j].key {
				return c.nodes[i].key < c.nodes[j].key
			}
			return c.nodes[i].id < c.nodes[j].id
		})
		ids := make([]string, len(c.nodes))
		for i, n := range c.nodes {
			ids[i] = n.id
			plan.ArchiveNodeIDs = append(plan.ArchiveNodeIDs, n.id)
			plan.ExpectedNodeVersions[n.id] = n.version
		}
		plan.Rationale = append(plan.Rationale, fmt.Sprintf("component_%03d:policy_class_%d:reclaim_estimate_%d:%s", index, c.policy, (c.reclaim+3)/4, strings.Join(ids, ",")))
		simulated -= (c.reclaim + 3) / 4
	}

	selected := map[string]bool{}
	for _, id := range plan.ArchiveNodeIDs {
		selected[id] = true
	}
	var simulatedDoc canonicalMainGraphDocument
	if err := json.Unmarshal(before.Bytes, &simulatedDoc); err != nil {
		return CompactionPlan{}, fmt.Errorf("decode projection for compaction simulation: %w", err)
	}
	simulatedDoc.GraphRevision = revision + 1
	keptNodes := simulatedDoc.Nodes[:0]
	for _, node := range simulatedDoc.Nodes {
		if !selected[node.ID] {
			keptNodes = append(keptNodes, node)
		}
	}
	simulatedDoc.Nodes = keptNodes
	keptEdges := simulatedDoc.Edges[:0]
	for _, edge := range simulatedDoc.Edges {
		if selected[edge.FromNodeID] || selected[edge.ToNodeID] {
			plan.RetireEdgeIDs = append(plan.RetireEdgeIDs, edge.ID)
			plan.ExpectedEdgeVersions[edge.ID] = edge.Version
			continue
		}
		keptEdges = append(keptEdges, edge)
	}
	simulatedDoc.Edges = keptEdges
	filterIDs := func(ids []string) []string {
		out := ids[:0]
		for _, id := range ids {
			if !selected[id] {
				out = append(out, id)
			}
		}
		return out
	}
	simulatedDoc.FrontierNodeIDs = filterIDs(simulatedDoc.FrontierNodeIDs)
	simulatedDoc.CurrentTruthNodeIDs = filterIDs(simulatedDoc.CurrentTruthNodeIDs)
	simulatedProjection, err := measureCanonicalMainGraphDocument(projectID, revision+1, simulatedDoc)
	if err != nil {
		return CompactionPlan{}, err
	}
	plan.SimulatedAfterHash = simulatedProjection.Hash
	plan.SimulatedAfterTokens = simulatedProjection.EstimatedTokens
	if err := tx.Commit(); err != nil {
		return CompactionPlan{}, err
	}
	sort.Strings(plan.PreservedAnchorIDs)
	sort.Strings(plan.RetireEdgeIDs)
	return plan, nil
}

func compactionPolicyClass(n mutableNode) int {
	status, _ := n.props["status"].(string)
	confidence, _ := n.props["confidence"].(string)
	if status == "superseded" || status == "rejected" || status == "false_positive" || confidence == "deprecated" {
		return 0
	}
	if n.nodeType == NodeTypeAttempt && status == "succeeded" {
		return 1
	}
	if n.nodeType == NodeTypeExplorationObjective {
		return 2
	}
	if n.nodeType == NodeTypeEntity || n.nodeType == NodeTypeEvidenceArtifact {
		return 3
	}
	return 4
}

func (s *GraphService) restoreHoldActiveTx(ctx context.Context, tx *sql.Tx, projectID, nodeID string) (bool, error) {
	var restoreRevision int
	var nodesJSON string
	err := tx.QueryRowContext(ctx, `SELECT result_graph_revision,restored_node_ids_json FROM blackboard_restore_manifests WHERE project_id=? ORDER BY result_graph_revision DESC LIMIT 1`, projectID).Scan(&restoreRevision, &nodesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read latest restore hold: %w", err)
	}
	var nodes []string
	if err := json.Unmarshal([]byte(nodesJSON), &nodes); err != nil {
		return false, fmt.Errorf("decode restore hold members: %w", err)
	}
	found := false
	for _, id := range nodes {
		if id == nodeID {
			found = true
			break
		}
	}
	if !found {
		return false, nil
	}
	var pins int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_continuations c JOIN tasks t ON t.id=c.task_id WHERE t.project_id=? AND c.blackboard_graph_revision>=?`, projectID, restoreRevision).Scan(&pins)
	if err != nil {
		return false, fmt.Errorf("read restore hold continuation pins: %w", err)
	}
	return pins == 0, nil
}

func (s *GraphService) ApplyCompaction(ctx context.Context, plan CompactionPlan) (CompactionManifest, error) {
	var revision int
	if err := s.db.QueryRowContext(ctx, `SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, plan.ProjectID).Scan(&revision); err != nil {
		return CompactionManifest{}, err
	}
	if revision != plan.BaseGraphRevision {
		return CompactionManifest{}, validationError(ErrCodeVersionConflict, "compaction plan is stale", -1, "", "base_graph_revision")
	}
	before, err := s.CanonicalMainGraph(ctx, plan.ProjectID, revision)
	if err != nil {
		return CompactionManifest{}, err
	}
	if before.Hash != plan.BeforeHash {
		return CompactionManifest{}, validationError(ErrCodeVersionConflict, "compaction projection hash is stale", -1, "", "before_hash")
	}
	ops := make([]Operation, 0, len(plan.ArchiveNodeIDs))
	for i, id := range plan.ArchiveNodeIDs {
		var version int
		if err := s.db.QueryRowContext(ctx, `SELECT version FROM blackboard_node_heads WHERE project_id=? AND node_id=?`, plan.ProjectID, id).Scan(&version); err != nil {
			return CompactionManifest{}, err
		}
		if version != plan.ExpectedNodeVersions[id] {
			return CompactionManifest{}, validationError(ErrCodeVersionConflict, "compaction node version is stale", i, "", "expected_version")
		}
		ops = append(ops, Operation{OpID: fmt.Sprintf("archive-%03d", i), Kind: OpSetDisposition, Node: NodeRef{ID: id}, Disposition: SetDispositionInput{ExpectedVersion: version, Disposition: DispositionArchived}})
	}
	for i, id := range plan.RetireEdgeIDs {
		var version int
		var state string
		if err := s.db.QueryRowContext(ctx, `SELECT version,state FROM blackboard_edge_heads WHERE project_id=? AND edge_id=?`, plan.ProjectID, id).Scan(&version, &state); err != nil {
			return CompactionManifest{}, err
		}
		if version != plan.ExpectedEdgeVersions[id] || state != "active" {
			return CompactionManifest{}, validationError(ErrCodeVersionConflict, "compaction edge version is stale", i, "", "expected_edge_version")
		}
	}
	if len(ops) == 0 {
		return CompactionManifest{}, sql.ErrNoRows
	}
	var projectKind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id=?`, plan.ProjectID).Scan(&projectKind); err != nil {
		return CompactionManifest{}, err
	}
	maintenanceContext := SystemExecutionContext(plan.ProjectID, projectKind, blackboardCompactorActorID)
	maintenanceContext.compactionPlan = &plan
	key := fmt.Sprintf("compact:%d:%s", revision, before.Hash[:16])
	if _, err := s.Apply(ctx, MutationBatch{SchemaVersion: GraphMutationSchemaVersion, IdempotencyKey: key, Context: maintenanceContext, Operations: ops}); err != nil {
		return CompactionManifest{}, err
	}
	return s.LatestCompaction(ctx, plan.ProjectID)
}

func (s *GraphService) persistCompactionManifestTx(ctx context.Context, tx *sql.Tx, batch MutationBatch, result MutationResult) error {
	plan := batch.Context.compactionPlan
	if plan == nil {
		return nil
	}
	doc, err := canonicalMainGraphDocumentAt(ctx, tx, batch.Context.ProjectID, result.GraphRevision)
	if err != nil {
		return fmt.Errorf("render compaction result before commit: %w", err)
	}
	after, err := measureCanonicalMainGraphDocument(batch.Context.ProjectID, result.GraphRevision, doc)
	if err != nil {
		return err
	}
	if after.EstimatedTokens >= plan.BeforeTokens {
		return fmt.Errorf("compaction did not shrink canonical projection")
	}
	if after.Hash != plan.SimulatedAfterHash || after.EstimatedTokens != plan.SimulatedAfterTokens {
		return fmt.Errorf("compaction result did not match deterministic simulation")
	}
	retired, err := retiredEdgesForMutationQuery(ctx, tx, batch.Context.ProjectID, result.MutationSequence)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("compaction:%s:%d", batch.Context.ProjectID, result.GraphRevision)
	expectedJSON, _ := canonicalJSON(map[string]any{"nodes": plan.ExpectedNodeVersions, "edges": plan.ExpectedEdgeVersions})
	archivedJSON, _ := canonicalJSON(plan.ArchiveNodeIDs)
	retiredJSON, _ := canonicalJSON(retired)
	anchorsJSON, _ := canonicalJSON(plan.PreservedAnchorIDs)
	rationaleJSON, _ := canonicalJSON(plan.Rationale)
	_, err = tx.ExecContext(ctx, `INSERT INTO blackboard_compactions(project_id,manifest_id,base_graph_revision,result_graph_revision,before_hash,after_hash,before_bytes,after_bytes,before_tokens,after_tokens,expected_versions_json,archived_node_ids_json,retired_edge_ids_json,preserved_anchor_ids_json,rationale_json,mutation_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, batch.Context.ProjectID, id, plan.BaseGraphRevision, result.GraphRevision, plan.BeforeHash, after.Hash, plan.BeforeBytes, after.ByteCount, plan.BeforeTokens, after.EstimatedTokens, string(expectedJSON), string(archivedJSON), string(retiredJSON), string(anchorsJSON), string(rationaleJSON), result.MutationID, result.RecordedAt)
	if err != nil {
		return fmt.Errorf("persist compaction manifest before commit: %w", err)
	}
	return nil
}

func retiredEdgesForMutationQuery(ctx context.Context, q graphQuerier, projectID string, seq int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT edge_id FROM blackboard_edge_versions WHERE project_id=? AND mutation_seq=? AND state='retired' ORDER BY edge_id`, projectID, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *GraphService) persistRestoreManifestTx(ctx context.Context, tx *sql.Tx, batch MutationBatch, result MutationResult) error {
	manifest := batch.Context.restoreManifest
	if manifest == nil {
		return nil
	}
	beforeDoc, err := canonicalMainGraphDocumentAt(ctx, tx, batch.Context.ProjectID, result.GraphRevision-1)
	if err != nil {
		return fmt.Errorf("render restore base before commit: %w", err)
	}
	afterDoc, err := canonicalMainGraphDocumentAt(ctx, tx, batch.Context.ProjectID, result.GraphRevision)
	if err != nil {
		return fmt.Errorf("render restore result before commit: %w", err)
	}
	before, err := measureCanonicalMainGraphDocument(batch.Context.ProjectID, result.GraphRevision-1, beforeDoc)
	if err != nil {
		return err
	}
	after, err := measureCanonicalMainGraphDocument(batch.Context.ProjectID, result.GraphRevision, afterDoc)
	if err != nil {
		return err
	}
	nodes, _ := canonicalJSON(manifest.Nodes)
	edgeIDs, err := createdEdgesForMutationQuery(ctx, tx, batch.Context.ProjectID, result.MutationSequence)
	if err != nil {
		return err
	}
	edges, _ := canonicalJSON(edgeIDs)
	_, err = tx.ExecContext(ctx, `INSERT INTO blackboard_restore_manifests(project_id,manifest_id,base_graph_revision,result_graph_revision,restored_node_ids_json,restored_edge_ids_json,before_hash,after_hash,before_tokens,after_tokens,mutation_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, batch.Context.ProjectID, manifest.ID, result.GraphRevision-1, result.GraphRevision, string(nodes), string(edges), before.Hash, after.Hash, before.EstimatedTokens, after.EstimatedTokens, result.MutationID, result.RecordedAt)
	if err != nil {
		return fmt.Errorf("persist restore manifest before commit: %w", err)
	}
	return nil
}

func createdEdgesForMutationQuery(ctx context.Context, q graphQuerier, projectID string, seq int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM blackboard_edges WHERE project_id=? AND created_mutation_seq=? ORDER BY id`, projectID, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *GraphService) LatestCompaction(ctx context.Context, projectID string) (CompactionManifest, error) {
	var m CompactionManifest
	var archived, retired, anchors string
	err := s.db.QueryRowContext(ctx, `SELECT manifest_id,base_graph_revision,result_graph_revision,before_hash,after_hash,before_bytes,after_bytes,before_tokens,after_tokens,archived_node_ids_json,retired_edge_ids_json,preserved_anchor_ids_json,mutation_id,created_at FROM blackboard_compactions WHERE project_id=? ORDER BY result_graph_revision DESC LIMIT 1`, projectID).Scan(&m.ID, &m.BaseGraphRevision, &m.ResultGraphRevision, &m.BeforeHash, &m.AfterHash, &m.BeforeBytes, &m.AfterBytes, &m.BeforeTokens, &m.AfterTokens, &archived, &retired, &anchors, &m.MutationID, &m.CreatedAt)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal([]byte(archived), &m.ArchivedNodeIDs); err != nil {
		return m, fmt.Errorf("decode compaction archived nodes: %w", err)
	}
	if err := json.Unmarshal([]byte(retired), &m.RetiredEdgeIDs); err != nil {
		return m, fmt.Errorf("decode compaction retired edges: %w", err)
	}
	if err := json.Unmarshal([]byte(anchors), &m.PreservedAnchorIDs); err != nil {
		return m, fmt.Errorf("decode compaction preserved anchors: %w", err)
	}
	return m, nil
}
