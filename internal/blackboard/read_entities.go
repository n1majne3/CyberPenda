package blackboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// EntityCollectionRequest is the versioned Blackboard read shape for this projection.
type EntityCollectionRequest struct {
	ParentID, AncestorID, Kind, Status, ScopeStatus, Query string
	IncludeCounts                                          *bool
	Limit                                                  int
	Cursor                                                 string
}

// EntityItemV1 is the versioned Blackboard read shape for this projection.
type EntityItemV1 struct {
	Entity                 NodeRefV1      `json:"entity"`
	Kind                   string         `json:"kind"`
	Name                   string         `json:"name"`
	Locator                string         `json:"locator"`
	Description            string         `json:"description"`
	ScopeStatus            string         `json:"scope_status"`
	Status                 string         `json:"status"`
	ParentEntities         []NodeRefV1    `json:"parent_entities"`
	ChildCount             int            `json:"child_count"`
	RecordCounts           map[string]int `json:"record_counts"`
	HighestFindingSeverity *string        `json:"highest_finding_severity"`
	HealthSeverity         *string        `json:"health_severity"`
}

// EntityCollectionV1 is the versioned Blackboard read shape for this projection.
type EntityCollectionV1 struct {
	Items      []EntityItemV1 `json:"items"`
	Page       PageV1         `json:"page"`
	sourcePins map[string]any
}

// EntityDetailRequest is the versioned Blackboard read shape for this projection.
type EntityDetailRequest struct{ NodeID string }

// NodeDetailV1 is the versioned Blackboard read shape for this projection.
type NodeDetailV1 struct {
	ID          string         `json:"id"`
	NodeType    NodeType       `json:"node_type"`
	StableKey   string         `json:"stable_key"`
	Version     int            `json:"version"`
	Disposition Disposition    `json:"disposition"`
	Properties  map[string]any `json:"properties"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	MergeTarget *NodeRefV1     `json:"merge_target"`
}

// EntityDetailV1 is the versioned Blackboard read shape for this projection.
type EntityDetailV1 struct {
	Entity          NodeDetailV1                `json:"entity"`
	Parents         NodeRefPreviewV1            `json:"parents"`
	Children        NodeRefPreviewV1            `json:"children"`
	Breadcrumbs     [][]NodeRefV1               `json:"breadcrumbs"`
	PathsTruncated  bool                        `json:"paths_truncated"`
	DescendantCount int                         `json:"descendant_count"`
	Records         map[string]NodeRefPreviewV1 `json:"records"`
	Health          RecordHealthV1              `json:"health"`
	sourcePins      map[string]any
}

func buildEntityCollection(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request EntityCollectionRequest, cursor *readCursor, cursorKey []byte) (EntityCollectionV1, error) {
	limit := request.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return EntityCollectionV1{}, readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 200", "limit")
	}
	if len([]rune(request.Query)) > 500 {
		return EntityCollectionV1{}, readValidationError(ErrCodeInvalidQuery, "query exceeds 500 Unicode scalar values", "query")
	}
	if request.Kind != "" {
		if err := validateEnumFilters("kind", []string{request.Kind}, "network", "host", "ip_address", "domain", "service", "endpoint", "application", "identity", "credential", "data_store", "file", "binary", "function", "challenge_component"); err != nil {
			return EntityCollectionV1{}, err
		}
	}
	if request.Status != "" {
		if err := validateEnumFilters("status", []string{request.Status}, "active", "retired", "superseded"); err != nil {
			return EntityCollectionV1{}, err
		}
	}
	if request.ScopeStatus != "" {
		if err := validateEnumFilters("scope_status", []string{request.ScopeStatus}, "in_scope", "unknown", "out_of_scope"); err != nil {
			return EntityCollectionV1{}, err
		}
	}
	pinnedHealthRun := ""
	if cursor != nil && cursor.SourcePins != nil {
		pinnedHealthRun = cursor.SourcePins["health_run_id"]
	}
	healthRanks, healthRunID, err := healthSubjectRanks(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision, pinnedHealthRun)
	if err != nil {
		return EntityCollectionV1{}, err
	}
	byID := entityNodes(snapshot)
	descendants := map[string]bool{}
	if request.AncestorID != "" {
		for id := range entityDescendants(snapshot, request.AncestorID) {
			descendants[id] = true
		}
	}
	items := []EntityItemV1{}
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeEntity || node.Disposition != DispositionMain {
			continue
		}
		parents := entityParents(snapshot, byID, node.ID)
		if request.ParentID == "root" && len(parents) > 0 {
			continue
		}
		if request.ParentID != "" && request.ParentID != "root" && !containsRefID(parents, request.ParentID) {
			continue
		}
		if request.AncestorID != "" && !descendants[node.ID] {
			continue
		}
		kind := propertyString(node, "kind")
		name := propertyString(node, "name")
		locator := propertyString(node, "locator")
		status := propertyString(node, "status")
		scope := propertyString(node, "scope_status")
		if request.Kind != "" && kind != request.Kind {
			continue
		}
		if request.Status != "" && status != request.Status {
			continue
		}
		if request.ScopeStatus != "" && scope != request.ScopeStatus {
			continue
		}
		if request.Query != "" && !strings.Contains(normalizeSearch(name+" "+locator+" "+node.StableKey), normalizeSearch(request.Query)) {
			continue
		}
		item := EntityItemV1{Entity: nodeRefForNode(node), Kind: kind, Name: name, Locator: locator, Description: propertyString(node, "description"), ScopeStatus: scope, Status: status, ParentEntities: parents, ChildCount: len(entityChildren(snapshot, byID, node.ID)), RecordCounts: map[string]int{}}
		if request.IncludeCounts == nil || *request.IncludeCounts {
			item.RecordCounts = entityRecordCounts(snapshot, node.ID)
		}
		if rank := healthRanks[node.ID]; rank > 0 && rank < 99 {
			value := map[int]string{1: "critical", 2: "warning", 3: "info"}[rank]
			item.HealthSeverity = &value
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return entityItemLess(items[i], items[j]) })
	queryHash, err := projectionQueryHash("CyberPenda.Blackboard.EntityQuery.v1", struct {
		ParentID, AncestorID, Kind, Status, ScopeStatus, Query string
		IncludeCounts                                          bool
	}{request.ParentID, request.AncestorID, request.Kind, request.Status, request.ScopeStatus, normalizeSearch(request.Query), request.IncludeCounts == nil || *request.IncludeCounts})
	if err != nil {
		return EntityCollectionV1{}, err
	}
	tuple := func(item EntityItemV1) []string {
		return []string{fmt.Sprintf("%02d", entityKindOrdinal(item.Kind)), normalizeSearch(item.Name), item.Locator, item.Entity.StableKey, item.Entity.ID}
	}
	start, err := projectionPageStart(cursor, queryHash, "entities", limit, len(items), func(i int) []string { return tuple(items[i]) })
	if err != nil {
		return EntityCollectionV1{}, err
	}
	total := len(items)
	end := start + limit
	if end > total {
		end = total
	}
	pageItems := append([]EntityItemV1(nil), items[start:end]...)
	next := ""
	if end < total && len(pageItems) > 0 {
		next, err = encodeReadCursor(readCursor{Version: 1, Projection: ReadKindEntityCollectionV1, ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash, Sort: "entities", Limit: limit, Last: tuple(pageItems[len(pageItems)-1]), SourcePins: stringSourcePins("health_run_id", healthRunID)}, cursorKey)
		if err != nil {
			return EntityCollectionV1{}, err
		}
	}
	return EntityCollectionV1{Items: pageItems, Page: PageV1{Limit: limit, TotalItems: total, NextCursor: next}, sourcePins: anySourcePins("health_run_id", healthRunID)}, nil
}

func buildEntityDetail(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request EntityDetailRequest) (EntityDetailV1, error) {
	byID := entityNodes(snapshot)
	node, ok := byID[request.NodeID]
	if !ok {
		return EntityDetailV1{}, readValidationError(ErrCodeRecordNotFound, "Entity does not exist in this Project", "node_id")
	}
	props := clonePropertyMap(node.PropertyMap)
	if propertyString(node, "kind") == "credential" {
		for _, key := range []string{"value", "secret", "token", "password"} {
			delete(props, key)
		}
	}
	parents := entityParents(snapshot, byID, node.ID)
	children := entityChildren(snapshot, byID, node.ID)
	paths, truncated := shortestEntityBreadcrumbs(snapshot, byID, node.ID, 20)
	desc := entityDescendants(snapshot, node.ID)
	records := map[string]NodeRefPreviewV1{}
	groups := map[string][]NodeRefV1{}
	for _, e := range snapshot.Edges {
		if e.State == "active" && e.EdgeType == EdgeTypeAbout && e.ToNodeID == node.ID {
			for _, n := range snapshot.Nodes {
				if n.ID == e.FromNodeID && n.Disposition == DispositionMain {
					groups[string(n.NodeType)] = append(groups[string(n.NodeType)], nodeRefForNode(n))
					break
				}
			}
		}
	}
	for kind, refs := range groups {
		records[kind] = refsPreview(refs, 25)
	}
	health, healthRunID, err := recordHealthAt(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision, node.ID)
	if err != nil {
		return EntityDetailV1{}, err
	}
	parentPreview := refsPreview(parents, 25)
	parentPreview.RecordsHref = "/blackboard/entities?ancestor_id=" + node.ID
	childPreview := refsPreview(children, 25)
	childPreview.RecordsHref = "/blackboard/entities?parent_id=" + node.ID
	for kind, preview := range records {
		preview.RecordsHref = "/blackboard/records?about_entity_id=" + node.ID + "&node_type=" + kind
		records[kind] = preview
	}
	return EntityDetailV1{Entity: NodeDetailV1{ID: node.ID, NodeType: node.NodeType, StableKey: node.StableKey, Version: node.Version, Disposition: node.Disposition, Properties: props, CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt}, Parents: parentPreview, Children: childPreview, Breadcrumbs: paths, PathsTruncated: truncated, DescendantCount: len(desc), Records: records, Health: health, sourcePins: anySourcePins("health_run_id", healthRunID)}, nil
}

func entityNodes(snapshot GraphSnapshot) map[string]NodeRecord {
	out := map[string]NodeRecord{}
	for _, n := range snapshot.Nodes {
		if n.NodeType == NodeTypeEntity && n.Disposition == DispositionMain {
			out[n.ID] = n
		}
	}
	return out
}
func entityParents(snapshot GraphSnapshot, byID map[string]NodeRecord, id string) []NodeRefV1 {
	refs := []NodeRefV1{}
	for _, e := range snapshot.Edges {
		if e.State == "active" && e.EdgeType == EdgeTypePartOf && e.FromNodeID == id {
			if n, ok := byID[e.ToNodeID]; ok {
				refs = append(refs, nodeRefForNode(n))
			}
		}
	}
	sortEntityRefs(refs, byID)
	return refs
}
func entityChildren(snapshot GraphSnapshot, byID map[string]NodeRecord, id string) []NodeRefV1 {
	refs := []NodeRefV1{}
	for _, e := range snapshot.Edges {
		if e.State == "active" && e.EdgeType == EdgeTypePartOf && e.ToNodeID == id {
			if n, ok := byID[e.FromNodeID]; ok {
				refs = append(refs, nodeRefForNode(n))
			}
		}
	}
	sortEntityRefs(refs, byID)
	return refs
}
func entityDescendants(snapshot GraphSnapshot, id string) map[string]bool {
	byID := entityNodes(snapshot)
	seen := map[string]bool{}
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range entityChildren(snapshot, byID, cur) {
			if !seen[child.ID] {
				seen[child.ID] = true
				queue = append(queue, child.ID)
			}
		}
	}
	delete(seen, id)
	return seen
}
func entityRecordCounts(snapshot GraphSnapshot, id string) map[string]int {
	counts := map[string]int{"objectives": 0, "attempts": 0, "facts": 0, "findings": 0, "solutions": 0, "evidence": 0}
	names := map[NodeType]string{NodeTypeExplorationObjective: "objectives", NodeTypeAttempt: "attempts", NodeTypeProjectFact: "facts", NodeTypeFinding: "findings", NodeTypeSolution: "solutions", NodeTypeEvidenceArtifact: "evidence"}
	by := map[string]NodeRecord{}
	for _, n := range snapshot.Nodes {
		by[n.ID] = n
	}
	for _, e := range snapshot.Edges {
		if e.State == "active" && e.EdgeType == EdgeTypeAbout && e.ToNodeID == id {
			if n, ok := by[e.FromNodeID]; ok && n.Disposition == DispositionMain {
				if key := names[n.NodeType]; key != "" {
					counts[key]++
				}
			}
		}
	}
	return counts
}
func entityKindOrdinal(kind string) int {
	order := []string{"network", "host", "ip_address", "domain", "service", "endpoint", "application", "identity", "credential", "data_store", "file", "binary", "function", "challenge_component"}
	for i, v := range order {
		if v == kind {
			return i
		}
	}
	return len(order)
}
func entityItemLess(a, b EntityItemV1) bool {
	if entityKindOrdinal(a.Kind) != entityKindOrdinal(b.Kind) {
		return entityKindOrdinal(a.Kind) < entityKindOrdinal(b.Kind)
	}
	an, bn := normalizeSearch(a.Name), normalizeSearch(b.Name)
	if an != bn {
		return an < bn
	}
	if a.Locator != b.Locator {
		return a.Locator < b.Locator
	}
	if a.Entity.StableKey != b.Entity.StableKey {
		return a.Entity.StableKey < b.Entity.StableKey
	}
	return a.Entity.ID < b.Entity.ID
}
func sortEntityRefs(refs []NodeRefV1, byID map[string]NodeRecord) {
	sort.Slice(refs, func(i, j int) bool {
		a, b := byID[refs[i].ID], byID[refs[j].ID]
		return entityItemLess(EntityItemV1{Entity: refs[i], Kind: propertyString(a, "kind"), Name: propertyString(a, "name"), Locator: propertyString(a, "locator")}, EntityItemV1{Entity: refs[j], Kind: propertyString(b, "kind"), Name: propertyString(b, "name"), Locator: propertyString(b, "locator")})
	})
}
func shortestEntityBreadcrumbs(snapshot GraphSnapshot, byID map[string]NodeRecord, target string, limit int) ([][]NodeRefV1, bool) {
	// Walk upward breadth-first and retain every shortest root path.
	type path []string
	queue := []path{{target}}
	complete := []path{}
	shortest := -1
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if shortest >= 0 && len(p) > shortest {
			continue
		}
		parents := entityParents(snapshot, byID, p[len(p)-1])
		if len(parents) == 0 {
			shortest = len(p)
			complete = append(complete, p)
			continue
		}
		for _, parent := range parents {
			np := append(path(nil), p...)
			np = append(np, parent.ID)
			queue = append(queue, np)
		}
	}
	out := make([][]NodeRefV1, 0, len(complete))
	for _, p := range complete {
		refs := make([]NodeRefV1, len(p))
		for i := range p {
			refs[len(p)-1-i] = nodeRefForNode(byID[p[i]])
		}
		out = append(out, refs)
	}
	sort.Slice(out, func(i, j int) bool {
		for k := 0; k < len(out[i]) && k < len(out[j]); k++ {
			a, b := byID[out[i][k].ID], byID[out[j][k].ID]
			ai := EntityItemV1{Entity: out[i][k], Kind: propertyString(a, "kind"), Name: propertyString(a, "name"), Locator: propertyString(a, "locator")}
			bi := EntityItemV1{Entity: out[j][k], Kind: propertyString(b, "kind"), Name: propertyString(b, "name"), Locator: propertyString(b, "locator")}
			if entityItemLess(ai, bi) {
				return true
			}
			if entityItemLess(bi, ai) {
				return false
			}
		}
		return len(out[i]) < len(out[j])
	})
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated
}
