package blackboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// legacyCompatibilityLimit is the default and maximum page size for legacy
// list projections (read contract §18: default and maximum limit are 200 for
// compatibility).
const legacyCompatibilityLimit = 200

// LegacyFactIndexRequest projects main ProjectFact nodes into the legacy
// /facts/index shape. IncludeDeprecated is nil (default) for Current Truth
// only; a caller sets it true to additionally return deprecated main facts.
// Query applies the canonical lexical search over fact fields.
type LegacyFactIndexRequest struct {
	IncludeDeprecated *bool
	Query             string
	Limit             int
	Cursor            string
}

// LegacyFactIndexV1 is the legacy Fact index envelope with additive pagination.
type LegacyFactIndexV1 struct {
	Facts                  []FactIndexEntry `json:"facts"`
	NextCursor             string           `json:"next_cursor,omitempty"`
	CompatibilityTruncated bool             `json:"compatibility_truncated"`
}

// LegacyFactDetailRequest resolves a Fact by stable key, following alias and
// merge redirects to the canonical main node (read contract §18.1).
type LegacyFactDetailRequest struct {
	FactKey string
}

// LegacyFactDetailV1 mirrors the legacy Fact point read with additive Version
// and alias/merge resolution fields (read contract §18.1).
type LegacyFactDetailV1 struct {
	ID                   string  `json:"id"`
	ProjectID            string  `json:"project_id"`
	FactKey              string  `json:"fact_key"`
	Version              int     `json:"version"`
	Category             string  `json:"category"`
	Summary              string  `json:"summary"`
	Body                 string  `json:"body"`
	Confidence           string  `json:"confidence"`
	ScopeStatus          string  `json:"scope_status,omitempty"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
	ResolvedFromAlias    *string `json:"resolved_from_alias"`
	ResolvedFromMergedID *string `json:"resolved_from_merged_id"`
}

// LegacyFactVersionsRequest reconstructs the version history of a Fact.
// Versions are bounded by a single record and returned in full (ascending).
type LegacyFactVersionsRequest struct {
	FactKey string
}

// LegacyFactVersionRow is one Fact version in the legacy ascending order.
type LegacyFactVersionRow struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	FactKey     string `json:"fact_key"`
	Version     int    `json:"version"`
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Body        string `json:"body"`
	Confidence  string `json:"confidence"`
	ScopeStatus string `json:"scope_status,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// LegacyFactVersionsV1 is the legacy Fact versions envelope.
type LegacyFactVersionsV1 struct {
	Versions               []LegacyFactVersionRow `json:"versions"`
	NextCursor             string                 `json:"next_cursor,omitempty"`
	CompatibilityTruncated bool                   `json:"compatibility_truncated"`
}

// LegacyFactRelationsRequest lists Fact-to-Fact relations for a source Fact.
// Relations for one Fact are bounded and returned in full (created_at order).
type LegacyFactRelationsRequest struct {
	FactKey string
}

// LegacyFactRelationRow mirrors the legacy FactRelation shape (read contract §18.2).
type LegacyFactRelationRow struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	SourceFactKey string `json:"source_fact_key"`
	TargetFactKey string `json:"target_fact_key"`
	Relation      string `json:"relation"`
	Summary       string `json:"summary"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// LegacyFactRelationsV1 is the legacy Fact relations envelope.
type LegacyFactRelationsV1 struct {
	Relations              []LegacyFactRelationRow `json:"relations"`
	NextCursor             string                  `json:"next_cursor,omitempty"`
	CompatibilityTruncated bool                    `json:"compatibility_truncated"`
}

// LegacyFindingCollectionRequest lists main Findings in the legacy order.
type LegacyFindingCollectionRequest struct {
	Limit  int
	Cursor string
}

// LegacyFindingV1 mirrors the legacy Finding shape with derived Severity/CVSSPending.
type LegacyFindingV1 struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	FindingKey     string `json:"finding_key"`
	Version        int    `json:"version"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Target         string `json:"target"`
	Proof          string `json:"proof"`
	Impact         string `json:"impact"`
	Recommendation string `json:"recommendation"`
	CVSSVersion    string `json:"cvss_version"`
	CVSSVector     string `json:"cvss_vector"`
	CVSSPending    bool   `json:"cvss_pending"`
	Severity       string `json:"severity"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// LegacyFindingCollectionV1 is the legacy Findings envelope with additive pagination.
type LegacyFindingCollectionV1 struct {
	Findings               []LegacyFindingV1 `json:"findings"`
	NextCursor             string            `json:"next_cursor,omitempty"`
	CompatibilityTruncated bool              `json:"compatibility_truncated"`
}

// LegacyFindingVersionsRequest reconstructs the version history of a Finding.
// Versions are bounded by a single record and returned in full (ascending).
type LegacyFindingVersionsRequest struct {
	FindingKey string
}

// LegacyFindingVersionRow is one Finding version in legacy ascending order.
type LegacyFindingVersionRow struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	FindingKey     string `json:"finding_key"`
	Version        int    `json:"version"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Target         string `json:"target"`
	Proof          string `json:"proof"`
	Impact         string `json:"impact"`
	Recommendation string `json:"recommendation"`
	CVSSVersion    string `json:"cvss_version"`
	CVSSVector     string `json:"cvss_vector"`
	CVSSPending    bool   `json:"cvss_pending"`
	Severity       string `json:"severity"`
	CreatedAt      string `json:"created_at"`
}

// LegacyFindingVersionsV1 is the legacy Finding versions envelope.
type LegacyFindingVersionsV1 struct {
	Versions               []LegacyFindingVersionRow `json:"versions"`
	NextCursor             string                    `json:"next_cursor,omitempty"`
	CompatibilityTruncated bool                      `json:"compatibility_truncated"`
}

// LegacyEvidenceCollectionRequest lists main EvidenceArtifacts.
type LegacyEvidenceCollectionRequest struct {
	Limit  int
	Cursor string
}

// LegacyEvidenceArtifactV1 mirrors the legacy EvidenceArtifact shape plus the
// additive attachments list (read contract §18.4).
type LegacyEvidenceArtifactV1 struct {
	ID           string      `json:"id"`
	ProjectID    string      `json:"project_id"`
	EvidenceKey  string      `json:"evidence_key"`
	AttachToType string      `json:"attach_to_type"`
	AttachToKey  string      `json:"attach_to_key"`
	ArtifactType string      `json:"artifact_type"`
	SourcePath   string      `json:"source_path"`
	ManagedPath  string      `json:"managed_path"`
	SHA256       string      `json:"sha256"`
	Summary      string      `json:"summary"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
	Attachments  []NodeRefV1 `json:"attachments"`
}

// LegacyEvidenceCollectionV1 is the legacy Evidence envelope with additive pagination.
type LegacyEvidenceCollectionV1 struct {
	Evidence               []LegacyEvidenceArtifactV1 `json:"evidence"`
	NextCursor             string                     `json:"next_cursor,omitempty"`
	CompatibilityTruncated bool                       `json:"compatibility_truncated"`
}

// LegacyReportEnvelopeV1 is the legacy POST /report response shape that the
// graph-path report adapters wrap PentestReportV1 markdown into (§18.5).
type LegacyReportEnvelopeV1 struct {
	Status   string `json:"status"`
	Format   string `json:"format"`
	Markdown string `json:"markdown"`
}

// buildLegacyFactIndex projects main ProjectFact nodes into the legacy Fact index.
func buildLegacyFactIndex(snapshot GraphSnapshot, request LegacyFactIndexRequest, cursor *readCursor, cursorKey []byte) (LegacyFactIndexV1, error) {
	limit := legacyListLimit(request.Limit)
	if err := legacyValidateLimit(limit); err != nil {
		return LegacyFactIndexV1{}, err
	}
	includeDeprecated := request.IncludeDeprecated != nil && *request.IncludeDeprecated
	query := normalizeSearchText(strings.TrimSpace(request.Query))
	var entries []FactIndexEntry
	var tuples [][]string
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeProjectFact || node.Disposition != DispositionMain {
			continue
		}
		confidence := propertyString(node, "confidence")
		if !includeDeprecated && confidence == string(ConfidenceDeprecated) {
			continue
		}
		if query != "" && !matchesLexicalQuery(node, query) {
			continue
		}
		entry := FactIndexEntry{
			FactKey:     node.StableKey,
			Category:    propertyString(node, "category"),
			Summary:     propertyString(node, "summary"),
			Confidence:  Confidence(confidence),
			ScopeStatus: ScopeStatus(propertyString(node, "scope_status")),
		}
		entries = append(entries, entry)
		tuples = append(tuples, []string{legacyConfidenceRank(string(entry.Confidence)), legacyScopeRank(string(entry.ScopeStatus)), entry.Category, node.StableKey, node.ID})
	}
	sortLegacyTuples(entries, tuples)
	queryHash, err := projectionQueryHash("CyberPenda.Blackboard.LegacyFactIndexQuery.v1", struct {
		IncludeDeprecated bool
		Query             string
	}{includeDeprecated, query})
	if err != nil {
		return LegacyFactIndexV1{}, err
	}
	start, next, err := legacyPageWindow(cursor, queryHash, "legacy_fact_index", limit, len(entries), tuples, ReadKindLegacyFactIndexV1, snapshot, cursorKey)
	if err != nil {
		return LegacyFactIndexV1{}, err
	}
	end := start + limit
	if end > len(entries) {
		end = len(entries)
	}
	page := append([]FactIndexEntry(nil), entries[start:end]...)
	return LegacyFactIndexV1{Facts: page, NextCursor: next, CompatibilityTruncated: next != ""}, nil
}

// buildLegacyFactDetail resolves a Fact by stable key and projects the legacy
// point read shape, preserving alias/merge resolution metadata.
func buildLegacyFactDetail(snapshot GraphSnapshot, request LegacyFactDetailRequest) (LegacyFactDetailV1, error) {
	if strings.TrimSpace(request.FactKey) == "" {
		return LegacyFactDetailV1{}, readValidationError(ErrCodeInvalidQuery, "fact_key is required", "fact_key")
	}
	resolved, alias, merged, err := resolveLegacyNodeByKey(snapshot, NodeTypeProjectFact, request.FactKey)
	if err != nil {
		return LegacyFactDetailV1{}, err
	}
	return LegacyFactDetailV1{
		ID:                   resolved.ID,
		ProjectID:            resolved.ProjectID,
		FactKey:              resolved.StableKey,
		Version:              resolved.Version,
		Category:             propertyString(resolved, "category"),
		Summary:              propertyString(resolved, "summary"),
		Body:                 propertyString(resolved, "body"),
		Confidence:           propertyString(resolved, "confidence"),
		ScopeStatus:          propertyString(resolved, "scope_status"),
		CreatedAt:            resolved.CreatedAt,
		UpdatedAt:            resolved.UpdatedAt,
		ResolvedFromAlias:    alias,
		ResolvedFromMergedID: merged,
	}, nil
}

// buildLegacyFactVersions reconstructs Fact versions in ascending order.
func buildLegacyFactVersions(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request LegacyFactVersionsRequest) (LegacyFactVersionsV1, error) {
	resolved, _, _, err := resolveLegacyNodeByKey(snapshot, NodeTypeProjectFact, request.FactKey)
	if err != nil {
		return LegacyFactVersionsV1{}, err
	}
	rows, err := legacyNodeVersions(ctx, tx, snapshot, resolved.ID)
	if err != nil {
		return LegacyFactVersionsV1{}, err
	}
	versions := []LegacyFactVersionRow{}
	for _, v := range rows {
		versions = append(versions, LegacyFactVersionRow{
			ID:          resolved.ID,
			ProjectID:   resolved.ProjectID,
			FactKey:     resolved.StableKey,
			Version:     v.version,
			Category:    stringProp(v.props, "category"),
			Summary:     stringProp(v.props, "summary"),
			Body:        stringProp(v.props, "body"),
			Confidence:  stringProp(v.props, "confidence"),
			ScopeStatus: stringProp(v.props, "scope_status"),
			CreatedAt:   v.updatedAt,
		})
	}
	return LegacyFactVersionsV1{Versions: versions, CompatibilityTruncated: false}, nil
}

// buildLegacyFactRelations lists active Fact-to-Fact relations where the
// resolved Fact is the source (read contract §18.2).
func buildLegacyFactRelations(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request LegacyFactRelationsRequest) (LegacyFactRelationsV1, error) {
	resolved, _, _, err := resolveLegacyNodeByKey(snapshot, NodeTypeProjectFact, request.FactKey)
	if err != nil {
		return LegacyFactRelationsV1{}, err
	}
	byID := legacyNodeIndex(snapshot)
	timestamps, err := legacyEdgeTimestamps(ctx, tx, snapshot)
	if err != nil {
		return LegacyFactRelationsV1{}, err
	}
	relations := []LegacyFactRelationRow{}
	for _, edge := range snapshot.Edges {
		if edge.State != "active" || edge.FromNodeID != resolved.ID {
			continue
		}
		relation := string(edge.EdgeType)
		if relation != string(EdgeTypeSupports) && relation != string(EdgeTypeContradicts) && relation != string(EdgeTypeLeadsTo) {
			// depends_on is reserved for Exploration Objective prerequisites;
			// a migrated legacy fact-to-fact depends_on is audit-only and is
			// surfaced here only when the migration mapping preserved it (§18.2).
			if relation != string(EdgeTypeDependsOn) {
				continue
			}
		}
		target, ok := byID[edge.ToNodeID]
		if !ok || target.NodeType != NodeTypeProjectFact {
			continue
		}
		created, updated := timestamps[edge.ID], timestamps[edge.ID]
		relations = append(relations, LegacyFactRelationRow{
			ID:            edge.ID,
			ProjectID:     edge.ProjectID,
			SourceFactKey: resolved.StableKey,
			TargetFactKey: target.StableKey,
			Relation:      relation,
			Summary:       edge.Summary,
			CreatedAt:     created.created,
			UpdatedAt:     updated.updated,
		})
	}
	sort.SliceStable(relations, func(i, j int) bool {
		return compareRowTuple([]string{relations[i].CreatedAt, relations[i].ID}, []string{relations[j].CreatedAt, relations[j].ID}) < 0
	})
	return LegacyFactRelationsV1{Relations: relations, CompatibilityTruncated: false}, nil
}

// buildLegacyFindingCollection projects main Findings into the legacy list shape.
func buildLegacyFindingCollection(snapshot GraphSnapshot, request LegacyFindingCollectionRequest, cursor *readCursor, cursorKey []byte) (LegacyFindingCollectionV1, error) {
	limit := legacyListLimit(request.Limit)
	if err := legacyValidateLimit(limit); err != nil {
		return LegacyFindingCollectionV1{}, err
	}
	var findings []LegacyFindingV1
	var tuples [][]string
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeFinding || node.Disposition != DispositionMain {
			continue
		}
		finding := legacyFindingFromNode(node)
		findings = append(findings, finding)
		tuples = append(tuples, legacyFindingTuple(finding))
	}
	sortLegacyTuples(findings, tuples)
	queryHash, err := projectionQueryHash("CyberPenda.Blackboard.LegacyFindingQuery.v1", struct{}{})
	if err != nil {
		return LegacyFindingCollectionV1{}, err
	}
	start, next, err := legacyPageWindow(cursor, queryHash, "legacy_finding", limit, len(findings), tuples, ReadKindLegacyFindingCollectionV1, snapshot, cursorKey)
	if err != nil {
		return LegacyFindingCollectionV1{}, err
	}
	end := start + limit
	if end > len(findings) {
		end = len(findings)
	}
	page := append([]LegacyFindingV1(nil), findings[start:end]...)
	return LegacyFindingCollectionV1{Findings: page, NextCursor: next, CompatibilityTruncated: next != ""}, nil
}

// buildLegacyFindingVersions reconstructs Finding versions in ascending order.
func buildLegacyFindingVersions(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request LegacyFindingVersionsRequest) (LegacyFindingVersionsV1, error) {
	resolved, _, _, err := resolveLegacyNodeByKey(snapshot, NodeTypeFinding, request.FindingKey)
	if err != nil {
		return LegacyFindingVersionsV1{}, err
	}
	rows, err := legacyNodeVersions(ctx, tx, snapshot, resolved.ID)
	if err != nil {
		return LegacyFindingVersionsV1{}, err
	}
	versions := []LegacyFindingVersionRow{}
	for _, v := range rows {
		vector := stringProp(v.props, "cvss_vector")
		versions = append(versions, LegacyFindingVersionRow{
			ID:             resolved.ID,
			ProjectID:      resolved.ProjectID,
			FindingKey:     resolved.StableKey,
			Version:        v.version,
			Title:          stringProp(v.props, "title"),
			Description:    stringProp(v.props, "description"),
			Status:         stringProp(v.props, "status"),
			Target:         stringProp(v.props, "target"),
			Proof:          stringProp(v.props, "proof"),
			Impact:         stringProp(v.props, "impact"),
			Recommendation: stringProp(v.props, "recommendation"),
			CVSSVersion:    stringProp(v.props, "cvss_version"),
			CVSSVector:     vector,
			CVSSPending:    strings.TrimSpace(vector) == "",
			Severity:       deriveSeverity(vector),
			CreatedAt:      v.updatedAt,
		})
	}
	return LegacyFindingVersionsV1{Versions: versions, CompatibilityTruncated: false}, nil
}

// buildLegacyEvidenceCollection projects main EvidenceArtifacts into the legacy
// list shape, one row per artifact with deterministic singular attach target.
func buildLegacyEvidenceCollection(snapshot GraphSnapshot, request LegacyEvidenceCollectionRequest, cursor *readCursor, cursorKey []byte) (LegacyEvidenceCollectionV1, error) {
	limit := legacyListLimit(request.Limit)
	if err := legacyValidateLimit(limit); err != nil {
		return LegacyEvidenceCollectionV1{}, err
	}
	byID := legacyNodeIndex(snapshot)
	var artifacts []LegacyEvidenceArtifactV1
	var tuples [][]string
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeEvidenceArtifact || node.Disposition != DispositionMain {
			continue
		}
		attachments := []NodeRefV1{}
		var factTargets, findingTargets []NodeRecord
		for _, edge := range snapshot.Edges {
			if edge.State != "active" || edge.EdgeType != EdgeTypeEvidences || edge.FromNodeID != node.ID {
				continue
			}
			target, ok := byID[edge.ToNodeID]
			if !ok {
				continue
			}
			attachments = append(attachments, nodeRefForNode(target))
			if target.NodeType == NodeTypeProjectFact {
				factTargets = append(factTargets, target)
			} else if target.NodeType == NodeTypeFinding {
				findingTargets = append(findingTargets, target)
			}
		}
		sortNodeRefs(attachments)
		// §18.4 deterministic singular attach target: tier 1 is the migrated
		// legacy attach target (carried as migrated_attach_to_type/_key node
		// properties when present and still current); tier 2 falls back to the
		// first current ProjectFact or Finding by node-type ordinal, stable key,
		// and ID; tier 3 leaves the singular fields empty.
		attachType, attachKey := legacySingularAttachTarget(node, factTargets, findingTargets, byID)
		artifacts = append(artifacts, LegacyEvidenceArtifactV1{
			ID:           node.ID,
			ProjectID:    node.ProjectID,
			EvidenceKey:  node.StableKey,
			AttachToType: attachType,
			AttachToKey:  attachKey,
			ArtifactType: propertyString(node, "artifact_type"),
			SourcePath:   propertyString(node, "source_path"),
			ManagedPath:  propertyString(node, "managed_path"),
			SHA256:       propertyString(node, "sha256"),
			Summary:      propertyString(node, "summary"),
			CreatedAt:    node.CreatedAt,
			UpdatedAt:    node.UpdatedAt,
			Attachments:  attachments,
		})
		tuples = append(tuples, []string{legacyEvidenceStatusRank(propertyString(node, "status")), invertLexical(propertyString(node, "captured_at")), node.StableKey, node.ID})
	}
	sortLegacyTuples(artifacts, tuples)
	queryHash, err := projectionQueryHash("CyberPenda.Blackboard.LegacyEvidenceQuery.v1", struct{}{})
	if err != nil {
		return LegacyEvidenceCollectionV1{}, err
	}
	start, next, err := legacyPageWindow(cursor, queryHash, "legacy_evidence", limit, len(artifacts), tuples, ReadKindLegacyEvidenceCollectionV1, snapshot, cursorKey)
	if err != nil {
		return LegacyEvidenceCollectionV1{}, err
	}
	end := start + limit
	if end > len(artifacts) {
		end = len(artifacts)
	}
	page := append([]LegacyEvidenceArtifactV1(nil), artifacts[start:end]...)
	return LegacyEvidenceCollectionV1{Evidence: page, NextCursor: next, CompatibilityTruncated: next != ""}, nil
}

// legacySingularAttachTarget applies the §18.4 deterministic singular attach
// target selection:
//  1. the original migrated legacy target (carried as migrated_attach_to_type /
//     migrated_attach_to_key node properties) when it remains a current main
//     ProjectFact or Finding;
//  2. otherwise the first current ProjectFact or Finding evidences target by
//     node-type ordinal (ProjectFact before Finding), stable key, then ID;
//  3. otherwise empty strings.
func legacySingularAttachTarget(node NodeRecord, factTargets, findingTargets []NodeRecord, byID map[string]NodeRecord) (string, string) {
	if migratedType := propertyString(node, "migrated_attach_to_type"); migratedType == "fact" || migratedType == "finding" {
		if migratedKey := propertyString(node, "migrated_attach_to_key"); migratedKey != "" {
			for _, target := range append(append([]NodeRecord{}, factTargets...), findingTargets...) {
				if target.Disposition == DispositionMain && target.StableKey == migratedKey {
					return migratedType, migratedKey
				}
			}
		}
	}
	pick := func(targets []NodeRecord, label string) (string, string, bool) {
		if len(targets) == 0 {
			return "", "", false
		}
		best := targets[0]
		for _, n := range targets[1:] {
			if n.StableKey < best.StableKey || (n.StableKey == best.StableKey && n.ID < best.ID) {
				best = n
			}
		}
		return label, best.StableKey, true
	}
	if label, key, ok := pick(factTargets, "fact"); ok {
		return label, key
	}
	if label, key, ok := pick(findingTargets, "finding"); ok {
		return label, key
	}
	return "", ""
}

func legacyFindingFromNode(node NodeRecord) LegacyFindingV1 {
	vector := propertyString(node, "cvss_vector")
	return LegacyFindingV1{
		ID:             node.ID,
		ProjectID:      node.ProjectID,
		FindingKey:     node.StableKey,
		Version:        node.Version,
		Title:          propertyString(node, "title"),
		Description:    propertyString(node, "description"),
		Status:         propertyString(node, "status"),
		Target:         propertyString(node, "target"),
		Proof:          propertyString(node, "proof"),
		Impact:         propertyString(node, "impact"),
		Recommendation: propertyString(node, "recommendation"),
		CVSSVersion:    propertyString(node, "cvss_version"),
		CVSSVector:     vector,
		CVSSPending:    strings.TrimSpace(vector) == "",
		Severity:       deriveSeverity(vector),
		CreatedAt:      node.CreatedAt,
		UpdatedAt:      node.UpdatedAt,
	}
}

func legacyFindingTuple(finding LegacyFindingV1) []string {
	return []string{legacyFindingStatusRank(finding.Status), legacySeverityRank(finding.Severity), finding.Target, finding.Title, finding.FindingKey, finding.ID}
}

// resolveLegacyNodeByKey resolves a stable key (alias/merge aware) to its
// canonical main node, returning additive resolution metadata.
func resolveLegacyNodeByKey(snapshot GraphSnapshot, nodeType NodeType, key string) (NodeRecord, *string, *string, error) {
	byID := legacyNodeIndex(snapshot)
	canonicalID := ""
	var alias *string
	var merged *string
	for _, graphKey := range snapshot.Keys {
		if graphKey.NodeType == nodeType && graphKey.Key == key {
			canonicalID = graphKey.CanonicalNodeID
			if graphKey.Role == "alias" {
				value := key
				alias = &value
			}
			break
		}
	}
	if canonicalID == "" {
		return NodeRecord{}, nil, nil, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "stable_key")
	}
	node, ok := byID[canonicalID]
	if !ok {
		return NodeRecord{}, nil, nil, readValidationError(ErrCodeSnapshotUnavailable, "canonical record cannot be reconstructed", "stable_key")
	}
	for node.MergeTargetID != "" {
		from := node.ID
		merged = &from
		next, exists := byID[node.MergeTargetID]
		if !exists {
			return NodeRecord{}, nil, nil, readValidationError(ErrCodeSnapshotUnavailable, "merged target cannot be reconstructed", "stable_key")
		}
		node = next
	}
	return node, alias, merged, nil
}

type legacyNodeVersion struct {
	version   int
	props     map[string]any
	updatedAt string
}

func legacyNodeVersions(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, nodeID string) ([]legacyNodeVersion, error) {
	query := `SELECT version,properties_json,updated_at FROM blackboard_node_versions WHERE project_id=? AND node_id=? AND result_graph_revision<=? ORDER BY version ASC`
	rows, err := tx.QueryContext(ctx, query, snapshot.ProjectID, nodeID, snapshot.GraphRevision)
	if err != nil {
		return nil, fmt.Errorf("read legacy node versions: %w", err)
	}
	defer rows.Close()
	out := []legacyNodeVersion{}
	for rows.Next() {
		var version int
		var properties, updatedAt string
		if err := rows.Scan(&version, &properties, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan legacy node version: %w", err)
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(properties), &props); err != nil {
			return nil, fmt.Errorf("decode legacy node version: %w", err)
		}
		out = append(out, legacyNodeVersion{version: version, props: props, updatedAt: updatedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy node versions: %w", err)
	}
	return out, nil
}

type legacyEdgeTimestamp struct {
	created string
	updated string
}

// legacyEdgeTimestamps loads created_at/updated_at for every edge active at the
// observed revision so legacy relation rows carry real timestamps.
func legacyEdgeTimestamps(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot) (map[string]legacyEdgeTimestamp, error) {
	query := `SELECT e.id, e.created_at, v.updated_at
		FROM blackboard_edges e
		JOIN blackboard_edge_versions v
		  ON v.project_id=e.project_id AND v.edge_id=e.id AND v.result_graph_revision=?
		WHERE e.project_id=?`
	rows, err := tx.QueryContext(ctx, query, snapshot.GraphRevision, snapshot.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("read legacy edge timestamps: %w", err)
	}
	defer rows.Close()
	out := map[string]legacyEdgeTimestamp{}
	for rows.Next() {
		var id, created, updated string
		if err := rows.Scan(&id, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan legacy edge timestamp: %w", err)
		}
		out[id] = legacyEdgeTimestamp{created: created, updated: updated}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy edge timestamps: %w", err)
	}
	return out, nil
}

func legacyNodeIndex(snapshot GraphSnapshot) map[string]NodeRecord {
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	return byID
}

func legacyListLimit(requested int) int {
	if requested <= 0 {
		return legacyCompatibilityLimit
	}
	if requested > legacyCompatibilityLimit {
		return legacyCompatibilityLimit
	}
	return requested
}

func legacyValidateLimit(limit int) error {
	if limit < 1 || limit > legacyCompatibilityLimit {
		return readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 200", "limit")
	}
	return nil
}

// legacyPageWindow resolves the cursor start offset for a legacy collection and
// returns an opaque next_cursor when more pages remain. It reuses the canonical
// projectionPageStart/encodeReadCursor machinery so legacy cursors obey the
// same invalid_cursor contract as other reads.
func legacyPageWindow(cursor *readCursor, queryHash, sortName string, limit, total int, tuples [][]string, projection ReadKind, snapshot GraphSnapshot, cursorKey []byte) (int, string, error) {
	start, err := projectionPageStart(cursor, queryHash, sortName, limit, total, func(i int) []string { return tuples[i] })
	if err != nil {
		return 0, "", err
	}
	if total <= start+limit {
		return start, "", nil
	}
	last := tuples[start+limit-1]
	encoded, err := encodeReadCursor(readCursor{Version: 1, Projection: projection, ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash, Sort: sortName, Limit: limit, Last: last}, cursorKey)
	if err != nil {
		return 0, "", err
	}
	return start, encoded, nil
}

// sortLegacyTuples sorts items in place by their parallel tuple ordering.
func sortLegacyTuples[T any](items []T, tuples [][]string) {
	sort.SliceStable(items, func(i, j int) bool {
		return compareRowTuple(tuples[i], tuples[j]) < 0
	})
}

func legacyConfidenceRank(value string) string {
	switch value {
	case "confirmed":
		return "0"
	case "tentative":
		return "1"
	case "deprecated":
		return "2"
	default:
		return "3"
	}
}

func legacyScopeRank(value string) string {
	switch value {
	case "in_scope":
		return "0"
	case "unknown":
		return "1"
	case "out_of_scope":
		return "2"
	default:
		return "3"
	}
}

func legacyFindingStatusRank(value string) string {
	switch value {
	case "confirmed":
		return "0"
	case "unconfirmed":
		return "1"
	case "false_positive":
		return "2"
	default:
		return "3"
	}
}

func legacySeverityRank(value string) string {
	switch value {
	case "critical":
		return "0"
	case "high":
		return "1"
	case "medium":
		return "2"
	case "low":
		return "3"
	case "pending", "":
		return "4"
	default:
		return "5"
	}
}

func legacyEvidenceStatusRank(value string) string {
	switch value {
	case "available":
		return "0"
	case "missing":
		return "1"
	case "superseded":
		return "2"
	default:
		return "3"
	}
}
