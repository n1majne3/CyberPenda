package blackboard

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	CanonicalMainGraphRendererV1 = "canonical_main_graph_v1"
	UTF8BytesDiv4EstimatorV1     = "utf8_bytes_div_4_v1"
)

// CanonicalMainGraphProjection is the exact measured storage projection for a
// Project graph revision.
type CanonicalMainGraphProjection struct {
	ProjectID        string
	GraphRevision    int
	RendererVersion  string
	EstimatorVersion string
	Bytes            []byte
	Hash             string
	ByteCount        int
	EstimatedTokens  int
}

type compactProvenance struct {
	ActorType        ActorType `json:"actor_type"`
	ActorID          string    `json:"actor_id"`
	TaskID           *string   `json:"task_id"`
	ContinuationID   *string   `json:"continuation_id"`
	RuntimeProfileID *string   `json:"runtime_profile_id"`
	Runner           *string   `json:"runner"`
	SourceEventIDs   []string  `json:"source_event_ids"`
	MigrationSource  any       `json:"migration_source"`
	RecordedAt       string    `json:"recorded_at"`
}

type canonicalMainGraphDocument struct {
	SchemaVersion       int                 `json:"schema_version"`
	ProjectID           string              `json:"project_id"`
	ProjectKind         string              `json:"project_kind"`
	GraphRevision       int                 `json:"graph_revision"`
	Nodes               []canonicalMainNode `json:"nodes"`
	Edges               []canonicalMainEdge `json:"edges"`
	FrontierNodeIDs     []string            `json:"frontier_node_ids"`
	CurrentTruthNodeIDs []string            `json:"current_truth_node_ids"`
}

type canonicalMainNode struct {
	ID                string            `json:"id"`
	NodeType          NodeType          `json:"node_type"`
	StableKey         string            `json:"stable_key"`
	Version           int               `json:"version"`
	Disposition       Disposition       `json:"disposition"`
	Properties        map[string]any    `json:"properties"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
	CreatedProvenance compactProvenance `json:"created_provenance"`
	UpdatedProvenance compactProvenance `json:"updated_provenance"`
}

type canonicalMainEdge struct {
	ID                string            `json:"id"`
	EdgeType          EdgeType          `json:"edge_type"`
	FromNodeID        string            `json:"from_node_id"`
	ToNodeID          string            `json:"to_node_id"`
	Version           int               `json:"version"`
	State             string            `json:"state"`
	Summary           string            `json:"summary"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
	CreatedProvenance compactProvenance `json:"created_provenance"`
	UpdatedProvenance compactProvenance `json:"updated_provenance"`
}

// CanonicalMainGraph reconstructs and measures CanonicalMainGraphV1 at exactly
// revision. It reads only immutable ledger rows, so historical renderings do
// not depend on current materialized heads.
func (s *GraphService) CanonicalMainGraph(ctx context.Context, projectID string, revision int) (CanonicalMainGraphProjection, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CanonicalMainGraphProjection{}, graphStorageError("begin canonical main graph", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := verifyMutationChain(ctx, tx, projectID); err != nil {
		return CanonicalMainGraphProjection{}, fmt.Errorf("verify graph ledger before projection: %w", err)
	}
	doc, err := canonicalMainGraphDocumentAt(ctx, tx, projectID, revision)
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	projection, err := measureCanonicalMainGraphDocument(projectID, revision, doc)
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	if err := tx.Commit(); err != nil {
		return CanonicalMainGraphProjection{}, graphStorageError("commit canonical main graph", err)
	}
	return projection, nil
}

func measureCanonicalMainGraphDocument(projectID string, revision int, doc canonicalMainGraphDocument) (CanonicalMainGraphProjection, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return CanonicalMainGraphProjection{}, fmt.Errorf("encode canonical main graph: %w", err)
	}
	data := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	hash := framedHash("CyberPenda.Blackboard.MainProjection.v1", data)
	return CanonicalMainGraphProjection{ProjectID: projectID, GraphRevision: revision, RendererVersion: CanonicalMainGraphRendererV1, EstimatorVersion: UTF8BytesDiv4EstimatorV1, Bytes: data, Hash: hex.EncodeToString(hash), ByteCount: len(data), EstimatedTokens: (len(data) + 3) / 4}, nil
}

func canonicalMainGraphDocumentAt(ctx context.Context, q graphQuerier, projectID string, revision int) (canonicalMainGraphDocument, error) {
	snapshot, err := reconstructGraph(ctx, q, projectID, revision)
	if err != nil {
		return canonicalMainGraphDocument{}, err
	}
	nodesByID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		nodesByID[node.ID] = node
	}

	nodes := make([]canonicalMainNode, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if node.Disposition != DispositionMain {
			continue
		}
		created, err := provenanceForOperation(ctx, q, projectID, node.ID, 0, true)
		if err != nil {
			return canonicalMainGraphDocument{}, err
		}
		updated, err := provenanceForOperation(ctx, q, projectID, node.ID, node.Version, false)
		if err != nil {
			return canonicalMainGraphDocument{}, err
		}
		nodes = append(nodes, canonicalMainNode{ID: node.ID, NodeType: node.NodeType, StableKey: node.StableKey, Version: node.Version, Disposition: node.Disposition, Properties: node.PropertyMap, CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt, CreatedProvenance: created, UpdatedProvenance: updated})
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodeTypeOrdinal(nodes[i].NodeType) != nodeTypeOrdinal(nodes[j].NodeType) {
			return nodeTypeOrdinal(nodes[i].NodeType) < nodeTypeOrdinal(nodes[j].NodeType)
		}
		if nodes[i].StableKey != nodes[j].StableKey {
			return nodes[i].StableKey < nodes[j].StableKey
		}
		return nodes[i].ID < nodes[j].ID
	})

	edges := make([]canonicalMainEdge, 0, len(snapshot.Edges))
	for _, edge := range snapshot.Edges {
		from, fromOK := nodesByID[edge.FromNodeID]
		to, toOK := nodesByID[edge.ToNodeID]
		if edge.State != "active" || !fromOK || !toOK || from.Disposition != DispositionMain || to.Disposition != DispositionMain {
			continue
		}
		created, err := provenanceForOperation(ctx, q, projectID, edge.ID, 0, true)
		if err != nil {
			return canonicalMainGraphDocument{}, err
		}
		updated, err := provenanceForOperation(ctx, q, projectID, edge.ID, edge.Version, false)
		if err != nil {
			return canonicalMainGraphDocument{}, err
		}
		var createdAt, updatedAt string
		if err := q.QueryRowContext(ctx, `SELECT e.created_at,v.updated_at FROM blackboard_edges e JOIN blackboard_edge_versions v ON v.project_id=e.project_id AND v.edge_id=e.id AND v.version=? WHERE e.project_id=? AND e.id=?`, edge.Version, projectID, edge.ID).Scan(&createdAt, &updatedAt); err != nil {
			return canonicalMainGraphDocument{}, fmt.Errorf("read canonical edge timestamps: %w", err)
		}
		edges = append(edges, canonicalMainEdge{ID: edge.ID, EdgeType: edge.EdgeType, FromNodeID: edge.FromNodeID, ToNodeID: edge.ToNodeID, Version: edge.Version, State: edge.State, Summary: edge.Summary, CreatedAt: createdAt, UpdatedAt: updatedAt, CreatedProvenance: created, UpdatedProvenance: updated})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edgeTypeOrdinal(edges[i].EdgeType) != edgeTypeOrdinal(edges[j].EdgeType) {
			return edgeTypeOrdinal(edges[i].EdgeType) < edgeTypeOrdinal(edges[j].EdgeType)
		}
		if edges[i].FromNodeID != edges[j].FromNodeID {
			return edges[i].FromNodeID < edges[j].FromNodeID
		}
		if edges[i].ToNodeID != edges[j].ToNodeID {
			return edges[i].ToNodeID < edges[j].ToNodeID
		}
		return edges[i].ID < edges[j].ID
	})

	return canonicalMainGraphDocument{
		SchemaVersion: GraphMutationSchemaVersion, ProjectID: snapshot.ProjectID, ProjectKind: snapshot.ProjectKind,
		GraphRevision: revision, Nodes: nodes, Edges: edges,
		FrontierNodeIDs: historicalFrontierNodeIDs(snapshot), CurrentTruthNodeIDs: historicalCurrentTruthNodeIDs(snapshot),
	}, nil
}

func provenanceForOperation(ctx context.Context, q graphQuerier, projectID, identityID string, version int, creation bool) (compactProvenance, error) {
	var mutationSeq, operationIndex int
	var err error
	if creation {
		err = q.QueryRowContext(ctx, `SELECT created_mutation_seq,created_operation_index FROM (SELECT project_id,id,created_mutation_seq,created_operation_index FROM blackboard_nodes UNION ALL SELECT project_id,id,created_mutation_seq,created_operation_index FROM blackboard_edges) WHERE project_id=? AND id=?`, projectID, identityID).Scan(&mutationSeq, &operationIndex)
	} else {
		err = q.QueryRowContext(ctx, `SELECT mutation_seq,operation_index FROM (SELECT project_id,node_id AS id,version,mutation_seq,operation_index FROM blackboard_node_versions UNION ALL SELECT project_id,edge_id AS id,version,mutation_seq,operation_index FROM blackboard_edge_versions) WHERE project_id=? AND id=? AND version=?`, projectID, identityID, version).Scan(&mutationSeq, &operationIndex)
	}
	if err != nil {
		return compactProvenance{}, fmt.Errorf("read projection provenance operation: %w", err)
	}
	var p compactProvenance
	var actorType string
	var taskID, continuationID, profileID, runner, migrationJSON sql.NullString
	err = q.QueryRowContext(ctx, `SELECT p.actor_type,p.actor_id,p.task_id,p.continuation_id,p.runtime_profile_id,p.runner,p.migration_source_json,p.recorded_at FROM blackboard_graph_operations o JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id WHERE o.project_id=? AND o.mutation_seq=? AND o.operation_index=?`, projectID, mutationSeq, operationIndex).Scan(&actorType, &p.ActorID, &taskID, &continuationID, &profileID, &runner, &migrationJSON, &p.RecordedAt)
	if err != nil {
		return compactProvenance{}, fmt.Errorf("read projection provenance: %w", err)
	}
	p.ActorType = ActorType(actorType)
	p.TaskID, p.ContinuationID, p.RuntimeProfileID, p.Runner = nullStringPointer(taskID), nullStringPointer(continuationID), nullStringPointer(profileID), nullStringPointer(runner)
	p.SourceEventIDs = []string{}
	rows, err := q.QueryContext(ctx, `SELECT event_id FROM blackboard_graph_provenance_events pe JOIN blackboard_graph_operations o ON o.project_id=pe.project_id AND o.provenance_id=pe.provenance_id WHERE o.project_id=? AND o.mutation_seq=? AND o.operation_index=? ORDER BY pe.ordinal`, projectID, mutationSeq, operationIndex)
	if err != nil {
		return compactProvenance{}, fmt.Errorf("read projection provenance events: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return compactProvenance{}, err
		}
		p.SourceEventIDs = append(p.SourceEventIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return compactProvenance{}, fmt.Errorf("iterate projection provenance events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return compactProvenance{}, fmt.Errorf("close projection provenance events: %w", err)
	}
	if migrationJSON.Valid {
		var source any
		if err := json.Unmarshal([]byte(migrationJSON.String), &source); err != nil {
			return compactProvenance{}, fmt.Errorf("decode migration provenance: %w", err)
		}
		p.MigrationSource = source
	}
	return p, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	v := value.String
	return &v
}

func historicalCurrentTruthNodeIDs(snapshot GraphSnapshot) []string {
	var nodes []NodeRecord
	for _, node := range snapshot.Nodes {
		if node.NodeType == NodeTypeProjectFact && node.Disposition == DispositionMain {
			confidence, _ := node.PropertyMap["confidence"].(string)
			if confidence == "tentative" || confidence == "confirmed" {
				nodes = append(nodes, node)
			}
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].StableKey != nodes[j].StableKey {
			return nodes[i].StableKey < nodes[j].StableKey
		}
		return nodes[i].ID < nodes[j].ID
	})
	ids := make([]string, len(nodes))
	for i := range nodes {
		ids[i] = nodes[i].ID
	}
	return ids
}

func historicalFrontierNodeIDs(snapshot GraphSnapshot) []string {
	nodes := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		nodes[node.ID] = node
	}
	var candidates []NodeRecord
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeExplorationObjective || node.Disposition != DispositionMain || node.PropertyMap["status"] != "open" {
			continue
		}
		ready := true
		for _, edge := range snapshot.Edges {
			if edge.State != "active" {
				continue
			}
			var prerequisiteID string
			if edge.EdgeType == EdgeTypeDependsOn && edge.FromNodeID == node.ID {
				prerequisiteID = edge.ToNodeID
			}
			if edge.EdgeType == EdgeTypeBlocks && edge.ToNodeID == node.ID {
				prerequisiteID = edge.FromNodeID
			}
			if prerequisiteID == "" {
				continue
			}
			prerequisite, ok := nodes[prerequisiteID]
			if !ok || prerequisite.NodeType != NodeTypeExplorationObjective || prerequisite.Disposition != DispositionMain || prerequisite.PropertyMap["status"] != "resolved" {
				ready = false
				break
			}
		}
		if ready {
			candidates = append(candidates, node)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].StableKey != candidates[j].StableKey {
			return candidates[i].StableKey < candidates[j].StableKey
		}
		return candidates[i].ID < candidates[j].ID
	})
	ids := make([]string, len(candidates))
	for i := range candidates {
		ids[i] = candidates[i].ID
	}
	return ids
}

// CanonicalMainGraphPin is the immutable metadata a Continuation records for
// its exact Blackboard Snapshot.
type CanonicalMainGraphPin struct {
	ProjectID        string `json:"project_id"`
	GraphRevision    int    `json:"blackboard_graph_revision"`
	RendererVersion  string `json:"blackboard_renderer_version"`
	EstimatorVersion string `json:"blackboard_estimator_version"`
	ProjectionHash   string `json:"blackboard_projection_hash"`
	ProjectionBytes  int    `json:"blackboard_projection_bytes"`
	EstimatedTokens  int    `json:"blackboard_projection_estimated_tokens"`
}

// ImmutablePin copies projection metadata into a value suitable for durable
// Continuation storage. The projection bytes themselves remain reconstructible
// from the graph ledger and revision.
func (p CanonicalMainGraphProjection) ImmutablePin() CanonicalMainGraphPin {
	return CanonicalMainGraphPin{
		ProjectID: p.ProjectID, GraphRevision: p.GraphRevision,
		RendererVersion: p.RendererVersion, EstimatorVersion: p.EstimatorVersion,
		ProjectionHash: p.Hash, ProjectionBytes: p.ByteCount, EstimatedTokens: p.EstimatedTokens,
	}
}

// MaterializeCanonicalMainGraphSnapshot regenerates a missing task-local
// blackboard.json from the pinned historical revision, verifies every pinned
// measurement, and atomically installs an owner-only file.
func (s *GraphService) MaterializeCanonicalMainGraphSnapshot(ctx context.Context, pin CanonicalMainGraphPin, path string) error {
	if err := validateCanonicalMainGraphPinVersions(pin); err != nil {
		return err
	}
	projection, err := s.CanonicalMainGraph(ctx, pin.ProjectID, pin.GraphRevision)
	if err != nil {
		return err
	}
	if err := verifyProjectionMatchesPin(pin, projection.Bytes); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".blackboard-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary snapshot: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temporary snapshot: %w", err)
	}
	if _, err := tmp.Write(projection.Bytes); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install canonical snapshot: %w", err)
	}
	return VerifyCanonicalMainGraphSnapshot(pin, path)
}

// VerifyCanonicalMainGraphSnapshot fails closed when task-local bytes differ
// from the immutable Continuation pin.
func VerifyCanonicalMainGraphSnapshot(pin CanonicalMainGraphPin, path string) error {
	if err := validateCanonicalMainGraphPinVersions(pin); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read canonical snapshot: %w", err)
	}
	return verifyProjectionMatchesPin(pin, data)
}

func validateCanonicalMainGraphPinVersions(pin CanonicalMainGraphPin) error {
	if pin.RendererVersion != CanonicalMainGraphRendererV1 || pin.EstimatorVersion != UTF8BytesDiv4EstimatorV1 {
		return fmt.Errorf("unsupported canonical main graph pin versions %q/%q", pin.RendererVersion, pin.EstimatorVersion)
	}
	return nil
}

func verifyProjectionMatchesPin(pin CanonicalMainGraphPin, data []byte) error {
	if len(data) != pin.ProjectionBytes {
		return fmt.Errorf("canonical snapshot byte count mismatch: got %d want %d", len(data), pin.ProjectionBytes)
	}
	if estimate := (len(data) + 3) / 4; estimate != pin.EstimatedTokens {
		return fmt.Errorf("canonical snapshot token estimate mismatch: got %d want %d", estimate, pin.EstimatedTokens)
	}
	hash := hex.EncodeToString(framedHash("CyberPenda.Blackboard.MainProjection.v1", data))
	if hash != pin.ProjectionHash {
		return fmt.Errorf("canonical snapshot hash mismatch: got %s want %s", hash, pin.ProjectionHash)
	}
	return nil
}

// RemeasureCanonicalMainGraph renders the current graph after a semantic write
// and refreshes only the rebuildable projection cache. A rendering failure
// leaves the already-committed ledger and its dirty/unknown state untouched.
func (s *GraphService) RemeasureCanonicalMainGraph(ctx context.Context, projectID string) (CanonicalMainGraphProjection, error) {
	return s.remeasureCanonicalMainGraphAt(ctx, projectID, s.clock.Now().UTC().Format(time.RFC3339Nano))
}

func (s *GraphService) remeasureCanonicalMainGraphAt(ctx context.Context, projectID, measuredAt string) (CanonicalMainGraphProjection, error) {
	var revision, dirtyRevision int
	if err := s.db.QueryRowContext(ctx, `SELECT current_graph_revision,projection_dirty_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&revision, &dirtyRevision); err != nil {
		return CanonicalMainGraphProjection{}, graphStorageError("read graph revision for projection sizing", err)
	}
	projection, err := s.CanonicalMainGraph(ctx, projectID, revision)
	if err != nil {
		return CanonicalMainGraphProjection{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE blackboard_graph_state
		SET current_main_projection_hash=?, projection_renderer_version=?, projection_estimator_version=?,
		    projection_bytes=?, projection_estimated_tokens=?, budget_state=?, projection_dirty_revision=0, updated_at=?
		WHERE project_id=? AND current_graph_revision=? AND projection_dirty_revision=?`,
		projection.Hash, projection.RendererVersion, projection.EstimatorVersion,
		projection.ByteCount, projection.EstimatedTokens, budgetStateForEstimatedTokens(projection.EstimatedTokens),
		measuredAt, projectID, revision, dirtyRevision)
	if err != nil {
		return CanonicalMainGraphProjection{}, graphStorageError("store canonical main graph measurement", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return CanonicalMainGraphProjection{}, graphStorageError("check canonical main graph measurement", err)
	}
	if updated != 1 {
		return CanonicalMainGraphProjection{}, fmt.Errorf("canonical main graph changed during projection sizing")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO blackboard_projection_metrics(project_id,graph_revision,projection_hash,renderer_version,estimator_version,projection_bytes,estimated_tokens,budget_state,measured_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,graph_revision) DO UPDATE SET projection_hash=excluded.projection_hash,renderer_version=excluded.renderer_version,estimator_version=excluded.estimator_version,projection_bytes=excluded.projection_bytes,estimated_tokens=excluded.estimated_tokens,budget_state=excluded.budget_state,measured_at=excluded.measured_at`,
		projectID, revision, projection.Hash, projection.RendererVersion, projection.EstimatorVersion, projection.ByteCount, projection.EstimatedTokens, budgetStateForEstimatedTokens(projection.EstimatedTokens), measuredAt)
	if err != nil {
		return CanonicalMainGraphProjection{}, graphStorageError("store projection metric", err)
	}
	return projection, nil
}
