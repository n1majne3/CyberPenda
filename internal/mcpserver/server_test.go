package mcpserver_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardcompat"
	"pentest/internal/mcpserver"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

func connectMCP(t *testing.T, deps mcpserver.Deps) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := mcpserver.New(deps)
	t1, t2 := sdkmcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestLegacyMCPWriteUsesGraphCompatibilityAndDeprecationMetadata(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "mcp-compatibility.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	projects := project.NewService(db)
	projectRow, err := projects.Create("MCP compatibility", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("activate graph epoch: %v", err)
	}
	graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	tasks := task.NewService(db)
	projectInterface := projectinterface.NewService(projectinterface.Deps{DB: db, Graph: graph, Tasks: tasks})
	compatibility := blackboardcompat.NewService(blackboardcompat.Deps{
		DB: db, Graph: graph, Reads: blackboard.NewBlackboardReadService(db), ProjectInterface: projectInterface, Tasks: tasks,
	})
	session := connectMCP(t, mcpserver.Deps{
		Projects: projects, Facts: blackboard.NewService(db), Tasks: tasks,
		Reads: blackboard.NewBlackboardReadService(db), ProjectInterface: projectInterface,
		Compatibility: compatibility,
	})

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}
	deprecatedTools := map[string]bool{}
	for _, tool := range listed.Tools {
		deprecated, _ := tool.Meta["deprecated"].(bool)
		deprecatedTools[tool.Name] = deprecated
	}
	for _, name := range []string{
		"upsert_project_fact", "get_project_fact", "list_project_facts", "search_project_facts",
		"deprecate_project_fact", "upsert_fact_relation", "record_vulnerability", "list_vulnerabilities",
		"attach_evidence", "generate_report", "submit_task_summary",
	} {
		if !deprecatedTools[name] {
			t.Errorf("%s missing MCP deprecation metadata", name)
		}
	}

	body := callTool(t, session, "upsert_project_fact", map[string]any{
		"project_id": projectRow.ID, "fact_key": "fact:mcp", "category": "service",
		"summary": "MCP compatibility", "confidence": "tentative", "scope_status": "in_scope",
		"idempotency_key": "mcp:fact",
	})
	if !strings.Contains(body, `"fact_key":"fact:mcp"`) || !strings.Contains(body, `"version":1`) {
		t.Fatalf("legacy MCP payload = %s", body)
	}
	if _, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectRow.ID, NodeType: blackboard.NodeTypeProjectFact, Key: "fact:mcp"}); err != nil {
		t.Fatalf("read graph Fact: %v", err)
	}
	missing, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "upsert_fact_relation", Arguments: map[string]any{
		"project_id": projectRow.ID, "source_fact_key": "fact:mcp", "target_fact_key": "fact:missing",
		"relation": "supports", "idempotency_key": "mcp:missing-endpoint",
	}})
	if err != nil {
		t.Fatalf("call missing-endpoint relation: %v", err)
	}
	if !missing.IsError || !strings.Contains(mcpResultText(missing), `"code":"node_not_found"`) {
		t.Fatalf("missing-endpoint MCP error = %#v body=%s", missing, mcpResultText(missing))
	}
	legacy, err := blackboard.NewService(db).FactIndex(projectRow.ID, blackboard.FactIndexOptions{IncludeDeprecated: true})
	if err != nil || len(legacy) != 0 {
		t.Fatalf("legacy storage = %+v err=%v", legacy, err)
	}
}

func TestRetiredLegacyMCPReadsAreNotRegisteredAndCannotBeCalled(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "mcp-retired-reads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projects := project.NewService(db)
	projectRow, err := projects.Create("MCP retired reads", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO blackboard_compatibility_read_retirement(id,retired_at,bundled_web_cli_projections_only,observation_waived) VALUES(1,'2026-07-14T00:00:00Z',1,0)`); err != nil {
		t.Fatal(err)
	}
	graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	tasks := task.NewService(db)
	projectInterface := projectinterface.NewService(projectinterface.Deps{DB: db, Graph: graph, Tasks: tasks})
	compatibility := blackboardcompat.NewService(blackboardcompat.Deps{DB: db, Graph: graph, Reads: blackboard.NewBlackboardReadService(db), ProjectInterface: projectInterface, Tasks: tasks})
	session := connectMCP(t, mcpserver.Deps{Projects: projects, Facts: blackboard.NewService(db), Tasks: tasks, Reads: blackboard.NewBlackboardReadService(db), ProjectInterface: projectInterface, Compatibility: compatibility})

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		for _, retired := range []string{"get_project_fact", "list_project_facts", "search_project_facts", "list_vulnerabilities", "generate_report"} {
			if tool.Name == retired {
				t.Fatalf("retired MCP read tool %s remains registered", retired)
			}
		}
	}
	if _, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "get_project_fact", Arguments: map[string]any{"project_id": projectRow.ID, "fact_key": "fact:x"}}); err == nil || !strings.Contains(err.Error(), `unknown tool "get_project_fact"`) {
		t.Fatalf("retired tool call error=%v", err)
	}
}

func TestMCPReadRetirementQueryErrorKeepsLegacyToolsRegistered(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "mcp-retirement-error.db"))
	if err != nil {
		t.Fatal(err)
	}
	compatibility := blackboardcompat.NewService(blackboardcompat.Deps{DB: db})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	session := connectMCP(t, mcpserver.Deps{Compatibility: compatibility})
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, tool := range listed.Tools {
		registered[tool.Name] = true
	}
	for _, name := range []string{"get_project_fact", "list_project_facts", "search_project_facts", "list_vulnerabilities", "generate_report"} {
		if !registered[name] {
			t.Fatalf("query error incorrectly removed %s", name)
		}
	}
}

func callTool(t *testing.T, session *sdkmcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned error: %#v", name, res)
	}
	if len(res.Content) == 0 {
		t.Fatalf("%s returned no content", name)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("%s expected text content, got %T", name, res.Content[0])
	}
	return text.Text
}

func mcpResultText(result *sdkmcp.CallToolResult) string {
	var out strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}

// TestUpsertProjectFactWritesEquivalentState proves the trusted MCP server
// upserts facts through the same blackboard service as HTTP and CLI.
func TestUpsertProjectFactWritesEquivalentState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	projects := project.NewService(db)
	facts := blackboard.NewService(db)
	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	session := connectMCP(t, mcpserver.Deps{Projects: projects, Facts: facts})
	callTool(t, session, "upsert_project_fact", map[string]any{
		"project_id":   proj.ID,
		"fact_key":     "target:example.com",
		"category":     "target",
		"summary":      "example.com is in scope",
		"body":         "primary web target",
		"confidence":   "confirmed",
		"scope_status": "in_scope",
	})

	got, err := facts.GetFact(proj.ID, "target:example.com")
	if err != nil {
		t.Fatalf("get fact: %v", err)
	}
	if got.Summary != "example.com is in scope" || got.Body != "primary web target" {
		t.Fatalf("unexpected stored fact: %#v", got)
	}
}

func TestMCPFactReadSearchAndDeprecate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	projects := project.NewService(db)
	facts := blackboard.NewService(db)
	proj, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	session := connectMCP(t, mcpserver.Deps{Projects: projects, Facts: facts})

	callTool(t, session, "upsert_project_fact", map[string]any{
		"project_id": proj.ID, "fact_key": "dns:example.com", "summary": "example.com resolves",
	})
	body := callTool(t, session, "get_project_fact", map[string]any{
		"project_id": proj.ID, "fact_key": "dns:example.com",
	})
	if !strings.Contains(body, `"fact_key":"dns:example.com"`) {
		t.Fatalf("get_project_fact body missing key: %s", body)
	}
	index := callTool(t, session, "list_project_facts", map[string]any{"project_id": proj.ID})
	if !strings.Contains(index, "dns:example.com") {
		t.Fatalf("list_project_facts missing fact: %s", index)
	}
	search := callTool(t, session, "search_project_facts", map[string]any{
		"project_id": proj.ID, "query": "example",
	})
	if !strings.Contains(search, "dns:example.com") {
		t.Fatalf("search_project_facts missing match: %s", search)
	}
	callTool(t, session, "deprecate_project_fact", map[string]any{
		"project_id": proj.ID, "fact_key": "dns:example.com",
	})
	index = callTool(t, session, "list_project_facts", map[string]any{"project_id": proj.ID})
	if strings.Contains(index, "dns:example.com") {
		t.Fatalf("deprecated fact should be excluded from default index: %s", index)
	}
}

func TestMCPFindingEvidenceAndReportFlow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	projects := project.NewService(db)
	facts := blackboard.NewService(db)
	tasks := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	launched, err := tasks.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Goal:      "enumerate example.com",
		Runner:    task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	session := connectMCP(t, mcpserver.Deps{Projects: projects, Facts: facts, Tasks: tasks})
	callTool(t, session, "upsert_project_fact", map[string]any{
		"project_id": proj.ID, "fact_key": "recon:subs", "summary": "Found 3 subdomains",
	})
	callTool(t, session, "record_vulnerability", map[string]any{
		"project_id": proj.ID, "finding_key": "sqli-login", "title": "SQL injection in login",
		"status": "confirmed", "target": "https://example.com/login", "proof": "' or 1=1--",
		"impact": "auth bypass", "recommendation": "parameterize queries",
		"cvss_version": "4.0", "cvss_vector": "CVSS:4.0/AV:N/VC:H/VI:H",
	})
	callTool(t, session, "attach_evidence", map[string]any{
		"project_id": proj.ID, "evidence_key": "login-proof", "attach_to_type": "finding",
		"attach_to_key": "sqli-login", "artifact_type": "http_exchange", "summary": "login replay",
	})

	listBody := callTool(t, session, "list_vulnerabilities", map[string]any{"project_id": proj.ID})
	if !strings.Contains(listBody, "sqli-login") {
		t.Fatalf("list_vulnerabilities missing finding: %s", listBody)
	}

	reportBody := callTool(t, session, "generate_report", map[string]any{
		"project_id": proj.ID, "task_id": launched.ID,
	})
	if !strings.Contains(reportBody, "SQL injection in login") || !strings.Contains(reportBody, "Confirmed Findings") {
		t.Fatalf("generate_report missing expected content: %s", reportBody)
	}
}

func TestMCPSubmitTaskSummaryWritesEquivalentState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	projects := project.NewService(db)
	tasks := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	launched, err := tasks.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Goal:      "enumerate example.com",
		Runner:    task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	session := connectMCP(t, mcpserver.Deps{Projects: projects, Tasks: tasks})
	body := callTool(t, session, "submit_task_summary", map[string]any{
		"project_id":   proj.ID,
		"task_id":      launched.ID,
		"summary":      "Mapped APIs and solved 10 easy challenges.",
		"submitted_by": "claude_code",
	})
	if !strings.Contains(body, `"version":1`) {
		t.Fatalf("expected versioned summary response, got %s", body)
	}

	versions, err := tasks.SummaryVersions(launched.ID)
	if err != nil {
		t.Fatalf("list summary versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Summary != "Mapped APIs and solved 10 easy challenges." {
		t.Fatalf("unexpected stored summary: %#v", versions)
	}
}
