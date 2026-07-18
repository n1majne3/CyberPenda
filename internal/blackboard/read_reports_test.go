package blackboard_test

import (
	"bytes"
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

func boolPtr(v bool) *bool { return &v }

func newReportGraphServices(t *testing.T) (*blackboard.GraphService, *project.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return blackboard.NewGraphService(db, nil, nil), project.NewService(db)
}

func seedPentestReportGraph(t *testing.T, graph *blackboard.GraphService, projects *project.Service) (project.Project, task.Task, task.Task) {
	t.Helper()
	created, err := projects.Create("Acme External", "External perimeter assessment", project.Scope{
		Domains:       []string{"example.com"},
		TestingLimits: []string{"business hours only"},
		Notes:         "authorized external only",
	}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(graph.DBForTesting(), projects)
	tasks.SetGoalProjector(graph)

	sandboxTask, err := tasks.Create(task.CreateRequest{
		ProjectID: created.ID,
		Goal:      "Enumerate external perimeter",
		Runner:    task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create sandbox Task: %v", err)
	}
	hostTask, err := tasks.Create(task.CreateRequest{
		ProjectID: created.ID,
		Goal:      "Validate host-only artifact capture",
		Runner:    task.RunnerHost,
	})
	if err != nil {
		t.Fatalf("create host Task: %v", err)
	}

	sandboxCtx := blackboard.SystemExecutionContext(created.ID, created.Kind, "u04-sandbox")
	sandboxCtx.TaskID = sandboxTask.ID
	sandboxCtx.Runner = string(task.RunnerSandbox)

	hostCtx := blackboard.SystemExecutionContext(created.ID, created.Kind, "u04-host")
	hostCtx.TaskID = hostTask.ID
	hostCtx.Runner = string(task.RunnerHost)

	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u04:report-seed",
		Context:        sandboxCtx,
		Operations: []blackboard.Operation{
			{
				OpID: "entity", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:login"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "service", "name": "Login", "locator": "https://example.com/login", "scope_status": "in_scope"}},
			},
			{
				OpID: "confirmed-fact", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:tls"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category": "service", "summary": "TLS terminates at the edge", "body": "Certificate inspection confirmed TLS termination",
					"confidence": "confirmed", "scope_status": "in_scope",
				}},
			},
			{
				OpID: "tentative-fact", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:waf"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category": "service", "summary": "WAF may be present", "confidence": "tentative", "scope_status": "in_scope",
				}},
			},
			{
				OpID: "oos-fact", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:partner"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category": "asset", "summary": "Partner portal is out of scope", "body": "Client excluded partner portal",
					"confidence": "confirmed", "scope_status": "out_of_scope",
				}},
			},
			{
				OpID: "evidence", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:sqli"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"artifact_type": "http_exchange", "managed_path": "artifacts/sqli.txt", "summary": "SQL error payload",
					"status": "available", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					"media_type": "text/plain", "size_bytes": float64(42),
				}},
			},
			{
				OpID: "missing-evidence", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:missing"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"artifact_type": "other", "managed_path": "missing://lost", "summary": "Lost screenshot", "status": "missing",
				}},
			},
			{
				OpID: "produced-only", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:produced-only"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"artifact_type": "other", "managed_path": "artifacts/noise.bin", "summary": "Produced but not linked as proof",
					"status": "available", "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				}},
			},
			{
				OpID: "confirmed", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:sqli"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"title": "SQL injection in login", "status": "confirmed", "target": "https://example.com/login",
					"description": "Login endpoint accepts SQL metacharacters", "proof": "Error-based injection observed",
					"impact": "Authentication bypass", "recommendation": "Use parameterized queries",
					"cvss_version": "4.0", "cvss_vector": "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:N/SC:N/SI:N/SA:N",
				}},
			},
			{
				OpID: "unconfirmed", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:header"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"title": "Missing security header", "status": "unconfirmed", "target": "https://example.com",
					"description": "CSP header is absent",
				}},
			},
			{
				OpID: "false-positive", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:fp"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"title": "False positive XSS", "status": "unconfirmed", "target": "https://example.com/search",
				}},
			},
			{
				OpID: "mark-fp", Kind: blackboard.OpTransitionNode,
				Node:       blackboard.NodeRef{OpID: "false-positive"},
				Transition: blackboard.TransitionNodeInput{ExpectedVersion: 1, Status: "false_positive", Summary: "Reflected but not executable"},
			},
			{
				OpID: "evidences", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence"}, To: blackboard.NodeRef{OpID: "confirmed"}, Summary: "proves injection"},
			},
			{
				OpID: "missing-link", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "missing-evidence"}, To: blackboard.NodeRef{OpID: "confirmed"}, Summary: "missing proof still listed"},
			},
			{
				OpID: "supports", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupports, From: blackboard.NodeRef{OpID: "confirmed-fact"}, To: blackboard.NodeRef{OpID: "confirmed"}, Summary: "TLS context supports finding"},
			},
			{
				OpID: "about", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeAbout, From: blackboard.NodeRef{OpID: "confirmed"}, To: blackboard.NodeRef{OpID: "entity"}, Summary: "about login"},
			},
			{
				OpID: "objective", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:remaining"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Map remaining admin surface", "status": "open"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("seed sandbox graph: %v", err)
	}

	// Host-runner contribution: a fact with host provenance. Produced-only evidence is
	// created under host context so it cannot masquerade as proof without an evidences edge.
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u04:host-seed",
		Context:        hostCtx,
		Operations: []blackboard.Operation{
			{
				OpID: "host-fact", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:host-tooling"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category": "procedure", "summary": "Host runner used for packet capture", "body": "Operator authorized host capture",
					"confidence": "confirmed", "scope_status": "in_scope",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("seed host graph: %v", err)
	}
	return created, sandboxTask, hostTask
}

// TestPentestReportSameSourceHashProducesByteIdenticalJSONAndMarkdown is U04's
// first red test at BlackboardReadService.Read. Same source inputs must produce
// byte-identical semantic JSON and Markdown with no render-time clock.
func TestPentestReportSameSourceHashProducesByteIdenticalJSONAndMarkdown(t *testing.T) {
	graph, projects := newReportGraphServices(t)
	created, _, _ := seedPentestReportGraph(t, graph, projects)
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())

	req := blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       created.ID,
		Kind:            blackboard.ReadKindPentestReportV1,
		PentestReport: &blackboard.PentestReportRequest{
			IncludeUnconfirmed:       boolPtr(true),
			IncludeTentativeFacts:    boolPtr(true),
			IncludeOutOfScopeContext: boolPtr(true),
			IncludeUnresolvedWork:    boolPtr(false),
			ScopeContext:             "current",
			EvidenceDetail:           "summary",
			Format:                   "json",
		},
	}

	first, err := reads.Read(context.Background(), req)
	if err != nil {
		t.Fatalf("first JSON report: %v", err)
	}
	second, err := reads.Read(context.Background(), req)
	if err != nil {
		t.Fatalf("second JSON report: %v", err)
	}
	firstJSON, err := json.Marshal(first.Result)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondJSON, err := json.Marshal(second.Result)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("JSON reports differ:\n%s\nvs\n%s", firstJSON, secondJSON)
	}

	report, ok := first.Result.(blackboard.PentestReportV1)
	if !ok {
		t.Fatalf("result type %T want PentestReportV1", first.Result)
	}
	if report.ReportVersion != "pentest_report_v1" {
		t.Fatalf("report_version = %q", report.ReportVersion)
	}
	if report.Source.SourceHash == "" || report.Source.RendererVersion != "pentest_markdown_v1" {
		t.Fatalf("missing source hash or renderer: %+v", report.Source)
	}
	if strings.Contains(string(firstJSON), "generated") && strings.Contains(strings.ToLower(string(firstJSON)), "generated_at") {
		t.Fatalf("report must not include render-time clock fields: %s", firstJSON)
	}
	if report.Summary.ConfirmedFindings != 1 || report.Summary.UnconfirmedFindings != 1 {
		t.Fatalf("summary counts = confirmed %d unconfirmed %d", report.Summary.ConfirmedFindings, report.Summary.UnconfirmedFindings)
	}
	if len(report.ConfirmedFindings) != 1 || report.ConfirmedFindings[0].Title != "SQL injection in login" {
		t.Fatalf("confirmed findings = %#v", report.ConfirmedFindings)
	}
	// Produced-only evidence must not appear as proof on the Finding.
	for _, item := range report.ConfirmedFindings[0].Evidence {
		if item.StableKey == "evidence:produced-only" {
			t.Fatalf("produced-only artifact presented as proof: %#v", item)
		}
	}
	missingVisible := false
	for _, item := range report.ConfirmedFindings[0].Evidence {
		if item.StableKey == "evidence:missing" && item.Status == "missing" {
			missingVisible = true
		}
	}
	if !missingVisible {
		t.Fatalf("missing Evidence must remain visible without invented proof: %#v", report.ConfirmedFindings[0].Evidence)
	}
	if report.Engagement.RunnerSummary.Host < 1 || report.Engagement.RunnerSummary.Sandbox < 1 {
		t.Fatalf("runner contributions must count host and sandbox Tasks: %+v", report.Engagement.RunnerSummary)
	}

	mdReq := req
	mdReq.PentestReport = &blackboard.PentestReportRequest{
		IncludeUnconfirmed:       boolPtr(true),
		IncludeTentativeFacts:    boolPtr(true),
		IncludeOutOfScopeContext: boolPtr(true),
		ScopeContext:             "current",
		EvidenceDetail:           "summary",
		Format:                   "markdown",
	}
	mdFirst, err := reads.Read(context.Background(), mdReq)
	if err != nil {
		t.Fatalf("first Markdown report: %v", err)
	}
	mdSecond, err := reads.Read(context.Background(), mdReq)
	if err != nil {
		t.Fatalf("second Markdown report: %v", err)
	}
	mdA, ok := mdFirst.Result.(blackboard.ReportMarkdownV1)
	if !ok {
		t.Fatalf("markdown result type %T want ReportMarkdownV1", mdFirst.Result)
	}
	mdB, ok := mdSecond.Result.(blackboard.ReportMarkdownV1)
	if !ok {
		t.Fatalf("markdown result type %T want ReportMarkdownV1", mdSecond.Result)
	}
	if mdA.Source.SourceHash != report.Source.SourceHash {
		t.Fatalf("markdown source_hash %q != json source_hash %q", mdA.Source.SourceHash, report.Source.SourceHash)
	}
	if mdA.Markdown != mdB.Markdown {
		t.Fatalf("Markdown reports differ")
	}
	if !strings.HasSuffix(mdA.Markdown, "\n") || strings.HasSuffix(mdA.Markdown, "\n\n") {
		t.Fatalf("Markdown must end with exactly one LF, got %q", mdA.Markdown[len(mdA.Markdown)-3:])
	}
	if strings.Contains(mdA.Markdown, "\r") {
		t.Fatalf("Markdown must use LF only")
	}
	if strings.Contains(strings.ToLower(mdA.Markdown), "generated:") {
		t.Fatalf("Markdown must not include render-time clock: %s", mdA.Markdown)
	}
	for _, needle := range []string{
		"# Pentest Report",
		"Acme External",
		"SQL injection in login",
		"Missing security header",
		"TLS terminates at the edge",
		"WAF may be present",
		"Partner portal is out of scope",
		"host",
		"evidence:missing",
	} {
		if !strings.Contains(mdA.Markdown, needle) {
			t.Fatalf("Markdown missing %q:\n%s", needle, mdA.Markdown)
		}
	}
	if strings.Contains(mdA.Markdown, "evidence:produced-only") {
		t.Fatalf("produced-only artifact leaked into Markdown proof sections")
	}
	if strings.Contains(mdA.Markdown, "False positive XSS") {
		t.Fatalf("false positives must not appear in the main report")
	}
}

func TestPentestReportRejectsCTFProjectsAndCTFRejectsPentest(t *testing.T) {
	graph, projects := newReportGraphServices(t)
	pentest, err := projects.Create("Pentest", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create pentest: %v", err)
	}
	ctf, err := projects.CreateWithKind("Challenge", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create ctf: %v", err)
	}
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())

	_, err = reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       ctf.ID,
		Kind:            blackboard.ReadKindPentestReportV1,
		PentestReport:   &blackboard.PentestReportRequest{ScopeContext: "current"},
	})
	assertReadErrorCode(t, err, blackboard.ErrCodeProjectKindMismatch)

	_, err = reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       pentest.ID,
		Kind:            blackboard.ReadKindCTFSolutionV1,
		CTFSolution:     &blackboard.CTFSolutionRequest{},
	})
	assertReadErrorCode(t, err, blackboard.ErrCodeProjectKindMismatch)
}

func TestCTFSolutionIncludesVerifiedCandidatesEvidenceAndNoRedaction(t *testing.T) {
	graph, projects := newReportGraphServices(t)
	ctfProject, _, ctfCtx := createCTFTaskContext(t, graph, projects, "Recover the challenge flag")
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u04:ctf-seed",
		Context:        ctfCtx,
		Operations: []blackboard.Operation{
			{
				OpID: "evidence", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:flag"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"artifact_type": "other", "managed_path": "artifacts/flag.txt", "summary": "Flag file", "status": "available",
					"sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				}},
			},
			{
				OpID: "procedure", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:procedure"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"kind": "procedure", "summary": "Dump and decode the blob", "status": "verified", "verification_summary": "worked",
				}},
			},
			{
				OpID: "candidate", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:candidate"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"kind": "flag", "summary": "Alternate candidate", "value": "FLAG{candidate}",
				}},
			},
			{
				OpID: "flag", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:flag"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"kind": "flag", "summary": "Primary flag", "value": "FLAG{correct}", "status": "verified", "verification_summary": "accepted",
				}},
			},
			{
				OpID: "evidences", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence"}, To: blackboard.NodeRef{OpID: "flag"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("seed CTF solution graph: %v", err)
	}

	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	envelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       ctfProject.ID,
		Kind:            blackboard.ReadKindCTFSolutionV1,
		CTFSolution: &blackboard.CTFSolutionRequest{
			IncludeCandidates: boolPtr(true),
			IncludeProcedure:  boolPtr(true),
			Format:            "json",
		},
	})
	if err != nil {
		t.Fatalf("read CTF solution: %v", err)
	}
	solution, ok := envelope.Result.(blackboard.CTFSolutionV1)
	if !ok {
		t.Fatalf("result type %T", envelope.Result)
	}
	if !solution.Solved || solution.PrimaryVerifiedFlag == nil || solution.PrimaryVerifiedFlag.Value != "FLAG{correct}" {
		t.Fatalf("expected verified flag value disclosed on CTF route, got %+v", solution)
	}
	if len(solution.CandidateFlags) != 1 || solution.CandidateFlags[0].Value != "FLAG{candidate}" {
		t.Fatalf("candidate flags = %#v", solution.CandidateFlags)
	}
	if len(solution.Procedures) != 1 {
		t.Fatalf("procedures = %#v", solution.Procedures)
	}
	if len(solution.Evidence) != 1 || solution.Evidence[0].StableKey != "evidence:flag" {
		t.Fatalf("evidence = %#v", solution.Evidence)
	}

	mdEnvelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       ctfProject.ID,
		Kind:            blackboard.ReadKindCTFSolutionV1,
		CTFSolution:     &blackboard.CTFSolutionRequest{IncludeCandidates: boolPtr(true), IncludeProcedure: boolPtr(true), Format: "markdown"},
	})
	if err != nil {
		t.Fatalf("markdown CTF solution: %v", err)
	}
	md, ok := mdEnvelope.Result.(blackboard.ReportMarkdownV1)
	if !ok {
		t.Fatalf("markdown type %T", mdEnvelope.Result)
	}
	if !strings.HasSuffix(md.Markdown, "\n") || strings.HasSuffix(md.Markdown, "\n\n") {
		t.Fatalf("CTF Markdown must end with exactly one LF")
	}
	if !strings.Contains(md.Markdown, "FLAG{correct}") && !strings.Contains(md.Markdown, `FLAG\{correct\}`) {
		t.Fatalf("CTF Markdown must disclose flag value on explicit route")
	}
}

func TestPentestReportIncludeDefaultsApplyWhenOnlyFormatSet(t *testing.T) {
	graph, projects := newReportGraphServices(t)
	created, _, _ := seedPentestReportGraph(t, graph, projects)
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())

	// Only format is set; include_* must still default true/false per contract.
	envelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       created.ID,
		Kind:            blackboard.ReadKindPentestReportV1,
		PentestReport:   &blackboard.PentestReportRequest{Format: "json"},
	})
	if err != nil {
		t.Fatalf("read with format-only request: %v", err)
	}
	report := envelope.Result.(blackboard.PentestReportV1)
	if report.Summary.UnconfirmedFindings != 1 {
		t.Fatalf("include_unconfirmed should default true, unconfirmed=%d", report.Summary.UnconfirmedFindings)
	}
	if report.Summary.TentativeFacts != 1 {
		t.Fatalf("include_tentative_facts should default true, tentative=%d", report.Summary.TentativeFacts)
	}
	if len(report.CurrentTruth.OutOfScope) != 1 {
		t.Fatalf("include_out_of_scope_context should default true, out_of_scope=%d", len(report.CurrentTruth.OutOfScope))
	}
	if report.UnresolvedWork != nil {
		t.Fatalf("include_unresolved_work should default false")
	}

	// Explicit false must suppress defaults.
	suppressed, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       created.ID,
		Kind:            blackboard.ReadKindPentestReportV1,
		PentestReport: &blackboard.PentestReportRequest{
			IncludeUnconfirmed:       boolPtr(false),
			IncludeTentativeFacts:    boolPtr(false),
			IncludeOutOfScopeContext: boolPtr(false),
			Format:                   "json",
		},
	})
	if err != nil {
		t.Fatalf("read with explicit false includes: %v", err)
	}
	suppressedReport := suppressed.Result.(blackboard.PentestReportV1)
	if suppressedReport.Summary.UnconfirmedFindings != 0 || len(suppressedReport.UnconfirmedFindings) != 0 {
		t.Fatalf("explicit include_unconfirmed=false leaked unconfirmed findings")
	}
	if suppressedReport.Summary.TentativeFacts != 0 || len(suppressedReport.CurrentTruth.Tentative) != 0 {
		t.Fatalf("explicit include_tentative_facts=false leaked tentative facts")
	}
	if len(suppressedReport.CurrentTruth.OutOfScope) != 0 {
		t.Fatalf("explicit include_out_of_scope_context=false leaked out-of-scope facts")
	}
}

func TestPentestReportTaskScopeContextPinsSnapshotAndMarksCrossTask(t *testing.T) {
	graph, projects := newReportGraphServices(t)
	created, sandboxTask, hostTask := seedPentestReportGraph(t, graph, projects)
	// Change current Project Scope after Tasks captured snapshots.
	if _, err := projects.Update(created.ID, created.Name, created.Description, project.Scope{Domains: []string{"changed.example"}, Notes: "mutated current scope"}, true, project.Defaults{}, false); err != nil {
		t.Fatalf("update current scope: %v", err)
	}
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())

	current, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       created.ID,
		Kind:            blackboard.ReadKindPentestReportV1,
		PentestReport:   &blackboard.PentestReportRequest{ScopeContext: "current"},
	})
	if err != nil {
		t.Fatalf("current scope report: %v", err)
	}
	currentReport := current.Result.(blackboard.PentestReportV1)
	if len(currentReport.Engagement.Scope.Domains) != 1 || currentReport.Engagement.Scope.Domains[0] != "changed.example" {
		t.Fatalf("current scope should use live Project Scope, got %+v", currentReport.Engagement.Scope)
	}

	taskScoped, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       created.ID,
		Kind:            blackboard.ReadKindPentestReportV1,
		PentestReport:   &blackboard.PentestReportRequest{ScopeContext: "task:" + sandboxTask.ID},
	})
	if err != nil {
		t.Fatalf("task scope report: %v", err)
	}
	taskReport := taskScoped.Result.(blackboard.PentestReportV1)
	if len(taskReport.Engagement.Scope.Domains) != 1 || taskReport.Engagement.Scope.Domains[0] != "example.com" {
		t.Fatalf("task scope should pin immutable snapshot, got %+v", taskReport.Engagement.Scope)
	}
	if taskReport.Source.ScopeContext != "task:"+sandboxTask.ID {
		t.Fatalf("scope_context = %q", taskReport.Source.ScopeContext)
	}
	hostMarked := false
	for _, contrib := range taskReport.Engagement.ContributingTasks {
		if contrib.TaskID == hostTask.ID {
			hostMarked = contrib.CrossTask
		}
	}
	if !hostMarked {
		t.Fatalf("host Task conclusions should be marked cross_task under sandbox Scope Snapshot")
	}
	// Confirmed finding was authored under sandbox Task; host-authored fact should be cross-task on findings when selected.
	for _, finding := range taskReport.ConfirmedFindings {
		if finding.Finding.StableKey == "finding:sqli" && finding.CrossTask {
			t.Fatalf("sandbox-authored finding should not be cross_task under sandbox scope")
		}
	}
}

func TestCTFSolutionDefaultsIncludeCandidatesWhenOnlyFormatSet(t *testing.T) {
	graph, projects := newReportGraphServices(t)
	ctfProject, _, ctfCtx := createCTFTaskContext(t, graph, projects, "Recover the challenge flag")
	_, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "u04:ctf-defaults",
		Context:        ctfCtx,
		Operations: []blackboard.Operation{
			{OpID: "candidate", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:candidate"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "flag", "summary": "Candidate", "value": "FLAG{cand}"}}},
			{OpID: "flag", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:flag"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "flag", "summary": "Primary", "value": "FLAG{ok}", "status": "verified", "verification_summary": "accepted"}}},
			{OpID: "procedure", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeSolution, StableKey: "solution:procedure"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "procedure", "summary": "Steps", "status": "verified", "verification_summary": "worked"}}},
		},
	})
	if err != nil {
		t.Fatalf("seed CTF defaults graph: %v", err)
	}
	reads := blackboard.NewBlackboardReadService(graph.DBForTesting())
	envelope, err := reads.Read(context.Background(), blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       ctfProject.ID,
		Kind:            blackboard.ReadKindCTFSolutionV1,
		CTFSolution:     &blackboard.CTFSolutionRequest{Format: "json"},
	})
	if err != nil {
		t.Fatalf("CTF format-only read: %v", err)
	}
	solution := envelope.Result.(blackboard.CTFSolutionV1)
	if len(solution.CandidateFlags) != 1 {
		t.Fatalf("include_candidates should default true, got %#v", solution.CandidateFlags)
	}
	if len(solution.Procedures) != 1 {
		t.Fatalf("include_procedure should default true, got %#v", solution.Procedures)
	}
}
