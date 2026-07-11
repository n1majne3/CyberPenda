package blackboard_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

// seedLegacyProjectionGraph builds a graph that exercises every legacy
// compatibility projection: facts at varied confidence/scope (including a
// deprecated one and an alias that merges into a canonical fact), findings in
// each status (confirmed / unconfirmed / false_positive), multi-target and
// missing evidence, and supports/contradicts/leads_to relations between facts.
// It returns the project id plus the deterministic stable keys the tests assert
// against.
func seedLegacyProjectionGraph(t *testing.T) (*blackboard.GraphService, *project.Service, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	projects := project.NewService(db)

	created, err := projects.Create("Acme External", "External perimeter assessment", project.Scope{
		Domains: []string{"example.com"},
	}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)
	sandbox, err := tasks.Create(task.CreateRequest{ProjectID: created.ID, Goal: "Enumerate perimeter", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	ctx := blackboard.SystemExecutionContext(created.ID, created.Kind, "u05-seed")
	ctx.TaskID = sandbox.ID
	ctx.Runner = string(task.RunnerSandbox)

	// All nodes, the alias merge, and edges are applied in one batch so OpID
	// references resolve. Ordering within the batch is preserved.
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u05:seed",
		Context:        ctx,
		Operations: []blackboard.Operation{
			{OpID: "entity", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:login"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "service", "name": "Login", "locator": "https://example.com/login", "scope_status": "in_scope"}}},
			{OpID: "confirmed-service", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:admin-service"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Admin service exposed", "body": "Administrative interface reachable", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "tentative-dns", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:dns-spray"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "dns", "summary": "DNS spray candidate", "body": "Possible NXDOMAIN spray", "confidence": "tentative", "scope_status": "in_scope"}}},
			{OpID: "deprecated-old", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:stale-note"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Stale note", "body": "Deprecated hypothesis", "confidence": "deprecated", "scope_status": "in_scope"}}},
			{OpID: "out-of-scope", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:partner-asset"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Partner asset", "body": "Out of engagement scope", "confidence": "confirmed", "scope_status": "out_of_scope"}}},
			{OpID: "alias-key", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:old-admin"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Old admin label", "body": "pre-merge", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "sqli", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:sqli-login"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "SQL injection in login", "description": "Auth bypass via SQLi", "status": "confirmed", "target": "https://example.com/login", "proof": "dumped users table", "impact": "auth bypass", "recommendation": "parameterize queries", "cvss_version": "3.1", "cvss_vector": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}}},
			{OpID: "header-weak", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:missing-header"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Missing security header", "description": "HSTS absent", "status": "unconfirmed", "target": "https://example.com", "proof": "", "impact": "downgrade risk", "recommendation": "set HSTS"}}},
			{OpID: "false-positive", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:wonky-banner"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "Wonky banner", "description": "False alarm", "status": "false_positive", "target": "https://example.com", "proof": "n/a", "impact": "none", "recommendation": "ignore"}}},
			{OpID: "evidence-multi", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:sqli-response"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"artifact_type": "http_exchange", "source_path": "captures/sqli.txt", "managed_path": "artifacts/evidence/sqli-response.txt", "sha256": "111aa9a2bbc2dd417859723c4073f7f99e3a3a4bb21a86e9b6c9d6e0f0a1b2c3", "media_type": "text/plain", "summary": "Captured auth bypass", "status": "available"}}},
			{OpID: "evidence-missing", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:lost-trace"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"artifact_type": "log", "source_path": "", "managed_path": "missing://evidence/lost-trace", "sha256": "", "summary": "Lost trace", "status": "missing"}}},
			// alias: old-admin merges into admin-service.
			{OpID: "merge-alias", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{OpID: "alias-key"}, Canonical: blackboard.NodeRef{OpID: "confirmed-service"}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}},
			// evidence -> finding + fact (multi-target).
			{OpID: "evidences-finding", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence-multi"}, To: blackboard.NodeRef{OpID: "sqli"}, Summary: "proves injection"}},
			{OpID: "evidences-fact", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence-multi"}, To: blackboard.NodeRef{OpID: "confirmed-service"}, Summary: "corroborates admin exposure"}},
			{OpID: "evidences-missing", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence-missing"}, To: blackboard.NodeRef{OpID: "sqli"}, Summary: "missing proof"}},
			// fact -> fact relations where admin-service is the source.
			{OpID: "supports", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupports, From: blackboard.NodeRef{OpID: "confirmed-service"}, To: blackboard.NodeRef{OpID: "tentative-dns"}, Summary: "admin exposure supports dns spray"}},
			{OpID: "leads-to", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeLeadsTo, From: blackboard.NodeRef{OpID: "confirmed-service"}, To: blackboard.NodeRef{OpID: "out-of-scope"}, Summary: "leads to partner asset review"}},
			{OpID: "contradicts", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeContradicts, From: blackboard.NodeRef{OpID: "out-of-scope"}, To: blackboard.NodeRef{OpID: "confirmed-service"}, Summary: "partner scope contradicts"}},
		},
	}); err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	return graph, projects, created.ID
}

func readLegacy[V any](t *testing.T, graph *blackboard.GraphService, projectID string, kind blackboard.ReadKind, req any) V {
	t.Helper()
	request := blackboard.ReadRequest{ProtocolVersion: blackboard.BlackboardReadProtocolVersion, ProjectID: projectID, Kind: kind}
	switch r := req.(type) {
	case blackboard.LegacyFactIndexRequest:
		request.LegacyFactIndex = &r
	case blackboard.LegacyFactDetailRequest:
		request.LegacyFactDetail = &r
	case blackboard.LegacyFactVersionsRequest:
		request.LegacyFactVersions = &r
	case blackboard.LegacyFactRelationsRequest:
		request.LegacyFactRelations = &r
	case blackboard.LegacyFindingCollectionRequest:
		request.LegacyFindingCollection = &r
	case blackboard.LegacyFindingVersionsRequest:
		request.LegacyFindingVersions = &r
	case blackboard.LegacyEvidenceCollectionRequest:
		request.LegacyEvidenceCollection = &r
	default:
		t.Fatalf("unsupported legacy request type %T", req)
	}
	envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), request)
	if err != nil {
		t.Fatalf("Read(%s): %v", kind, err)
	}
	if envelope.Projection != string(kind) {
		t.Fatalf("Read(%s) projection = %q", kind, envelope.Projection)
	}
	result, ok := envelope.Result.(V)
	if !ok {
		t.Fatalf("Read(%s) result type = %T", kind, envelope.Result)
	}
	return result
}

func TestLegacyFactFindingEvidenceGoldenResponsesComeOnlyFromGraphReadService(t *testing.T) {
	graph, _, projectID := seedLegacyProjectionGraph(t)
	deprecatedFalse := false

	t.Run("fact_index_excludes_deprecated_and_orders_current_truth", func(t *testing.T) {
		index := readLegacy[blackboard.LegacyFactIndexV1](t, graph, projectID, blackboard.ReadKindLegacyFactIndexV1, blackboard.LegacyFactIndexRequest{IncludeDeprecated: &deprecatedFalse})
		// Current Truth only: confirmed-service, out-of-scope, tentative-dns
		// (deprecated + merged alias excluded). Order §6.2: confidence, scope_status,
		// category, stable_key, id.
		if len(index.Facts) != 3 {
			t.Fatalf("fact index count = %d, want 3 (%+v)", len(index.Facts), index.Facts)
		}
		if index.Facts[0].FactKey != "fact:admin-service" || index.Facts[0].Confidence != "confirmed" {
			t.Fatalf("first fact = %+v", index.Facts[0])
		}
		if index.Facts[1].FactKey != "fact:partner-asset" || index.Facts[1].ScopeStatus != "out_of_scope" {
			t.Fatalf("second fact = %+v", index.Facts[1])
		}
		if index.Facts[2].FactKey != "fact:dns-spray" || index.Facts[2].Confidence != "tentative" {
			t.Fatalf("third fact = %+v", index.Facts[2])
		}
	})

	t.Run("fact_index_include_deprecated_surfaces_deprecated", func(t *testing.T) {
		include := true
		index := readLegacy[blackboard.LegacyFactIndexV1](t, graph, projectID, blackboard.ReadKindLegacyFactIndexV1, blackboard.LegacyFactIndexRequest{IncludeDeprecated: &include})
		keys := make([]string, 0, len(index.Facts))
		for _, f := range index.Facts {
			keys = append(keys, f.FactKey)
		}
		if !strings.Contains(strings.Join(keys, ","), "fact:stale-note") {
			t.Fatalf("include_deprecated missing deprecated fact: %v", keys)
		}
	})

	t.Run("fact_detail_resolves_alias_and_reports_resolution", func(t *testing.T) {
		detail := readLegacy[blackboard.LegacyFactDetailV1](t, graph, projectID, blackboard.ReadKindLegacyFactDetailV1, blackboard.LegacyFactDetailRequest{FactKey: "fact:old-admin"})
		if detail.FactKey != "fact:admin-service" {
			t.Fatalf("alias resolved to %q, want fact:admin-service", detail.FactKey)
		}
		if detail.ResolvedFromAlias == nil || *detail.ResolvedFromAlias != "fact:old-admin" {
			t.Fatalf("resolved_from_alias = %#v, want fact:old-admin", detail.ResolvedFromAlias)
		}
		if detail.Version < 1 {
			t.Fatalf("version = %d, want >= 1", detail.Version)
		}
		if detail.Body == "" {
			t.Fatalf("body empty on legacy detail")
		}
	})

	t.Run("fact_detail_unknown_key_returns_record_not_found", func(t *testing.T) {
		req := blackboard.ReadRequest{ProtocolVersion: blackboard.BlackboardReadProtocolVersion, ProjectID: projectID, Kind: blackboard.ReadKindLegacyFactDetailV1, LegacyFactDetail: &blackboard.LegacyFactDetailRequest{FactKey: "fact:missing"}}
		_, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), req)
		if err == nil {
			t.Fatalf("unknown fact key expected error, got nil")
		}
	})

	t.Run("fact_versions_ascending", func(t *testing.T) {
		versions := readLegacy[blackboard.LegacyFactVersionsV1](t, graph, projectID, blackboard.ReadKindLegacyFactVersionsV1, blackboard.LegacyFactVersionsRequest{FactKey: "fact:admin-service"})
		if len(versions.Versions) < 1 {
			t.Fatalf("versions empty")
		}
		for i := 1; i < len(versions.Versions); i++ {
			if versions.Versions[i-1].Version > versions.Versions[i].Version {
				t.Fatalf("versions not ascending: %+v", versions.Versions)
			}
		}
		if versions.Versions[0].FactKey != "fact:admin-service" {
			t.Fatalf("version fact_key = %q", versions.Versions[0].FactKey)
		}
	})

	t.Run("fact_relations_source_only_and_preserves_direction", func(t *testing.T) {
		relations := readLegacy[blackboard.LegacyFactRelationsV1](t, graph, projectID, blackboard.ReadKindLegacyFactRelationsV1, blackboard.LegacyFactRelationsRequest{FactKey: "fact:admin-service"})
		// admin-service is source of supports and leads_to (2); contradicts has
		// admin-service as target so it must NOT appear.
		if len(relations.Relations) != 2 {
			t.Fatalf("relations count = %d, want 2 (%+v)", len(relations.Relations), relations.Relations)
		}
		seen := map[string]bool{}
		for _, r := range relations.Relations {
			if r.SourceFactKey != "fact:admin-service" {
				t.Fatalf("relation source = %q, want fact:admin-service", r.SourceFactKey)
			}
			seen[r.Relation+"->"+r.TargetFactKey] = true
		}
		if !seen["supports->fact:dns-spray"] || !seen["leads_to->fact:partner-asset"] {
			t.Fatalf("missing expected relations, got %v", seen)
		}
	})

	t.Run("fact_relations_returns_empty_for_fact_with_no_source_edges", func(t *testing.T) {
		// §18.2: depends_on is audit-only and surfaced only when the migration
		// mapping preserved a fact-to-fact row; no such edge is seeded here.
		// fact:dns-spray has no outgoing supports/contradicts/leads_to edges, so
		// it returns the empty set.
		relations := readLegacy[blackboard.LegacyFactRelationsV1](t, graph, projectID, blackboard.ReadKindLegacyFactRelationsV1, blackboard.LegacyFactRelationsRequest{FactKey: "fact:dns-spray"})
		if len(relations.Relations) != 0 {
			t.Fatalf("dns-spray relations = %d, want 0 (%+v)", len(relations.Relations), relations.Relations)
		}
	})

	t.Run("finding_collection_keeps_false_positive_and_sorts_63", func(t *testing.T) {
		collection := readLegacy[blackboard.LegacyFindingCollectionV1](t, graph, projectID, blackboard.ReadKindLegacyFindingCollectionV1, blackboard.LegacyFindingCollectionRequest{})
		if len(collection.Findings) != 3 {
			t.Fatalf("findings count = %d, want 3 (%+v)", len(collection.Findings), collection.Findings)
		}
		// §6.3: confirmed before unconfirmed before false_positive.
		if collection.Findings[0].Status != "confirmed" {
			t.Fatalf("first finding status = %q, want confirmed", collection.Findings[0].Status)
		}
		if collection.Findings[len(collection.Findings)-1].Status != "false_positive" {
			t.Fatalf("last finding status = %q, want false_positive", collection.Findings[len(collection.Findings)-1].Status)
		}
		var sqli *blackboard.LegacyFindingV1
		for i := range collection.Findings {
			if collection.Findings[i].FindingKey == "finding:sqli-login" {
				sqli = &collection.Findings[i]
			}
		}
		if sqli == nil {
			t.Fatalf("sqli finding missing")
		}
		if sqli.Severity != "critical" {
			t.Fatalf("sqli severity = %q, want critical", sqli.Severity)
		}
		if sqli.CVSSPending {
			t.Fatalf("sqli cvss_pending = true, want false")
		}
	})

	t.Run("finding_versions_ascending", func(t *testing.T) {
		versions := readLegacy[blackboard.LegacyFindingVersionsV1](t, graph, projectID, blackboard.ReadKindLegacyFindingVersionsV1, blackboard.LegacyFindingVersionsRequest{FindingKey: "finding:sqli-login"})
		if len(versions.Versions) < 1 || versions.Versions[0].Version != 1 {
			t.Fatalf("finding versions = %+v", versions.Versions)
		}
	})

	t.Run("evidence_multi_target_appears_once_with_complete_attachments", func(t *testing.T) {
		collection := readLegacy[blackboard.LegacyEvidenceCollectionV1](t, graph, projectID, blackboard.ReadKindLegacyEvidenceCollectionV1, blackboard.LegacyEvidenceCollectionRequest{})
		// Two evidence artifacts; multi-target one appears once.
		if len(collection.Evidence) != 2 {
			t.Fatalf("evidence count = %d, want 2 (%+v)", len(collection.Evidence), collection.Evidence)
		}
		var multi *blackboard.LegacyEvidenceArtifactV1
		for i := range collection.Evidence {
			if collection.Evidence[i].EvidenceKey == "evidence:sqli-response" {
				multi = &collection.Evidence[i]
			}
		}
		if multi == nil {
			t.Fatalf("multi-target evidence missing")
		}
		if len(multi.Attachments) != 2 {
			t.Fatalf("attachments = %d, want 2 (%+v)", len(multi.Attachments), multi.Attachments)
		}
		// Singular legacy attach target is selected deterministically: the
		// ProjectFact precedes the Finding by node-type ordinal (§18.4 tier 2).
		if multi.AttachToType != "fact" || multi.AttachToKey != "fact:admin-service" {
			t.Fatalf("singular attach = %s/%s, want fact/fact:admin-service", multi.AttachToType, multi.AttachToKey)
		}
		for _, e := range collection.Evidence {
			if e.Attachments == nil {
				t.Fatalf("evidence %q has nil attachments", e.EvidenceKey)
			}
		}
	})

	t.Run("responses_have_no_legacy_table_markers_and_marshal_to_legacy_shape", func(t *testing.T) {
		// Additive fields must be JSON-serializable to the documented legacy
		// envelope; the top-level list key is preserved (facts/findings/evidence).
		index := readLegacy[blackboard.LegacyFactIndexV1](t, graph, projectID, blackboard.ReadKindLegacyFactIndexV1, blackboard.LegacyFactIndexRequest{})
		raw, err := json.Marshal(index)
		if err != nil {
			t.Fatalf("marshal fact index: %v", err)
		}
		if !strings.Contains(string(raw), `"facts"`) {
			t.Fatalf("fact index JSON missing facts key: %s", raw)
		}
		if strings.Contains(string(raw), `"generated_at"`) {
			t.Fatalf("legacy response must not carry generated_at: %s", raw)
		}
	})

	t.Run("dashboard_summary_preserves_legacy_counts_shape", func(t *testing.T) {
		req := blackboard.ReadRequest{ProtocolVersion: blackboard.BlackboardReadProtocolVersion, ProjectID: projectID, Kind: blackboard.ReadKindProjectBlackboardSummaryV1, ProjectSummary: &blackboard.ProjectBlackboardSummaryRequest{}}
		envelope, err := blackboard.NewBlackboardReadService(graph.DBForTesting()).Read(context.Background(), req)
		if err != nil {
			t.Fatalf("dashboard summary: %v", err)
		}
		summary, ok := envelope.Result.(blackboard.ProjectBlackboardSummaryV1)
		if !ok {
			t.Fatalf("dashboard result type = %T", envelope.Result)
		}
		// §18.5: counts.facts includes deprecated; counts.findings includes false
		// positives; the merged alias fact is excluded.
		if summary.Counts.Tasks != 1 || summary.Counts.Facts != 4 || summary.Counts.Findings != 3 || summary.Counts.Evidence != 2 {
			t.Fatalf("dashboard counts = %+v, want {Tasks:1 Facts:4 Findings:3 Evidence:2}", summary.Counts)
		}
	})
}
