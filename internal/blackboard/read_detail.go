package blackboard

import (
	"context"
	"database/sql"
	"sort"
)

// RecordDetailRequest is the versioned Blackboard read shape for this projection.
type RecordDetailRequest struct {
	NodeID  string
	Literal bool
}

// EdgeRowV1 is the versioned Blackboard read shape for this projection.
type EdgeRowV1 struct {
	ID                string              `json:"id"`
	EdgeType          EdgeType            `json:"edge_type"`
	From              NodeRefV1           `json:"from"`
	To                NodeRefV1           `json:"to"`
	Version           int                 `json:"version"`
	State             string              `json:"state"`
	Summary           string              `json:"summary"`
	UpdatedAt         string              `json:"updated_at"`
	UpdatedProvenance ProvenanceSummaryV1 `json:"updated_provenance"`
}

// EdgePreviewV1 is the versioned Blackboard read shape for this projection.
type EdgePreviewV1 struct {
	Items         []EdgeRowV1 `json:"items"`
	TotalItems    int         `json:"total_items"`
	TraversalHref string      `json:"traversal_href"`
}

// RecordHealthV1 is the versioned Blackboard read shape for this projection.
type RecordHealthV1 struct {
	HighestSeverity *string `json:"highest_severity"`
	ResultCount     int     `json:"result_count"`
}

// RecordDerivedV1 is the versioned Blackboard read shape for this projection.
type RecordDerivedV1 struct {
	IsCurrentTruth          bool           `json:"is_current_truth"`
	IsFrontier              bool           `json:"is_frontier"`
	CTFSolvedContributor    bool           `json:"ctf_solved_contributor"`
	NonActionable           bool           `json:"non_actionable"`
	Health                  RecordHealthV1 `json:"health"`
	FrontierBlockingReasons []string       `json:"frontier_blocking_reasons,omitempty"`
}

// CapabilityHintV1 is the versioned Blackboard read shape for this projection.
type CapabilityHintV1 struct {
	Allowed    bool     `json:"allowed"`
	ReasonCode *string  `json:"reason_code"`
	Targets    []string `json:"targets,omitempty"`
}

// RecordCapabilitiesV1 is the versioned Blackboard read shape for this projection.
type RecordCapabilitiesV1 struct {
	ExpectedVersion int              `json:"expected_version"`
	Patch           CapabilityHintV1 `json:"patch"`
	Transition      CapabilityHintV1 `json:"transition"`
	Merge           CapabilityHintV1 `json:"merge"`
	Archive         CapabilityHintV1 `json:"archive"`
	Restore         CapabilityHintV1 `json:"restore"`
}

// RecordDetailV1 is the versioned Blackboard read shape for this projection.
type RecordDetailV1 struct {
	Node                 NodeDetailV1     `json:"node"`
	ResolvedFromMergedID *string          `json:"resolved_from_merged_id"`
	Derived              RecordDerivedV1  `json:"derived"`
	AboutEntities        NodeRefPreviewV1 `json:"about_entities"`
	Relationships        struct {
		Incoming EdgePreviewV1 `json:"incoming"`
		Outgoing EdgePreviewV1 `json:"outgoing"`
	} `json:"relationships"`
	Evidence NodeRefPreviewV1 `json:"evidence"`
	Support  struct {
		Supporting    NodeRefPreviewV1 `json:"supporting"`
		Contradicting NodeRefPreviewV1 `json:"contradicting"`
		Satisfies     NodeRefPreviewV1 `json:"satisfies"`
	} `json:"support"`
	Capabilities RecordCapabilitiesV1 `json:"capabilities"`
	sourcePins   map[string]any
}

func buildRecordDetail(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request RecordDetailRequest) (RecordDetailV1, error) {
	byID := map[string]NodeRecord{}
	for _, n := range snapshot.Nodes {
		byID[n.ID] = n
	}
	node, ok := byID[request.NodeID]
	if !ok {
		return RecordDetailV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "node_id")
	}
	var resolved *string
	if node.MergeTargetID != "" && !request.Literal {
		source := node.ID
		resolved = &source
		target, ok := byID[node.MergeTargetID]
		if !ok {
			return RecordDetailV1{}, readValidationError(ErrCodeSnapshotUnavailable, "merged target cannot be reconstructed", "node_id")
		}
		node = target
	}
	var merge *NodeRefV1
	if node.MergeTargetID != "" {
		if target, ok := byID[node.MergeTargetID]; ok {
			ref := nodeRefForNode(target)
			merge = &ref
		}
	}
	detail := RecordDetailV1{Node: NodeDetailV1{ID: node.ID, NodeType: node.NodeType, StableKey: node.StableKey, Version: node.Version, Disposition: node.Disposition, Properties: clonePropertyMap(node.PropertyMap), CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt, MergeTarget: merge}, ResolvedFromMergedID: resolved}
	detail.Derived.IsCurrentTruth = node.NodeType == NodeTypeProjectFact && node.Disposition == DispositionMain && (node.PropertyMap["confidence"] == "confirmed" || node.PropertyMap["confidence"] == "tentative")
	for _, id := range historicalFrontierNodeIDs(snapshot) {
		if id == node.ID {
			detail.Derived.IsFrontier = true
		}
	}
	detail.Derived.NonActionable = propertyString(node, "scope_status") == "out_of_scope"
	detail.Derived.CTFSolvedContributor = node.NodeType == NodeTypeSolution && propertyString(node, "kind") == "flag" && propertyString(node, "status") == "verified"
	health, healthRunID, err := recordHealthAt(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision, node.ID)
	if err != nil {
		return RecordDetailV1{}, err
	}
	detail.Derived.Health = health
	detail.sourcePins = anySourcePins("health_run_id", healthRunID)
	if node.NodeType == NodeTypeExplorationObjective && !detail.Derived.IsFrontier && propertyString(node, "status") == "open" {
		detail.Derived.FrontierBlockingReasons = objectiveBlockingReasons(snapshot, byID, node.ID)
	}
	about := []NodeRefV1{}
	evidence := []NodeRefV1{}
	if node.NodeType == NodeTypeEvidenceArtifact {
		evidence = append(evidence, nodeRefForNode(node))
	}
	supporting := []NodeRefV1{}
	contradicting := []NodeRefV1{}
	satisfies := []NodeRefV1{}
	incoming := []EdgeRowV1{}
	outgoing := []EdgeRowV1{}
	for _, e := range snapshot.Edges {
		if e.State != "active" {
			continue
		}
		from, fromOK := byID[e.FromNodeID]
		to, toOK := byID[e.ToNodeID]
		if !fromOK || !toOK {
			continue
		}
		row, err := edgeRowAt(ctx, tx, snapshot.ProjectID, e, from, to)
		if err != nil {
			return RecordDetailV1{}, err
		}
		if e.ToNodeID == node.ID {
			incoming = append(incoming, row)
		}
		if e.FromNodeID == node.ID {
			outgoing = append(outgoing, row)
		}
		if e.EdgeType == EdgeTypeAbout && e.FromNodeID == node.ID {
			about = append(about, nodeRefForNode(to))
		}
		if e.EdgeType == EdgeTypeEvidences && e.ToNodeID == node.ID && from.NodeType == NodeTypeEvidenceArtifact {
			evidence = append(evidence, nodeRefForNode(from))
		}
		if e.EdgeType == EdgeTypeSupports && e.ToNodeID == node.ID {
			supporting = append(supporting, nodeRefForNode(from))
		}
		if e.EdgeType == EdgeTypeContradicts && (e.ToNodeID == node.ID || e.FromNodeID == node.ID) {
			if e.ToNodeID == node.ID {
				contradicting = append(contradicting, nodeRefForNode(from))
			} else {
				contradicting = append(contradicting, nodeRefForNode(to))
			}
		}
		if e.EdgeType == EdgeTypeSatisfies && e.ToNodeID == node.ID {
			satisfies = append(satisfies, nodeRefForNode(from))
		}
	}
	sortEdgeRows := func(rows []EdgeRowV1) {
		sort.Slice(rows, func(i, j int) bool {
			if edgeTypeOrdinal(rows[i].EdgeType) != edgeTypeOrdinal(rows[j].EdgeType) {
				return edgeTypeOrdinal(rows[i].EdgeType) < edgeTypeOrdinal(rows[j].EdgeType)
			}
			oi, oj := rows[i].From, rows[j].From
			if oi.ID == node.ID {
				oi = rows[i].To
			}
			if oj.ID == node.ID {
				oj = rows[j].To
			}
			if nodeTypeOrdinal(oi.NodeType) != nodeTypeOrdinal(oj.NodeType) {
				return nodeTypeOrdinal(oi.NodeType) < nodeTypeOrdinal(oj.NodeType)
			}
			if oi.StableKey != oj.StableKey {
				return oi.StableKey < oj.StableKey
			}
			return rows[i].ID < rows[j].ID
		})
	}
	sortEdgeRows(incoming)
	sortEdgeRows(outgoing)
	detail.AboutEntities = refsPreview(about, 25)
	detail.AboutEntities.RecordsHref = "/blackboard/records?about_entity_id=" + node.ID
	detail.Evidence = refsPreview(evidence, 25)
	detail.Evidence.RecordsHref = "/blackboard/records?node_type=evidence_artifact&edge_type=evidences"
	detail.Support.Supporting = refsPreview(supporting, 25)
	detail.Support.Contradicting = refsPreview(contradicting, 25)
	detail.Support.Satisfies = refsPreview(satisfies, 25)
	detail.Relationships.Incoming = edgePreview(incoming, 25, "incoming", node.ID)
	detail.Relationships.Outgoing = edgePreview(outgoing, 25, "outgoing", node.ID)
	detail.Capabilities = recordCapabilities(node)
	return detail, nil
}
func edgePreview(rows []EdgeRowV1, limit int, direction, id string) EdgePreviewV1 {
	total := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return EdgePreviewV1{Items: rows, TotalItems: total, TraversalHref: "/blackboard/records/" + id + "/traversal?direction=" + direction + "&max_depth=1"}
}
func recordCapabilities(node NodeRecord) RecordCapabilitiesV1 {
	allowed := CapabilityHintV1{Allowed: true}
	denied := func(code string) CapabilityHintV1 { return CapabilityHintV1{Allowed: false, ReasonCode: &code} }
	caps := RecordCapabilitiesV1{ExpectedVersion: node.Version, Patch: denied("patch_not_supported"), Merge: allowed}
	if node.Disposition == DispositionMain {
		if recordArchiveLifecycleEligible(node) {
			caps.Archive = allowed
		} else {
			caps.Archive = denied("archive_guard_active_record")
		}
		caps.Restore = denied("record_not_archived")
	} else if node.Disposition == DispositionArchived {
		caps.Patch = denied("record_archived")
		caps.Merge = denied("record_archived")
		caps.Archive = denied("record_already_archived")
		caps.Restore = allowed
	} else {
		caps.Patch = denied("record_merged")
		caps.Merge = denied("record_merged")
		caps.Archive = denied("record_merged")
		caps.Restore = denied("record_merged")
	}
	switch node.NodeType {
	case NodeTypeExplorationObjective:
		caps.Transition = CapabilityHintV1{Allowed: node.Disposition == DispositionMain, Targets: []string{"resolved", "abandoned", "superseded"}}
	case NodeTypeAttempt:
		caps.Transition = CapabilityHintV1{Allowed: node.Disposition == DispositionMain, Targets: []string{"succeeded", "failed", "blocked", "inconclusive", "interrupted"}}
	case NodeTypeHypothesis:
		caps.Transition = CapabilityHintV1{Allowed: node.Disposition == DispositionMain, Targets: []string{"supported", "contradicted", "inconclusive", "superseded"}}
	case NodeTypeProjectFact:
		caps.Transition = CapabilityHintV1{Allowed: node.Disposition == DispositionMain, Targets: []string{"tentative", "confirmed", "deprecated"}}
	case NodeTypeFinding:
		caps.Transition = CapabilityHintV1{Allowed: node.Disposition == DispositionMain, Targets: []string{"confirmed", "false_positive"}}
	default:
		caps.Transition = denied("transition_not_supported")
	}
	return caps
}
