package blackboard

import (
	"fmt"
	"sort"
	"strings"
)

// CurrentTruthRequest is the versioned Blackboard read shape for this projection.
type CurrentTruthRequest struct {
	Confidence  []string
	ScopeStatus []string
	Category    string
	EntityID    string
	Query       string
	Limit       int
	Cursor      string
}

// NodeRefPreviewV1 is the versioned Blackboard read shape for this projection.
type NodeRefPreviewV1 struct {
	Items         []NodeRefV1 `json:"items"`
	TotalItems    int         `json:"total_items"`
	TraversalHref string      `json:"traversal_href,omitempty"`
	RecordsHref   string      `json:"records_href,omitempty"`
}

// CurrentTruthSupportV1 is the versioned Blackboard read shape for this projection.
type CurrentTruthSupportV1 struct {
	Evidence             NodeRefPreviewV1 `json:"evidence"`
	SupportingRecords    NodeRefPreviewV1 `json:"supporting_records"`
	ContradictingRecords NodeRefPreviewV1 `json:"contradicting_records"`
}

// CurrentTruthItemV1 is the versioned Blackboard read shape for this projection.
type CurrentTruthItemV1 struct {
	Fact          NodeRefV1             `json:"fact"`
	Category      string                `json:"category"`
	Summary       string                `json:"summary"`
	Body          string                `json:"body"`
	Confidence    string                `json:"confidence"`
	ScopeStatus   string                `json:"scope_status"`
	NonActionable bool                  `json:"non_actionable"`
	Support       CurrentTruthSupportV1 `json:"support"`
}

// CurrentTruthV1 is the versioned Blackboard read shape for this projection.
type CurrentTruthV1 struct {
	Items []CurrentTruthItemV1 `json:"items"`
	Page  PageV1               `json:"page"`
}

func buildCurrentTruth(snapshot GraphSnapshot, request CurrentTruthRequest, cursor *readCursor, cursorKey []byte) (CurrentTruthV1, error) {
	limit := request.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return CurrentTruthV1{}, readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 200", "limit")
	}
	if len([]rune(request.Query)) > 500 {
		return CurrentTruthV1{}, readValidationError(ErrCodeInvalidQuery, "query exceeds 500 Unicode scalar values", "query")
	}
	if err := validateEnumFilters("confidence", request.Confidence, "confirmed", "tentative"); err != nil {
		return CurrentTruthV1{}, err
	}
	if err := validateEnumFilters("scope_status", request.ScopeStatus, "in_scope", "unknown", "out_of_scope"); err != nil {
		return CurrentTruthV1{}, err
	}
	byID := map[string]NodeRecord{}
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	items := []CurrentTruthItemV1{}
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeProjectFact || node.Disposition != DispositionMain {
			continue
		}
		confidence, _ := stringProperty(node.PropertyMap, "confidence")
		if confidence != "confirmed" && confidence != "tentative" {
			continue
		}
		scope, _ := stringProperty(node.PropertyMap, "scope_status")
		category, _ := stringProperty(node.PropertyMap, "category")
		if len(request.Confidence) > 0 && !containsString(request.Confidence, confidence) {
			continue
		}
		if len(request.ScopeStatus) > 0 && !containsString(request.ScopeStatus, scope) {
			continue
		}
		if request.Category != "" && request.Category != category {
			continue
		}
		if request.EntityID != "" && !hasActiveEdge(snapshot, EdgeTypeAbout, node.ID, request.EntityID) {
			continue
		}
		if request.Query != "" && lexicalSearchRank(node, normalizeSearch(request.Query)) == 99 {
			continue
		}
		item := CurrentTruthItemV1{Fact: nodeRefForNode(node), Category: category, Confidence: confidence, ScopeStatus: scope, NonActionable: scope == "out_of_scope"}
		item.Summary, _ = stringProperty(node.PropertyMap, "summary")
		item.Body, _ = stringProperty(node.PropertyMap, "body")
		item.Support.Evidence = relatedNodePreview(snapshot, byID, node.ID, EdgeTypeEvidences, true, 10)
		item.Support.SupportingRecords = relatedNodePreview(snapshot, byID, node.ID, EdgeTypeSupports, true, 10)
		item.Support.ContradictingRecords = relatedNodePreview(snapshot, byID, node.ID, EdgeTypeContradicts, false, 10)
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		ci := map[string]int{"confirmed": 0, "tentative": 1}[items[i].Confidence]
		cj := map[string]int{"confirmed": 0, "tentative": 1}[items[j].Confidence]
		if ci != cj {
			return ci < cj
		}
		si := scopeOrder(items[i].ScopeStatus)
		sj := scopeOrder(items[j].ScopeStatus)
		if si != sj {
			return si < sj
		}
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		if items[i].Fact.StableKey != items[j].Fact.StableKey {
			return items[i].Fact.StableKey < items[j].Fact.StableKey
		}
		return items[i].Fact.ID < items[j].Fact.ID
	})
	queryHash, err := projectionQueryHash("CyberPenda.Blackboard.CurrentTruthQuery.v1", struct {
		Confidence, ScopeStatus   []string
		Category, EntityID, Query string
	}{sortedUniqueStrings(request.Confidence), sortedUniqueStrings(request.ScopeStatus), request.Category, request.EntityID, normalizeSearch(request.Query)})
	if err != nil {
		return CurrentTruthV1{}, err
	}
	tuple := func(item CurrentTruthItemV1) []string {
		return []string{fmt.Sprintf("%02d", map[string]int{"confirmed": 0, "tentative": 1}[item.Confidence]), fmt.Sprintf("%02d", scopeOrder(item.ScopeStatus)), item.Category, item.Fact.StableKey, item.Fact.ID}
	}
	start, err := projectionPageStart(cursor, queryHash, "current_truth", limit, len(items), func(i int) []string { return tuple(items[i]) })
	if err != nil {
		return CurrentTruthV1{}, err
	}
	total := len(items)
	end := start + limit
	if end > total {
		end = total
	}
	pageItems := append([]CurrentTruthItemV1{}, items[start:end]...)
	next := ""
	if end < total && len(pageItems) > 0 {
		next, err = encodeReadCursor(readCursor{Version: 1, Projection: ReadKindCurrentTruthV1, ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash, Sort: "current_truth", Limit: limit, Last: tuple(pageItems[len(pageItems)-1])}, cursorKey)
		if err != nil {
			return CurrentTruthV1{}, err
		}
	}
	return CurrentTruthV1{Items: pageItems, Page: PageV1{Limit: limit, TotalItems: total, NextCursor: next}}, nil
}

func scopeOrder(value string) int {
	switch value {
	case "in_scope":
		return 0
	case "unknown":
		return 1
	case "out_of_scope":
		return 2
	}
	return 3
}
func hasActiveEdge(snapshot GraphSnapshot, typ EdgeType, from, to string) bool {
	for _, edge := range snapshot.Edges {
		if edge.State == "active" && edge.EdgeType == typ && edge.FromNodeID == from && edge.ToNodeID == to {
			return true
		}
	}
	return false
}

func relatedNodePreview(snapshot GraphSnapshot, byID map[string]NodeRecord, subject string, typ EdgeType, incomingOnly bool, limit int) NodeRefPreviewV1 {
	refs := []NodeRefV1{}
	seen := map[string]bool{}
	for _, edge := range snapshot.Edges {
		if edge.State != "active" || edge.EdgeType != typ {
			continue
		}
		other := ""
		if edge.ToNodeID == subject {
			other = edge.FromNodeID
		} else if !incomingOnly && edge.FromNodeID == subject {
			other = edge.ToNodeID
		}
		if other == "" || seen[other] {
			continue
		}
		node, ok := byID[other]
		if !ok || node.Disposition != DispositionMain {
			continue
		}
		seen[other] = true
		refs = append(refs, nodeRefForNode(node))
	}
	sort.Slice(refs, func(i, j int) bool {
		if nodeTypeOrdinal(refs[i].NodeType) != nodeTypeOrdinal(refs[j].NodeType) {
			return nodeTypeOrdinal(refs[i].NodeType) < nodeTypeOrdinal(refs[j].NodeType)
		}
		if refs[i].StableKey != refs[j].StableKey {
			return refs[i].StableKey < refs[j].StableKey
		}
		return refs[i].ID < refs[j].ID
	})
	total := len(refs)
	if len(refs) > limit {
		refs = refs[:limit]
	}
	return NodeRefPreviewV1{Items: refs, TotalItems: total, TraversalHref: "/blackboard/records/" + subject + "/traversal?edge_type=" + string(typ)}
}

func normalizeSearch(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

// ExplorationFrontierRequest is the versioned Blackboard read shape for this projection.
type ExplorationFrontierRequest struct {
	ParentGoalID string
	EntityID     string
	Query        string
	Limit        int
	Cursor       string
}

// FrontierItemV1 is the versioned Blackboard read shape for this projection.
type FrontierItemV1 struct {
	Rank                  int              `json:"rank"`
	Objective             NodeRefV1        `json:"objective"`
	ObjectiveText         string           `json:"objective_text"`
	ParentGoals           NodeRefPreviewV1 `json:"parent_goals"`
	AboutEntities         NodeRefPreviewV1 `json:"about_entities"`
	ResolvedPrerequisites NodeRefPreviewV1 `json:"resolved_prerequisites"`
	OpenAttempts          NodeRefPreviewV1 `json:"open_attempts"`
	DerivedReasons        []string         `json:"derived_reasons"`
	createdAt             string
	goalRank              int
}

// ExplorationFrontierV1 is the versioned Blackboard read shape for this projection.
type ExplorationFrontierV1 struct {
	Items []FrontierItemV1 `json:"items"`
	Page  PageV1           `json:"page"`
}

func buildExplorationFrontier(snapshot GraphSnapshot, request ExplorationFrontierRequest, cursor *readCursor, cursorKey []byte) (ExplorationFrontierV1, error) {
	limit := request.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return ExplorationFrontierV1{}, readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 200", "limit")
	}
	if len([]rune(request.Query)) > 500 {
		return ExplorationFrontierV1{}, readValidationError(ErrCodeInvalidQuery, "query exceeds 500 Unicode scalar values", "query")
	}
	byID := map[string]NodeRecord{}
	for _, n := range snapshot.Nodes {
		byID[n.ID] = n
	}
	items := []FrontierItemV1{}
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeExplorationObjective || node.Disposition != DispositionMain || node.PropertyMap["status"] != "open" {
			continue
		}
		if !objectiveReady(snapshot, byID, node.ID) {
			continue
		}
		if request.EntityID != "" && !hasActiveEdge(snapshot, EdgeTypeAbout, node.ID, request.EntityID) {
			continue
		}
		text, _ := stringProperty(node.PropertyMap, "objective")
		if request.Query != "" && !strings.Contains(normalizeSearch(text), normalizeSearch(request.Query)) {
			continue
		}
		item := FrontierItemV1{Objective: nodeRefForNode(node), ObjectiveText: text, createdAt: node.CreatedAt, goalRank: 3, DerivedReasons: []string{"objective_open", "all_dependencies_resolved", "all_blockers_resolved"}}
		parents := []NodeRefV1{}
		entities := []NodeRefV1{}
		prerequisites := []NodeRefV1{}
		attempts := []NodeRefV1{}
		for _, edge := range snapshot.Edges {
			if edge.State != "active" {
				continue
			}
			switch {
			case edge.EdgeType == EdgeTypePartOf && edge.FromNodeID == node.ID:
				if other, ok := byID[edge.ToNodeID]; ok && other.Disposition == DispositionMain && other.NodeType == NodeTypeGoal {
					parents = append(parents, nodeRefForNode(other))
					rank := goalTaskStatusRank(propertyString(other, "task_status"))
					if rank < item.goalRank {
						item.goalRank = rank
					}
				}
			case edge.EdgeType == EdgeTypeAbout && edge.FromNodeID == node.ID:
				if other, ok := byID[edge.ToNodeID]; ok && other.Disposition == DispositionMain {
					entities = append(entities, nodeRefForNode(other))
				}
			case edge.EdgeType == EdgeTypeDependsOn && edge.FromNodeID == node.ID:
				if other, ok := byID[edge.ToNodeID]; ok && other.PropertyMap["status"] == "resolved" {
					prerequisites = append(prerequisites, nodeRefForNode(other))
				}
			case edge.EdgeType == EdgeTypeBlocks && edge.ToNodeID == node.ID:
				if other, ok := byID[edge.FromNodeID]; ok && other.PropertyMap["status"] == "resolved" {
					prerequisites = append(prerequisites, nodeRefForNode(other))
				}
			case edge.EdgeType == EdgeTypeTests && edge.ToNodeID == node.ID:
				if other, ok := byID[edge.FromNodeID]; ok && other.NodeType == NodeTypeAttempt && other.PropertyMap["status"] == "open" {
					attempts = append(attempts, nodeRefForNode(other))
				}
			}
		}
		if request.ParentGoalID != "" && !containsRefID(parents, request.ParentGoalID) {
			continue
		}
		item.ParentGoals = refsPreview(parents, 25)
		item.ParentGoals.TraversalHref = "/blackboard/records/" + node.ID + "/traversal?edge_type=part_of&direction=outgoing"
		item.AboutEntities = refsPreview(entities, 25)
		item.AboutEntities.TraversalHref = "/blackboard/records/" + node.ID + "/traversal?edge_type=about&direction=outgoing"
		item.ResolvedPrerequisites = refsPreview(prerequisites, 25)
		item.ResolvedPrerequisites.TraversalHref = "/blackboard/records/" + node.ID + "/traversal?edge_type=depends_on"
		item.OpenAttempts = refsPreview(attempts, 25)
		item.OpenAttempts.TraversalHref = "/blackboard/records/" + node.ID + "/traversal?edge_type=tests&direction=incoming"
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].goalRank != items[j].goalRank {
			return items[i].goalRank < items[j].goalRank
		}
		if items[i].createdAt != items[j].createdAt {
			return items[i].createdAt < items[j].createdAt
		}
		if items[i].Objective.StableKey != items[j].Objective.StableKey {
			return items[i].Objective.StableKey < items[j].Objective.StableKey
		}
		return items[i].Objective.ID < items[j].Objective.ID
	})
	for i := range items {
		items[i].Rank = i + 1
	}
	queryHash, err := projectionQueryHash("CyberPenda.Blackboard.FrontierQuery.v1", struct{ ParentGoalID, EntityID, Query string }{request.ParentGoalID, request.EntityID, normalizeSearch(request.Query)})
	if err != nil {
		return ExplorationFrontierV1{}, err
	}
	tuple := func(item FrontierItemV1) []string {
		return []string{fmt.Sprintf("%02d", item.goalRank), item.createdAt, item.Objective.StableKey, item.Objective.ID}
	}
	start, err := projectionPageStart(cursor, queryHash, "frontier", limit, len(items), func(i int) []string { return tuple(items[i]) })
	if err != nil {
		return ExplorationFrontierV1{}, err
	}
	total := len(items)
	end := start + limit
	if end > total {
		end = total
	}
	pageItems := append([]FrontierItemV1{}, items[start:end]...)
	next := ""
	if end < total && len(pageItems) > 0 {
		next, err = encodeReadCursor(readCursor{Version: 1, Projection: ReadKindExplorationFrontierV1, ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash, Sort: "frontier", Limit: limit, Last: tuple(pageItems[len(pageItems)-1])}, cursorKey)
		if err != nil {
			return ExplorationFrontierV1{}, err
		}
	}
	return ExplorationFrontierV1{Items: pageItems, Page: PageV1{Limit: limit, TotalItems: total, NextCursor: next}}, nil
}

func objectiveReady(snapshot GraphSnapshot, byID map[string]NodeRecord, id string) bool {
	for _, e := range snapshot.Edges {
		if e.State != "active" {
			continue
		}
		other := ""
		if e.EdgeType == EdgeTypeDependsOn && e.FromNodeID == id {
			other = e.ToNodeID
		}
		if e.EdgeType == EdgeTypeBlocks && e.ToNodeID == id {
			other = e.FromNodeID
		}
		if other != "" {
			n, ok := byID[other]
			if !ok || n.NodeType != NodeTypeExplorationObjective || n.Disposition != DispositionMain || n.PropertyMap["status"] != "resolved" {
				return false
			}
		}
	}
	return true
}
func propertyString(node NodeRecord, key string) string {
	v, _ := stringProperty(node.PropertyMap, key)
	return v
}
func containsRefID(refs []NodeRefV1, id string) bool {
	for _, r := range refs {
		if r.ID == id {
			return true
		}
	}
	return false
}
func refsPreview(refs []NodeRefV1, limit int) NodeRefPreviewV1 {
	seen := map[string]bool{}
	unique := refs[:0]
	for _, ref := range refs {
		if !seen[ref.ID] {
			seen[ref.ID] = true
			unique = append(unique, ref)
		}
	}
	refs = unique
	sort.Slice(refs, func(i, j int) bool {
		if nodeTypeOrdinal(refs[i].NodeType) != nodeTypeOrdinal(refs[j].NodeType) {
			return nodeTypeOrdinal(refs[i].NodeType) < nodeTypeOrdinal(refs[j].NodeType)
		}
		if refs[i].StableKey != refs[j].StableKey {
			return refs[i].StableKey < refs[j].StableKey
		}
		return refs[i].ID < refs[j].ID
	})
	total := len(refs)
	if len(refs) > limit {
		refs = refs[:limit]
	}
	return NodeRefPreviewV1{Items: refs, TotalItems: total}
}
