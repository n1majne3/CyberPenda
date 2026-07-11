package blackboard

import (
	"context"
	"database/sql"
	"sort"
)

type GraphExplorerRequest struct {
	SeedNodeIDs         []string
	NodeTypes           []NodeType
	EdgeTypes           []EdgeType
	Lifecycle           []string
	ScopeStatus         []string
	EntityKind          []string
	Direction           string
	MaxDepth            int
	Query               string
	IncludeArchived     bool
	IncludeRetiredEdges bool
	MaxNodes            int
	MaxEdges            int
}

type GraphExplorerNodeV1 struct {
	Row    NodeRowV1 `json:"row"`
	XGroup string    `json:"x_group"`
	IsSeed bool      `json:"is_seed"`
}

type GraphExplorerGraphV1 struct {
	Nodes []GraphExplorerNodeV1 `json:"nodes"`
	Edges []EdgeRowV1           `json:"edges"`
}

type GraphExplorerTableV1 struct {
	Nodes []NodeRowV1 `json:"nodes"`
	Edges []EdgeRowV1 `json:"edges"`
}

type GraphExplorerLegendV1 struct {
	NodeTypes       map[string]int `json:"node_types"`
	EdgeTypes       map[string]int `json:"edge_types"`
	LifecycleValues map[string]int `json:"lifecycle_values"`
}

type GraphExplorerLimitsV1 struct {
	MaxNodes  int  `json:"max_nodes"`
	MaxEdges  int  `json:"max_edges"`
	NodeCount int  `json:"node_count"`
	EdgeCount int  `json:"edge_count"`
	Truncated bool `json:"truncated"`
}

type GraphExplorerV1 struct {
	Graph                 GraphExplorerGraphV1  `json:"graph"`
	Table                 GraphExplorerTableV1  `json:"table"`
	Legend                GraphExplorerLegendV1 `json:"legend"`
	Limits                GraphExplorerLimitsV1 `json:"limits"`
	EquivalentRecordQuery map[string]any        `json:"equivalent_record_query"`
}

func buildGraphExplorer(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request GraphExplorerRequest) (GraphExplorerV1, error) {
	if request.MaxNodes == 0 {
		request.MaxNodes = 200
	}
	if request.MaxEdges == 0 {
		request.MaxEdges = 500
	}
	if request.MaxNodes < 1 || request.MaxNodes > 500 {
		return GraphExplorerV1{}, readValidationError(ErrCodeInvalidQuery, "max_nodes must be between 1 and 500", "max_nodes")
	}
	if request.MaxEdges < 1 || request.MaxEdges > 1000 {
		return GraphExplorerV1{}, readValidationError(ErrCodeInvalidQuery, "max_edges must be between 1 and 1000", "max_edges")
	}
	for _, nodeType := range request.NodeTypes {
		if nodeTypeOrdinal(nodeType) < 0 {
			return GraphExplorerV1{}, readValidationError(ErrCodeInvalidQuery, "unknown node_type", "node_type")
		}
	}
	for _, edgeType := range request.EdgeTypes {
		if edgeTypeOrdinal(edgeType) >= len(edgeTypeOrdinals) {
			return GraphExplorerV1{}, readValidationError(ErrCodeInvalidQuery, "unknown edge_type", "edge_type")
		}
	}
	if request.Direction == "" {
		request.Direction = "both"
	}
	if request.Direction != "incoming" && request.Direction != "outgoing" && request.Direction != "both" {
		return GraphExplorerV1{}, readValidationError(ErrCodeInvalidQuery, "direction must be incoming, outgoing, or both", "direction")
	}
	if request.MaxDepth == 0 {
		request.MaxDepth = 1
	}
	if request.MaxDepth < 1 || request.MaxDepth > 5 {
		return GraphExplorerV1{}, readValidationError(ErrCodeInvalidQuery, "max_depth must be between 1 and 5", "max_depth")
	}
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	seedSet := stringSet(request.SeedNodeIDs)
	selectedIDs := map[string]bool{}
	if len(request.SeedNodeIDs) > 0 {
		for _, seedID := range sortedUniqueStrings(request.SeedNodeIDs) {
			if _, ok := byID[seedID]; !ok {
				return GraphExplorerV1{}, readValidationError(ErrCodeRecordNotFound, "seed record does not exist in this Project", "seed_node_id")
			}
			for id := range explorerReachableIDs(snapshot, request, seedID) {
				selectedIDs[id] = true
			}
		}
	} else {
		for _, node := range snapshot.Nodes {
			if explorerNodeMatches(node, request) {
				selectedIDs[node.ID] = true
			}
		}
	}
	// Filters also constrain seeded results without ever silently replacing the
	// requested topology with a relevance-selected sample.
	for id := range selectedIDs {
		if !seedSet[id] && !explorerNodeMatches(byID[id], request) {
			delete(selectedIDs, id)
		}
	}
	selectedNodes := []NodeRecord{}
	for id := range selectedIDs {
		selectedNodes = append(selectedNodes, byID[id])
	}
	sort.Slice(selectedNodes, func(i, j int) bool {
		if nodeTypeOrdinal(selectedNodes[i].NodeType) != nodeTypeOrdinal(selectedNodes[j].NodeType) {
			return nodeTypeOrdinal(selectedNodes[i].NodeType) < nodeTypeOrdinal(selectedNodes[j].NodeType)
		}
		if selectedNodes[i].StableKey != selectedNodes[j].StableKey {
			return selectedNodes[i].StableKey < selectedNodes[j].StableKey
		}
		return selectedNodes[i].ID < selectedNodes[j].ID
	})
	edgeFilter := map[EdgeType]bool{}
	for _, edgeType := range request.EdgeTypes {
		edgeFilter[edgeType] = true
	}
	selectedEdges := []EdgeRecord{}
	for _, edge := range snapshot.Edges {
		if !selectedIDs[edge.FromNodeID] || !selectedIDs[edge.ToNodeID] {
			continue
		}
		if edge.State != "active" && !request.IncludeRetiredEdges {
			continue
		}
		if len(edgeFilter) > 0 && !edgeFilter[edge.EdgeType] {
			continue
		}
		selectedEdges = append(selectedEdges, edge)
	}
	if len(selectedNodes) > request.MaxNodes || len(selectedEdges) > request.MaxEdges {
		return GraphExplorerV1{}, &ValidationError{Code: ErrCodeProjectionTooLarge, Message: "Graph Explorer projection exceeds explicit limits", OperationIndex: -1, Path: "graph_explorer", Details: map[string]any{"node_count": len(selectedNodes), "edge_count": len(selectedEdges), "max_nodes": request.MaxNodes, "max_edges": request.MaxEdges, "suggested_filters": []string{"seed_node_id", "node_type", "edge_type", "lifecycle", "scope_status", "entity_kind", "query"}}}
	}
	graphNodes := []GraphExplorerNodeV1{}
	tableNodes := []NodeRowV1{}
	legend := GraphExplorerLegendV1{NodeTypes: map[string]int{}, EdgeTypes: map[string]int{}, LifecycleValues: map[string]int{}}
	for _, node := range selectedNodes {
		row, err := nodeRowAt(ctx, tx, snapshot, node)
		if err != nil {
			return GraphExplorerV1{}, err
		}
		xGroup := string(node.NodeType)
		if node.NodeType == NodeTypeEntity {
			xGroup = "entity:" + propertyString(node, "kind")
		} else if len(row.AboutEntities) > 0 {
			xGroup = row.AboutEntities[0].StableKey
		}
		graphNodes = append(graphNodes, GraphExplorerNodeV1{Row: row, XGroup: xGroup, IsSeed: seedSet[node.ID]})
		tableNodes = append(tableNodes, row)
		legend.NodeTypes[string(node.NodeType)]++
		if row.Lifecycle != nil {
			legend.LifecycleValues[row.Lifecycle.Value]++
		}
	}
	edgeRows := []EdgeRowV1{}
	for _, edge := range selectedEdges {
		row, err := edgeRowAt(ctx, tx, snapshot.ProjectID, edge, byID[edge.FromNodeID], byID[edge.ToNodeID])
		if err != nil {
			return GraphExplorerV1{}, err
		}
		edgeRows = append(edgeRows, row)
		legend.EdgeTypes[string(edge.EdgeType)]++
	}
	sort.Slice(edgeRows, func(i, j int) bool { return edgeRowLess(edgeRows[i], edgeRows[j]) })
	query := map[string]any{"node_type": request.NodeTypes, "edge_type": request.EdgeTypes, "lifecycle": request.Lifecycle, "scope_status": request.ScopeStatus, "entity_kind": request.EntityKind, "query": request.Query}
	return GraphExplorerV1{Graph: GraphExplorerGraphV1{Nodes: graphNodes, Edges: edgeRows}, Table: GraphExplorerTableV1{Nodes: tableNodes, Edges: append([]EdgeRowV1(nil), edgeRows...)}, Legend: legend, Limits: GraphExplorerLimitsV1{MaxNodes: request.MaxNodes, MaxEdges: request.MaxEdges, NodeCount: len(selectedNodes), EdgeCount: len(selectedEdges)}, EquivalentRecordQuery: query}, nil
}

func explorerNodeMatches(node NodeRecord, request GraphExplorerRequest) bool {
	if !request.IncludeArchived && node.Disposition != DispositionMain {
		return false
	}
	if len(request.NodeTypes) > 0 && !containsNodeType(request.NodeTypes, node.NodeType) {
		return false
	}
	_, lifecycle := lifecycleForNode(node)
	if len(request.Lifecycle) > 0 && !stringSet(request.Lifecycle)[lifecycle] {
		return false
	}
	if len(request.ScopeStatus) > 0 && !stringSet(request.ScopeStatus)[propertyString(node, "scope_status")] {
		return false
	}
	if len(request.EntityKind) > 0 && (node.NodeType != NodeTypeEntity || !stringSet(request.EntityKind)[propertyString(node, "kind")]) {
		return false
	}
	return request.Query == "" || matchesLexicalQuery(node, normalizeSearchText(request.Query))
}

func explorerReachableIDs(snapshot GraphSnapshot, request GraphExplorerRequest, seedID string) map[string]bool {
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	edgeFilter := map[EdgeType]bool{}
	for _, edgeType := range request.EdgeTypes {
		edgeFilter[edgeType] = true
	}
	nodeFilter := map[NodeType]bool{}
	for _, nodeType := range request.NodeTypes {
		nodeFilter[nodeType] = true
	}
	type item struct {
		id    string
		depth int
	}
	queue := []item{{seedID, 0}}
	seen := map[string]bool{seedID: true}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= request.MaxDepth {
			continue
		}
		for _, edge := range snapshot.Edges {
			if edge.State != "active" && !request.IncludeRetiredEdges {
				continue
			}
			if len(edgeFilter) > 0 && !edgeFilter[edge.EdgeType] {
				continue
			}
			nextID := ""
			if (request.Direction == "outgoing" || request.Direction == "both") && edge.FromNodeID == current.id {
				nextID = edge.ToNodeID
			}
			if (request.Direction == "incoming" || request.Direction == "both") && edge.ToNodeID == current.id {
				nextID = edge.FromNodeID
			}
			if nextID == "" || seen[nextID] {
				continue
			}
			next, ok := byID[nextID]
			if !ok || (!request.IncludeArchived && next.Disposition != DispositionMain) || (len(nodeFilter) > 0 && !nodeFilter[next.NodeType]) {
				continue
			}
			seen[nextID] = true
			queue = append(queue, item{nextID, current.depth + 1})
		}
	}
	return seen
}
