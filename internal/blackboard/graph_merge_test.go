package blackboard_test

import (
	"context"
	"errors"
	"testing"

	"pentest/internal/blackboard"
)

// TestMergePreservesSourceHistoryAliasesEdgesAndCanonicalIdentity is C08's
// first-red test at BlackboardGraphService.Apply. Merge is explicit and
// non-destructive: canonical properties win, source history remains literal,
// the source key becomes a one-hop alias, and active source edges are rewired.
func TestMergePreservesSourceHistoryAliasesEdgesAndCanonicalIdentity(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)

	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "c08:create-merge-fixture",
		Context:        execCtx,
		Operations: []blackboard.Operation{
			{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "dns:canonical.example"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "dns", Summary: "canonical summary", ScopeStatus: blackboard.ScopeStatusInScope}}},
			{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "dns:duplicate.example"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "dns", Summary: "source summary must not overwrite", ScopeStatus: blackboard.ScopeStatusInScope}}},
			{OpID: "entity", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:example.com"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "example.com", "locator": "example.com", "scope_status": "in_scope"}}},
			{OpID: "source-about", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{OpID: "source"}, To: blackboard.NodeRef{OpID: "entity"}, Summary: "source evidence"}},
		},
	})
	if err != nil {
		t.Fatalf("create merge fixture: %v", err)
	}
	canonicalID := created.Operations[0].NodeID
	sourceID := created.Operations[1].NodeID
	edgeID := created.Operations[3].EdgeID

	merged, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "c08:merge-source",
		Context:        execCtx,
		Operations: []blackboard.Operation{{
			OpID: "merge",
			Kind: blackboard.OpMergeNodes,
			Merge: blackboard.MergeNodesInput{
				Source:                   blackboard.NodeRef{ID: sourceID},
				Canonical:                blackboard.NodeRef{ID: canonicalID},
				SourceExpectedVersion:    1,
				CanonicalExpectedVersion: 1,
			},
		}},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := merged.Operations[0].NodeID; got != canonicalID {
		t.Fatalf("canonical identity changed: got %q want %q", got, canonicalID)
	}

	throughAlias, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeProjectFact, Key: "dns:duplicate.example"})
	if err != nil {
		t.Fatalf("read through source alias: %v", err)
	}
	if throughAlias.Node.ID != canonicalID || throughAlias.ResolvedFromAlias != "dns:duplicate.example" {
		t.Fatalf("alias did not resolve one hop to canonical: %+v", throughAlias)
	}
	if throughAlias.Node.ProjectFact.Summary != "canonical summary" {
		t.Fatalf("source properties overwrote canonical properties: %+v", throughAlias.Node.ProjectFact)
	}

	literal, err := graph.ReadLiteralNode(context.Background(), blackboard.ReadLiteralNodeRequest{ProjectID: projectID, NodeID: sourceID})
	if err != nil {
		t.Fatalf("read literal merged source: %v", err)
	}
	if literal.Node.Disposition != blackboard.DispositionMerged || literal.Node.MergeTargetID != canonicalID || len(literal.Versions) != 2 {
		t.Fatalf("source identity/history was not preserved: %+v", literal)
	}

	rewired, err := graph.ReadEdge(context.Background(), blackboard.ReadEdgeRequest{ProjectID: projectID, EdgeID: edgeID})
	if err != nil {
		t.Fatalf("read rewired edge: %v", err)
	}
	if rewired.FromNodeID != canonicalID || rewired.ToNodeID != created.Operations[2].NodeID || rewired.State != "active" || rewired.Version != 2 {
		t.Fatalf("source edge was not rewired: %+v", rewired)
	}
}

func TestAliasWritesReportRedirectAndCreateNeverUpdatesThroughAlias(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	_, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:alias-fixture", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:canonical"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "canonical", Body: "operator basis", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:old"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "duplicate", ScopeStatus: blackboard.ScopeStatusInScope}}},
	}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:alias-merge", Context: execCtx, Operations: []blackboard.Operation{{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{ID: created.Operations[1].NodeID}, Canonical: blackboard.NodeRef{ID: created.Operations[0].NodeID}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}}}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	updated, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:write-alias", Context: execCtx, Operations: []blackboard.Operation{{OpID: "deprecate", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:old"}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "deprecated"}}}})
	if err != nil {
		t.Fatalf("write through alias: %v", err)
	}
	if updated.Operations[0].ResolvedFromAlias != "fact:old" || updated.Operations[0].NodeID != created.Operations[0].NodeID {
		t.Fatalf("write redirect not reported: %+v", updated.Operations[0])
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:create-through-alias", Context: execCtx, Operations: []blackboard.Operation{{OpID: "duplicate-create", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:old"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "must conflict", ScopeStatus: blackboard.ScopeStatusInScope}}}}})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeNodeKeyConflict {
		t.Fatalf("expected node_key_conflict through alias, got %v", err)
	}
}

func TestArchiveGuardsRetireEdgesAndRestoreManifestUsesCurrentRedirects(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:archive-fixture", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:archive"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "archive only after deprecation", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "old-entity", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:old"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "old", "locator": "old.example", "scope_status": "in_scope", "status": "retired"}}},
		{OpID: "canonical-entity", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:canonical"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "canonical", "locator": "canonical.example", "scope_status": "in_scope", "status": "retired"}}},
		{OpID: "about", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{OpID: "fact"}, To: blackboard.NodeRef{OpID: "old-entity"}, Summary: "historical topology"}},
	}})
	if err != nil {
		t.Fatalf("create archive fixture: %v", err)
	}
	factID, oldEntityID, canonicalEntityID := created.Operations[0].NodeID, created.Operations[1].NodeID, created.Operations[2].NodeID

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:guard-live-truth", Context: execCtx, Operations: []blackboard.Operation{{OpID: "archive", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: factID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 1, Disposition: blackboard.DispositionArchived}}}})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeArchiveGuardFailed {
		t.Fatalf("expected archive guard for Current Truth, got %v", err)
	}

	deprecated, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:deprecate-before-archive", Context: execCtx, Operations: []blackboard.Operation{{OpID: "deprecate", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: factID}, Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "deprecated"}}}})
	if err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:archive", Context: execCtx, Operations: []blackboard.Operation{{OpID: "archive", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: factID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: deprecated.Operations[0].NodeVersion, Disposition: blackboard.DispositionArchived}}}})
	if err != nil {
		t.Fatalf("archive deprecated fact: %v", err)
	}
	retired, err := graph.ReadEdge(context.Background(), blackboard.ReadEdgeRequest{ProjectID: projectID, EdgeID: created.Operations[3].EdgeID})
	if err != nil || retired.State != "retired" {
		t.Fatalf("touching edge not retired atomically: edge=%+v err=%v", retired, err)
	}

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:merge-entity-after-archive", Context: execCtx, Operations: []blackboard.Operation{{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{ID: oldEntityID}, Canonical: blackboard.NodeRef{ID: canonicalEntityID}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}}}})
	if err != nil {
		t.Fatalf("merge entity after archive: %v", err)
	}
	execCtx = blackboard.SystemRestoreExecutionContext(projectID, execCtx.ProjectKind, execCtx.ActorID, blackboard.RestoreManifest{ID: "restore:archive", Nodes: []string{factID}, Edges: []blackboard.RestoreEdge{{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{ID: factID}, To: blackboard.NodeRef{ID: oldEntityID}, Summary: "restored topology"}}})
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:restore", Context: execCtx, Operations: []blackboard.Operation{{OpID: "restore", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: factID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: deprecated.Operations[0].NodeVersion + 1, Disposition: blackboard.DispositionMain, RestoreManifestID: "restore:archive"}}}})
	if err != nil {
		t.Fatalf("restore from manifest: %v", err)
	}
	restored, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeProjectFact, Key: "fact:archive"})
	if err != nil || restored.Node.Disposition != blackboard.DispositionMain {
		t.Fatalf("fact not restored: %+v err=%v", restored, err)
	}
	restoredEdge, err := graph.ReadActiveEdge(context.Background(), blackboard.ReadActiveEdgeRequest{ProjectID: projectID, EdgeType: blackboard.EdgeTypeAbout, FromNodeID: factID, ToNodeID: canonicalEntityID})
	if err != nil || restoredEdge.State != "active" {
		t.Fatalf("restore did not resolve merged endpoint: edge=%+v err=%v", restoredEdge, err)
	}
}

func TestDuplicateFingerprintsAreDeterministicAndNeverAutoMerge(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:duplicates", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "one", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:one"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "Ａdmin—Portal", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "two", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:two"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "admin portal", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "different", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:different"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "dns", Summary: "admin portal", ScopeStatus: blackboard.ScopeStatusInScope}}},
	}})
	if err != nil {
		t.Fatalf("create duplicates: %v", err)
	}
	candidates, err := graph.DuplicateCandidates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("duplicate candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].NodeType != blackboard.NodeTypeProjectFact || len(candidates[0].NodeIDs) != 2 || candidates[0].NodeIDs[0] != created.Operations[0].NodeID || candidates[0].NodeIDs[1] != created.Operations[1].NodeID {
		t.Fatalf("unexpected deterministic candidates: %+v", candidates)
	}
	for _, key := range []string{"fact:one", "fact:two"} {
		read, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeProjectFact, Key: key})
		if err != nil || read.Node.Disposition != blackboard.DispositionMain || read.ResolvedFromAlias != "" {
			t.Fatalf("duplicate candidate auto-merged %s: %+v err=%v", key, read, err)
		}
	}
}

func TestMergeCollapsesDuplicateEdgesRetiresSelfEdgesAndFlattensAliases(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:edge-collapse", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:canonical-edge"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "canonical", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:source-edge"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "source", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "entity", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:edge-collapse"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "edge-collapse", "locator": "edge-collapse.example", "scope_status": "in_scope"}}},
		{OpID: "canonical-about", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{OpID: "canonical"}, To: blackboard.NodeRef{OpID: "entity"}, Summary: "older canonical summary"}},
		{OpID: "source-about", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{OpID: "source"}, To: blackboard.NodeRef{OpID: "entity"}, Summary: "newer source summary"}},
		{OpID: "self-after-merge", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupports, From: blackboard.NodeRef{OpID: "source"}, To: blackboard.NodeRef{OpID: "canonical"}, Summary: "must retire"}},
	}})
	if err != nil {
		t.Fatalf("create collapse fixture: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:edge-collapse-merge", Context: execCtx, Operations: []blackboard.Operation{{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{ID: created.Operations[1].NodeID}, Canonical: blackboard.NodeRef{ID: created.Operations[0].NodeID}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}}}})
	if err != nil {
		t.Fatalf("merge with duplicate/self edges: %v", err)
	}
	winner, err := graph.ReadEdge(context.Background(), blackboard.ReadEdgeRequest{ProjectID: projectID, EdgeID: created.Operations[3].EdgeID})
	if err != nil || winner.State != "active" || winner.Version != 2 || winner.Summary != "newer source summary" {
		t.Fatalf("canonical edge did not win with latest summary: %+v err=%v", winner, err)
	}
	loser, err := graph.ReadEdge(context.Background(), blackboard.ReadEdgeRequest{ProjectID: projectID, EdgeID: created.Operations[4].EdgeID})
	if err != nil || loser.State != "retired" || loser.Version != 2 {
		t.Fatalf("duplicate edge not retired: %+v err=%v", loser, err)
	}
	self, err := graph.ReadEdge(context.Background(), blackboard.ReadEdgeRequest{ProjectID: projectID, EdgeID: created.Operations[5].EdgeID})
	if err != nil || self.State != "retired" || self.Version != 2 {
		t.Fatalf("self edge not retired: %+v err=%v", self, err)
	}

	third, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:third-canonical", Context: execCtx, Operations: []blackboard.Operation{{OpID: "third", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:third"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "final canonical", ScopeStatus: blackboard.ScopeStatusInScope}}}}})
	if err != nil {
		t.Fatalf("create third canonical: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:flatten-aliases", Context: execCtx, Operations: []blackboard.Operation{{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{ID: created.Operations[0].NodeID}, Canonical: blackboard.NodeRef{ID: third.Operations[0].NodeID}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}}}})
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	for _, key := range []string{"fact:source-edge", "fact:canonical-edge"} {
		read, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeProjectFact, Key: key})
		if err != nil || read.Node.ID != third.Operations[0].NodeID || read.ResolvedFromAlias != key {
			t.Fatalf("alias chain not flattened for %s: %+v err=%v", key, read, err)
		}
	}
}

func TestMergeRejectsRewiredCycleWithoutPartialWrites(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:merge-cycle-fixture", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "network:canonical"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "network", "name": "canonical", "locator": "10.0.0.0/24", "scope_status": "in_scope"}}},
		{OpID: "middle", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "network:middle"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "network", "name": "middle", "locator": "10.0.0.0/16", "scope_status": "in_scope"}}},
		{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "network:source"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "network", "name": "source", "locator": "10.0.0.0/8", "scope_status": "in_scope"}}},
		{OpID: "canonical-middle", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "canonical"}, To: blackboard.NodeRef{OpID: "middle"}}},
		{OpID: "middle-source", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "middle"}, To: blackboard.NodeRef{OpID: "source"}}},
	}})
	if err != nil {
		t.Fatalf("create cycle fixture: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:merge-cycle", Context: execCtx, Operations: []blackboard.Operation{{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{ID: created.Operations[2].NodeID}, Canonical: blackboard.NodeRef{ID: created.Operations[0].NodeID}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}}}})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeMergeConflict {
		t.Fatalf("expected merge_conflict, got %v", err)
	}
	read, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeEntity, Key: "network:source"})
	if err != nil || read.Node.ID != created.Operations[2].NodeID || read.ResolvedFromAlias != "" || read.Node.Disposition != blackboard.DispositionMain {
		t.Fatalf("failed merge left partial alias/disposition: %+v err=%v", read, err)
	}
}

func TestSameBatchMergeObservesPriorCreatesAndEdges(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	result, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:same-batch-merge", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:batch-canonical"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "canonical", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:batch-source"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "source", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "entity", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:batch"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "batch", "locator": "batch.example", "scope_status": "in_scope"}}},
		{OpID: "source-about", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{OpID: "source"}, To: blackboard.NodeRef{OpID: "entity"}}},
		{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{OpID: "source"}, Canonical: blackboard.NodeRef{OpID: "canonical"}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}},
	}})
	if err != nil {
		t.Fatalf("same-batch merge: %v", err)
	}
	alias, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectID, NodeType: blackboard.NodeTypeProjectFact, Key: "fact:batch-source"})
	if err != nil || alias.Node.ID != result.Operations[0].NodeID || alias.ResolvedFromAlias != "fact:batch-source" {
		t.Fatalf("same-batch alias missing: %+v err=%v", alias, err)
	}
	edge, err := graph.ReadEdge(context.Background(), blackboard.ReadEdgeRequest{ProjectID: projectID, EdgeID: result.Operations[3].EdgeID})
	if err != nil || edge.FromNodeID != result.Operations[0].NodeID || edge.Version != 2 {
		t.Fatalf("same-batch edge not rewired: %+v err=%v", edge, err)
	}
}

func TestRestoreMultipleArchivedNodesRecreatesInternalTopology(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:multi-restore-fixture", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "child", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "network:restore-child"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "network", "name": "child", "locator": "10.1.0.0/16", "scope_status": "in_scope", "status": "retired"}}},
		{OpID: "parent", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "network:restore-parent"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "network", "name": "parent", "locator": "10.0.0.0/8", "scope_status": "in_scope", "status": "retired"}}},
		{OpID: "part-of", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{OpID: "child"}, To: blackboard.NodeRef{OpID: "parent"}}},
	}})
	if err != nil {
		t.Fatalf("create restore fixture: %v", err)
	}
	childID, parentID := created.Operations[0].NodeID, created.Operations[1].NodeID
	archived, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:archive-pair", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "archive-child", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: childID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 1, Disposition: blackboard.DispositionArchived}},
		{OpID: "archive-parent", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: parentID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 1, Disposition: blackboard.DispositionArchived}},
	}})
	if err != nil {
		t.Fatalf("archive pair: %v", err)
	}
	execCtx = blackboard.SystemRestoreExecutionContext(projectID, execCtx.ProjectKind, execCtx.ActorID, blackboard.RestoreManifest{ID: "restore:pair", Nodes: []string{childID, parentID}, Edges: []blackboard.RestoreEdge{{EdgeType: blackboard.EdgeTypePartOf, From: blackboard.NodeRef{ID: childID}, To: blackboard.NodeRef{ID: parentID}}}})
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:restore-pair", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "restore-child", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: childID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: archived.Operations[0].NodeVersion, Disposition: blackboard.DispositionMain, RestoreManifestID: "restore:pair"}},
		{OpID: "restore-parent", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: parentID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: archived.Operations[1].NodeVersion, Disposition: blackboard.DispositionMain, RestoreManifestID: "restore:pair"}},
	}})
	if err != nil {
		t.Fatalf("restore pair: %v", err)
	}
	if _, err := graph.ReadActiveEdge(context.Background(), blackboard.ReadActiveEdgeRequest{ProjectID: projectID, EdgeType: blackboard.EdgeTypePartOf, FromNodeID: childID, ToNodeID: parentID}); err != nil {
		t.Fatalf("restored internal topology missing: %v", err)
	}
}

func TestMergeRejectsActiveEdgesIntoArchivedCanonical(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	_, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:archived-canonical-fixture", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:active-source"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "source", "locator": "source.example", "scope_status": "in_scope", "status": "retired"}}},
		{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:archived-canonical"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "canonical", "locator": "canonical.example", "scope_status": "in_scope", "status": "retired"}}},
		{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:touching"}, Create: blackboard.CreateNodeInput{Properties: blackboard.ProjectFactProperties{Category: "service", Summary: "touching", ScopeStatus: blackboard.ScopeStatusInScope}}},
		{OpID: "about", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{OpID: "fact"}, To: blackboard.NodeRef{OpID: "source"}}},
	}})
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:archive-canonical", Context: execCtx, Operations: []blackboard.Operation{{OpID: "archive", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: created.Operations[1].NodeID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 1, Disposition: blackboard.DispositionArchived}}}})
	if err != nil {
		t.Fatalf("archive canonical: %v", err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:reject-archived-canonical", Context: execCtx, Operations: []blackboard.Operation{{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{ID: created.Operations[0].NodeID}, Canonical: blackboard.NodeRef{ID: created.Operations[1].NodeID}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 2}}}})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeMergeConflict {
		t.Fatalf("expected merge_conflict, got %v", err)
	}
}

func TestDispositionNoOpKeepsRevisionAndCombinedReferenceMustAgree(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	_, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:reference-fixture", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "one", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:one"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "one", "locator": "one.example", "scope_status": "in_scope", "status": "retired"}}},
		{OpID: "two", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "host:two"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "two", "locator": "two.example", "scope_status": "in_scope", "status": "retired"}}},
	}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	noop, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:disposition-noop", Context: execCtx, Operations: []blackboard.Operation{{OpID: "noop", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: created.Operations[0].NodeID}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 1, Disposition: blackboard.DispositionMain}}}})
	if err != nil || noop.GraphRevision != created.GraphRevision || noop.Operations[0].Changed {
		t.Fatalf("disposition no-op advanced state: %+v err=%v", noop, err)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:mismatched-combined-ref", Context: execCtx, Operations: []blackboard.Operation{{OpID: "archive", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: created.Operations[0].NodeID, NodeType: blackboard.NodeTypeEntity, StableKey: "host:two"}, Disposition: blackboard.SetDispositionInput{ExpectedVersion: 1, Disposition: blackboard.DispositionArchived}}}})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeNodeNotFound {
		t.Fatalf("expected mismatched combined reference failure, got %v", err)
	}
}

func TestEntityDuplicateFingerprintCanonicalizesIPv6(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "c08:ipv6-duplicates", Context: execCtx, Operations: []blackboard.Operation{
		{OpID: "expanded", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "ip:expanded"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "ip_address", "name": "expanded", "locator": "2001:0db8:0000:0000:0000:0000:0000:0001", "scope_status": "in_scope"}}},
		{OpID: "compressed", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "ip:compressed"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "ip_address", "name": "compressed", "locator": "2001:db8::1", "scope_status": "in_scope"}}},
	}})
	if err != nil {
		t.Fatalf("create IPv6 entities: %v", err)
	}
	candidates, err := graph.DuplicateCandidates(context.Background(), projectID)
	if err != nil || len(candidates) != 1 || candidates[0].NodeType != blackboard.NodeTypeEntity || len(candidates[0].NodeIDs) != 2 {
		t.Fatalf("IPv6 duplicate fingerprint drift: %+v err=%v", candidates, err)
	}
}
