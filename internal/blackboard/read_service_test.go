package blackboard_test

import (
	"context"
	"testing"

	"pentest/internal/blackboard"
)

// TestReadCursorPinsRevisionWhileConcurrentWriterCommits is the U01 first-red
// test. It proves pagination continues against the first page's graph revision
// even when a writer commits before the cursor is followed.
func TestReadCursorPinsRevisionWhileConcurrentWriterCommits(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	seedFacts(t, graph, execCtx, "fact:a", "fact:b", "fact:c")

	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	first, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       projectID,
		Kind:            blackboard.ReadKindRecordCollectionV1,
		RecordCollection: &blackboard.RecordCollectionRequest{
			NodeTypes: []blackboard.NodeType{blackboard.NodeTypeProjectFact},
			Sort:      blackboard.RecordSortStableKey,
			Limit:     2,
		},
	})
	if err != nil {
		t.Fatalf("read first page: %v", err)
	}
	firstResult := first.Result.(blackboard.RecordCollectionV1)
	if got, want := recordKeys(firstResult), []string{"fact:a", "fact:b"}; !equalStrings(got, want) {
		t.Fatalf("first page keys = %v want %v", got, want)
	}
	if firstResult.Page.NextCursor == "" {
		t.Fatal("first page next cursor is empty")
	}
	pinnedRevision := first.ObservedGraphRevision

	seedFacts(t, graph, execCtx, "fact:bb")

	second, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       projectID,
		Kind:            blackboard.ReadKindRecordCollectionV1,
		RecordCollection: &blackboard.RecordCollectionRequest{
			NodeTypes: []blackboard.NodeType{blackboard.NodeTypeProjectFact},
			Sort:      blackboard.RecordSortStableKey,
			Limit:     2,
			Cursor:    firstResult.Page.NextCursor,
		},
	})
	if err != nil {
		t.Fatalf("read second page: %v", err)
	}
	secondResult := second.Result.(blackboard.RecordCollectionV1)
	if second.ObservedGraphRevision != pinnedRevision {
		t.Fatalf("second page revision = %d want pinned %d", second.ObservedGraphRevision, pinnedRevision)
	}
	if got, want := recordKeys(secondResult), []string{"fact:c"}; !equalStrings(got, want) {
		t.Fatalf("second page keys = %v want %v", got, want)
	}
	if secondResult.Page.TotalItems != 3 {
		t.Fatalf("second page total_items = %d want 3", secondResult.Page.TotalItems)
	}
}

func seedFacts(t *testing.T, graph *blackboard.GraphService, execCtx blackboard.ExecutionContext, keys ...string) {
	t.Helper()
	operations := make([]blackboard.Operation, 0, len(keys))
	for i, key := range keys {
		operations = append(operations, blackboard.Operation{
			OpID: "fact-" + key,
			Kind: blackboard.OpCreateNode,
			Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: key},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
				"category":     "test",
				"summary":      key,
				"scope_status": "in_scope",
			}},
		})
		_ = i
	}
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:" + keys[0],
		Context:        execCtx,
		Operations:     operations,
	}); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
}

func recordKeys(result blackboard.RecordCollectionV1) []string {
	keys := make([]string, len(result.Items))
	for i, item := range result.Items {
		keys[i] = item.Ref.StableKey
	}
	return keys
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestReadCursorRejectsChangedProjectFilterSortLimitAndProjection(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	otherProjectID, _ := mustGraphProject(t, projects)
	seedFacts(t, graph, execCtx, "fact:a", "fact:b")
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())

	first, err := reads.Read(context.Background(), recordCollectionRequest(projectID, 1, ""))
	if err != nil {
		t.Fatalf("read first page: %v", err)
	}
	cursor := first.Result.(blackboard.RecordCollectionV1).Page.NextCursor
	if cursor == "" {
		t.Fatal("first page cursor is empty")
	}

	cases := []struct {
		name    string
		request blackboard.ReadRequest
	}{
		{name: "project", request: recordCollectionRequest(otherProjectID, 1, cursor)},
		{name: "limit", request: recordCollectionRequest(projectID, 2, cursor)},
		{name: "filter", request: func() blackboard.ReadRequest {
			req := recordCollectionRequest(projectID, 1, cursor)
			req.RecordCollection.NodeTypes = []blackboard.NodeType{blackboard.NodeTypeFinding}
			return req
		}()},
		{name: "sort", request: func() blackboard.ReadRequest {
			req := recordCollectionRequest(projectID, 1, cursor)
			req.RecordCollection.Sort = blackboard.RecordSortUpdatedDesc
			return req
		}()},
		{name: "projection", request: blackboard.ReadRequest{
			ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
			ProjectID:       projectID,
			Kind:            blackboard.ReadKindRecordHistoryV1,
			RecordHistory:   &blackboard.RecordHistoryRequest{NodeID: "node_1", Limit: 1, Cursor: cursor},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reads.Read(context.Background(), tc.request)
			assertReadErrorCode(t, err, blackboard.ErrCodeInvalidCursor)
		})
	}
}

func recordCollectionRequest(projectID string, limit int, cursor string) blackboard.ReadRequest {
	return blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       projectID,
		Kind:            blackboard.ReadKindRecordCollectionV1,
		RecordCollection: &blackboard.RecordCollectionRequest{
			NodeTypes: []blackboard.NodeType{blackboard.NodeTypeProjectFact},
			Sort:      blackboard.RecordSortStableKey,
			Limit:     limit,
			Cursor:    cursor,
		},
	}
}

func assertReadErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil want %s", want)
	}
	validation, ok := err.(*blackboard.ValidationError)
	if !ok {
		t.Fatalf("error type = %T want *blackboard.ValidationError: %v", err, err)
	}
	if validation.Code != want {
		t.Fatalf("error code = %q want %q", validation.Code, want)
	}
}

func TestReadResolveFollowsAliasWhileHistoryRequiresLiteralMergedIdentity(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:merge-fixture",
		Context:        execCtx,
		Operations: []blackboard.Operation{
			{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:canonical"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "canonical", "scope_status": "in_scope"}}},
			{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:old"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "source", "scope_status": "in_scope"}}},
		},
	})
	if err != nil {
		t.Fatalf("create merge fixture: %v", err)
	}
	canonicalID, sourceID := created.Operations[0].NodeID, created.Operations[1].NodeID
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:merge",
		Context:        execCtx,
		Operations: []blackboard.Operation{{
			OpID: "merge", Kind: blackboard.OpMergeNodes,
			Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{ID: sourceID}, Canonical: blackboard.NodeRef{ID: canonicalID}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1},
		}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	resolvedEnvelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       projectID,
		Kind:            blackboard.ReadKindRecordResolveV1,
		RecordResolve:   &blackboard.RecordResolveRequest{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:old"},
	})
	if err != nil {
		t.Fatalf("resolve alias: %v", err)
	}
	resolved := resolvedEnvelope.Result.(blackboard.RecordResolveV1)
	if resolved.Resolved.ID != canonicalID || resolved.ResolvedFromAlias == nil || *resolved.ResolvedFromAlias != "fact:old" {
		t.Fatalf("resolved alias = %+v want canonical %s", resolved, canonicalID)
	}

	_, err = reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       projectID,
		Kind:            blackboard.ReadKindRecordHistoryV1,
		RecordHistory:   &blackboard.RecordHistoryRequest{NodeID: sourceID},
	})
	assertReadErrorCode(t, err, blackboard.ErrCodeLiteralIdentityRequired)

	historyEnvelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       projectID,
		Kind:            blackboard.ReadKindRecordHistoryV1,
		RecordHistory:   &blackboard.RecordHistoryRequest{NodeID: sourceID, Literal: true},
	})
	if err != nil {
		t.Fatalf("read literal history: %v", err)
	}
	history := historyEnvelope.Result.(blackboard.RecordHistoryV1)
	if history.Record.ID != sourceID || len(history.Versions) != 2 || history.Merge == nil || history.Merge.ID != canonicalID {
		t.Fatalf("literal history = %+v", history)
	}
}

func TestReadAtRevisionReconstructsArchivedRowsAndCurrentDefaultHidesThem(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:archive-fixture",
		Context:        execCtx,
		Operations: []blackboard.Operation{
			{OpID: "keep", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:keep"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "keep", "scope_status": "in_scope"}}},
			{OpID: "archive", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:archive"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "archive", "scope_status": "in_scope"}}},
		},
	})
	if err != nil {
		t.Fatalf("create archive fixture: %v", err)
	}
	archivedID := created.Operations[1].NodeID
	deprecated, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:deprecate",
		Context:        execCtx,
		Operations: []blackboard.Operation{{
			OpID: "deprecate", Kind: blackboard.OpTransitionNode,
			Node:       blackboard.NodeRef{ID: archivedID},
			Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "deprecated"},
		}},
	})
	if err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:archive",
		Context:        execCtx,
		Operations: []blackboard.Operation{{
			OpID: "archive", Kind: blackboard.OpSetDisposition,
			Node:        blackboard.NodeRef{ID: archivedID},
			Disposition: blackboard.SetDispositionInput{ExpectedVersion: deprecated.Operations[0].NodeVersion, Disposition: blackboard.DispositionArchived},
		}},
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	current, err := reads.Read(context.Background(), recordCollectionRequest(projectID, 50, ""))
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if got, want := recordKeys(current.Result.(blackboard.RecordCollectionV1)), []string{"fact:keep"}; !equalStrings(got, want) {
		t.Fatalf("current default keys = %v want %v", got, want)
	}

	withArchivedRequest := recordCollectionRequest(projectID, 50, "")
	withArchivedRequest.RecordCollection.Dispositions = []blackboard.Disposition{blackboard.DispositionMain, blackboard.DispositionArchived}
	withArchived, err := reads.Read(context.Background(), withArchivedRequest)
	if err != nil {
		t.Fatalf("read with archived: %v", err)
	}
	if got, want := recordKeys(withArchived.Result.(blackboard.RecordCollectionV1)), []string{"fact:archive", "fact:keep"}; !equalStrings(got, want) {
		t.Fatalf("explicit archived keys = %v want %v", got, want)
	}

	historicalRequest := recordCollectionRequest(projectID, 50, "")
	historicalRequest.AtRevision = &created.GraphRevision
	historical, err := reads.Read(context.Background(), historicalRequest)
	if err != nil {
		t.Fatalf("read historical: %v", err)
	}
	if got, want := recordKeys(historical.Result.(blackboard.RecordCollectionV1)), []string{"fact:archive", "fact:keep"}; !equalStrings(got, want) {
		t.Fatalf("historical keys = %v want %v", got, want)
	}
}

func TestReadLexicalSearchUsesNormalizedFixedRanking(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:search-fixture",
		Context:        execCtx,
		Operations: []blackboard.Operation{
			{OpID: "exact-key", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "alpha"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "last label", "scope_status": "in_scope"}}},
			{OpID: "key-prefix", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "alpha-prefix"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "prefix key", "scope_status": "in_scope"}}},
			{OpID: "exact-label", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:exact-label"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "Alpha", "scope_status": "in_scope"}}},
			{OpID: "label-prefix", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:label-prefix"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "Alpha service", "scope_status": "in_scope"}}},
			{OpID: "body", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:body"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "body match", "body": "contains alpha evidence", "scope_status": "in_scope"}}},
		},
	})
	if err != nil {
		t.Fatalf("seed search fixture: %v", err)
	}
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	envelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       projectID,
		Kind:            blackboard.ReadKindRecordCollectionV1,
		RecordCollection: &blackboard.RecordCollectionRequest{
			NodeTypes: []blackboard.NodeType{blackboard.NodeTypeProjectFact},
			Query:     "  ＡＬＰＨＡ  ",
			Limit:     50,
		},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := recordKeys(envelope.Result.(blackboard.RecordCollectionV1)), []string{"alpha", "alpha-prefix", "fact:exact-label", "fact:label-prefix", "fact:body"}; !equalStrings(got, want) {
		t.Fatalf("search order = %v want %v", got, want)
	}
}

func TestReadHistoryCursorPagesVersionsWithoutDuplicateOrGap(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:history-create",
		Context:        execCtx,
		Operations:     []blackboard.Operation{{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:history"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "history", "scope_status": "in_scope"}}}},
	})
	if err != nil {
		t.Fatalf("create history fixture: %v", err)
	}
	nodeID := created.Operations[0].NodeID
	deprecated, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:history-deprecated",
		Context:        execCtx,
		Operations: []blackboard.Operation{{
			OpID: "transition", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{ID: nodeID},
			Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "deprecated", Summary: "deprecated"},
		}},
	})
	if err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u01:history-archive",
		Context:        execCtx,
		Operations: []blackboard.Operation{{
			OpID: "archive", Kind: blackboard.OpSetDisposition, Node: blackboard.NodeRef{ID: nodeID},
			Disposition: blackboard.SetDispositionInput{ExpectedVersion: deprecated.Operations[0].NodeVersion, Disposition: blackboard.DispositionArchived},
		}},
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	cursor := ""
	got := []int{}
	for {
		envelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
			ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
			ProjectID:       projectID,
			Kind:            blackboard.ReadKindRecordHistoryV1,
			RecordHistory:   &blackboard.RecordHistoryRequest{NodeID: nodeID, Literal: true, Limit: 1, Cursor: cursor},
		})
		if err != nil {
			t.Fatalf("read history page: %v", err)
		}
		result := envelope.Result.(blackboard.RecordHistoryV1)
		if len(result.Versions) != 1 {
			t.Fatalf("history page versions = %d want 1", len(result.Versions))
		}
		got = append(got, result.Versions[0].Version)
		cursor = result.Page.NextCursor
		if cursor == "" {
			break
		}
	}
	if want := []int{3, 2, 1}; !equalInts(got, want) {
		t.Fatalf("history versions = %v want %v", got, want)
	}
}

func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestReadPaginationHasNoDuplicateOrGapForEqualSortValues(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	seedFacts(t, graph, execCtx, "fact:a", "fact:b", "fact:c")
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())

	cursor := ""
	keys := []string{}
	for page := 0; page < 4; page++ {
		envelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
			ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
			ProjectID:       projectID,
			Kind:            blackboard.ReadKindRecordCollectionV1,
			RecordCollection: &blackboard.RecordCollectionRequest{
				NodeTypes: []blackboard.NodeType{blackboard.NodeTypeProjectFact},
				Sort:      blackboard.RecordSortUpdatedDesc,
				Limit:     1,
				Cursor:    cursor,
			},
		})
		if err != nil {
			t.Fatalf("read page %d: %v", page, err)
		}
		result := envelope.Result.(blackboard.RecordCollectionV1)
		if len(result.Items) != 1 {
			t.Fatalf("page %d items = %d want 1", page, len(result.Items))
		}
		keys = append(keys, result.Items[0].Ref.StableKey)
		cursor = result.Page.NextCursor
		if cursor == "" {
			break
		}
	}
	if want := []string{"fact:a", "fact:b", "fact:c"}; !equalStrings(keys, want) {
		t.Fatalf("paged keys = %v want %v", keys, want)
	}
}

func TestReadRejectsUnknownRecordFilters(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, _ := mustGraphProject(t, projects)
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	for _, request := range []blackboard.RecordCollectionRequest{
		{NodeTypes: []blackboard.NodeType{"mystery"}},
		{Dispositions: []blackboard.Disposition{"missing"}},
	} {
		_, err := reads.Read(context.Background(), blackboard.ReadRequest{
			ProtocolVersion:  blackboard.BlackboardReadProtocolVersion,
			ProjectID:        projectID,
			Kind:             blackboard.ReadKindRecordCollectionV1,
			RecordCollection: &request,
		})
		assertReadErrorCode(t, err, blackboard.ErrCodeInvalidQuery)
	}
}

// TestBlackboardWorkAttentionOrdersCriticalHealthBeforeFrontierAndActiveWork is
// the U02 first-red test. It proves the shared Work projection applies the
// canonical attention classes instead of falling back to timestamp ordering.
func TestBlackboardWorkAttentionOrdersCriticalHealthBeforeFrontierAndActiveWork(t *testing.T) {
	graph, projects, _ := newGraphServices(t)
	projectID, execCtx := mustGraphProject(t, projects)
	created, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "u02:attention", Context: execCtx,
		Operations: []blackboard.Operation{
			{OpID: "critical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:critical-health"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Critical health subject", "status": "unconfirmed", "target": "example.com"}}},
			{OpID: "warning", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:warning-health"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Warning health subject", "status": "unconfirmed", "target": "example.com"}}},
			{OpID: "frontier", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:frontier"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "What is exposed?", "status": "open"}}},
			{OpID: "attempt", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:open"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"status": "open", "summary": "Enumerate services"}}},
			{OpID: "tests", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeTests, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{OpID: "frontier"}}},
		},
	})
	if err != nil {
		t.Fatalf("seed attention fixture: %v", err)
	}
	criticalID := created.Operations[0].NodeID
	warningID := created.Operations[1].NodeID
	if _, err := graph.DBForTesting().Exec(`INSERT INTO blackboard_health_runs(project_id,run_id,checked_graph_revision,checked_state_hash,checked_projection_hash,checker_version,status,artifact_scan_status,started_at,completed_at,metrics_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, projectID, "health:u02", created.GraphRevision, created.ResultingStateHash, "projection", "blackboard_health_v1", "critical", "complete", created.RecordedAt, created.RecordedAt, `{}`); err != nil {
		t.Fatalf("seed Health run: %v", err)
	}
	for _, result := range []struct{ fingerprint, severity, subjectID string }{{"critical", "critical", criticalID}, {"warning", "warning", warningID}} {
		if _, err := graph.DBForTesting().Exec(`INSERT INTO blackboard_health_results(project_id,run_id,fingerprint,code,severity,subject_kind,subject_id,details_json) VALUES(?,?,?,?,?,?,?,?)`, projectID, "health:u02", result.fingerprint, "fixture", result.severity, "node", result.subjectID, `{}`); err != nil {
			t.Fatalf("seed Health result: %v", err)
		}
	}

	envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       projectID,
		Kind:            blackboard.ReadKindBlackboardWorkV1,
		BlackboardWork:  &blackboard.BlackboardWorkRequest{},
	})
	if err != nil {
		t.Fatalf("read Blackboard Work: %v", err)
	}
	work := envelope.Result.(blackboard.BlackboardWorkViewV1)
	got := recordKeys(blackboard.RecordCollectionV1{Items: work.Attention.Items})
	want := []string{"finding:critical-health", "finding:warning-health", "objective:frontier", "attempt:open"}
	if !equalStrings(got, want) {
		t.Fatalf("attention order = %v want %v", got, want)
	}
}
