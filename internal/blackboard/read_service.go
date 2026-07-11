package blackboard

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"pentest/internal/store"
)

const BlackboardReadProtocolVersion = 1

const (
	ReadKindRecordCollectionV1 ReadKind = "record_collection_v1"
	ReadKindRecordResolveV1    ReadKind = "record_resolve_v1"
	ReadKindRecordHistoryV1    ReadKind = "record_history_v1"
	ReadKindCanonicalGraphV1   ReadKind = "canonical_main_graph_v1"
)

const (
	RecordSortAttention   = "attention"
	RecordSortUpdatedDesc = "updated_desc"
	RecordSortCreatedAsc  = "created_asc"
	RecordSortStableKey   = "stable_key"
	RecordSortSeverity    = "severity"
)

const recordSortRelevance = "relevance"

const readCursorDomain = "CyberPenda.Blackboard.ReadCursor.v1"

type ReadKind string

type ReadRequest struct {
	ProtocolVersion  int
	ProjectID        string
	Kind             ReadKind
	AtRevision       *int
	RecordCollection *RecordCollectionRequest
	RecordResolve    *RecordResolveRequest
	RecordHistory    *RecordHistoryRequest
}

type ReadEnvelope struct {
	ProtocolVersion       int            `json:"protocol_version"`
	Projection            string         `json:"projection"`
	ProjectID             string         `json:"project_id"`
	ProjectKind           string         `json:"project_kind"`
	ObservedGraphRevision int            `json:"observed_graph_revision"`
	ObservedStateHash     string         `json:"observed_state_hash"`
	SourcePins            map[string]any `json:"source_pins"`
	ProjectionHash        string         `json:"projection_hash"`
	Result                any            `json:"result"`
}

type RecordCollectionRequest struct {
	NodeTypes    []NodeType
	Dispositions []Disposition
	Lifecycle    []string
	Query        string
	Sort         string
	Limit        int
	Cursor       string
}

type RecordResolveRequest struct {
	NodeType  NodeType
	StableKey string
	NodeID    string
}

type RecordResolveV1 struct {
	Requested            NodeRefV1 `json:"requested"`
	Resolved             NodeRefV1 `json:"resolved"`
	ResolvedFromAlias    *string   `json:"resolved_from_alias"`
	ResolvedFromMergedID *string   `json:"resolved_from_merged_id"`
}

type RecordHistoryRequest struct {
	NodeID        string
	Literal       bool
	BeforeVersion int
	Limit         int
	Cursor        string
}

type NodeRefV1 struct {
	ID        string   `json:"id"`
	NodeType  NodeType `json:"node_type"`
	StableKey string   `json:"stable_key"`
	Label     string   `json:"label"`
}

type LifecycleV1 struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type RelationshipCountsV1 struct {
	AboutEntities  int `json:"about_entities"`
	Incoming       int `json:"incoming"`
	Outgoing       int `json:"outgoing"`
	Evidence       int `json:"evidence"`
	Contradictions int `json:"contradictions"`
}

type ProvenanceSummaryV1 struct {
	ActorType        ActorType `json:"actor_type"`
	ActorID          string    `json:"actor_id"`
	TaskID           *string   `json:"task_id"`
	ContinuationID   *string   `json:"continuation_id"`
	RuntimeProfileID *string   `json:"runtime_profile_id"`
	Runner           *string   `json:"runner"`
	SourceEventCount int       `json:"source_event_count"`
	MigrationSource  any       `json:"migration_source"`
	RecordedAt       string    `json:"recorded_at"`
}

type NodeRowV1 struct {
	Ref                NodeRefV1            `json:"ref"`
	Version            int                  `json:"version"`
	Disposition        Disposition          `json:"disposition"`
	Lifecycle          *LifecycleV1         `json:"lifecycle"`
	ScopeStatus        *string              `json:"scope_status"`
	Severity           *string              `json:"severity"`
	Secondary          *string              `json:"secondary"`
	UpdatedAt          string               `json:"updated_at"`
	AboutEntities      []NodeRefV1          `json:"about_entities"`
	RelationshipCounts RelationshipCountsV1 `json:"relationship_counts"`
	UpdatedProvenance  ProvenanceSummaryV1  `json:"updated_provenance"`
	searchRank         int
}

type PageV1 struct {
	Limit      int    `json:"limit"`
	TotalItems int    `json:"total_items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type RecordCollectionV1 struct {
	Items  []NodeRowV1    `json:"items"`
	Facets map[string]any `json:"facets"`
	Page   PageV1         `json:"page"`
}

type RecordHistoryV1 struct {
	Record     NodeRefV1            `json:"record"`
	Versions   []NodeHistoryVersion `json:"versions"`
	Page       PageV1               `json:"page"`
	KeyHistory []GraphKey           `json:"key_history"`
	Merge      *NodeRefV1           `json:"merge"`
}

type NodeHistoryVersion struct {
	Version      int            `json:"version"`
	Disposition  Disposition    `json:"disposition"`
	Properties   map[string]any `json:"properties"`
	UpdatedAt    string         `json:"updated_at"`
	SemanticHash string         `json:"semantic_hash"`
}

type BlackboardReadService struct {
	db           *store.DB
	cursorKey    []byte
	cursorKeyErr error
}

func NewBlackboardReadService(db *store.DB) *BlackboardReadService {
	service := &BlackboardReadService{db: db}
	service.cursorKeyErr = db.QueryRow(`SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&service.cursorKey)
	if service.cursorKeyErr == nil && len(service.cursorKey) != 32 {
		service.cursorKeyErr = fmt.Errorf("Blackboard read cursor secret has invalid length %d", len(service.cursorKey))
	}
	return service
}

type readCursor struct {
	Version    int      `json:"version"`
	Projection ReadKind `json:"projection"`
	ProjectID  string   `json:"project_id"`
	Revision   int      `json:"revision"`
	QueryHash  string   `json:"query_hash"`
	Sort       string   `json:"sort"`
	Limit      int      `json:"limit"`
	Last       []string `json:"last"`
}

func (s *BlackboardReadService) Read(ctx context.Context, request ReadRequest) (ReadEnvelope, error) {
	if s.cursorKeyErr != nil {
		return ReadEnvelope{}, readValidationError(ErrCodeSnapshotUnavailable, "Blackboard read cursor state is unavailable", "cursor")
	}
	if request.ProtocolVersion != BlackboardReadProtocolVersion {
		return ReadEnvelope{}, validationError(ErrCodeUnsupportedSchemaVersion, "unsupported Blackboard read protocol version", -1, "", "protocol_version")
	}
	if request.ProjectID == "" {
		return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "project_id is required", "project_id")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReadEnvelope{}, graphStorageError("begin Blackboard read", err)
	}
	defer func() { _ = tx.Rollback() }()

	var projectKind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id=?`, request.ProjectID).Scan(&projectKind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReadEnvelope{}, validationError(ErrCodeProjectNotFound, "project does not exist", -1, "", "project_id")
		}
		return ReadEnvelope{}, fmt.Errorf("read project kind: %w", err)
	}

	revision, cursor, err := resolveReadRevision(ctx, tx, request, s.cursorKey)
	if err != nil {
		return ReadEnvelope{}, err
	}
	if err := verifyMutationChain(ctx, tx, request.ProjectID); err != nil {
		return ReadEnvelope{}, fmt.Errorf("verify graph ledger before read: %w", err)
	}
	snapshot, err := reconstructGraph(ctx, tx, request.ProjectID, revision)
	if err != nil {
		var validation *ValidationError
		if errors.As(err, &validation) && validation.Code == ErrCodeInvalidRequest {
			return ReadEnvelope{}, readValidationError(ErrCodeRevisionNotFound, validation.Message, "at_revision")
		}
		return ReadEnvelope{}, readValidationError(ErrCodeSnapshotUnavailable, err.Error(), "at_revision")
	}

	var result any
	switch request.Kind {
	case ReadKindRecordCollectionV1:
		if request.RecordCollection == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "record_collection request is required", "record_collection")
		}
		result, err = buildRecordCollection(ctx, tx, snapshot, *request.RecordCollection, cursor, s.cursorKey)
	case ReadKindRecordResolveV1:
		if request.RecordResolve == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "record_resolve request is required", "record_resolve")
		}
		result, err = buildRecordResolve(snapshot, *request.RecordResolve)
	case ReadKindRecordHistoryV1:
		if request.RecordHistory == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "record_history request is required", "record_history")
		}
		result, err = buildRecordHistory(ctx, tx, snapshot, *request.RecordHistory, cursor, s.cursorKey)
	case ReadKindCanonicalGraphV1:
		result, err = canonicalMainGraphDocumentAt(ctx, tx, request.ProjectID, revision)
	default:
		err = readValidationError(ErrCodeInvalidQuery, "unknown read projection", "kind")
	}
	if err != nil {
		return ReadEnvelope{}, err
	}

	envelope := ReadEnvelope{
		ProtocolVersion:       BlackboardReadProtocolVersion,
		Projection:            string(request.Kind),
		ProjectID:             request.ProjectID,
		ProjectKind:           projectKind,
		ObservedGraphRevision: snapshot.GraphRevision,
		ObservedStateHash:     snapshot.StateHash,
		SourcePins:            map[string]any{},
		Result:                result,
	}
	envelope.ProjectionHash, err = hashReadEnvelope(envelope)
	if err != nil {
		return ReadEnvelope{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReadEnvelope{}, graphStorageError("commit Blackboard read", err)
	}
	return envelope, nil
}

func resolveReadRevision(ctx context.Context, tx *sql.Tx, request ReadRequest, cursorKey []byte) (int, *readCursor, error) {
	var latest int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(result_graph_revision),0) FROM blackboard_graph_mutations WHERE project_id=?`, request.ProjectID).Scan(&latest); err != nil {
		return 0, nil, fmt.Errorf("read current graph revision: %w", err)
	}
	if request.RecordCollection != nil && request.RecordCollection.Cursor != "" {
		cursor, err := decodeReadCursor(request.RecordCollection.Cursor, cursorKey)
		if err != nil {
			return 0, nil, err
		}
		if cursor.ProjectID != request.ProjectID || cursor.Projection != request.Kind {
			return 0, nil, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
		}
		return cursor.Revision, &cursor, nil
	}
	if request.RecordHistory != nil && request.RecordHistory.Cursor != "" {
		cursor, err := decodeReadCursor(request.RecordHistory.Cursor, cursorKey)
		if err != nil {
			return 0, nil, err
		}
		if cursor.ProjectID != request.ProjectID || cursor.Projection != request.Kind {
			return 0, nil, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
		}
		return cursor.Revision, &cursor, nil
	}
	if request.AtRevision != nil {
		if *request.AtRevision < 0 || *request.AtRevision > latest {
			return 0, nil, readValidationError(ErrCodeRevisionNotFound, "requested graph revision does not exist", "at_revision")
		}
		return *request.AtRevision, nil, nil
	}
	return latest, nil, nil
}

func buildRecordCollection(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request RecordCollectionRequest, cursor *readCursor, cursorKey []byte) (RecordCollectionV1, error) {
	normalized, err := normalizeRecordCollectionRequest(request)
	if err != nil {
		return RecordCollectionV1{}, err
	}
	queryHash, err := normalizedRecordQueryHash(normalized)
	if err != nil {
		return RecordCollectionV1{}, err
	}
	if cursor != nil && (cursor.QueryHash != queryHash || cursor.Sort != normalized.Sort || cursor.Limit != normalized.Limit) {
		return RecordCollectionV1{}, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
	}

	rows := make([]NodeRowV1, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if !recordMatches(node, normalized) {
			continue
		}
		row, err := nodeRowAt(ctx, tx, snapshot, node)
		if err != nil {
			return RecordCollectionV1{}, err
		}
		row.searchRank = lexicalSearchRank(node, normalized.Query)
		rows = append(rows, row)
	}
	sortRecordRows(rows, normalized.Sort)
	total := len(rows)
	start := 0
	if cursor != nil {
		start = sort.Search(len(rows), func(i int) bool { return compareRowTuple(rowTuple(rows[i], normalized.Sort), cursor.Last) > 0 })
	}
	end := start + normalized.Limit
	if end > len(rows) {
		end = len(rows)
	}
	pageRows := append([]NodeRowV1(nil), rows[start:end]...)
	next := ""
	if end < len(rows) && len(pageRows) > 0 {
		next, err = encodeReadCursor(readCursor{
			Version: BlackboardReadProtocolVersion, Projection: ReadKindRecordCollectionV1,
			ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash,
			Sort: normalized.Sort, Limit: normalized.Limit, Last: rowTuple(pageRows[len(pageRows)-1], normalized.Sort),
		}, cursorKey)
		if err != nil {
			return RecordCollectionV1{}, err
		}
	}
	return RecordCollectionV1{Items: pageRows, Facets: recordFacets(rows), Page: PageV1{Limit: normalized.Limit, TotalItems: total, NextCursor: next}}, nil
}

func normalizeRecordCollectionRequest(request RecordCollectionRequest) (RecordCollectionRequest, error) {
	if request.Limit == 0 {
		request.Limit = 50
	}
	if request.Limit < 1 || request.Limit > 200 {
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 200", "limit")
	}
	if request.Sort == "" {
		if normalizeSearchText(request.Query) != "" {
			request.Sort = recordSortRelevance
		} else {
			request.Sort = RecordSortUpdatedDesc
		}
	}
	switch request.Sort {
	case RecordSortAttention, RecordSortUpdatedDesc, RecordSortCreatedAsc, RecordSortStableKey, RecordSortSeverity, recordSortRelevance:
	default:
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "unsupported sort", "sort")
	}
	if len([]rune(request.Query)) > 500 {
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "query exceeds 500 Unicode scalar values", "query")
	}
	if len(request.NodeTypes) > 50 || len(request.Dispositions) > 50 || len(request.Lifecycle) > 50 {
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "repeated filter exceeds 50 values", "filters")
	}
	request.Query = normalizeSearchText(request.Query)
	request.NodeTypes = sortedUniqueNodeTypes(request.NodeTypes)
	for _, nodeType := range request.NodeTypes {
		if nodeTypeOrdinal(nodeType) < 0 {
			return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "unknown node_type filter", "node_type")
		}
	}
	request.Dispositions = sortedUniqueDispositions(request.Dispositions)
	for _, disposition := range request.Dispositions {
		if disposition != DispositionMain && disposition != DispositionArchived && disposition != DispositionMerged {
			return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "unknown disposition filter", "disposition")
		}
	}
	request.Lifecycle = sortedUniqueStrings(request.Lifecycle)
	if len(request.Dispositions) == 0 {
		request.Dispositions = []Disposition{DispositionMain}
	}
	return request, nil
}

func recordMatches(node NodeRecord, request RecordCollectionRequest) bool {
	if len(request.NodeTypes) > 0 && !containsNodeType(request.NodeTypes, node.NodeType) {
		return false
	}
	if !containsDisposition(request.Dispositions, node.Disposition) {
		return false
	}
	_, lifecycle := lifecycleForNode(node)
	if len(request.Lifecycle) > 0 && !containsString(request.Lifecycle, lifecycle) {
		return false
	}
	if request.Query != "" && !matchesLexicalQuery(node, request.Query) {
		return false
	}
	return true
}

func nodeRowAt(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, node NodeRecord) (NodeRowV1, error) {
	field, value := lifecycleForNode(node)
	var lifecycle *LifecycleV1
	if field != "" {
		lifecycle = &LifecycleV1{Field: field, Value: value}
	}
	var scopeStatus, severity, secondary *string
	if value, ok := stringProperty(node.PropertyMap, "scope_status"); ok {
		scopeStatus = &value
	}
	if value, ok := stringProperty(node.PropertyMap, "severity"); ok {
		severity = &value
	}
	if value := secondaryForNode(node); value != "" {
		secondary = &value
	}
	about, counts := relationshipsForNode(snapshot, node.ID)
	provenance, err := provenanceSummaryForNodeVersion(ctx, tx, snapshot.ProjectID, node.ID, node.Version)
	if err != nil {
		return NodeRowV1{}, err
	}
	return NodeRowV1{
		Ref: nodeRefForNode(node), Version: node.Version, Disposition: node.Disposition,
		Lifecycle: lifecycle, ScopeStatus: scopeStatus, Severity: severity, Secondary: secondary,
		UpdatedAt: node.UpdatedAt, AboutEntities: about, RelationshipCounts: counts, UpdatedProvenance: provenance,
	}, nil
}

func provenanceSummaryForNodeVersion(ctx context.Context, tx *sql.Tx, projectID, nodeID string, version int) (ProvenanceSummaryV1, error) {
	var p ProvenanceSummaryV1
	var actorType string
	var taskID, continuationID, profileID, runner, migration sql.NullString
	var provenanceID string
	err := tx.QueryRowContext(ctx, `SELECT p.id,p.actor_type,p.actor_id,p.task_id,p.continuation_id,p.runtime_profile_id,p.runner,p.migration_source_json,p.recorded_at
		FROM blackboard_node_versions v
		JOIN blackboard_graph_operations o ON o.project_id=v.project_id AND o.mutation_seq=v.mutation_seq AND o.operation_index=v.operation_index
		JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id
		WHERE v.project_id=? AND v.node_id=? AND v.version=?`, projectID, nodeID, version).
		Scan(&provenanceID, &actorType, &p.ActorID, &taskID, &continuationID, &profileID, &runner, &migration, &p.RecordedAt)
	if err != nil {
		return ProvenanceSummaryV1{}, fmt.Errorf("read node provenance summary: %w", err)
	}
	p.ActorType = ActorType(actorType)
	p.TaskID, p.ContinuationID, p.RuntimeProfileID, p.Runner = nullStringPointer(taskID), nullStringPointer(continuationID), nullStringPointer(profileID), nullStringPointer(runner)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_graph_provenance_events WHERE project_id=? AND provenance_id=?`, projectID, provenanceID).Scan(&p.SourceEventCount); err != nil {
		return ProvenanceSummaryV1{}, fmt.Errorf("count provenance events: %w", err)
	}
	if migration.Valid {
		if err := json.Unmarshal([]byte(migration.String), &p.MigrationSource); err != nil {
			return ProvenanceSummaryV1{}, fmt.Errorf("decode migration source: %w", err)
		}
	}
	return p, nil
}

func relationshipsForNode(snapshot GraphSnapshot, nodeID string) ([]NodeRefV1, RelationshipCountsV1) {
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	about := []NodeRefV1{}
	seenAbout := map[string]bool{}
	var counts RelationshipCountsV1
	for _, edge := range snapshot.Edges {
		if edge.State != "active" {
			continue
		}
		if edge.ToNodeID == nodeID {
			counts.Incoming++
		}
		if edge.FromNodeID == nodeID {
			counts.Outgoing++
		}
		if edge.EdgeType == EdgeTypeAbout && edge.FromNodeID == nodeID {
			counts.AboutEntities++
			if entity, ok := byID[edge.ToNodeID]; ok && len(about) < 3 && !seenAbout[entity.ID] {
				about = append(about, nodeRefForNode(entity))
				seenAbout[entity.ID] = true
			}
		}
		if edge.EdgeType == EdgeTypeEvidences && edge.ToNodeID == nodeID {
			counts.Evidence++
		}
		if edge.EdgeType == EdgeTypeContradicts && (edge.FromNodeID == nodeID || edge.ToNodeID == nodeID) {
			counts.Contradictions++
		}
	}
	sort.Slice(about, func(i, j int) bool {
		return compareStrings(about[i].StableKey, about[j].StableKey, about[i].ID, about[j].ID) < 0
	})
	return about, counts
}

func buildRecordResolve(snapshot GraphSnapshot, request RecordResolveRequest) (RecordResolveV1, error) {
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	requested := NodeRefV1{ID: request.NodeID, NodeType: request.NodeType, StableKey: request.StableKey}
	var canonicalID string
	var alias *string
	var merged *string
	if request.NodeID != "" {
		node, ok := byID[request.NodeID]
		if !ok {
			return RecordResolveV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "node_id")
		}
		canonicalID = node.ID
		for node.MergeTargetID != "" {
			from := canonicalID
			merged = &from
			canonicalID = node.MergeTargetID
			var exists bool
			node, exists = byID[canonicalID]
			if !exists {
				return RecordResolveV1{}, readValidationError(ErrCodeSnapshotUnavailable, "merged target cannot be reconstructed", "node_id")
			}
		}
	} else {
		if request.NodeType == "" || request.StableKey == "" {
			return RecordResolveV1{}, readValidationError(ErrCodeInvalidQuery, "node_type and stable_key are required", "stable_key")
		}
		for _, key := range snapshot.Keys {
			if key.NodeType == request.NodeType && key.Key == request.StableKey {
				canonicalID = key.CanonicalNodeID
				if key.Role == "alias" {
					value := request.StableKey
					alias = &value
				}
				break
			}
		}
	}
	resolved, ok := byID[canonicalID]
	if !ok {
		return RecordResolveV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "stable_key")
	}
	return RecordResolveV1{Requested: requested, Resolved: nodeRefForNode(resolved), ResolvedFromAlias: alias, ResolvedFromMergedID: merged}, nil
}

func buildRecordHistory(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request RecordHistoryRequest, cursor *readCursor, cursorKey []byte) (RecordHistoryV1, error) {
	if request.NodeID == "" {
		return RecordHistoryV1{}, readValidationError(ErrCodeInvalidQuery, "node_id is required", "node_id")
	}
	limit := request.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return RecordHistoryV1{}, readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 200", "limit")
	}
	queryIdentity, err := canonicalJSON(struct {
		NodeID        string `json:"node_id"`
		Literal       bool   `json:"literal"`
		BeforeVersion int    `json:"before_version"`
	}{request.NodeID, request.Literal, request.BeforeVersion})
	if err != nil {
		return RecordHistoryV1{}, fmt.Errorf("encode history query: %w", err)
	}
	queryHash := hex.EncodeToString(framedHash("CyberPenda.Blackboard.ReadHistoryQuery.v1", queryIdentity))
	pageBefore := request.BeforeVersion
	if cursor != nil {
		if cursor.QueryHash != queryHash || cursor.Sort != "version_desc" || cursor.Limit != limit || len(cursor.Last) != 1 {
			return RecordHistoryV1{}, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
		}
		if _, err := fmt.Sscan(cursor.Last[0], &pageBefore); err != nil || pageBefore < 1 {
			return RecordHistoryV1{}, readValidationError(ErrCodeInvalidCursor, "The cursor is malformed.", "cursor")
		}
	}

	var current *NodeRecord
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].ID == request.NodeID {
			current = &snapshot.Nodes[i]
			break
		}
	}
	if current == nil {
		return RecordHistoryV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "node_id")
	}
	if current.MergeTargetID != "" && !request.Literal {
		return RecordHistoryV1{}, readValidationError(ErrCodeLiteralIdentityRequired, "literal=true is required for merged source history", "literal")
	}

	countQuery := `SELECT COUNT(*) FROM blackboard_node_versions WHERE project_id=? AND node_id=? AND result_graph_revision<=?`
	countArgs := []any{snapshot.ProjectID, request.NodeID, snapshot.GraphRevision}
	if request.BeforeVersion > 0 {
		countQuery += ` AND version<?`
		countArgs = append(countArgs, request.BeforeVersion)
	}
	var total int
	if err := tx.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return RecordHistoryV1{}, fmt.Errorf("count node history: %w", err)
	}

	query := `SELECT version,disposition,properties_json,updated_at,semantic_hash FROM blackboard_node_versions WHERE project_id=? AND node_id=? AND result_graph_revision<=?`
	args := []any{snapshot.ProjectID, request.NodeID, snapshot.GraphRevision}
	if pageBefore > 0 {
		query += ` AND version<?`
		args = append(args, pageBefore)
	}
	query += ` ORDER BY version DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return RecordHistoryV1{}, fmt.Errorf("read node history: %w", err)
	}
	defer rows.Close()
	versions := []NodeHistoryVersion{}
	for rows.Next() {
		var version NodeHistoryVersion
		var properties string
		if err := rows.Scan(&version.Version, &version.Disposition, &properties, &version.UpdatedAt, &version.SemanticHash); err != nil {
			return RecordHistoryV1{}, fmt.Errorf("scan node history: %w", err)
		}
		if err := json.Unmarshal([]byte(properties), &version.Properties); err != nil {
			return RecordHistoryV1{}, fmt.Errorf("decode node history: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return RecordHistoryV1{}, fmt.Errorf("iterate node history: %w", err)
	}
	next := ""
	if len(versions) > limit {
		versions = versions[:limit]
		next, err = encodeReadCursor(readCursor{Version: 1, Projection: ReadKindRecordHistoryV1, ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash, Sort: "version_desc", Limit: limit, Last: []string{fmt.Sprint(versions[len(versions)-1].Version)}}, cursorKey)
		if err != nil {
			return RecordHistoryV1{}, err
		}
	}
	keys := []GraphKey{}
	for _, key := range snapshot.Keys {
		if key.SourceNodeID == request.NodeID || key.CanonicalNodeID == request.NodeID {
			keys = append(keys, key)
		}
	}
	var merge *NodeRefV1
	if current.MergeTargetID != "" {
		for _, node := range snapshot.Nodes {
			if node.ID == current.MergeTargetID {
				ref := nodeRefForNode(node)
				merge = &ref
				break
			}
		}
	}
	return RecordHistoryV1{Record: nodeRefForNode(*current), Versions: versions, Page: PageV1{Limit: limit, TotalItems: total, NextCursor: next}, KeyHistory: keys, Merge: merge}, nil
}

func normalizedRecordQueryHash(request RecordCollectionRequest) (string, error) {
	value := struct {
		NodeTypes    []NodeType    `json:"node_types"`
		Dispositions []Disposition `json:"dispositions"`
		Lifecycle    []string      `json:"lifecycle"`
		Query        string        `json:"query"`
	}{request.NodeTypes, request.Dispositions, request.Lifecycle, request.Query}
	data, err := canonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("encode normalized record query: %w", err)
	}
	hash := framedHash("CyberPenda.Blackboard.ReadQuery.v1", data)
	return hex.EncodeToString(hash), nil
}

func encodeReadCursor(cursor readCursor, cursorKey []byte) (string, error) {
	payload, err := canonicalJSON(cursor)
	if err != nil {
		return "", fmt.Errorf("encode read cursor: %w", err)
	}
	mac := hmac.New(sha256.New, cursorKey)
	_, _ = mac.Write([]byte(readCursorDomain))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func decodeReadCursor(encoded string, cursorKey []byte) (readCursor, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor is malformed.", "cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor is malformed.", "cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor is malformed.", "cursor")
	}
	mac := hmac.New(sha256.New, cursorKey)
	_, _ = mac.Write([]byte(readCursorDomain))
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor signature is invalid.", "cursor")
	}
	var cursor readCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != BlackboardReadProtocolVersion {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor version is unsupported.", "cursor")
	}
	return cursor, nil
}

func hashReadEnvelope(envelope ReadEnvelope) (string, error) {
	value := struct {
		ProtocolVersion       int            `json:"protocol_version"`
		Projection            string         `json:"projection"`
		ProjectID             string         `json:"project_id"`
		ProjectKind           string         `json:"project_kind"`
		ObservedGraphRevision int            `json:"observed_graph_revision"`
		ObservedStateHash     string         `json:"observed_state_hash"`
		SourcePins            map[string]any `json:"source_pins"`
		Result                any            `json:"result"`
	}{envelope.ProtocolVersion, envelope.Projection, envelope.ProjectID, envelope.ProjectKind, envelope.ObservedGraphRevision, envelope.ObservedStateHash, envelope.SourcePins, envelope.Result}
	data, err := canonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("encode read projection: %w", err)
	}
	return hex.EncodeToString(framedHash("CyberPenda.Blackboard.ReadProjection.v1", data)), nil
}

func normalizeSearchText(value string) string {
	value = norm.NFKC.String(value)
	value = strings.ToLower(value)
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}

func matchesLexicalQuery(node NodeRecord, query string) bool {
	return lexicalSearchRank(node, query) < 6
}

func lexicalSearchRank(node NodeRecord, query string) int {
	if query == "" {
		return 0
	}
	key := normalizeSearchText(node.StableKey)
	locator, _ := stringProperty(node.PropertyMap, "locator")
	locator = normalizeSearchText(locator)
	label := normalizeSearchText(labelForNode(node))
	if key == query || (locator != "" && locator == query) {
		return 0
	}
	if strings.HasPrefix(key, query) || (locator != "" && strings.HasPrefix(locator, query)) {
		return 1
	}
	if label == query {
		return 2
	}
	if strings.HasPrefix(label, query) {
		return 3
	}
	joined := normalizeSearchText(strings.Join(searchableFields(node), " "))
	tokens := strings.Fields(query)
	all := true
	any := false
	for _, token := range tokens {
		matched := strings.Contains(joined, token)
		all = all && matched
		any = any || matched
	}
	if all {
		return 4
	}
	if any {
		return 5
	}
	return 6
}

func searchableFields(node NodeRecord) []string {
	fields := []string{node.StableKey}
	for _, key := range []string{"name", "locator", "objective", "summary", "detail", "statement", "rationale", "category", "body", "title", "target", "description", "proof", "impact", "recommendation", "value", "media_type", "digest", "directive"} {
		if value, ok := stringProperty(node.PropertyMap, key); ok {
			fields = append(fields, value)
		}
	}
	if node.NodeType == NodeTypeEvidenceArtifact {
		if value, ok := stringProperty(node.PropertyMap, "managed_path"); ok {
			if index := strings.LastIndexAny(value, `/\\`); index >= 0 {
				value = value[index+1:]
			}
			fields = append(fields, value)
		}
	}
	return fields
}

func nodeRefForNode(node NodeRecord) NodeRefV1 {
	return NodeRefV1{ID: node.ID, NodeType: node.NodeType, StableKey: node.StableKey, Label: labelForNode(node)}
}

func labelForNode(node NodeRecord) string {
	keys := map[NodeType][]string{
		NodeTypeGoal: {"text"}, NodeTypeEntity: {"name"}, NodeTypeExplorationObjective: {"objective"},
		NodeTypeAttempt: {"summary"}, NodeTypeObservation: {"summary"}, NodeTypeHypothesis: {"statement"},
		NodeTypeProjectFact: {"summary"}, NodeTypeFinding: {"title"}, NodeTypeSolution: {"summary"},
		NodeTypeEvidenceArtifact: {"summary"}, NodeTypeProjectDirective: {"directive"},
	}
	for _, key := range keys[node.NodeType] {
		if value, ok := stringProperty(node.PropertyMap, key); ok && value != "" {
			if node.NodeType == NodeTypeEntity {
				if locator, ok := stringProperty(node.PropertyMap, "locator"); ok && locator != "" && locator != value {
					return value + " — " + locator
				}
			}
			return value
		}
	}
	return node.StableKey
}

func lifecycleForNode(node NodeRecord) (string, string) {
	var field string
	switch node.NodeType {
	case NodeTypeGoal:
		field = "task_status"
	case NodeTypeExplorationObjective, NodeTypeAttempt, NodeTypeHypothesis, NodeTypeFinding, NodeTypeSolution, NodeTypeEvidenceArtifact, NodeTypeProjectDirective:
		field = "status"
	case NodeTypeProjectFact:
		field = "confidence"
	default:
		if node.Disposition != DispositionMain {
			return "disposition", string(node.Disposition)
		}
	}
	value, _ := stringProperty(node.PropertyMap, field)
	return field, value
}

func secondaryForNode(node NodeRecord) string {
	for _, key := range []string{"locator", "target", "category", "media_type", "kind"} {
		if value, ok := stringProperty(node.PropertyMap, key); ok {
			return value
		}
	}
	return ""
}

func stringProperty(properties map[string]any, key string) (string, bool) {
	value, ok := properties[key].(string)
	return value, ok
}

func sortRecordRows(rows []NodeRowV1, mode string) {
	sort.SliceStable(rows, func(i, j int) bool { return compareRowTuple(rowTuple(rows[i], mode), rowTuple(rows[j], mode)) < 0 })
}

func rowTuple(row NodeRowV1, mode string) []string {
	switch mode {
	case recordSortRelevance:
		return []string{fmt.Sprintf("%02d", row.searchRank), string(row.Ref.NodeType), row.Ref.StableKey, row.Ref.ID}
	case RecordSortCreatedAsc:
		return []string{row.UpdatedAt, string(row.Ref.NodeType), row.Ref.StableKey, row.Ref.ID}
	case RecordSortUpdatedDesc, RecordSortAttention:
		return []string{invertLexical(row.UpdatedAt), string(row.Ref.NodeType), row.Ref.StableKey, row.Ref.ID}
	case RecordSortSeverity:
		severity := ""
		if row.Severity != nil {
			severity = severityRank(*row.Severity)
		}
		return []string{severity, invertLexical(row.UpdatedAt), string(row.Ref.NodeType), row.Ref.StableKey, row.Ref.ID}
	default:
		return []string{row.Ref.StableKey, string(row.Ref.NodeType), row.Ref.ID}
	}
}

func invertLexical(value string) string {
	out := make([]byte, len(value))
	for i := range value {
		out[i] = 127 - value[i]
	}
	return string(out)
}

func severityRank(value string) string {
	ranks := map[string]string{"critical": "0", "high": "1", "medium": "2", "low": "3", "info": "4", "none": "5"}
	if rank, ok := ranks[value]; ok {
		return rank
	}
	return "9"
}

func compareRowTuple(left, right []string) int {
	length := len(left)
	if len(right) < length {
		length = len(right)
	}
	for i := 0; i < length; i++ {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func compareStrings(values ...string) int {
	for i := 0; i+1 < len(values); i += 2 {
		if values[i] < values[i+1] {
			return -1
		}
		if values[i] > values[i+1] {
			return 1
		}
	}
	return 0
}

func recordFacets(rows []NodeRowV1) map[string]any {
	nodeTypes := map[string]int{}
	dispositions := map[string]int{}
	for _, row := range rows {
		nodeTypes[string(row.Ref.NodeType)]++
		dispositions[string(row.Disposition)]++
	}
	return map[string]any{"node_type": nodeTypes, "disposition": dispositions}
}

func sortedUniqueNodeTypes(values []NodeType) []NodeType {
	set := map[NodeType]bool{}
	for _, value := range values {
		set[value] = true
	}
	out := make([]NodeType, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedUniqueDispositions(values []Disposition) []Disposition {
	set := map[Disposition]bool{}
	for _, value := range values {
		set[value] = true
	}
	out := make([]Disposition, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedUniqueStrings(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsNodeType(values []NodeType, target NodeType) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func containsDisposition(values []Disposition, target Disposition) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

const (
	ErrCodeInvalidQuery            = "invalid_query"
	ErrCodeInvalidCursor           = "invalid_cursor"
	ErrCodeRevisionNotFound        = "revision_not_found"
	ErrCodeRecordNotFound          = "record_not_found"
	ErrCodeLiteralIdentityRequired = "literal_identity_required"
	ErrCodeProjectionTooLarge      = "projection_too_large"
	ErrCodeProjectKindMismatch     = "project_kind_mismatch"
	ErrCodeHealthRunNotFound       = "health_run_not_found"
	ErrCodeHealthRunInProgress     = "health_run_in_progress"
	ErrCodeSnapshotUnavailable     = "snapshot_unavailable"
)

func readValidationError(code, message, path string) *ValidationError {
	return validationError(code, message, -1, "", path)
}
