package blackboard_test

import (
	"context"
	"errors"
	"testing"

	"pentest/internal/blackboard"
)

func TestGraphExplorerRejectsOversizedGraphWithExactCountsAndKeepsTableCanvasParity(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: 1, IdempotencyKey: "u03:explorer", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "host", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:host"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "Host", "locator": "example.com", "status": "active", "scope_status": "in_scope"}}},
		{OpID: "service", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:service"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "service", "name": "HTTPS", "locator": "example.com:443", "status": "active", "scope_status": "in_scope"}}},
		{OpID: "endpoint", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:endpoint"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "endpoint", "name": "Admin", "locator": "https://example.com/admin", "status": "active", "scope_status": "in_scope"}}},
		{OpID: "service-host", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "service"}, To: blackboard.NodeRef{OpID: "host"}}},
		{OpID: "endpoint-service", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "endpoint"}, To: blackboard.NodeRef{OpID: "service"}}},
	}})
	if err != nil {
		t.Fatalf("seed Explorer graph: %v", err)
	}
	read := blackboard.NewBlackboardReadService(graph.DBForTesting())
	_, err = read.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindGraphExplorerV1, GraphExplorer: &blackboard.GraphExplorerRequest{MaxNodes: 2, MaxEdges: 10}})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeProjectionTooLarge || validation.Details["node_count"] != 3 || validation.Details["edge_count"] != 2 {
		t.Fatalf("oversized Explorer error = %#v", err)
	}

	envelope, err := read.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: projectID, Kind: blackboard.ReadKindGraphExplorerV1, GraphExplorer: &blackboard.GraphExplorerRequest{MaxNodes: 10, MaxEdges: 10}})
	if err != nil {
		t.Fatalf("read Explorer: %v", err)
	}
	got := envelope.Result.(blackboard.GraphExplorerV1)
	if len(got.Graph.Nodes) != len(got.Table.Nodes) || len(got.Graph.Edges) != len(got.Table.Edges) || len(got.Graph.Edges) != 2 {
		t.Fatalf("Explorer parity counts = %#v", got)
	}
	for i := range got.Graph.Nodes {
		if got.Graph.Nodes[i].Row.Ref != got.Table.Nodes[i].Ref {
			t.Fatalf("node parity at %d: canvas=%#v table=%#v", i, got.Graph.Nodes[i].Row.Ref, got.Table.Nodes[i].Ref)
		}
	}
	for i := range got.Graph.Edges {
		if got.Graph.Edges[i].ID != got.Table.Edges[i].ID || got.Graph.Edges[i].From != got.Table.Edges[i].From || got.Graph.Edges[i].To != got.Table.Edges[i].To {
			t.Fatalf("edge parity at %d: canvas=%#v table=%#v", i, got.Graph.Edges[i], got.Table.Edges[i])
		}
	}
	if got.Graph.Edges[0].From.StableKey != "entity:endpoint" || got.Graph.Edges[0].To.StableKey != "entity:service" {
		t.Fatalf("Explorer reversed edge direction: %#v", got.Graph.Edges[0])
	}
}
