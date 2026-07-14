package blackboard

import (
	"context"
	"database/sql"
	"sort"
)

type GraphTraversalRequest struct {
	NodeID              string
	Direction           string
	EdgeTypes           []EdgeType
	NodeTypes           []NodeType
	MaxDepth            int
	MaxNodes            int
	IncludeArchived     bool
	IncludeRetiredEdges bool
}

type TraversalNodeV1 struct {
	Node  NodeRowV1 `json:"node"`
	Depth int       `json:"depth"`
}

type GraphTraversalLimitsV1 struct {
	MaxDepth         int    `json:"max_depth"`
	MaxNodes         int    `json:"max_nodes"`
	ReachedDepth     int    `json:"reached_depth"`
	Truncated        bool   `json:"truncated"`
	TruncationReason string `json:"truncation_reason,omitempty"`
}

type GraphTraversalV1 struct {
	Root   NodeRefV1              `json:"root"`
	Nodes  []TraversalNodeV1      `json:"nodes"`
	Edges  []EdgeRowV1            `json:"edges"`
	Limits GraphTraversalLimitsV1 `json:"limits"`
}

func buildGraphTraversal(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request GraphTraversalRequest) (GraphTraversalV1, error) {
	if request.NodeID == "" {
		return GraphTraversalV1{}, readValidationError(ErrCodeInvalidQuery, "node_id is required", "node_id")
	}
	if request.Direction == "" {
		request.Direction = "both"
	}
	if request.Direction != "incoming" && request.Direction != "outgoing" && request.Direction != "both" {
		return GraphTraversalV1{}, readValidationError(ErrCodeInvalidQuery, "direction must be incoming, outgoing, or both", "direction")
	}
	if request.MaxDepth == 0 {
		request.MaxDepth = 1
	}
	if request.MaxDepth < 1 || request.MaxDepth > 5 {
		return GraphTraversalV1{}, readValidationError(ErrCodeInvalidQuery, "max_depth must be between 1 and 5", "max_depth")
	}
	if request.MaxNodes == 0 {
		request.MaxNodes = 100
	}
	if request.MaxNodes < 1 || request.MaxNodes > 500 {
		return GraphTraversalV1{}, readValidationError(ErrCodeInvalidQuery, "max_nodes must be between 1 and 500", "max_nodes")
	}
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	root, ok := byID[request.NodeID]
	if !ok {
		return GraphTraversalV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "node_id")
	}
	edgeFilter := make(map[EdgeType]bool, len(request.EdgeTypes))
	for _, edgeType := range request.EdgeTypes {
		if edgeTypeOrdinal(edgeType) >= len(edgeTypeOrdinals) {
			return GraphTraversalV1{}, readValidationError(ErrCodeInvalidQuery, "unknown edge_type", "edge_type")
		}
		edgeFilter[edgeType] = true
	}
	nodeFilter := make(map[NodeType]bool, len(request.NodeTypes))
	for _, nodeType := range request.NodeTypes {
		if nodeTypeOrdinal(nodeType) < 0 {
			return GraphTraversalV1{}, readValidationError(ErrCodeInvalidQuery, "unknown node_type", "node_type")
		}
		nodeFilter[nodeType] = true
	}
	eligibleEdges := []EdgeRecord{}
	for _, edge := range snapshot.Edges {
		if edge.State != "active" && !request.IncludeRetiredEdges {
			continue
		}
		if len(edgeFilter) > 0 && !edgeFilter[edge.EdgeType] {
			continue
		}
		eligibleEdges = append(eligibleEdges, edge)
	}
	type queued struct {
		id    string
		depth int
	}
	queue := []queued{{id: root.ID, depth: 0}}
	visited := map[string]bool{root.ID: true}
	selected := []TraversalNodeV1{}
	selectedIDs := map[string]bool{root.ID: true}
	encounteredEdgeIDs := map[string]bool{}
	truncated := false
	reachedDepth := 0
	for len(queue) > 0 {
		currentDepth := queue[0].depth
		levelEnd := 0
		for levelEnd < len(queue) && queue[levelEnd].depth == currentDepth {
			levelEnd++
		}
		level := append([]queued(nil), queue[:levelEnd]...)
		queue = queue[levelEnd:]
		if currentDepth >= request.MaxDepth {
			for _, current := range level {
				for _, edge := range eligibleEdges {
					var nextID string
					matched := false
					if (request.Direction == "outgoing" || request.Direction == "both") && edge.FromNodeID == current.id {
						nextID, matched = edge.ToNodeID, true
					}
					if (request.Direction == "incoming" || request.Direction == "both") && edge.ToNodeID == current.id {
						nextID, matched = edge.FromNodeID, true
					}
					next, ok := byID[nextID]
					if matched && !visited[nextID] && ok && (request.IncludeArchived || next.Disposition == DispositionMain) && (len(nodeFilter) == 0 || nodeFilter[next.NodeType]) {
						truncated = true
					}
				}
			}
			continue
		}
		type candidate struct {
			node NodeRecord
			edge EdgeRecord
		}
		candidates := []candidate{}
		seenLevel := map[string]bool{}
		for _, current := range level {
			for _, edge := range eligibleEdges {
				var nextID string
				matched := false
				if (request.Direction == "outgoing" || request.Direction == "both") && edge.FromNodeID == current.id {
					nextID, matched = edge.ToNodeID, true
				}
				if (request.Direction == "incoming" || request.Direction == "both") && edge.ToNodeID == current.id {
					nextID, matched = edge.FromNodeID, true
				}
				if !matched {
					continue
				}
				next, ok := byID[nextID]
				if !ok || (!request.IncludeArchived && next.Disposition != DispositionMain) || (len(nodeFilter) > 0 && !nodeFilter[next.NodeType]) {
					continue
				}
				encounteredEdgeIDs[edge.ID] = true
				if visited[nextID] || seenLevel[nextID] {
					continue
				}
				seenLevel[nextID] = true
				candidates = append(candidates, candidate{node: next, edge: edge})
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if nodeTypeOrdinal(candidates[i].node.NodeType) != nodeTypeOrdinal(candidates[j].node.NodeType) {
				return nodeTypeOrdinal(candidates[i].node.NodeType) < nodeTypeOrdinal(candidates[j].node.NodeType)
			}
			if candidates[i].node.StableKey != candidates[j].node.StableKey {
				return candidates[i].node.StableKey < candidates[j].node.StableKey
			}
			return candidates[i].node.ID < candidates[j].node.ID
		})
		for _, candidate := range candidates {
			if len(selected) >= request.MaxNodes {
				truncated = true
				break
			}
			visited[candidate.node.ID] = true
			selectedIDs[candidate.node.ID] = true
			depth := currentDepth + 1
			row, err := nodeRowAt(ctx, tx, snapshot, candidate.node)
			if err != nil {
				return GraphTraversalV1{}, err
			}
			selected = append(selected, TraversalNodeV1{Node: row, Depth: depth})
			queue = append(queue, queued{id: candidate.node.ID, depth: depth})
			if depth > reachedDepth {
				reachedDepth = depth
			}
		}
		if truncated {
			break
		}
	}
	edges := []EdgeRowV1{}
	for _, edge := range eligibleEdges {
		if !encounteredEdgeIDs[edge.ID] || !selectedIDs[edge.FromNodeID] || !selectedIDs[edge.ToNodeID] {
			continue
		}
		row, err := edgeRowAt(ctx, tx, snapshot.ProjectID, edge, byID[edge.FromNodeID], byID[edge.ToNodeID])
		if err != nil {
			return GraphTraversalV1{}, err
		}
		edges = append(edges, row)
	}
	sort.Slice(edges, func(i, j int) bool { return edgeRowLess(edges[i], edges[j]) })
	limits := GraphTraversalLimitsV1{MaxDepth: request.MaxDepth, MaxNodes: request.MaxNodes, ReachedDepth: reachedDepth, Truncated: truncated}
	if truncated {
		if len(selected) >= request.MaxNodes {
			limits.TruncationReason = "max_nodes"
		} else {
			limits.TruncationReason = "max_depth"
		}
	}
	return GraphTraversalV1{Root: nodeRefForNode(root), Nodes: selected, Edges: edges, Limits: limits}, nil
}

func edgeRowLess(a, b EdgeRowV1) bool {
	if edgeTypeOrdinal(a.EdgeType) != edgeTypeOrdinal(b.EdgeType) {
		return edgeTypeOrdinal(a.EdgeType) < edgeTypeOrdinal(b.EdgeType)
	}
	if nodeTypeOrdinal(a.From.NodeType) != nodeTypeOrdinal(b.From.NodeType) {
		return nodeTypeOrdinal(a.From.NodeType) < nodeTypeOrdinal(b.From.NodeType)
	}
	if a.From.StableKey != b.From.StableKey {
		return a.From.StableKey < b.From.StableKey
	}
	if nodeTypeOrdinal(a.To.NodeType) != nodeTypeOrdinal(b.To.NodeType) {
		return nodeTypeOrdinal(a.To.NodeType) < nodeTypeOrdinal(b.To.NodeType)
	}
	if a.To.StableKey != b.To.StableKey {
		return a.To.StableKey < b.To.StableKey
	}
	return a.ID < b.ID
}
