package blackboard_test

import (
	"context"
	"errors"
	"testing"

	"pentest/internal/blackboard"
)

func TestGraphTraversalIsBreadthFirstDirectionPreservingAndExplicitWhenTruncated(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	result, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "u03:traversal", Context: execCtx,
		Operations: []blackboard.Operation{
			{OpID: "host", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:host"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "Host", "locator": "example.com", "status": "active", "scope_status": "in_scope"}}},
			{OpID: "service", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:service"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "service", "name": "HTTPS", "locator": "example.com:443", "status": "active", "scope_status": "in_scope"}}},
			{OpID: "endpoint", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:endpoint"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "endpoint", "name": "Admin", "locator": "https://example.com/admin", "status": "active", "scope_status": "in_scope"}}},
			{OpID: "service-host", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "service"}, To: blackboard.NodeRef{OpID: "host"}}},
			{OpID: "endpoint-service", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "endpoint"}, To: blackboard.NodeRef{OpID: "service"}}},
		},
	})
	if err != nil {
		t.Fatalf("seed traversal graph: %v", err)
	}
	rootID := result.Operations[0].NodeID
	read := blackboard.NewBlackboardReadService(graph.DBForTesting())
	envelope, err := read.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindGraphTraversalV1, GraphTraversal: &blackboard.GraphTraversalRequest{NodeID: rootID, Direction: "incoming", MaxDepth: 2, MaxNodes: 10}})
	if err != nil {
		t.Fatalf("read traversal: %v", err)
	}
	got := envelope.Result.(blackboard.GraphTraversalV1)
	if len(got.Nodes) != 2 || got.Nodes[0].Depth != 1 || got.Nodes[0].Node.Ref.StableKey != "entity:service" || got.Nodes[1].Depth != 2 || got.Nodes[1].Node.Ref.StableKey != "entity:endpoint" {
		t.Fatalf("breadth-first nodes = %#v", got.Nodes)
	}
	if len(got.Edges) != 2 || got.Edges[0].From.StableKey != "entity:endpoint" || got.Edges[0].To.StableKey != "entity:service" || got.Edges[1].From.StableKey != "entity:service" || got.Edges[1].To.StableKey != "entity:host" {
		t.Fatalf("direction-preserving edges = %#v", got.Edges)
	}
	if got.Limits.Truncated || got.Limits.ReachedDepth != 2 {
		t.Fatalf("unbounded traversal limits = %#v", got.Limits)
	}

	truncatedEnvelope, err := read.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindGraphTraversalV1, GraphTraversal: &blackboard.GraphTraversalRequest{NodeID: rootID, Direction: "incoming", MaxDepth: 5, MaxNodes: 1}})
	if err != nil {
		t.Fatalf("read truncated traversal: %v", err)
	}
	truncated := truncatedEnvelope.Result.(blackboard.GraphTraversalV1)
	if len(truncated.Nodes) != 1 || truncated.Nodes[0].Node.Ref.StableKey != "entity:service" || !truncated.Limits.Truncated || truncated.Limits.TruncationReason != "max_nodes" || truncated.Limits.ReachedDepth != 1 {
		t.Fatalf("truncated traversal = %#v", truncated)
	}
	depthEnvelope, err := read.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindGraphTraversalV1, GraphTraversal: &blackboard.GraphTraversalRequest{NodeID: rootID, Direction: "incoming", MaxDepth: 1, MaxNodes: 10}})
	if err != nil {
		t.Fatalf("read depth-bounded traversal: %v", err)
	}
	depthBounded := depthEnvelope.Result.(blackboard.GraphTraversalV1)
	if !depthBounded.Limits.Truncated || depthBounded.Limits.TruncationReason != "max_depth" || depthBounded.Limits.ReachedDepth != 1 {
		t.Fatalf("depth-bounded traversal limits = %#v", depthBounded.Limits)
	}

	_, err = read.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindGraphTraversalV1, GraphTraversal: &blackboard.GraphTraversalRequest{NodeID: rootID, Direction: "incoming", EdgeTypes: []blackboard.EdgeType{"invented"}}})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeInvalidQuery {
		t.Fatalf("unknown traversal edge type error = %#v", err)
	}

}
